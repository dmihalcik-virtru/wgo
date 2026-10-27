// Package cmd provides CLI commands for wgo.
package cmd

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/cleanup"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/links"
	"github.com/virtru/wgo/internal/prcache"
	"github.com/virtru/wgo/internal/store"
)

var (
	cleanWorktrees bool
	cleanBranches  bool
	cleanRepos     bool
	cleanRemote    bool
	cleanDryRun    bool
	cleanForce     bool
	cleanYes       bool
)

func init() {
	cleanCmd.Flags().BoolVar(&cleanWorktrees, "worktrees", false, "Only clean worktrees, including orphaned workspaces")
	cleanCmd.Flags().BoolVar(&cleanBranches, "branches", false, "Only clean local branches")
	cleanCmd.Flags().BoolVar(&cleanRepos, "repos", false, "Only clean full repos")
	cleanCmd.Flags().BoolVar(&cleanRemote, "remote", false, "Only clean remote branches")
	cleanCmd.Flags().BoolVar(&cleanDryRun, "dry-run", false, "Preview only, no changes")
	cleanCmd.Flags().BoolVar(&cleanForce, "force", false, "Delete local bookmarks even when their merge cannot be verified")
	cleanCmd.Flags().BoolVar(&cleanYes, "yes", false, "Auto-confirm all removals")
	rootCmd.AddCommand(cleanCmd)
}

var cleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Remove stale workspaces, branches, and repos",
	Long: `Find and remove stale workspaces, merged/closed branches, and unused repos.

Every workspace is classified by whether it holds anything that exists nowhere
else, using only the jj DAG and GitHub's record of its PRs, so the same state
always gets the same answer. These are proven safe and --yes removes them:

  idle            no changes of its own beyond trunk, untouched for
                  status.stale_days
  landed          its changes are commits of a merged PR, or trunk already
                  contains their content, or they sit on a bookmark whose PR
                  merged
  deletions-only  its changes only delete files

Workspaces that may hold unique work are listed with the evidence found and
removed only when you confirm each one; --yes never removes them:

  superseded      an older revision of merged or bookmarked work
  closed-pr       its work is on a bookmark whose PR closed unmerged
  conflicted      its changes are unresolved conflicts
  scratch         described as scratch, or only lockfiles differ
  probably-landed subjects match trunk but the content differs
  unpushed        work on no bookmark or remote

Workspaces in use (active, planned, fresh, in-progress) are kept. The scan
never snapshots a workspace; before removing one, wgo snapshots it (updating
it first if it is stale) and classifies it again if its working copy changed,
so edits jj had not yet seen are never deleted. One that needed a decision is
skipped instead if it changed, since the evidence you confirmed predates it.

Also forgets jj workspaces whose directory is gone (the warnings wgo dash
shows). Forgetting never touches the disk, and --worktrees includes them.

Shows a dry-run summary first, then prompts y/n per item (or use --yes to skip).
Uses GitHub for PR state when a token is available.`,
	RunE: runClean,
}

// worktreeKinds are the candidates --worktrees selects: workspaces to delete
// and workspaces whose directory is already gone, which only need forgetting.
var worktreeKinds = []cleanup.CandidateKind{cleanup.KindWorktree, cleanup.KindOrphanWorkspace}

