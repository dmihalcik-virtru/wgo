package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/git"
	"github.com/virtru/wgo/pkg/models"
)

// dotCmd represents the `wgo .` command.
var dotCmd = &cobra.Command{
	Use:   ".",
	Short: "Show current work context",
	Long: `Shows the current branch, status, remote tracking, and last commit
for the repository in the current directory.`,
	Run: func(cmd *cobra.Command, args []string) {
		if err := showContext(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(dotCmd)
}

func showContext() error {
	client, err := git.NewFromCwd()
	if err != nil {
		return fmt.Errorf("failed to create git client: %w", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %w", err)
	}

	// Check if we're in a git repo
	isRepo, err := client.IsRepo(cwd)
	if err != nil {
		return fmt.Errorf("failed to check if directory is a git repository: %w", err)
	}

	if !isRepo {
		return fmt.Errorf("not a git repository")
	}

	// Get repo information
	repoName, err := client.RepoName(cwd)
	if err != nil {
		return fmt.Errorf("failed to get repository name: %w", err)
	}

	branch, err := client.CurrentBranch(cwd)
	if err != nil {
		return fmt.Errorf("failed to get current branch: %w", err)
	}

	status, err := client.Status(cwd)
	if err != nil {
		return fmt.Errorf("failed to get git status: %w", err)
	}

	commit, err := client.LastCommit(cwd)
	if err != nil {
		return fmt.Errorf("failed to get last commit: %w", err)
	}

	ahead, behind, err := client.AheadBehind(cwd, branch)
	if err != nil {
		// Log but don't fail - ahead/behind is optional
		ahead, behind = 0, 0
	}

	remoteURL, err := client.RemoteURL(cwd)
	if err != nil {
		remoteURL = "(no remote)"
	}

	// Format output
	fmt.Printf("repo:   %s\n", repoName)
	fmt.Printf("branch: %s\n", branch)
	fmt.Printf("status: %s\n", formatStatus(status))
	fmt.Printf("remote: %s\n", formatRemote(ahead, behind, remoteURL))
	fmt.Printf("commit: %s %s (%s)\n",
		truncateHash(commit.Hash),
		commit.Message,
		formatTime(commit.Date))

	return nil
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

	// Extract repo name from URL for display
	repoDisplay := url
	if !strings.HasPrefix(url, "(no remote)") {
		// Extract just the repo identifier
		if idx := strings.LastIndex(url, "/"); idx != -1 {
			repoDisplay = url[idx+1:]
			if strings.HasSuffix(repoDisplay, ".git") {
				repoDisplay = repoDisplay[:len(repoDisplay)-4]
			}
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
