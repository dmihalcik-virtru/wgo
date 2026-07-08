package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/links"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/spec"
	"github.com/virtru/wgo/internal/store"
	"github.com/virtru/wgo/models"
)

var (
	dotJSON      bool
	dotPorcelain bool
	dotExitCode  bool
)

// dotCmd represents the `wgo .` command.
var dotCmd = &cobra.Command{
	Use:   ".",
	Short: "Show current work context",
	Long: `Shows the current branch, status, remote tracking, and last commit
for the repository in the current directory.`,
	Run: func(cmd *cobra.Command, args []string) {
		dirty, err := showContext()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if dotExitCode && dirty {
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(dotCmd)
	dotCmd.Flags().BoolVar(&dotJSON, "json", false, "JSON output")
	dotCmd.Flags().BoolVar(&dotPorcelain, "porcelain", false, "Print only the status word (clean, modified, staged, conflict)")
	dotCmd.Flags().BoolVar(&dotExitCode, "exit-code", false, "Exit 1 if working tree is dirty")
}

// showContext resolves the current work context once and renders it in the
// requested format. Returns (dirty, error).
func showContext() (bool, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return false, fmt.Errorf("failed to get current directory: %w", err)
	}

	ctx, err := buildContext(cwd)
	if err != nil {
		return false, err
	}

	switch {
	case dotPorcelain:
		fmt.Println(ctx.Status)
	case dotJSON:
		return ctx.Dirty, renderJSON(os.Stdout, ctx)
	default:
		renderText(os.Stdout, ctx, isTerminal())
	}

	return ctx.Dirty, nil
}

// buildContext resolves the full work context for the workspace at cwd. It
// performs no stdout I/O: every field the text renderer displays is populated
// here so the text and JSON projections can never drift.
func buildContext(cwd string) (*models.Context, error) {
	jjc := jj.NewCLI()

	if !jjc.IsRepo(cwd) {
		return nil, fmt.Errorf("not a jj repository")
	}

	wsRoot, err := jjc.Root(cwd)
	if err != nil {
		return nil, fmt.Errorf("failed to get workspace root: %w", err)
	}
	repoPath, err := jjc.MainWorkspaceRoot(cwd)
	if err != nil {
		repoPath = wsRoot
	}
	repoName := filepath.Base(repoPath)

	branch := currentBookmark(jjc, cwd)
	if branch == "" {
		branch = "(no bookmark)"
	}

	jjStatus, err := jjc.Status(cwd)
	if err != nil {
		return nil, fmt.Errorf("failed to get jj status: %w", err)
	}
	status := models.GitStatus{
		Modified: len(jjStatus.Modified),
		Added:    len(jjStatus.Added),
		Deleted:  len(jjStatus.Deleted),
	}

	commit := models.CommitInfo{}
	if entries, err := jjc.Log(cwd, "@-"); err == nil && len(entries) > 0 {
		line, _, _ := strings.Cut(entries[0].Description, "\n")
		commit = models.CommitInfo{
			Hash:    entries[0].CommitID,
			Message: strings.TrimSpace(line),
			Author:  entries[0].AuthorEmail,
			Date:    entries[0].AuthorTimestamp,
		}
	}

	ahead, behind := 0, 0
	syncUnknown := false
	if branch != "(no bookmark)" {
		if a, b, err := jjc.AheadBehind(cwd, branch); err == nil {
			ahead, behind = a, b
		} else {
			// AheadBehind returns (0,0,nil) for the legitimate "no remote
			// bookmark to compare" case, so a non-nil error is a genuine
			// failure: don't fabricate a "↑0 ↓0" in-sync reading.
			syncUnknown = true
		}
	}

	remoteURL := "(no remote)"
	if remotes, err := jjc.RemoteURLs(cwd); err == nil {
		if url := remotes["origin"]; url != "" {
			remoteURL = url
		}
	} else {
		// A genuine command failure, distinct from "no remote configured"
		// (which returns an empty map with a nil error).
		remoteURL = "(remote lookup failed)"
	}

	ctx := &models.Context{
		SchemaVersion: models.ContextSchemaVersion,
		Repo:          repoName,
		RepoURL:       links.RepoURL(remoteURL),
		Branch:        branch,
		Status:        statusWord(status),
		Changes:       status,
		Dirty:         isDirty(status),
		Ahead:         ahead,
		Behind:        behind,
		SyncUnknown:   syncUnknown,
		Remote:        remoteURL,
		BranchURL:     links.BranchURL(remoteURL, branch),
		Commit: models.CommitInfo{
			// Full hash builds the URL; the displayed/serialized hash is truncated.
			Hash:    truncateHash(commit.Hash),
			Message: commit.Message,
			Author:  commit.Author,
			Date:    commit.Date,
			URL:     links.CommitURL(remoteURL, commit.Hash),
		},
	}

	// PRs for the current branch (gracefully degrades if gh unavailable).
	gh := github.NewClient()
	if prs, err := gh.ListPRsForBranch(cwd, branch); err == nil {
		for _, pr := range prs {
			ctx.PRs = append(ctx.PRs, models.PRRef{
				Number: pr.Number,
				Title:  pr.Title,
				State:  pr.State,
				URL:    pr.URL,
			})
		}
	}

	// Tasks linked to this branch from the plan file.
	if s, err := store.New(); err == nil {
		if planContent, err := s.LoadPlan(); err == nil {
			if p, err := plan.Parse(planContent); err == nil {
				for _, task := range p.GetTasksForBranch(repoName, branch) {
					ctx.Tasks = append(ctx.Tasks, models.TaskRef{
						Bullet: string(task.Bullet),
						Text:   task.Text,
					})
				}
			}
		}
	}

	// Spec for ticket branches. Resolved against the workspace root (not cwd)
	// so `wgo .` finds spec/<TICKET>.md when run from a subdirectory.
	if ticket := spec.ParseTicketFromBranch(branch); ticket != "" {
		ctx.Ticket = ticket
		specPath, err := spec.FindByTicket(wsRoot, ticket)
		switch {
		case err != nil:
			ctx.SpecMissing = true
		default:
			if sf, err := spec.Parse(specPath); err == nil {
				rel, _ := filepath.Rel(wsRoot, specPath)
				ctx.Spec = &models.SpecRef{
					Path:    rel,
					Status:  string(sf.Frontmatter.Status),
					Updated: sf.Frontmatter.Updated.Format("2006-01-02"),
				}
			} else {
				// File exists but frontmatter is malformed: surface a distinct
				// warning instead of silently dropping the spec line.
				ctx.SpecUnreadable = true
			}
		}
	}

	ctx.Siblings, ctx.SiblingsOverflow = gatherSiblings(jjc, cwd)

	return ctx, nil
}

// renderText writes the human-readable context. tty controls OSC8 hyperlinks.
func renderText(w io.Writer, c *models.Context, tty bool) {
	fmt.Fprintf(w, "repo:   %s\n", links.Link(c.RepoURL, c.Repo, tty))
	fmt.Fprintf(w, "branch: %s\n", links.Link(c.BranchURL, c.Branch, tty))
	fmt.Fprintf(w, "status: %s\n", formatStatus(c.Changes))
	fmt.Fprintf(w, "remote: %s\n", formatRemote(c.Ahead, c.Behind, c.Remote, c.SyncUnknown))
	fmt.Fprintf(w, "commit: %s %s (%s)\n",
		links.Link(c.Commit.URL, c.Commit.Hash, tty),
		c.Commit.Message,
		formatTime(c.Commit.Date))

	for _, pr := range c.PRs {
		label := fmt.Sprintf("#%d %s", pr.Number, pr.Title)
		state := strings.ToUpper(pr.State)
		fmt.Fprintf(w, "pr:     %s [%s]\n", links.Link(pr.URL, label, tty), state)
	}

	for _, t := range c.Tasks {
		fmt.Fprintf(w, "task:   %s %s\n", t.Bullet, t.Text)
	}

	if c.Ticket != "" {
		switch {
		case c.Spec != nil:
			fmt.Fprintf(w, "spec:   📄 %s (%s, updated %s)\n",
				c.Spec.Path, c.Spec.Status, c.Spec.Updated)
		case c.SpecUnreadable:
			fmt.Fprintf(w, "spec:   ⚠ spec/%s.md present but unreadable (malformed frontmatter)\n", c.Ticket)
		case c.SpecMissing:
			fmt.Fprintf(w, "spec:   ⚠ no spec (run: wgo spec new %s)\n", c.Ticket)
		}
	}

	if len(c.Siblings) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Workspace siblings:")
		for _, s := range c.Siblings {
			fmt.Fprintf(w, "  %-12s %s   %s\n", s.Name+"/", s.Branch, s.Status)
		}
		if c.SiblingsOverflow > 0 {
			fmt.Fprintf(w, "  (…and %d more)\n", c.SiblingsOverflow)
		}
	}
}

