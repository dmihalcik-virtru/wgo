package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// StateVersion is the current schema version. Bumped to 2 for the jj
// migration; older state files (Version <= 1) are refused on load. Bumped to 3
// when agent sessions became keyed by session ID instead of workspace root;
// version-2 files are migrated on load (see normalizeAgentSessions).
//
// Version 3 also writes agent_sessions as a JSON array rather than an object.
// Version-2 binaries decode that field into a map, so they fail to parse a
// version-3 file and never rewrite it: an older wgo on PATH (one run by a
// hook, say) cannot strip the new session fields. Binaries from version 3 on
// refuse newer files explicitly (see NewerStateError).
const StateVersion = 3

// State represents the persistent state for wgo.
//
// As of version 2 (the jj migration), Annotation no longer records Parents
// or StackID, and State no longer carries a Stacks map. The jj DAG is the
// single source of truth for stack topology; wgo annotations only carry
// metadata that isn't derivable from jj (Purpose, SpecPath, SpecState).
type State struct {
	Version     int                   `json:"version"`
	Repos       map[string]RepoInfo   `json:"repos"`
	Annotations map[string]Annotation `json:"annotations"`
	Efforts     map[string]Effort     `json:"efforts"`
	// AgentSessions is keyed by AgentSession.ID (version 3). Version 2 keyed
	// it by workspace root. On disk it is an array sorted by ID (see
	// MarshalJSON); version-2 objects are still read.
	AgentSessions map[string]AgentSession `json:"agent_sessions"`
}

// stateJSON is State without its JSON methods, so they can delegate to the
// default encoding for every other field.
type stateJSON State

// MarshalJSON writes AgentSessions as an array sorted by ID, which keeps the
// git-versioned file's diffs stable and makes it unreadable to version-2
// binaries (see StateVersion).
func (s State) MarshalJSON() ([]byte, error) {
	sessions := make([]AgentSession, 0, len(s.AgentSessions))
	for _, sess := range s.AgentSessions {
		sessions = append(sessions, sess)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID < sessions[j].ID })
	return json.Marshal(struct {
		stateJSON
		AgentSessions []AgentSession `json:"agent_sessions"`
	}{stateJSON(s), sessions})
}

// UnmarshalJSON reads AgentSessions as either the version-3 array or the
// version-2 object. Array entries are keyed by ID here; LoadState's
// normalization repairs missing or duplicate IDs.
func (s *State) UnmarshalJSON(data []byte) error {
	aux := struct {
		*stateJSON
		AgentSessions json.RawMessage `json:"agent_sessions"`
	}{stateJSON: (*stateJSON)(s)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	raw := bytes.TrimSpace(aux.AgentSessions)
	s.AgentSessions = nil
	switch {
	case len(raw) == 0 || bytes.Equal(raw, []byte("null")):
	case raw[0] == '[':
		var list []AgentSession
		if err := json.Unmarshal(raw, &list); err != nil {
			return fmt.Errorf("agent_sessions: %w", err)
		}
		s.AgentSessions = make(map[string]AgentSession, len(list))
		for i, sess := range list {
			key := sess.ID
			if _, taken := s.AgentSessions[key]; taken || key == "" {
				key = fmt.Sprintf("%s#%d", sess.ID, i) // keeps both; normalization renames
			}
			s.AgentSessions[key] = sess
		}
	default:
		if err := json.Unmarshal(raw, &s.AgentSessions); err != nil {
			return fmt.Errorf("agent_sessions: %w", err)
		}
	}
	return nil
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
