package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	gh "github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/jj"
)

var (
	parkDryRun     bool
	parkNoBookmark bool
	parkName       string
)

var parkCmd = &cobra.Command{
	Use:   "park [repo-path]",
	Short: "Move in-progress work out of a main clone into its own workspace",
	Long: `A main clone under worktree.mains_dir is the trunk checkout: ` + "`wgo to`" + ` returns
it for trunk URLs, and wgo expects its @ to be a clean empty change on trunk.
When work accumulates there instead, ` + "`wgo park`" + ` relocates it to
<worktrees_dir>/<slug>/<repo> where it belongs.

Nothing is copied. jj workspaces share one repo store, so the work is simply
claimed by a new workspace and the clone's @ is returned to trunk.

The destination path is printed to stdout:
  cd $(wgo park)`,
	Args:              cobra.MaximumNArgs(1),
	ValidArgsFunction: cobra.NoFileCompletions,
	RunE:              runParkCmd,
	SilenceUsage:      true,
}

func init() {
	rootCmd.AddCommand(parkCmd)
	parkCmd.Flags().BoolVar(&parkDryRun, "dry-run", false, "Print the plan and exit without changing anything")
	parkCmd.Flags().BoolVar(&parkNoBookmark, "no-bookmark", false, "Do not create a bookmark for unbookmarked work")
	parkCmd.Flags().StringVar(&parkName, "name", "", "Destination slug (and bookmark name) to use instead of the derived one")
}

