package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/git"
	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/links"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/spec"
	"github.com/virtru/wgo/internal/stack"
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

// showContext prints the current repo context. Returns (dirty, error).
func showContext() (bool, error) {
	client, err := git.NewFromCwd()
	if err != nil {
		return false, fmt.Errorf("failed to create git client: %w", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return false, fmt.Errorf("failed to get current directory: %w", err)
	}

	isRepo, err := client.IsRepo(cwd)
	if err != nil {
		return false, fmt.Errorf("failed to check if directory is a git repository: %w", err)
	}
	if !isRepo {
		return false, fmt.Errorf("not a git repository")
	}

	repoName, err := client.RepoName(cwd)
	if err != nil {
		return false, fmt.Errorf("failed to get repository name: %w", err)
	}

	branch, err := client.CurrentBranch(cwd)
	if err != nil {
		return false, fmt.Errorf("failed to get current branch: %w", err)
	}
	repoPath, err := client.MainRepoPath(cwd)
	if err != nil {
		return false, fmt.Errorf("failed to determine canonical repo path: %w", err)
	}

	status, err := client.Status(cwd)
	if err != nil {
		return false, fmt.Errorf("failed to get git status: %w", err)
	}

	commit, err := client.LastCommit(cwd)
	if err != nil {
		return false, fmt.Errorf("failed to get last commit: %w", err)
	}

	ahead, behind, err := client.AheadBehind(cwd, branch)
	if err != nil {
		ahead, behind = 0, 0
	}

	remoteURL, err := client.RemoteURL(cwd)
	if err != nil {
		remoteURL = "(no remote)"
	}

	// Fetch PRs for the current branch (gracefully degrades if gh unavailable)
	gh := github.NewClient()
	prs, _ := gh.ListPRsForBranch(cwd, branch)

	dirty := isDirty(status)
	statusWord := statusWord(status)

	if dotPorcelain {
		fmt.Println(statusWord)
		return dirty, nil
	}

	repoLink := links.RepoURL(remoteURL)
	branchLink := links.BranchURL(remoteURL, branch)
	commitLink := links.CommitURL(remoteURL, commit.Hash)

	if dotJSON {
		out := map[string]interface{}{
			"repo":       repoName,
			"branch":     branch,
			"status":     statusWord,
			"dirty":      dirty,
			"ahead":      ahead,
			"behind":     behind,
			"remote":     remoteURL,
			"repo_url":   repoLink,
			"branch_url": branchLink,
			"commit": map[string]interface{}{
				"hash":    truncateHash(commit.Hash),
				"message": commit.Message,
				"author":  commit.Author,
				"date":    commit.Date.Format(time.RFC3339),
				"url":     commitLink,
			},
		}
		if len(prs) > 0 {
			prList := make([]map[string]interface{}, len(prs))
			for i, pr := range prs {
				prList[i] = map[string]interface{}{
					"number": pr.Number,
					"title":  pr.Title,
					"state":  pr.State,
					"url":    pr.URL,
				}
			}
			out["prs"] = prList
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return dirty, enc.Encode(out)
	}

	tty := isTerminal()
	fmt.Printf("repo:   %s\n", links.Link(repoLink, repoName, tty))
	fmt.Printf("branch: %s\n", links.Link(branchLink, branch, tty))
	fmt.Printf("status: %s\n", formatStatus(status))
	fmt.Printf("remote: %s\n", formatRemote(ahead, behind, remoteURL))
	fmt.Printf("commit: %s %s (%s)\n",
		links.Link(commitLink, truncateHash(commit.Hash), tty),
		commit.Message,
		formatTime(commit.Date))

	for _, pr := range prs {
		label := fmt.Sprintf("#%d %s", pr.Number, pr.Title)
		state := strings.ToUpper(pr.State)
		fmt.Printf("pr:     %s [%s]\n", links.Link(pr.URL, label, tty), state)
	}

	// Show tasks linked to this branch
	if s, err := store.New(); err == nil {
		if planContent, err := s.LoadPlan(); err == nil {
			if p, err := plan.Parse(planContent); err == nil {
				for _, task := range p.GetTasksForBranch(repoName, branch) {
					fmt.Printf("task:   %s %s\n", string(task.Bullet), task.Text)
				}
			}
		}
		if state, err := s.LoadState(); err == nil {
			showStackLine(state, repoPath, branch)
		}
	}

	// Show spec line for ticket branches
	if ticket := spec.ParseTicketFromBranch(branch); ticket != "" {
		specPath, err := spec.FindByTicket(cwd, ticket)
		if err == nil {
			if sf, err := spec.Parse(specPath); err == nil {
				rel, _ := filepath.Rel(cwd, specPath)
				fmt.Printf("spec:   📄 %s (%s, updated %s)\n",
					rel, string(sf.Frontmatter.Status),
					sf.Frontmatter.Updated.Format("2006-01-02"))
			}
		} else {
			fmt.Printf("spec:   ⚠ no spec (run: wgo spec new %s)\n", ticket)
		}
	}

	showSiblings(client, cwd)

	return dirty, nil
}

// showStackLine prints a one-line indicator like "stack: a → **b** → c"
// when the current branch belongs to a managed stack. Silent no-op otherwise.
func showStackLine(state *store.State, repoPath, branch string) {
	ann := state.GetAnnotation(repoPath, branch)
	if ann == nil || ann.StackID == "" {
		return
	}
	graph, err := stack.Build(state, ann.StackID)
	if err != nil {
		return
	}
	order, err := graph.TopoSort()
	if err != nil {
		return
	}
	selfKey := store.AnnotationKey(repoPath, branch)
	parts := make([]string, 0, len(order))
	for _, key := range order {
		_, b, _ := splitAnnotationKey(key)
		if key == selfKey {
			parts = append(parts, "**"+b+"**")
		} else {
			parts = append(parts, b)
		}
	}
	if len(parts) <= 1 {
		return // not interesting to show a one-node "stack"
	}
	fmt.Printf("stack:  %s\n", strings.Join(parts, " → "))
}

func splitAnnotationKey(key string) (string, string, error) {
	i := strings.LastIndex(key, ":")
	if i < 0 {
		return "", "", fmt.Errorf("bad key %q", key)
	}
	return key[:i], key[i+1:], nil
}

// showSiblings prints a "Workspace siblings:" section when the parent directory
// of the current worktree contains 2+ git repos (i.e. at least one sibling).
func showSiblings(client *git.CLIClient, cwd string) {
	wtPath, err := client.RootDir(cwd)
	if err != nil {
		return
	}
	parentDir := filepath.Dir(wtPath)

	entries, err := os.ReadDir(parentDir)
	if err != nil {
		return
	}

	const maxDisplay = 10

	type sibling struct {
		name   string
		branch string
		status string
	}

	var siblings []sibling
	overflow := 0

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(parentDir, e.Name())
		if p == wtPath {
			continue
		}
		isRepo, _ := client.IsRepo(p)
		if !isRepo {
			continue
		}
		if len(siblings) >= maxDisplay {
			overflow++
			continue
		}
		branch, _ := client.CurrentBranch(p)
		st, _ := client.Status(p)
		statusStr := "clean"
		if isDirty(st) {
			statusStr = formatStatus(st)
		}
		siblings = append(siblings, sibling{e.Name(), branch, statusStr})
	}

	if len(siblings) == 0 {
		return
	}

	fmt.Println()
	fmt.Println("Workspace siblings:")
	for _, s := range siblings {
		fmt.Printf("  %-12s %s   %s\n", s.name+"/", s.branch, s.status)
	}
	if overflow > 0 {
		fmt.Printf("  (…and %d more)\n", overflow)
	}
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

func formatRemote(ahead, behind int, url string) string {
	arrows := ""
	if ahead > 0 {
		arrows = fmt.Sprintf("↑%d ", ahead)
	}
	if behind > 0 {
		arrows = fmt.Sprintf("%s↓%d", arrows, behind)
	}
	if arrows == "" {
		arrows = "↑0 ↓0"
	}

	repoDisplay := url
	if !strings.HasPrefix(url, "(no remote)") {
		if idx := strings.LastIndex(url, "/"); idx != -1 {
			repoDisplay = strings.TrimSuffix(url[idx+1:], ".git")
		}
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
