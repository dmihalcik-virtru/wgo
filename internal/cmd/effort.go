package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/effort"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/store"
)

var (
	effortDescription string
	effortForce       bool
)

// planEffortCmd represents the `wgo plan effort` command group.
var planEffortCmd = &cobra.Command{
	Use:   "effort",
	Short: "Manage efforts (cross-repo features)",
	Long: `Manage efforts that group related work across repositories.

Each command updates state.json and the "## Efforts" section of the plan
together; if the plan cannot be written, state is left unchanged.

Bookmarks are named repo:bookmark, where repo is a discovered main clone's
directory name, its owner/repo, or its absolute path. When linked with
"wgo plan effort link", state stores the clone's absolute path; the plan shows
the clone's directory name, else its owner/repo, else its absolute path,
whichever is the first that is unambiguous.`,
}

// effortAddCmd represents the `wgo plan effort add` command.
var effortAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Add a new effort",
	Long:  `Add a new effort to group related work.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEffort(func(env *effortEnv) error { return env.add(args[0], effortDescription) })
	},
}

// effortLinkCmd represents the `wgo plan effort link` command.
var effortLinkCmd = &cobra.Command{
	Use:   "link <effort-name> <repo:bookmark>",
	Short: "Link a bookmark to an effort",
	Long:  `Link a bookmark in a discovered repository to an existing effort.`,
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEffort(func(env *effortEnv) error { return env.link(args[0], args[1]) })
	},
}

// effortUnlinkCmd represents the `wgo plan effort unlink` command.
var effortUnlinkCmd = &cobra.Command{
	Use:   "unlink <effort-name> <repo:bookmark>",
	Short: "Unlink a bookmark from an effort",
	Long:  `Unlink a bookmark from an existing effort.`,
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEffort(func(env *effortEnv) error { return env.unlink(args[0], args[1]) })
	},
}

// effortRemoveCmd represents the `wgo plan effort remove` command.
var effortRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove an effort",
	Long: `Remove an effort and its associations from state and the plan.

If the effort's plan block holds text wgo does not interpret (malformed
entries, notes), removal is refused unless --force is given.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEffort(func(env *effortEnv) error { return env.remove(args[0], effortForce) })
	},
}

func init() {
	planCmd.AddCommand(planEffortCmd)
	planEffortCmd.AddCommand(effortAddCmd)
	planEffortCmd.AddCommand(effortLinkCmd)
	planEffortCmd.AddCommand(effortUnlinkCmd)
	planEffortCmd.AddCommand(effortRemoveCmd)

	effortAddCmd.Flags().StringVarP(&effortDescription, "description", "d", "", "Description of the effort")
	effortRemoveCmd.Flags().BoolVar(&effortForce, "force", false, "Remove even if the plan block holds text wgo does not interpret")
}

// effortEnv is what the effort commands run against.
type effortEnv struct {
	store *store.FileStore
	// clones lists the discovered main clones; called only by commands that
	// resolve a repo:bookmark reference.
	clones func() ([]effort.MainCloneInfo, error)
	out    io.Writer
}

func runEffort(fn func(*effortEnv) error) error {
	s, err := store.New()
	if err != nil {
		return fmt.Errorf("failed to create store: %w", err)
	}
	return fn(&effortEnv{store: s, clones: discoverMainClones, out: os.Stderr})
}