// logPark prints to stderr so stdout stays clean for cd $(...), mirroring logTo.
func logPark(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// parkOpts carries the command's flags so the core stays free of package
// globals and tests can drive it directly.
type parkOpts struct {
	DryRun     bool
	NoBookmark bool
	Name       string
}

// parkPlan is the fully-resolved description of a park. It is produced by a
// read-only pass so --dry-run and the real run describe themselves identically,
// and so `wgo to` can render the same summary as a warning with no risk of
// mutating the repo.
type parkPlan struct {
	// RepoPath is the main workspace root the work is being moved out of.
	RepoPath string
	// Repo is the directory name of RepoPath, used as the last path segment
	// of Dest so the layout matches createWorktree's.
	Repo string
	// Trunk is the trunk bookmark name, "" when jj could not name one.
	Trunk string
	// TrunkRevset names the commit @ is returned to.
	TrunkRevset string
	// Head is the main workspace's @ after jj's implicit snapshot.
	Head jj.Change
	// Work is (trunk)..@, newest first — everything being moved.
	Work []jj.Change
	// DirtyFiles is display only; Head.Empty is the authority on whether the
	// working copy carries uncommitted edits.
	DirtyFiles []string
	// Bookmark is an existing non-trunk bookmark already carrying the work,
	// reused as the slug when present.
	Bookmark string
	// CreateBookmark is the bookmark to create, "" when reusing Bookmark or
	// when --no-bookmark was passed.
	CreateBookmark string
	// Slug is the sanitized directory name under worktrees_dir.
	Slug string
	// WorkspaceName is the jj workspace id (slash-free by construction).
	WorkspaceName string
	// Dest is the destination workspace root.
	Dest string
	// destPreexisting records whether Dest was already on disk at plan time,
	// so rollback never removes a directory this command did not create.
	destPreexisting bool
	// resumeWorkspace is true when a workspace of the same name is already
	// registered at Dest, making the WorkspaceAdd step a no-op.
	resumeWorkspace bool
}

// ref names the revision the destination workspace should land on. The change
// id is used rather than the commit id throughout: a working-copy snapshot
// between two of our own jj invocations rewrites the commit id but preserves
// the change id, and jj carries bookmarks onto rewritten commits.
func (p *parkPlan) ref() string {
	if p.CreateBookmark != "" {
		return p.CreateBookmark
	}
	if p.Bookmark != "" {
		return p.Bookmark
	}
	return p.Head.ChangeID
}

func runParkCmd(_ *cobra.Command, args []string) error {
	if err := config.Init(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	cfg := config.Get()
	if cfg.Worktree.WorktreesDir == "" {
		return fmt.Errorf("worktree.worktrees_dir not configured; set it in ~/.wgo/config.toml")
	}

	jjc := jj.NewCLI()

	target := ""
	if len(args) > 0 {
		target = args[0]
	}
	repoPath, err := resolveParkTarget(jjc, target)
	if err != nil {
		return err
	}

	return runPark(jjc, cfg, repoPath, parkOpts{
		DryRun:     parkDryRun,
		NoBookmark: parkNoBookmark,
		Name:       parkName,
	})
}

// resolveParkTarget maps the command argument (or the cwd, honouring -C) to the
// main workspace root of the containing repo. Running from inside a secondary
// workspace is an error rather than a silent retarget: "park this" is
// ambiguous there, and the user almost certainly meant a different directory.
func resolveParkTarget(jjc jj.Client, arg string) (string, error) {
	target := arg
	if target == "" {
		cwd, err := resolveCwd()
		if err != nil {
			return "", err
		}
		target = cwd
	}
	if !jjc.IsRepo(target) {
		return "", fmt.Errorf("%s is not inside a jj repository", target)
	}

	root, err := jjc.WorkspaceRoot(target)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root for %s: %w", target, err)
	}
	main, err := jjc.MainWorkspaceRoot(target)
	if err != nil {
		return "", fmt.Errorf("resolve main workspace for %s: %w", target, err)
	}
	if absResolved(root) != absResolved(main) {
		return "", fmt.Errorf("%s is a feature workspace, not a main clone; "+
			"run `wgo park %s` to park work stranded in its main clone", root, main)
	}
	return main, nil
}

// runPark is the testable core: everything the cobra wrapper does after config
// loading and target resolution.
func runPark(jjc jj.Client, cfg *config.Config, repoPath string, opts parkOpts) error {
	p, ok, err := planPark(jjc, cfg, repoPath, opts)
	if err != nil {
		return err
	}
	if !ok {
		logPark("nothing to park: @ in %s is a clean empty change on trunk", repoPath)
		return nil
	}

	if err := preflightPark(jjc, p); err != nil {
		return err
	}

	describeParkPlan(p, "")

	if opts.DryRun {
		logPark("dry run: nothing was changed")
		return nil
	}

	if err := applyPark(jjc, p); err != nil {
		return err
	}

	fmt.Println(p.Dest)
	return nil
}

// planPark inspects the main workspace at repoPath and resolves what work, if
// any, is stranded there and where it should go.
//
// ok is false — with a nil plan — when @ is a plain empty change on trunk, the
// expected resting state of a main clone. Read-only apart from the working-copy
// snapshot jj takes implicitly when CurrentChange runs inside the workspace.
func planPark(jjc jj.Client, cfg *config.Config, repoPath string, opts parkOpts) (*parkPlan, bool, error) {
	// Runs inside the workspace, so jj snapshots the working copy first. Every
	// later emptiness judgement depends on that having happened.
	head, err := jjc.CurrentChange(repoPath)
	if err != nil {
		return nil, false, fmt.Errorf("read @ in %s: %w", repoPath, err)
	}

	trunk := localTrunkBookmark(jjc, repoPath)
	trunkRev := trunkRevset(jjc, repoPath, trunk)
	if trunkRev == "" {
		return nil, false, fmt.Errorf("no trunk found in %s to return @ to; "+
			"run `jj -R %s git fetch` first", repoPath, repoPath)
	}

	span := fmt.Sprintf("(%s)..(%s)", trunkRev, head.ChangeID)
	n, err := jjc.CountRevset(repoPath, span)
	if err != nil {
		return nil, false, fmt.Errorf("count %s in %s: %w", span, repoPath, err)
	}
	switch {
	case n == 0:
		// @ is trunk itself or an ancestor of it.
		return nil, false, nil
	case n == 1 && head.Empty && strings.TrimSpace(head.Description) == "":
		// The expected resting state: a fresh empty change on top of trunk.
		return nil, false, nil
	}

	work, err := jjc.Log(repoPath, span)
	if err != nil {
		return nil, false, fmt.Errorf("log %s in %s: %w", span, repoPath, err)
	}

	existing := ""
	if bm, err := jjc.NearestBookmark(repoPath); err == nil && bm != "" && bm != trunk {
		existing = bm
	}

	slug := parkSlug(work, head, existing, opts.Name)
	if slug == "" {
		return nil, false, fmt.Errorf("could not derive a name for the work in %s; pass --name", repoPath)
	}

	create := ""
	if existing == "" && !opts.NoBookmark {
		create = slug
	}

	_, dirty, _ := jjc.IsClean(repoPath)

	dest := filepath.Join(cfg.Worktree.WorktreesDir, slug, filepath.Base(repoPath))
	_, statErr := os.Stat(dest)

	return &parkPlan{
		RepoPath:        repoPath,
		Repo:            filepath.Base(repoPath),
		Trunk:           trunk,
		TrunkRevset:     trunkRev,
		Head:            head,
		Work:            work,
		DirtyFiles:      dirty,
		Bookmark:        existing,
		CreateBookmark:  create,
		Slug:            slug,
		WorkspaceName:   slug,
		Dest:            dest,
		destPreexisting: statErr == nil,
	}, true, nil
}

// parkSlug picks the destination slug for stranded work, in priority order:
// an explicit --name; an existing non-trunk bookmark already carrying the work
// (wgo keys workspaces on bookmarks, so reusing it keeps `wgo to owner/repo@<b>`
// working); a slug derived from the newest description; else a stable
// wip-<change id> so a retry after a failure lands in the same place.
func parkSlug(work []jj.Change, head jj.Change, existingBookmark, override string) string {
	if override != "" {
		return gh.SanitizeBranch(override)
	}
	if existingBookmark != "" {
		return gh.SanitizeBranch(existingBookmark)
	}
	for _, c := range work {
		line := strings.TrimSpace(strings.SplitN(c.Description, "\n", 2)[0])
		if line == "" {
			continue
		}
		if s := truncateSlug(gh.SanitizeBranch(strings.ToLower(line)), 40); s != "" {
			return s
		}
	}
	if id := head.ChangeID; len(id) >= 8 {
		return "wip-" + id[:8]
	}
	return ""
}

// preflightPark runs every check that must pass before the first mutation, so a
// rejected park leaves the repo byte-identical.
func preflightPark(jjc jj.Client, p *parkPlan) error {
	// A divergent change id would make every revset below ambiguous and leave
	// EditChange to fail halfway through the sequence.
	if n, err := jjc.CountRevset(p.RepoPath, p.Head.ChangeID); err == nil && n > 1 {
		return fmt.Errorf("change %s is divergent (%d commits share it); "+
			"resolve with `jj -R %s abandon` or `jj -R %s duplicate` first",
			p.Head.ChangeID, n, p.RepoPath, p.RepoPath)
	}

	if p.destPreexisting {
		entries, err := os.ReadDir(p.Dest)
		if err != nil {
			return fmt.Errorf("destination %s exists but is not readable: %w", p.Dest, err)
		}
		if len(entries) > 0 && !jjc.IsRepo(p.Dest) {
			return fmt.Errorf("destination %s already exists and is not a jj workspace; pass --name", p.Dest)
		}
	}

	workspaces, err := jjc.ListWorkspaces(p.RepoPath)
	if err != nil {
		return fmt.Errorf("list workspaces in %s: %w", p.RepoPath, err)
	}
	for _, ws := range workspaces {
		if ws.Name != p.WorkspaceName {
			continue
		}
		if absResolved(ws.Path) == absResolved(p.Dest) {
			// Same name, same place: a previous run got partway. Resume.
			p.resumeWorkspace = true
			break
		}
		return fmt.Errorf("workspace %q already exists at %s; pass --name to use a different one",
			p.WorkspaceName, ws.Path)
	}

	if p.Bookmark != "" {
		// Reusing a bookmark: make sure no other workspace is already sitting
		// on it, and that it is not conflicted.
		for _, ws := range workspaces {
			if absResolved(ws.Path) == absResolved(p.RepoPath) || absResolved(ws.Path) == absResolved(p.Dest) {
				continue
			}
			if currentBookmark(jjc, ws.Path) == p.Bookmark {
				return fmt.Errorf("work is already bookmarked %q and workspace %s holds it; "+
					"run `jj -R %s edit %s` there instead", p.Bookmark, ws.Path, ws.Path, p.Bookmark)
			}
		}
		bms, err := jjc.BookmarkList(p.RepoPath, jj.BookmarkListOpts{Local: true, Names: []string{p.Bookmark}})
		if err == nil {
			for _, b := range bms {
				if b.Name == p.Bookmark && b.Remote == "" && b.Conflict {
					return fmt.Errorf("bookmark %q is conflicted; resolve it with "+
						"`jj -R %s bookmark set %s -r <rev>` before parking", p.Bookmark, p.RepoPath, p.Bookmark)
				}
			}
		}
	}

	if p.CreateBookmark != "" {
		bms, err := jjc.BookmarkList(p.RepoPath, jj.BookmarkListOpts{Local: true, Names: []string{p.CreateBookmark}})
		if err == nil {
			for _, b := range bms {
				if b.Name == p.CreateBookmark && b.Remote == "" && b.Present {
					return fmt.Errorf("bookmark %q already exists in %s; pass --name to use a different one",
						p.CreateBookmark, p.RepoPath)
				}
			}
		}
	}

	return nil
}

// applyPark performs the move.
//
// The ordering is load-bearing:
//
//	M1 BookmarkCreate  anchors the work by name before anything moves, so even a
//	                   total failure below leaves it findable via `jj log -r <slug>`.
//	M2 WorkspaceAdd    populates the destination before the source is cleared.
//	                   `jj workspace add -r X` makes the new @ a *child* of X
//	                   (jj's own wording: "as if you had run `jj new X`"), so at
//	                   this instant no two workspaces share a change.
//	M3 New             returns the clone's @ to trunk. Must precede M5: while both
//	                   workspaces resolve @ to the same change, each snapshot
//	                   rewrites the shared commit and marks the other stale.
//	M4 UpdateStale     M3 advanced the operation head, and `jj edit` errors in a
//	                   stale workspace. A no-op on a healthy one, so it is free.
//	M5 EditChange      makes the destination's @ exactly what the clone's was.
//	                   The empty undescribed child from M2 is auto-abandoned.
//
// Rollback runs in reverse over whichever steps completed. `jj op restore` is
// deliberately not used: it is repo-global and would clobber concurrent work in
// the repo's other workspaces.
func applyPark(jjc jj.Client, p *parkPlan) error {
	ref := p.ref()

	bookmarkCreated := false
	if p.CreateBookmark != "" {
		logPark("creating bookmark %s...", p.CreateBookmark)
		if err := jjc.BookmarkCreate(p.RepoPath, p.CreateBookmark, p.Head.ChangeID); err != nil {
			return fmt.Errorf("create bookmark %s: %w", p.CreateBookmark, err)
		}
		bookmarkCreated = true
	}

	undoBookmark := func() {
		if bookmarkCreated {
			_ = jjc.BookmarkDelete(p.RepoPath, p.CreateBookmark)
		}
	}

	if !p.resumeWorkspace {
		if err := os.MkdirAll(filepath.Dir(p.Dest), 0o755); err != nil {
			undoBookmark()
			return fmt.Errorf("create workspace parent %s: %w", filepath.Dir(p.Dest), err)
		}
		logPark("creating workspace %s at %s...", p.WorkspaceName, p.Dest)
		if err := jjc.WorkspaceAdd(p.RepoPath, p.Dest, jj.WorkspaceAddOpts{Name: p.WorkspaceName, Revset: ref}); err != nil {
			undoBookmark()
			return fmt.Errorf("workspace add %s: %w%s", p.Dest, err, parkRecoveryHint(p))
		}
	}

	logPark("returning @ in %s to %s...", p.RepoPath, p.TrunkRevset)
	if err := jjc.New(p.RepoPath, p.TrunkRevset, ""); err != nil {
		if !p.resumeWorkspace {
			_ = jjc.WorkspaceForget(p.RepoPath, p.WorkspaceName)
			if !p.destPreexisting {
				_ = os.RemoveAll(p.Dest)
			}
		}
		undoBookmark()
		return fmt.Errorf("return @ to trunk in %s: %w%s", p.RepoPath, err, parkRecoveryHint(p))
	}

	_ = jjc.UpdateStale(p.Dest)

	// Past this point the move has succeeded: the destination holds the work as
	// an ancestor either way, so a failure here costs only the working-copy
	// shape and must not fail the command.
	if err := jjc.EditChange(p.Dest, ref); err != nil {
		logPark("warning: could not move @ in %s onto %s: %v", p.Dest, ref, err)
		logPark("         the work is there as an ancestor; run `jj -R %s edit %s` to sit on it", p.Dest, ref)
	}

	return nil
}

// parkRecoveryHint appends the command that recovers the work by hand, per the
// project's rule that errors suggest the fix.
func parkRecoveryHint(p *parkPlan) string {
	name := p.Head.ChangeID
	if p.CreateBookmark != "" {
		name = fmt.Sprintf("%s (bookmark %s)", p.Head.ChangeID, p.CreateBookmark)
	} else if p.Bookmark != "" {
		name = fmt.Sprintf("%s (bookmark %s)", p.Head.ChangeID, p.Bookmark)
	}
	return fmt.Sprintf("\nyour work is intact at change %s; recover with: jj -R %s edit %s",
		name, p.RepoPath, p.Head.ChangeID)
}

// describeParkPlan renders a plan to stderr. Shared by `wgo park` (as the
// preview, printed identically for --dry-run and the real run so the preview is
// trustworthy) and by `wgo to` (as the drift warning), so the two commands can
// never disagree about what counts as stranded work.
func describeParkPlan(p *parkPlan, prefix string) {
	trunk := p.Trunk
	if trunk == "" {
		trunk = p.TrunkRevset
	}
	logPark("%s@ in %s is not on %s", prefix, p.RepoPath, trunk)

	detail := fmt.Sprintf("%d change(s)", len(p.Work))
	if n := len(p.DirtyFiles); n > 0 {
		detail += fmt.Sprintf(", %d uncommitted file(s)", n)
	}
	if p.Bookmark != "" {
		detail += fmt.Sprintf(", bookmark %s", p.Bookmark)
	} else {
		detail += ", no bookmark"
	}
	logPark("%s  %s", prefix, detail)

	for _, c := range p.Work {
		line := strings.TrimSpace(strings.SplitN(c.Description, "\n", 2)[0])
		if line == "" {
			line = "(no description)"
		}
		logPark("%s  %s %s", prefix, shortChangeID(c.ChangeID), line)
	}

	if p.CreateBookmark != "" {
		logPark("%s  will create bookmark %s", prefix, p.CreateBookmark)
	}
	logPark("%s  destination: %s", prefix, p.Dest)
}

// shortChangeID trims a change id to jj's customary display length.
func shortChangeID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