// renderJSON writes the context as an indented, versioned JSON projection.
func renderJSON(w io.Writer, c *models.Context) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(c)
}

// gatherSiblings enumerates jj repos/workspaces in the parent directory of the
// current workspace, capping the returned list at 10 and reporting how many
// additional repos were found beyond the cap.
func gatherSiblings(jjc jj.Client, cwd string) ([]models.SiblingRef, int) {
	wsRoot, err := jjc.Root(cwd)
	if err != nil {
		return nil, 0
	}
	parentDir := filepath.Dir(wsRoot)

	entries, err := os.ReadDir(parentDir)
	if err != nil {
		return nil, 0
	}

	const maxDisplay = 10

	var siblings []models.SiblingRef
	overflow := 0

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(parentDir, e.Name())
		if p == wsRoot {
			continue
		}
		if !jjc.IsRepo(p) {
			continue
		}
		if len(siblings) >= maxDisplay {
			overflow++
			continue
		}
		branch := currentBookmark(jjc, p)
		st, _ := jjc.Status(p)
		statusStr := "clean"
		if !st.Clean {
			statusStr = formatStatus(models.GitStatus{
				Modified: len(st.Modified),
				Added:    len(st.Added),
				Deleted:  len(st.Deleted),
			})
		}
		siblings = append(siblings, models.SiblingRef{Name: e.Name(), Branch: branch, Status: statusStr})
	}

	return siblings, overflow
}

