// Package models defines core data structures for wgo.
package models

import "time"

// EngagementLevel represents how actively a developer is working on a repository.
type EngagementLevel int

const (
	EngagementObserver EngagementLevel = iota
	EngagementReviewing
	EngagementActivePR
	EngagementActiveUnpushed
)

// GitStatus contains detailed git status information.
type GitStatus struct {
	Modified  int `json:"modified"`  // Number of modified files
	Added     int `json:"added"`     // Number of added files
	Deleted   int `json:"deleted"`   // Number of deleted files
	Untracked int `json:"untracked"` // Number of untracked files
	Staged    int `json:"staged"`    // Number of staged files
	Ahead     int `json:"ahead"`     // Number of commits ahead of remote
	Behind    int `json:"behind"`    // Number of commits behind remote
	Conflicts int `json:"conflicts"` // Number of files with conflicts
}

// CommitInfo contains information about a Git commit.
type CommitInfo struct {
	Hash    string    `json:"hash"`          // Commit hash
	Message string    `json:"message"`       // Commit message
	Author  string    `json:"author"`        // Commit author
	Date    time.Time `json:"date"`          // Commit date
	URL     string    `json:"url,omitempty"` // Web URL for the commit
}

// BranchInfo contains information about a branch.
type BranchInfo struct {
	Name       string     `json:"name"`        // Branch name
	IsCurrent  bool       `json:"is_current"`  // Whether this is the current branch
	LastCommit CommitInfo `json:"last_commit"` // Information about the last commit
	RemoteName string     `json:"remote_name"` // Remote branch name if tracking
}

// RepoState represents the high-level state of a repository.
type RepoState string

const (
	StateClean    RepoState = "clean"
	StateModified RepoState = "modified"
	StateStaged   RepoState = "staged"
	StateConflict RepoState = "conflict"
	StateStale    RepoState = "stale"
)

// DiffStat contains line-level diff statistics.
type DiffStat struct {
	FilesChanged int `json:"files_changed"`
	Insertions   int `json:"insertions"`
	Deletions    int `json:"deletions"`
}

// RepoActivity contains the full status of a single repository.
type RepoActivity struct {
	Path            string          `json:"path"`
	Name            string          `json:"name"`
	Branch          string          `json:"branch"`
	Status          GitStatus       `json:"status"`
	State           RepoState       `json:"state"`
	LastCommit      CommitInfo      `json:"last_commit"`
	RecentCommits   int             `json:"recent_commits"`
	DiffStat        DiffStat        `json:"diff_stat"`
	LastActivity    time.Time       `json:"last_activity"`
	Annotation      string          `json:"annotation,omitempty"`
	RemoteURL       string          `json:"remote_url,omitempty"`
	RepoURL         string          `json:"repo_url,omitempty"`
	BranchURL       string          `json:"branch_url,omitempty"`
	IsCurrent       bool            `json:"is_current"`
	IsWorktree      bool            `json:"is_worktree,omitempty"`
	MainRepoName    string          `json:"main_repo_name,omitempty"`
	MainRepoPath    string          `json:"main_repo_path,omitempty"`
	SpecGlyph       string          `json:"spec_glyph,omitempty"`
	PRAuthor        string          `json:"pr_author,omitempty"`
	PRNumber        int             `json:"pr_number,omitempty"`
	PRURL           string          `json:"pr_url,omitempty"`
	IsDefaultBranch bool            `json:"is_default_branch"`
	EngagementLevel EngagementLevel `json:"engagement_level"`
}
