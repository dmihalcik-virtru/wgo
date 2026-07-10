package store

import (
	"strings"
	"time"
)

// StateVersion is the current schema version. Bumped to 2 for the jj
// migration; older state files (Version <= 1) are refused on load.
const StateVersion = 2

// State represents the persistent state for wgo.
//
// As of version 2 (the jj migration), Annotation no longer records Parents
// or StackID, and State no longer carries a Stacks map. The jj DAG is the
// single source of truth for stack topology; wgo annotations only carry
// metadata that isn't derivable from jj (Purpose, SpecPath, SpecState).
type State struct {
	Version       int                     `json:"version"`
	Repos         map[string]RepoInfo     `json:"repos"`
	Annotations   map[string]Annotation   `json:"annotations"`
	Efforts       map[string]Effort       `json:"efforts"`
	AgentSessions map[string]AgentSession `json:"agent_sessions"`
}

// RepoInfo contains information about a tracked repository.
type RepoInfo struct {
	RemoteURL string    `json:"remote_url"`
	LastSeen  time.Time `json:"last_seen"`
}

// Annotation contains the wgo-specific metadata for a bookmark (formerly a
// branch). Stack topology (Parents/StackID) was removed in the jj migration:
// jj's DAG owns that information now.
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

// AgentSession represents an active AI agent session. Sessions are keyed by
// workspace root (one agent per workspace); staleness is determined by
// LastActivity, which the hot-path heartbeat refreshes while an agent is live.
type AgentSession struct {
	Tool         string    `json:"tool"` // e.g., "claude", "codex", "cursor"
	WorktreePath string    `json:"worktree_path"`
	Branch       string    `json:"branch"`
	StartTime    time.Time `json:"start_time"`
	LastActivity time.Time `json:"last_activity"`
	// PID is the recording process's parent PID, informational only; staleness
	// (LastActivity) is the authoritative liveness signal.
	PID int `json:"pid,omitempty"`
}

// UpsertAgentSession records or refreshes the agent session for a workspace
// root. An existing session's StartTime is preserved (so "since" is stable) and
// LastActivity is bumped to now; a new session stamps both to now.
func (s *State) UpsertAgentSession(wsRoot, tool, branch string, pid int) {
	now := time.Now()
	sess := AgentSession{
		Tool:         tool,
		WorktreePath: wsRoot,
		Branch:       branch,
		StartTime:    now,
		LastActivity: now,
		PID:          pid,
	}
	if existing, ok := s.AgentSessions[wsRoot]; ok && !existing.StartTime.IsZero() {
		sess.StartTime = existing.StartTime
	}
	s.AgentSessions[wsRoot] = sess
}

// GetAgentSession retrieves the agent session for a workspace root.
func (s *State) GetAgentSession(wsRoot string) *AgentSession {
	if sess, ok := s.AgentSessions[wsRoot]; ok {
		return &sess
	}
	return nil
}

// RemoveAgentSession removes the agent session for a workspace root.
func (s *State) RemoveAgentSession(wsRoot string) {
	delete(s.AgentSessions, wsRoot)
}

// ActiveAgentSessions returns the agent sessions whose LastActivity is within
// window (i.e. non-stale), keyed by workspace root.
func (s *State) ActiveAgentSessions(window time.Duration) map[string]AgentSession {
	active := make(map[string]AgentSession)
	for root, sess := range s.AgentSessions {
		if time.Since(sess.LastActivity) <= window {
			active[root] = sess
		}
	}
	return active
}

// NewState creates a new empty State at the current schema version.
func NewState() *State {
	return &State{
		Version:       StateVersion,
		Repos:         make(map[string]RepoInfo),
		Annotations:   make(map[string]Annotation),
		Efforts:       make(map[string]Effort),
		AgentSessions: make(map[string]AgentSession),
	}
}

// AnnotationKey is the canonical "repoPath:bookmark" key used throughout
// state. The semantic name is bookmark (jj) rather than branch (git), but
// the format is unchanged for backwards compatibility with existing
// state files at version 2.
func AnnotationKey(repoPath, bookmark string) string {
	return repoPath + ":" + bookmark
}

// AddAnnotation adds or updates an annotation for a bookmark. Pre-existing
// spec metadata is preserved.
func (s *State) AddAnnotation(repoPath, bookmark, purpose string) {
	key := AnnotationKey(repoPath, bookmark)
	now := time.Now()

	existing, exists := s.Annotations[key]
	if exists {
		s.Annotations[key] = Annotation{
			Purpose:   purpose,
			SpecPath:  existing.SpecPath,
			SpecState: existing.SpecState,
			CreatedAt: existing.CreatedAt,
			UpdatedAt: now,
		}
	} else {
		s.Annotations[key] = Annotation{
			Purpose:   purpose,
			CreatedAt: now,
			UpdatedAt: now,
		}
	}
}

// GetAnnotation retrieves an annotation for a bookmark.
func (s *State) GetAnnotation(repoPath, bookmark string) *Annotation {
	key := repoPath + ":" + bookmark
	if ann, exists := s.Annotations[key]; exists {
		return &ann
	}
	return nil
}

// RemoveAnnotation removes an annotation.
func (s *State) RemoveAnnotation(repoPath, bookmark string) {
	key := repoPath + ":" + bookmark
	delete(s.Annotations, key)
}

// SetSpec updates the spec path and state for a bookmark annotation.
func (s *State) SetSpec(repoPath, bookmark, specPath, specState string) {
	key := repoPath + ":" + bookmark
	now := time.Now()
	ann := s.Annotations[key]
	ann.SpecPath = specPath
	ann.SpecState = specState
	ann.UpdatedAt = now
	if ann.CreatedAt.IsZero() {
		ann.CreatedAt = now
	}
	s.Annotations[key] = ann
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