func isDirty(status models.GitStatus) bool {
	return status.Modified > 0 || status.Added > 0 || status.Deleted > 0 ||
		status.Staged > 0 || status.Untracked > 0 || status.Conflicts > 0
}

func statusWord(status models.GitStatus) string {
	if status.Conflicts > 0 {
		return "conflict"
	}
	if status.Staged > 0 {
		return "staged"
	}
	if status.Modified > 0 || status.Added > 0 || status.Deleted > 0 || status.Untracked > 0 {
		return "modified"
	}
	return "clean"
}

func formatStatus(status models.GitStatus) string {
	parts := []string{}

	if status.Modified > 0 {
		parts = append(parts, fmt.Sprintf("%d modified", status.Modified))
	}
	if status.Added > 0 {
		parts = append(parts, fmt.Sprintf("%d added", status.Added))
	}
	if status.Deleted > 0 {
		parts = append(parts, fmt.Sprintf("%d deleted", status.Deleted))
	}
	if status.Staged > 0 {
		parts = append(parts, fmt.Sprintf("%d staged", status.Staged))
	}
	if status.Untracked > 0 {
		parts = append(parts, fmt.Sprintf("%d untracked", status.Untracked))
	}
	if status.Conflicts > 0 {
		parts = append(parts, fmt.Sprintf("%d conflicted", status.Conflicts))
	}

	if len(parts) == 0 {
		return "clean"
	}

	return strings.Join(parts, ", ")
}

func formatRemote(ahead, behind int, url string, syncUnknown bool) string {
	var arrows string
	switch {
	case syncUnknown:
		// Ahead/behind lookup failed: don't fabricate an in-sync reading.
		arrows = "↑? ↓?"
	case ahead > 0 || behind > 0:
		if ahead > 0 {
			arrows = fmt.Sprintf("↑%d ", ahead)
		}
		if behind > 0 {
			arrows = fmt.Sprintf("%s↓%d", arrows, behind)
		}
	default:
		arrows = "↑0 ↓0"
	}

	// Sentinel URLs like "(no remote)" or "(remote lookup failed)" are not real
	// remotes; render them plainly rather than as "origin/(...)".
	if strings.HasPrefix(url, "(") {
		return fmt.Sprintf("%s %s", arrows, url)
	}

	repoDisplay := url
	if idx := strings.LastIndex(url, "/"); idx != -1 {
		repoDisplay = strings.TrimSuffix(url[idx+1:], ".git")
	}

	return fmt.Sprintf("%s (origin/%s)", arrows, repoDisplay)
}

func truncateHash(hash string) string {
	if len(hash) > 7 {
		return hash[:7]
	}
	return hash
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}

	now := time.Now()
	diff := now.Sub(t)

	if diff < time.Minute {
		return "just now"
	}
	if diff < time.Hour {
		minutes := int(diff.Minutes())
		if minutes == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", minutes)
	}
	if diff < 24*time.Hour {
		hours := int(diff.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	}

	days := int(diff.Hours() / 24)
	if days == 1 {
		return "1 day ago"
	}
	return fmt.Sprintf("%d days ago", days)
}