// discoverMainClones lists main clones through the configured discovery.
func discoverMainClones() ([]effort.MainCloneInfo, error) {
	if err := config.Init(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	repos, err := discovery.FromConfig(config.Get()).DiscoverAll()
	if err != nil {
		return nil, fmt.Errorf("discover repositories: %w", err)
	}
	return effort.MainClones(jj.NewCLI(), repos), nil
}

// mutate loads state and the plan, applies fn to both and persists them as a
// pair: the plan is written inside the state lock and before state, so a
// failed plan write leaves state untouched. A failed state write restores the
// previous plan, but only after the lock is released and only if the plan file
// still holds what this call wrote; SavePlan does not take the state lock, so
// the restore cannot deadlock and must not clobber a concurrent edit.
func (env *effortEnv) mutate(fn func(st *store.State, p *plan.Plan) error) error {
	if err := env.store.EnsureDir(); err != nil {
		return err
	}
	var original, rendered string
	planWritten := false
	err := env.store.MutateState(func(st *store.State) (bool, error) {
		content, err := env.store.LoadPlan()
		if err != nil {
			return false, fmt.Errorf("failed to load plan: %w", err)
		}
		p, err := plan.Parse(content)
		if err != nil {
			return false, fmt.Errorf("failed to parse plan: %w", err)
		}
		for _, d := range p.Diagnostics {
			fmt.Fprintf(env.out, "warning: %s\n", d)
		}
		if err := fn(st, p); err != nil {
			return false, err
		}
		if out := p.Render(); out != content {
			if err := env.store.SavePlan(out); err != nil {
				return false, fmt.Errorf("failed to save plan (state left unchanged): %w", err)
			}
			original, rendered, planWritten = content, out, true
		}
		return true, nil
	})
	if err == nil || !planWritten {
		return err
	}
	current, lerr := env.store.LoadPlan()
	switch {
	case lerr != nil:
		return fmt.Errorf("%w; could not re-read the plan to restore it: %w", err, lerr)
	case current != rendered:
		return fmt.Errorf("%w; the plan was changed externally and was not restored", err)
	}
	if rerr := env.store.SavePlan(original); rerr != nil {
		return fmt.Errorf("%w; restoring the previous plan also failed: %w", err, rerr)
	}
	return fmt.Errorf("%w (plan restored)", err)
}

// effortNotFoundError distinguishes "no such effort" from an ambiguous name.
type effortNotFoundError struct{ msg string }

func (e effortNotFoundError) Error() string { return e.msg }

// findEffort resolves a name to an effort ID in state or the plan: the ID the
// name generates first, then a case-insensitive name match.
func findEffort(st *store.State, p *plan.Plan, name string) (string, error) {
	id := plan.GenerateEffortID(name)
	if _, ok := st.Efforts[id]; ok {
		return id, nil
	}
	if _, ok := p.Efforts[id]; ok {
		return id, nil
	}
	var matches []string
	for eid, e := range st.Efforts {
		if strings.EqualFold(strings.TrimSpace(e.Name), strings.TrimSpace(name)) {
			matches = append(matches, eid)
		}
	}
	for eid, e := range p.Efforts {
		if strings.EqualFold(strings.TrimSpace(e.Name), strings.TrimSpace(name)) && !slices.Contains(matches, eid) {
			matches = append(matches, eid)
		}
	}
	sort.Strings(matches)
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		known := knownEffortNames(st, p)
		msg := fmt.Sprintf("effort %q not found", name)
		if len(known) > 0 {
			msg += "; known efforts: " + strings.Join(known, ", ")
		}
		return "", effortNotFoundError{fmt.Sprintf("%s. Create it with: wgo plan effort add %q", msg, name)}
	default:
		return "", fmt.Errorf("effort name %q is ambiguous, it matches IDs %s; fix the duplicate in the plan or state", name, strings.Join(matches, ", "))
	}
}

func knownEffortNames(st *store.State, p *plan.Plan) []string {
	set := map[string]bool{}
	for _, e := range st.Efforts {
		set[fmt.Sprintf("%q", e.Name)] = true
	}
	for _, e := range p.Efforts {
		set[fmt.Sprintf("%q", e.Name)] = true
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (env *effortEnv) add(name, description string) error {
	name = strings.TrimSpace(name)
	if err := plan.ValidateEffort(plan.EffortEntry{Name: name, Description: description}); err != nil {
		return err
	}
	if err := env.mutate(func(st *store.State, p *plan.Plan) error {
		id, err := findEffort(st, p, name)
		var notFound effortNotFoundError
		switch {
		case err == nil:
			return env.addExisting(st, p, id, name, description)
		case !errors.As(err, &notFound):
			return err
		}
		id = plan.GenerateEffortID(name)
		now := time.Now()
		st.Efforts[id] = store.Effort{Name: name, Description: description, Branches: []string{}, CreatedAt: now, UpdatedAt: now}
		p.Efforts[id] = plan.EffortEntry{ID: id, Name: name, Description: description}
		return nil
	}); err != nil {
		return err
	}
	fmt.Fprintf(env.out, "Added effort: %s\n", name)
	return nil
}

// addExisting handles `add` for a name that is already an effort. When it
// exists on only one side, the other side is projected from it (a hand-written
// plan effort wins); when on both, it is an error. A differing --description
// is never silently dropped.
func (env *effortEnv) addExisting(st *store.State, p *plan.Plan, id, name, description string) error {
	se, inState := st.Efforts[id]
	pe, inPlan := p.Efforts[id]
	if inState && inPlan {
		return fmt.Errorf("effort %q already exists; use `wgo plan effort link %q <repo:bookmark>` to add bookmarks", name, name)
	}
	existing := pe.Description
	if inState {
		existing = se.Description
	}
	if description != "" && description != existing {
		return fmt.Errorf("effort %q already exists with description %q; edit it with: wgo plan edit", name, existing)
	}
	now := time.Now()
	if !inState {
		// A hand-written plan effort wins: adopt its fields.
		se = store.Effort{Name: pe.Name, Description: pe.Description, Branches: append([]string{}, pe.Branches...), CreatedAt: now, UpdatedAt: now}
		st.Efforts[id] = se
	} else {
		p.Efforts[id] = plan.EffortEntry{ID: id, Name: se.Name, Description: se.Description, Branches: planRefs(se.Branches, nil)}
	}
	return nil
}

// planRefs converts state references for the plan. Without clone information
// they are written as stored.
func planRefs(refs []string, clones []effort.MainCloneInfo) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, displayFor(r, clones, nil))
	}
	return out
}