func runClean(cmd *cobra.Command, args []string) error {
	if err := config.Init(); err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	cfg := config.Get()

	jjc := jj.NewCLI()
	ghClient := github.NewClient()

	fmt.Fprintln(os.Stderr, "Scanning for cleanup candidates...")

	// Kind filters, if any are set, also limit what the scan looks at.
	var kinds []cleanup.CandidateKind
	if cleanWorktrees {
		kinds = append(kinds, worktreeKinds...)
	}
	if cleanBranches {
		kinds = append(kinds, cleanup.KindLocalBranch)
	}
	if cleanRepos {
		kinds = append(kinds, cleanup.KindRepo)
	}
	if cleanRemote {
		kinds = append(kinds, cleanup.KindRemoteBranch)
	}
	showWorkspaces := len(kinds) == 0 || cleanWorktrees

	scan, err := cleanup.FindCandidates(cfg, jjc, ghClient, kinds...)
	if err != nil {
		return fmt.Errorf("failed to find candidates: %w", err)
	}
	candidates := scan.Candidates
	if len(kinds) > 0 {
		candidates = cleanup.FilterKinds(candidates, kinds...)
	}

	// Stack-aware safety used to consult store.Annotation.Parents to keep
	// branches alive when something still pointed at them. The jj migration
	// removed that shadow state — jj's DAG is the single source of truth —
	// so we no longer second-guess the user's clean intent here. The user
	// (or `jj bookmark forget`) is responsible for breaking dependencies
	// first.

	remove, decide := splitByTier(candidates)
	if len(remove) > 0 {
		printCandidateTable(remove)
	}
	if len(decide) > 0 {
		printDecisions(decide)
	}
	if showWorkspaces {
		printKept(scan.Kept)
	}
	if len(remove) == 0 && len(decide) == 0 {
		fmt.Println("Nothing to clean.")
		return nil
	}

	if cleanDryRun {
		fmt.Printf("\n%d to remove, %d need a decision. Run without --dry-run to remove.\n", len(remove), len(decide))
		return nil
	}

	// Interactive or auto-confirm removal
	st, err := store.New()
	if err != nil {
		return fmt.Errorf("failed to open store: %w", err)
	}

	removed := 0
	var untrackedRepos []string
	scanner := bufio.NewScanner(os.Stdin)
	classifier := cleanup.NewScanClassifier(cfg, jjc, ghClient)
	// confirmAll starts as --yes and turns on when the user answers "all".
	// Either way it covers only proven candidates: a workspace that needs a
	// decision is always asked about on its own.
	confirmAll := cleanYes

	ordered := append(append([]Candidate(nil), remove...), decide...)
	for i, c := range ordered {
		mustDecide := c.Class != nil && c.Class.Tier == cleanup.TierDecide
		if mustDecide && cleanYes {
			fmt.Fprintf(os.Stderr, "\nNot removed with --yes: %d workspace(s) need a decision (listed above).\n", len(ordered)-i)
			break
		}

		if mustDecide || !confirmAll {
			prompt := "[y/N/a(ll)/q(uit)]"
			if mustDecide {
				prompt = "[y/N/q(uit)]"
				fmt.Printf("\n%s %s\n", c.Class.Type, c.DisplayPath())
				for _, line := range decisionLines(c) {
					fmt.Printf("    %s\n", line)
				}
			}
			promptPath := c.DisplayPath()
			promptReason := c.Reason
			if isTerminal() {
				remoteURL := getRemoteURL(jjc, c.RepoPath)
				if c.Branch != "" {
					if branchURL := links.BranchURL(remoteURL, c.Branch); branchURL != "" {
						promptPath = strings.Replace(promptPath, c.Branch, links.Hyperlink(branchURL, c.Branch), 1)
					}
				}
				promptReason = linkPRs(c, promptReason)
			}
			fmt.Printf("\n[%d/%d] Remove %s %s (%s)? %s: ",
				i+1, len(ordered), c.Kind, promptPath, promptReason, prompt)
			if !scanner.Scan() {
				break
			}
			ans := strings.ToLower(strings.TrimSpace(scanner.Text()))
			switch {
			case (ans == "a" || ans == "all") && !mustDecide:
				confirmAll = true
			case ans == "q" || ans == "quit":
				fmt.Println("Aborted.")
				return nil
			case ans == "y" || ans == "yes":
				// proceed
			default:
				fmt.Printf("  Skipped.\n")
				continue
			}
		}

		if c.Kind == cleanup.KindWorktree && c.Class != nil {
			if why := recheckWorkspace(c, jjc, classifier); why != "" {
				fmt.Fprintf(os.Stderr, "  Skipping %s: %s\n", c.DisplayPath(), why)
				continue
			}
		}

		if err := executeRemoval(c, jjc, ghClient, nil); err != nil {
			fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
		} else {
			fmt.Printf("  Removed %s %s\n", c.Kind, c.DisplayPath())
			removed++
			if c.Kind == KindRepo {
				untrackedRepos = append(untrackedRepos, c.RepoPath)
			}
		}
	}

	// Apply the untracks under the lock at the end (re-reading current state)
	// rather than holding it across the interactive prompts above.
	if len(untrackedRepos) > 0 {
		if err := st.MutateState(func(state *store.State) (bool, error) {
			for _, repoPath := range untrackedRepos {
				state.UntrackRepo(repoPath)
			}
			return true, nil
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to save state: %v\n", err)
		}
	}

	fmt.Printf("\nDone. Removed %d item(s).\n", removed)
	return nil
}

// splitByTier separates workspaces that need a decision from everything that
// is proven removable, keeping their order.
func splitByTier(candidates []Candidate) (remove, decide []Candidate) {
	for _, c := range candidates {
		if c.Class != nil && c.Class.Tier == cleanup.TierDecide {
			decide = append(decide, c)
		} else {
			remove = append(remove, c)
		}
	}
	sort.SliceStable(decide, func(i, j int) bool {
		if decide[i].Class.Type != decide[j].Class.Type {
			return decide[i].Class.Type < decide[j].Class.Type
		}
		return decide[i].Path < decide[j].Path
	})
	return remove, decide
}

// recheckWorkspace snapshots a workspace before it is removed — the scan read
// it without doing so — and classifies it again if its @ moved, so files edited
// since jj last looked are never deleted unexamined. It returns why the
// workspace must be skipped, or "". A proven workspace that classifies the
// same way is proven again; one that needed a decision is skipped whenever its
// @ moved, since the person confirmed evidence that predates the change.
//
// A workspace whose @ a fetch rebased is stale and cannot be snapshotted until
// it is updated. `jj workspace update-stale` keeps edits jj had not recorded,
// but in a divergent sibling of the new @ rather than in it, where a fresh
// classification would not look; any non-empty revision of the old @ change
// left beside the new @ therefore means there were such edits.
//
// Files jj does not track at all are not in any commit, so removing the
// directory would lose them: new files jj refused to snapshot (too large, or
// left out by snapshot.auto-track) and files outside a sparse workspace's
// patterns. Either stops the removal. Ignored files are removed, as `git
// worktree remove` removes them.
func recheckWorkspace(c Candidate, jjc *jj.CLIClient, classifier *cleanup.Classifier) string {
	st, err := jjc.Status(c.Path)
	if err != nil {
		if !strings.Contains(err.Error(), "working copy is stale") {
			return "could not snapshot it: " + jj.BriefError(err)
		}
		if err := jjc.UpdateStale(c.Path); err != nil {
			return "its working copy is stale and could not be updated: " + jj.BriefError(err)
		}
		if c.Class.AtChange != "" {
			kept, err := jjc.ReadOnly().Log(c.Path, "change_id("+c.Class.AtChange+") ~ empty() ~ @")
			if err != nil {
				return "could not check its updated working copy: " + jj.BriefError(err)
			}
			if len(kept) > 0 {
				return fmt.Sprintf("updating its stale working copy kept unrecorded edits in commit %s; review them before removing it",
					kept[0].CommitID[:min(12, len(kept[0].CommitID))])
			}
		}
		if st, err = jjc.Status(c.Path); err != nil {
			return "could not snapshot it: " + jj.BriefError(err)
		}
	}
	if len(st.Untracked) > 0 {
		return fmt.Sprintf("jj does not track %s (e.g. %s); remove it by hand once they can be lost",
			plural(len(st.Untracked), "file"), st.Untracked[0])
	}
	if outside, err := outsideSparse(c.Path, jjc); err != nil {
		return "could not check its sparse patterns: " + jj.BriefError(err)
	} else if outside != "" {
		return "it has files outside its sparse patterns, which jj does not track (e.g. " + outside + ")"
	}
	at, err := jjc.ReadOnly().Log(c.Path, "@")
	if err != nil || len(at) != 1 {
		return "could not read it after snapshotting"
	}
	if at[0].CommitID == c.Class.AtCommit {
		return ""
	}
	if c.Class.Tier == cleanup.TierDecide {
		// Removal rests on the person's confirmation of the evidence shown,
		// and that evidence did not include whatever moved @.
		return "it changed after you confirmed it; run wgo clean again to review it"
	}
	if classifier == nil {
		return "its working copy changed since the scan"
	}
	now := classifier.Classify(c.Class.Ref)
	if now.Type != c.Class.Type || now.Tier != c.Class.Tier {
		return "it changed since the scan; now " + now.Reason()
	}
	return ""
}

// outsideSparse returns a file in the workspace that lies outside its sparse
// patterns, or "" when it has none or is not sparse. jj neither tracks nor
// reports such files.
func outsideSparse(wsPath string, jjc *jj.CLIClient) (string, error) {
	patterns, err := jjc.SparseList(wsPath)
	if err != nil {
		return "", err
	}
	if slices.Contains(patterns, ".") {
		return "", nil
	}
	covered := func(rel string) bool {
		return slices.ContainsFunc(patterns, func(p string) bool { return rel == p || strings.HasPrefix(rel, p+"/") })
	}
	var found string
	err = filepath.WalkDir(wsPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(wsPath, path)
		rel = filepath.ToSlash(rel)
		switch {
		case rel == ".":
			return nil
		case rel == ".jj" || rel == ".git":
			// jj's own state; a secondary workspace's .git is a file
			// pointing at the repo's git worktree.
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		case d.IsDir() && covered(rel):
			return filepath.SkipDir
		case d.IsDir() || covered(rel) || d.Name() == ".DS_Store":
			return nil
		}
		found = rel
		return fs.SkipAll
	})
	return found, err
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// decisionLines are the detail lines shown for a workspace that needs a
// decision: what its work is, the evidence, and how to keep it.
func decisionLines(c Candidate) []string {
	cl := c.Class
	work := cl.Work.String()
	if !cl.LastUsed.IsZero() {
		work += ", last edited here " + cleanup.FormatAge(cl.LastUsed)
	}
	if cl.Work.Subject != "" {
		work += ": " + cl.Work.Subject
	}
	lines := []string{work}
	for _, e := range cl.Evidence {
		lines = append(lines, linkPRs(c, e))
	}
	if cl.Hint != "" {
		lines = append(lines, "→ "+cl.Hint)
	}
	return lines
}

// printDecisions lists the workspaces --yes leaves alone, with the evidence a
// person needs to decide.
func printDecisions(decide []Candidate) {
	fmt.Printf("\nNeeds a decision (%d) — not removed by --yes:\n", len(decide))
	for _, c := range decide {
		fmt.Printf("\n  %-16s %s\n", c.Class.Type, tildePath(c.DisplayPath()))
		for _, line := range decisionLines(c) {
			fmt.Printf("  %-16s %s\n", "", line)
		}
	}
}

// printKept summarises the workspaces that are kept, listing any jj could not
// read since those may need attention.
func printKept(kept []Candidate) {
	if len(kept) == 0 {
		return
	}
	counts := map[cleanup.WorkspaceType]int{}
	var unreadable []Candidate
	for _, c := range kept {
		counts[c.Class.Type]++
		if c.Class.Type == cleanup.TypeUnreadable {
			unreadable = append(unreadable, c)
		}
	}
	types := make([]string, 0, len(counts))
	for t := range counts {
		types = append(types, string(t))
	}
	sort.Strings(types)
	var parts []string
	for _, t := range types {
		parts = append(parts, fmt.Sprintf("%d %s", counts[cleanup.WorkspaceType(t)], t))
	}
	fmt.Printf("\nKept %d workspace(s): %s.\n", len(kept), strings.Join(parts, ", "))
	for _, c := range unreadable {
		fmt.Printf("  unreadable %s: %s\n", tildePath(c.DisplayPath()), strings.Join(c.Class.Evidence, "; "))
	}
}

// tildePath abbreviates the home directory to ~.
func tildePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !strings.HasPrefix(p, home+string(os.PathSeparator)) {
		return p
	}
	return "~" + p[len(home):]
}

