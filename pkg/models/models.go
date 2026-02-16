// Package models defines core data structures for wgo.
package models

import "time"

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
	Hash    string    `json:"hash"`    // Commit hash
	Message string    `json:"message"` // Commit message
	Author  string    `json:"author"`  // Commit author
	Date    time.Time `json:"date"`    // Commit date
}

// BranchInfo contains information about a branch.
type BranchInfo struct {
	Name        string    `json:"name"`         // Branch name
	IsCurrent   bool      `json:"is_current"`   // Whether this is the current branch
	LastCommit  CommitInfo `json:"last_commit"` // Information about the last commit
	RemoteName  string    `json:"remote_name"`  // Remote branch name if tracking
}