// displayFor returns the plan form of a stored reference. A reference that
// does not resolve is returned as stored; warn, when non-nil, is told why.
func displayFor(ref string, clones []effort.MainCloneInfo, warn func(ref string, err error)) string {
	if clones == nil {
		return ref
	}
	clone, bookmark, err := effort.ResolveRef(ref, clones)
	if err != nil {
		if warn != nil {
			warn(ref, err)
		}
		return ref
	}
	return effort.DisplayRef(clone, bookmark, clones)
}

// normalizeFor returns the state form of a plan reference, or the reference
// unchanged when it does not resolve to one clone; warn, when non-nil, is
// told why.
func normalizeFor(ref string, clones []effort.MainCloneInfo, warn func(ref string, err error)) string {
	clone, bookmark, err := effort.ResolveRef(ref, clones)
	if err != nil {
		if warn != nil {
			warn(ref, err)
		}
		return ref
	}
	return effort.NormalizeBookmarkRef(clone.Path, bookmark)
}

// keptAsWritten returns the warn callback for displayFor and normalizeFor.
func (env *effortEnv) keptAsWritten(ref string, err error) {
	fmt.Fprintf(env.out, "warning: reference %s was kept as written (%v)\n", ref, err)
}

// sameRef reports whether a stored or plan reference names the given
// normalized clone path and bookmark.
func sameRef(ref, normalized string, clones []effort.MainCloneInfo) bool {
	if ref == normalized {
		return true
	}
	clone, bookmark, err := effort.ResolveRef(ref, clones)
	if err != nil {
		return false
	}
	return effort.NormalizeBookmarkRef(clone.Path, bookmark) == normalized
}

// resolveLinkRef resolves a user-supplied repo:bookmark against the
// discovered main clones.
func (env *effortEnv) resolveLinkRef(ref string) (normalized, display string, clones []effort.MainCloneInfo, err error) {
	if _, _, ok := strings.Cut(ref, ":"); !ok {
		return "", "", nil, fmt.Errorf("invalid bookmark %q: use repo:bookmark, e.g. wgo:%s", ref, ref)
	}
	clones, err = env.clones()
	if err != nil {
		return "", "", nil, err
	}
	clone, bookmark, err := effort.ResolveRef(ref, clones)
	if err != nil {
		return "", "", nil, err
	}
	return effort.NormalizeBookmarkRef(clone.Path, bookmark), effort.DisplayRef(clone, bookmark, clones), clones, nil
}