// linkPRs hyperlinks "PR #N" in text to the candidate's PR on a terminal.
func linkPRs(c Candidate, text string) string {
	if !isTerminal() || c.PRInfo == nil || c.PRInfo.URL == "" {
		return text
	}
	return prNumberRe.ReplaceAllStringFunc(text, func(match string) string {
		return links.Hyperlink(c.PRInfo.URL, match)
	})
}

// KindRepo is re-exported from cleanup for use inside this file.
const KindRepo = cleanup.KindRepo

// remoteURLCache caches origin URLs by repo path to avoid repeated jj calls.
var remoteURLCache = map[string]string{}

func getRemoteURL(jjc jj.Client, repoPath string) string {
	if u, ok := remoteURLCache[repoPath]; ok {
		return u
	}
	remotes, err := jjc.RemoteURLs(repoPath)
	u := ""
	if err == nil {
		u = remotes["origin"]
	}
	remoteURLCache[repoPath] = u
	return u
}

var prNumberRe = regexp.MustCompile(`PR #(\d+)`)

func printCandidateTable(candidates []Candidate) {
	tty := isTerminal()
	jjc := jj.NewCLI()

	fmt.Printf("\n%-12s %-40s %s\n", "KIND", "LOCATION", "REASON")
	fmt.Println(strings.Repeat("-", 80))
	for _, c := range candidates {
		path := c.DisplayPath()

		// Link branch name in display path
		if tty && c.Branch != "" {
			remoteURL := getRemoteURL(jjc, c.RepoPath)
			branchURL := links.BranchURL(remoteURL, c.Branch)
			if branchURL != "" {
				linked := links.Hyperlink(branchURL, c.Branch)
				path = strings.Replace(path, c.Branch, linked, 1)
			}
		}

		fmt.Printf("%-12s %-40s %s\n", c.Kind, tildePath(path), linkPRs(c, c.Reason))
	}
}

