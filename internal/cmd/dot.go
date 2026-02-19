package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/git"
	"github.com/virtru/wgo/internal/links"
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

	return dirty, nil
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