func (env *effortEnv) link(effortName, ref string) error {
	normalized, display, clones, err := env.resolveLinkRef(ref)
	if err != nil {
		return err
	}
	var name string
	if err := env.mutate(func(st *store.State, p *plan.Plan) error {
		id, err := findEffort(st, p, effortName)
		if err != nil {
			return err
		}
		se, inState := st.Efforts[id]
		pe, inPlan := p.Efforts[id]
		if !inState {
			// Plan-only effort: start its state record from the plan.
			now := time.Now()
			se = store.Effort{Name: pe.Name, Description: pe.Description, Branches: []string{}, CreatedAt: now}
			for _, b := range pe.Branches {
				se.Branches = append(se.Branches, normalizeFor(b, clones, env.keptAsWritten))
			}
		}
		name = se.Name
		stateHas := slices.ContainsFunc(se.Branches, func(b string) bool { return sameRef(b, normalized, clones) })
		planHas := slices.ContainsFunc(pe.Branches, func(b string) bool { return sameRef(b, normalized, clones) })
		if inState && inPlan && stateHas && planHas {
			return fmt.Errorf("bookmark %s is already linked to effort %q; unlink it with: wgo plan effort unlink %q %s", display, name, name, display)
		}
		if !stateHas {
			se.Branches = append(se.Branches, normalized)
		}
		se.UpdatedAt = time.Now()
		st.Efforts[id] = se
		if !inPlan {
			pe = plan.EffortEntry{ID: id, Name: se.Name, Description: se.Description, Branches: make([]string, 0, len(se.Branches))}
			for _, b := range se.Branches {
				pe.Branches = append(pe.Branches, displayFor(b, clones, env.keptAsWritten))
			}
		} else if !planHas {
			pe.Branches = append(pe.Branches, display)
		}
		p.Efforts[id] = pe
		return nil
	}); err != nil {
		return err
	}
	fmt.Fprintf(env.out, "Linked %s to effort: %s\n", display, name)
	return nil
}

func (env *effortEnv) unlink(effortName, ref string) error {
	normalized, display, clones, err := env.resolveLinkRef(ref)
	// A reference whose clone is no longer discovered (moved, deleted,
	// discovery.base_dirs changed) can still be unlinked by its exact text.
	resolveErr := err
	resolved := err == nil
	if !resolved {
		normalized, display, clones = ref, ref, nil
		if _, _, ok := plan.ParseBranchRef(ref); !ok {
			return resolveErr
		}
	}
	var name string
	if err := env.mutate(func(st *store.State, p *plan.Plan) error {
		id, err := findEffort(st, p, effortName)
		if err != nil {
			return err
		}
		match := func(b string) bool { return b == ref || (resolved && sameRef(b, normalized, clones)) }
		se, inState := st.Efforts[id]
		pe, inPlan := p.Efforts[id]
		name = se.Name
		if !inState {
			name = pe.Name
		}
		stateHas := inState && slices.ContainsFunc(se.Branches, match)
		planHas := inPlan && slices.ContainsFunc(pe.Branches, match)
		if !stateHas && !planHas {
			return fmt.Errorf("bookmark %s is not linked to effort %q; see the effort's entries in the plan (wgo plan)", display, name)
		}
		if stateHas {
			se.Branches = slices.DeleteFunc(se.Branches, match)
			se.UpdatedAt = time.Now()
			st.Efforts[id] = se
		}
		if planHas {
			pe.Branches = slices.DeleteFunc(pe.Branches, match)
			p.Efforts[id] = pe
		}
		return nil
	}); err != nil {
		if !resolved {
			return fmt.Errorf("%w (reference %s no longer resolves: %v)", err, ref, resolveErr)
		}
		return err
	}
	if !resolved {
		fmt.Fprintf(env.out, "reference %s no longer resolves to a discovered clone; unlinked it by its exact text\n", ref)
	}
	fmt.Fprintf(env.out, "Unlinked %s from effort: %s\n", display, name)
	return nil
}

func (env *effortEnv) remove(effortName string, force bool) error {
	var name string
	dup := false
	if err := env.mutate(func(st *store.State, p *plan.Plan) error {
		id, err := findEffort(st, p, effortName)
		if err != nil {
			return err
		}
		name = effortName
		if e, ok := st.Efforts[id]; ok {
			name = e.Name
		} else if e, ok := p.Efforts[id]; ok {
			name = e.Name
		}
		if lines := p.EffortUnparsedLines(id); len(lines) > 0 && !force {
			return fmt.Errorf("the plan block for effort %q holds text wgo does not interpret and removing it would delete it:\n  %s\nrerun with --force, or move them out with: wgo plan edit",
				name, strings.Join(lines, "\n  "))
		}
		dup = p.EffortHasDuplicateHeading(id)
		if dup {
			fmt.Fprintf(env.out, "warning: the plan has more than one %q block; only the first is removed\n", "### "+name)
		}
		delete(st.Efforts, id)
		delete(p.Efforts, id)
		return nil
	}); err != nil {
		return err
	}
	if dup {
		fmt.Fprintf(env.out, "Removed effort %s from state; the plan still has another ### %s block, so it will be read as this effort. Edit it with: wgo plan edit\n", name, name)
		return nil
	}
	fmt.Fprintf(env.out, "Removed effort: %s\n", name)
	return nil
}