type Candidate = cleanup.Candidate

func executeRemoval(c Candidate, jjc jj.Client, ghClient github.Client, _ *store.State) error {
	switch c.Kind {
	case cleanup.KindWorktree:
		wsName := c.WorkspaceName
		if wsName == "" {
			// Look up the workspace name from its on-disk path.
			wsName = workspaceNameForPath(jjc, c.RepoPath, c.Path)
		}
		if wsName != "" {
			if err := jjc.WorkspaceForget(c.RepoPath, wsName); err != nil {
				return err
			}
		}
		return os.RemoveAll(c.Path)

	case cleanup.KindOrphanWorkspace:
		return forgetOrphanWorkspace(c, jjc)

	case cleanup.KindLocalBranch:
		force := cleanForce
		if !force && c.PRInfo != nil && c.PRInfo.IsMerged() {
			defaultBranch := defaultBranchOrFallback(jjc, c.RepoPath)
			target := defaultBranch + "@origin"

			// Check 1: local bookmark commit is fully contained in target
			// (standard merge — bookmark's history matches target).
			if isContained(jjc, c.RepoPath, c.Branch, target) {
				force = true
			}

			// Check 2: PR merge commit is an ancestor of target
			// (squash/rebase merge — GitHub created a new commit on target).
			if !force && c.PRInfo.MergeCommit != nil && c.PRInfo.MergeCommit.OID != "" {
				sha := c.PRInfo.MergeCommit.OID
				if isAncestor(jjc, c.RepoPath, sha, target) {
					force = true
				} else if fetchErr := jjc.GitFetch(c.RepoPath, "", nil); fetchErr == nil {
					if isAncestor(jjc, c.RepoPath, sha, target) {
						force = true
					}
				}
			}

			// Check 3: local bookmark has no commits beyond the pushed PR head.
			if !force && c.PRInfo.HeadSHA != "" {
				if isContained(jjc, c.RepoPath, c.Branch, c.PRInfo.HeadSHA) {
					force = true
				}
			}

			// Check 4: local bookmark has no commits beyond its remote
			// counterpart (jj's bookmark@origin equivalent of git upstream).
			if !force {
				remoteRef := c.Branch + "@origin"
				if isContained(jjc, c.RepoPath, c.Branch, remoteRef) {
					force = true
				}
			}

			if !force {
				var reasons []string
				reasons = append(reasons, fmt.Sprintf("not reachable from %s", target))
				if c.PRInfo.MergeCommit != nil && c.PRInfo.MergeCommit.OID != "" {
					reasons = append(reasons, fmt.Sprintf("PR merge commit (%s) not found locally or not in %s", c.PRInfo.MergeCommit.OID[:7], target))
				}
				if c.PRInfo.HeadSHA != "" {
					reasons = append(reasons, fmt.Sprintf("local commits exist beyond pushed PR head (%s)", c.PRInfo.HeadSHA[:7]))
				}
				reasons = append(reasons, fmt.Sprintf("local commits exist beyond remote bookmark (%s@origin)", c.Branch))
				return fmt.Errorf("cannot verify bookmark is safe to delete:\n- %s\nuse --force to delete anyway", strings.Join(reasons, "\n- "))
			}
		}
		if err := jjc.BookmarkDelete(c.RepoPath, c.Branch); err != nil {
			return err
		}
		invalidateBranchCache(jjc, c.RepoPath, c.Branch)
		return nil

	case cleanup.KindRemoteBranch:
		if ghClient == nil || !ghClient.Available() {
			return fmt.Errorf("github client not available for remote branch deletion")
		}
		if err := ghClient.DeleteRemoteBranch(c.RepoPath, c.Branch); err != nil {
			return err
		}
		invalidateBranchCache(jjc, c.RepoPath, c.Branch)
		return nil

	case cleanup.KindRepo:
		return os.RemoveAll(c.Path)

	default:
		return fmt.Errorf("unknown candidate kind: %v", c.Kind)
	}
}

