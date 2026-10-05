package cmd

import (
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
)

// planEffortCmd represents the `wgo plan effort` command group.
var planEffortCmd = &cobra.Command{
	Use:   "effort",
	Short: "Manage efforts (cross-repo features)",
	Long: `Manage efforts that group related work across repositories.

Each command updates state.json and the "## Efforts" section of the plan
together; if the plan cannot be written, state is left unchanged.

Bookmarks are named repo:bookmark, where repo is a discovered main clone's
directory name, its owner/repo, or its absolute path. State stores the clone's
absolute path; the plan shows the shortest name that is unambiguous.`,
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
	Long:  `Remove an effort and its associations from state and the plan.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEffort(func(env *effortEnv) error { return env.remove(args[0]) })
	},
}

func init() {
	planCmd.AddCommand(planEffortCmd)
	planEffortCmd.AddCommand(effortAddCmd)
	planEffortCmd.AddCommand(effortLinkCmd)
	planEffortCmd.AddCommand(effortUnlinkCmd)
	planEffortCmd.AddCommand(effortRemoveCmd)

	effortAddCmd.Flags().StringVarP(&effortDescription, "description", "d", "", "Description of the effort")
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
// failed plan write leaves state untouched, and a failed state write restores
// the previous plan. SavePlan does not take the state lock, so this cannot
// deadlock.
func (env *effortEnv) mutate(fn func(st *store.State, p *plan.Plan) error) error {
	if err := env.store.EnsureDir(); err != nil {
		return err
	}
	var original string
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
		if rendered := p.Render(); rendered != content {
			if err := env.store.SavePlan(rendered); err != nil {
				return false, fmt.Errorf("failed to save plan (state left unchanged): %w", err)
			}
			original, planWritten = content, true
		}
		return true, nil
	})
	if err != nil && planWritten {
		if rerr := env.store.SavePlan(original); rerr != nil {
			return fmt.Errorf("%w; restoring the previous plan also failed: %v", err, rerr)
		}
	}
	return err
}

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
		return "", fmt.Errorf("%s. Create it with: wgo plan effort add %q", msg, name)
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
	if name == "" {
		return fmt.Errorf("effort name must not be empty")
	}
	id := plan.GenerateEffortID(name)
	if err := env.mutate(func(st *store.State, p *plan.Plan) error {
		_, inState := st.Efforts[id]
		_, inPlan := p.Efforts[id]
		if inState && inPlan {
			return fmt.Errorf("effort %q already exists; use `wgo plan effort link %q <repo:bookmark>` to add bookmarks", name, name)
		}
		now := time.Now()
		if !inState {
			e := store.Effort{Name: name, Description: description, Branches: []string{}, CreatedAt: now, UpdatedAt: now}
			if pe, ok := p.Efforts[id]; ok {
				// A hand-written plan effort wins: adopt its fields.
				e.Description = pe.Description
				e.Branches = slices.Clone(pe.Branches)
				if e.Branches == nil {
					e.Branches = []string{}
				}
			}
			st.Efforts[id] = e
		}
		if !inPlan {
			se := st.Efforts[id]
			p.Efforts[id] = plan.EffortEntry{ID: id, Name: se.Name, Description: se.Description, Branches: planRefs(se.Branches, nil)}
		}
		return nil
	}); err != nil {
		return err
	}
	fmt.Fprintf(env.out, "Added effort: %s\n", name)
	return nil
}

// planRefs converts state references for the plan. Without clone information
// they are written as stored.
func planRefs(refs []string, clones []effort.MainCloneInfo) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, displayFor(r, clones))
	}
	return out
}

// displayFor returns the plan form of a stored reference.
func displayFor(ref string, clones []effort.MainCloneInfo) string {
	if clones == nil {
		return ref
	}
	clone, bookmark, err := effort.ResolveRef(ref, clones)
	if err != nil {
		return ref
	}
	return effort.DisplayRef(clone, bookmark, clones)
}

// normalizeFor returns the state form of a plan reference, or the
// reference unchanged when it does not resolve to one clone.
func normalizeFor(ref string, clones []effort.MainCloneInfo) string {
	clone, bookmark, err := effort.ResolveRef(ref, clones)
	if err != nil {
		return ref
	}
	return effort.NormalizeBookmarkRef(clone.Path, bookmark)
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
				se.Branches = append(se.Branches, normalizeFor(b, clones))
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
			pe = plan.EffortEntry{ID: id, Name: se.Name, Description: se.Description, Branches: planRefs(se.Branches, clones)}
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
	if err != nil {
		return err
	}
	var name string
	if err := env.mutate(func(st *store.State, p *plan.Plan) error {
		id, err := findEffort(st, p, effortName)
		if err != nil {
			return err
		}
		match := func(b string) bool { return sameRef(b, normalized, clones) }
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
		return err
	}
	fmt.Fprintf(env.out, "Unlinked %s from effort: %s\n", display, name)
	return nil
}

func (env *effortEnv) remove(effortName string) error {
	var name string
	dupWarning := false
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
		delete(st.Efforts, id)
		delete(p.Efforts, id)
		heading := "### " + name
		for _, d := range p.Diagnostics {
			if strings.Contains(d, "duplicate effort heading") && strings.Contains(d, fmt.Sprintf("%q", heading)) {
				dupWarning = true
			}
		}
		return nil
	}); err != nil {
		return err
	}
	fmt.Fprintf(env.out, "Removed effort: %s\n", name)
	if dupWarning {
		fmt.Fprintf(env.out, "warning: the plan has another %q block; it was kept and will be read as this effort. Edit it with: wgo plan edit\n", "### "+name)
	}
	return nil
}
