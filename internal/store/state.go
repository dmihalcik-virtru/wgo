package store

import (
	"sort"
	"strings"
	"time"
)

// State represents the persistent state for wgo.
type State struct {
	Version       int                     `json:"version"`
	Repos         map[string]RepoInfo     `json:"repos"`
	Annotations   map[string]Annotation   `json:"annotations"`
	Efforts       map[string]Effort       `json:"efforts"`
	AgentSessions map[string]AgentSession `json:"agent_sessions"`
	Stacks        map[string]Stack        `json:"stacks,omitempty"`
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
	// Parents lists the keys ("repo:branch") of branches this one stacks on
	// top of. Empty == based on the repo's default branch. Multiple entries
	// describe a merge node in the stack DAG.
	Parents []string `json:"parents,omitempty"`
	// StackID groups this branch into a named stack (see State.Stacks). Empty
	// means the branch is not part of a managed stack.
	StackID   string    `json:"stack_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Stack is a named collection of branches that form a DAG via Annotation.Parents.
// The graph itself is implicit — derived by walking parents. Stack only carries
// presentation/identity metadata.
type Stack struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// RootRef is the ref that root nodes (those with no Parents) rebase onto,
	// e.g. "origin/main". Defaults to the repo's default branch at creation time.
	RootRef   string    `json:"root_ref,omitempty"`
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
	Tool         string    `json:"tool"` // e.g., "claude", "codex", "cursor"
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
		Stacks:        make(map[string]Stack),
	}
}

// AnnotationKey is the canonical "repoPath:branch" key used throughout state.
func AnnotationKey(repoPath, branch string) string {
	return repoPath + ":" + branch
}

// AddAnnotation adds or updates an annotation for a branch. Pre-existing
// spec metadata, parent links, and stack membership are preserved.
func (s *State) AddAnnotation(repoPath, branch, purpose string) {
	key := AnnotationKey(repoPath, branch)
	now := time.Now()

	existing, exists := s.Annotations[key]
	if exists {
		s.Annotations[key] = Annotation{
			Purpose:   purpose,
			SpecPath:  existing.SpecPath,
			SpecState: existing.SpecState,
			Parents:   existing.Parents,
			StackID:   existing.StackID,
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

// GetAnnotation retrieves an annotation for a branch.
func (s *State) GetAnnotation(repoPath, branch string) *Annotation {
	key := repoPath + ":" + branch
	if ann, exists := s.Annotations[key]; exists {
		return &ann
	}
	return nil
}

// RemoveAnnotation removes an annotation.
func (s *State) RemoveAnnotation(repoPath, branch string) {
	key := repoPath + ":" + branch
	delete(s.Annotations, key)
}

// SetSpec updates the spec path and state for a branch annotation.
func (s *State) SetSpec(repoPath, branch, specPath, specState string) {
	key := repoPath + ":" + branch
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

// SetParents records the parent keys for a branch annotation. An empty slice
// (or nil) clears the parents, marking this branch as a stack root. The
// annotation is created if it doesn't already exist. Duplicate parent keys
// are dropped (order preserved) so callers that pass accidental repeats —
// e.g. `wgo stack push --on foo --on foo` — don't trigger false cycle
// detection or skewed indegree counts in graph operations.
func (s *State) SetParents(repoPath, branch string, parents []string) {
	key := AnnotationKey(repoPath, branch)
	now := time.Now()
	ann := s.Annotations[key]
	ann.Parents = dedupStrings(parents)
	ann.UpdatedAt = now
	if ann.CreatedAt.IsZero() {
		ann.CreatedAt = now
	}
	s.Annotations[key] = ann
}

// SetStackID assigns the branch to a stack. An empty stackID removes the
// branch from its stack but keeps the annotation otherwise intact.
func (s *State) SetStackID(repoPath, branch, stackID string) {
	key := AnnotationKey(repoPath, branch)
	now := time.Now()
	ann := s.Annotations[key]
	ann.StackID = stackID
	ann.UpdatedAt = now
	if ann.CreatedAt.IsZero() {
		ann.CreatedAt = now
	}
	s.Annotations[key] = ann
}

// AddStack creates or updates a stack record. On updates, fields the caller
// leaves at their zero value are preserved from the existing record, so a
// partial update like `AddStack(Stack{ID: id, Name: "renamed"})` does not
// silently clobber RootRef or other metadata. To explicitly clear a field,
// use the dedicated setter (or for now, replace the whole record by reading
// the existing one first).
func (s *State) AddStack(stack Stack) {
	if s.Stacks == nil {
		s.Stacks = make(map[string]Stack)
	}
	now := time.Now()
	if existing, ok := s.Stacks[stack.ID]; ok {
		stack.CreatedAt = existing.CreatedAt
		if stack.Name == "" {
			stack.Name = existing.Name
		}
		if stack.RootRef == "" {
			stack.RootRef = existing.RootRef
		}
	} else if stack.CreatedAt.IsZero() {
		stack.CreatedAt = now
	}
	stack.UpdatedAt = now
	s.Stacks[stack.ID] = stack
}

// GetStack returns the stack with the given ID, or nil if absent.
func (s *State) GetStack(id string) *Stack {
	if s.Stacks == nil {
		return nil
	}
	if st, ok := s.Stacks[id]; ok {
		return &st
	}
	return nil
}

// RemoveStack deletes the stack record and clears StackID from every
// annotation that referenced it. Parents links are left intact so the
// DAG remains queryable; callers that want to fully unstack should clear
// Parents separately.
func (s *State) RemoveStack(id string) {
	delete(s.Stacks, id)
	for key, ann := range s.Annotations {
		if ann.StackID == id {
			ann.StackID = ""
			s.Annotations[key] = ann
		}
	}
}

// AnnotationsInStack returns all annotation keys whose StackID matches.
// The returned slice is sorted lexicographically for deterministic iteration.
func (s *State) AnnotationsInStack(stackID string) []string {
	if stackID == "" {
		return nil
	}
	var keys []string
	for key, ann := range s.Annotations {
		if ann.StackID == stackID {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// dedupStrings returns a copy of in with duplicate entries removed, preserving
// the first occurrence of each value.
func dedupStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