// invalidateBranchCache drops the on-disk PR cache entry for a deleted branch so
// a stale PR does not linger in `wgo .`/statusline output.
func invalidateBranchCache(jjc jj.Client, repoPath, branch string) {
	remoteURL := ""
	if remotes, err := jjc.RemoteURLs(repoPath); err == nil {
		remoteURL = remotes["origin"]
	}
	_ = prcache.Invalidate(remoteURL, repoPath, branch)
}

// defaultBranchOrFallback returns the default branch for repoPath, falling
// back to "main" on any error.
func defaultBranchOrFallback(jjc jj.Client, repoPath string) string {
	branch, err := defaultBranchFor(jjc, repoPath)
	if err != nil || branch == "" {
		return "main"
	}
	return branch
}

// isContained returns true when src has no commits beyond dst — i.e. src is
// fully merged into dst. Equivalent to git's `<dst>..<src>` returning empty.
func isContained(jjc jj.Client, repo, src, dst string) bool {
	n, err := jjc.CountRevset(repo, fmt.Sprintf("(%s)..(%s)", dst, src))
	if err != nil {
		return false
	}
	return n == 0
}

// isAncestor reports whether anc is an ancestor of desc.
func isAncestor(jjc jj.Client, repo, anc, desc string) bool {
	n, err := jjc.CountRevset(repo, fmt.Sprintf("(%s) & ::(%s)", anc, desc))
	if err != nil {
		return false
	}
	return n > 0
}

