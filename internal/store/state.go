package store

import (
	"strings"
	"time"
)

// State represents the persistent state for wgo.
type State struct {
	Version        int                     `json:"version"`
	Repos          map[string]RepoInfo     `json:"repos"`
	Annotations    map[string]Annotation   `json:"annotations"`
	Efforts        map[string]Effort       `json:"efforts"`
	AgentSessions  map[string]AgentSession `json:"agent_sessions"`
}

// RepoInfo contains information about a tracked repository.
type RepoInfo struct {
	RemoteURL string    `json:"remote_url"`
	LastSeen  time.Time `json:"last_seen"`
}

// Annotation contains information about why a branch exists.
type Annotation struct {
	Purpose   string    `json:"purpose"`
	SpecPath  string    `json:"spec_path,omitempty"`
	SpecState string    `json:"spec_state,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Effort represents a cross-repo effort or feature.
type Effort struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Branches    []string  `json:"branches"` // Keys in format "repo:branch"
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// AgentSession represents an active AI agent session.
type AgentSession struct {
	Tool         string    `json:"tool"`        // e.g., "claude", "codex", "cursor"
	WorktreePath string    `json:"worktree_path"`
	Branch       string    `json:"branch"`
	StartTime    time.Time `json:"start_time"`
	LastActivity time.Time `json:"last_activity"`
}

// NewState creates a new empty State.
func NewState() *State {
	return &State{
		Version:       1,
		Repos:         make(map[string]RepoInfo),
		Annotations:   make(map[string]Annotation),
		Efforts:       make(map[string]Effort),
		AgentSessions: make(map[string]AgentSession),
	}
}

// AddAnnotation adds or updates an annotation for a branch.
func (s *State) AddAnnotation(repoPath, branch, purpose string) {
	key := repoPath + ":" + branch
	now := time.Now()
	createdAt := now
	if existing, exists := s.Annotations[key]; exists {
		createdAt = existing.CreatedAt
	}
	s.Annotations[key] = Annotation{
		Purpose:   purpose,
		CreatedAt: createdAt,
		UpdatedAt: now,
	}
}

// GetAnnotation retrieves an annotation for a branch.
func (s *State) GetAnnotation(repoPath, branch string) *Annotation {
	key := repoPath + ":" + branch
	if ann, exists := s.Annotations[key]; exists {
		return &ann
	}
	return nil
}

// SetSpec records spec_path and spec_state on an annotation, creating one if needed.
// Other fields (Purpose, CreatedAt) are preserved.
func (s *State) SetSpec(repoPath, branch, specPath, specState string) {
	key := repoPath + ":" + branch
	now := time.Now()
	ann := s.Annotations[key]
	if ann.CreatedAt.IsZero() {
		ann.CreatedAt = now
	}
	ann.SpecPath = specPath
	ann.SpecState = specState
	ann.UpdatedAt = now
	s.Annotations[key] = ann
}

// RemoveAnnotation removes an annotation.
func (s *State) RemoveAnnotation(repoPath, branch string) {
	key := repoPath + ":" + branch
	delete(s.Annotations, key)
}

// AddRepo adds or updates a repository.
func (s *State) AddRepo(path, remoteURL string) {
	s.Repos[path] = RepoInfo{
		RemoteURL: remoteURL,
		LastSeen:  time.Now(),
	}
}

// GetRepo retrieves repository information.
func (s *State) GetRepo(path string) *RepoInfo {
	if repo, exists := s.Repos[path]; exists {
		return &repo
	}
	return nil
}

// UntrackRepo removes a repository and all related annotations from state.
func (s *State) UntrackRepo(path string) {
	delete(s.Repos, path)
	prefix := path + ":"
	for key := range s.Annotations {
		if strings.HasPrefix(key, prefix) {
			delete(s.Annotations, key)
		}
	}
}