// workspaceNameForPath returns the jj workspace name whose on-disk root
// matches wsPath, or "" if no match. Required because jj.WorkspaceForget
// takes a workspace name, not a path.
// forgetOrphanWorkspace stops jj tracking a workspace whose directory is gone.
// It never touches the disk: a root that lost its .jj directory may now hold
// someone else's files. The workspace is re-checked first, so one that came
// back between the scan and the prompt is not forgotten.
func forgetOrphanWorkspace(c Candidate, jjc jj.Client) error {
	wss, err := jjc.ListWorkspaces(c.RepoPath)
	if err != nil {
		return err
	}
	for _, w := range wss {
		if w.Name != c.WorkspaceName {
			continue
		}
		_, orphaned, err := jj.OrphanReason(w)
		if err != nil {
			return fmt.Errorf("workspace %q: could not check its .jj directory: %w", w.Name, err)
		}
		if !orphaned {
			return fmt.Errorf("workspace %q is back at %s; not forgetting it", w.Name, w.Path)
		}
		if err := jjc.WorkspaceForget(c.RepoPath, w.Name); err != nil {
			return err
		}
		if _, err := os.Stat(w.Path); w.Path != "" && err == nil {
			fmt.Printf("  Left %s in place; delete it yourself if it is no longer needed\n", w.Path)
		}
		return nil
	}
	// Already forgotten (by jj or another wgo): nothing left to do.
	return nil
}

func workspaceNameForPath(jjc jj.Client, repoPath, wsPath string) string {
	ws, err := jjc.ListWorkspaces(repoPath)
	if err != nil {
		return ""
	}
	for _, w := range ws {
		if w.Path == wsPath {
			return w.Name
		}
	}
	return ""
}
