// Package effort resolves effort bookmark references and attributes workspaces
// to efforts.
//
// An effort is a named group of bookmarks spanning repositories. Efforts live
// in state.json (written by `wgo plan effort`) and in the `## Efforts` section
// of the plan file (written by hand or projected by the same commands); the
// plan wins for an effort present in both. Bookmark references are
// "repo:bookmark" strings. State stores the normalized form, the absolute
// main-clone path plus the exact bookmark; the plan shows the shortest repo
// name that is unambiguous among the discovered clones (see DisplayRef). Every
// reference, including an absolute "/path:bookmark", is resolved to a single
// discovered main clone before it is matched; one that names no discovered
// clone, or several, never matches and is reported as a diagnostic. The same
// bookmark name in two repositories therefore never matches.
package effort

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/virtru/wgo/internal/discovery"
	gh "github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/store"
)

// MainCloneInfo represents a discovered main clone repository.
type MainCloneInfo struct {
	Path  string // Absolute path to main clone
	Name  string // Directory name (e.g., "wgo")
	Owner string // Owner name if discoverable (e.g., "dmihalcik-virtru")
	Repo  string // Repo name if discoverable (e.g., "wgo")
}

// Slug returns "owner/repo" when both are known, otherwise "".
func (c MainCloneInfo) Slug() string {
	if c.Owner == "" || c.Repo == "" {
		return ""
	}
	return c.Owner + "/" + c.Repo
}

// describe renders a clone for error messages and diagnostics.
func (c MainCloneInfo) describe() string {
	if s := c.Slug(); s != "" {
		return s + " (" + c.Path + ")"
	}
	return c.Path
}

// splitRef splits "repo:bookmark" with the plan package's definition, so the
// plan parser and reference resolution agree on where the bookmark starts.
func splitRef(ref string) (string, string, bool) {
	return plan.ParseBranchRef(ref)
}

// MatchRepo returns the main clones a repo reference names. The reference
// may be a clone's directory name, its owner/repo slug, or its absolute path.
func MatchRepo(repoRef string, mainClones []MainCloneInfo) []MainCloneInfo {
	var matches []MainCloneInfo
	seen := map[string]bool{}
	for _, clone := range mainClones {
		if seen[clone.Path] {
			continue
		}
		hit := clone.Name == repoRef ||
			(clone.Slug() != "" && strings.EqualFold(clone.Slug(), repoRef)) ||
			(filepath.IsAbs(repoRef) && filepath.Clean(repoRef) == filepath.Clean(clone.Path))
		if hit {
			matches = append(matches, clone)
			seen[clone.Path] = true
		}
	}
	return matches
}

// ResolveRef resolves a "repo:bookmark" reference to exactly one discovered
// main clone. The error lists the candidates and says how to disambiguate.
func ResolveRef(ref string, mainClones []MainCloneInfo) (MainCloneInfo, string, error) {
	repoRef, bookmark, ok := splitRef(ref)
	if !ok {
		return MainCloneInfo{}, "", fmt.Errorf("invalid reference %q: want repo:bookmark", ref)
	}
	matches := MatchRepo(repoRef, mainClones)
	switch len(matches) {
	case 1:
		return matches[0], bookmark, nil
	case 0:
		var known []string
		for _, c := range mainClones {
			known = append(known, c.Name)
		}
		sort.Strings(known)
		hint := "no repositories were discovered; check discovery.base_dirs in ~/.wgo/config.toml"
		if len(known) > 0 {
			hint = "discovered repos: " + strings.Join(dedupe(known), ", ") +
				"; use one of these names, owner/repo, or the clone's absolute path"
		}
		return MainCloneInfo{}, "", fmt.Errorf("reference %s: no discovered main clone matches %q (%s)", ref, repoRef, hint)
	default:
		var cands []string
		for _, m := range matches {
			cands = append(cands, m.describe())
		}
		sort.Strings(cands)
		return MainCloneInfo{}, "", fmt.Errorf("reference %s: %q is ambiguous, it matches %d clones: %s; use owner/repo or the absolute path instead",
			ref, repoRef, len(matches), strings.Join(cands, ", "))
	}
}

// DisplayRef is the human-readable form of a reference written to the plan:
// the clone's directory name when no other discovered clone shares it, else
// its owner/repo slug when unique, else its absolute path. Each form resolves
// back to the same clone with ResolveRef.
func DisplayRef(clone MainCloneInfo, bookmark string, mainClones []MainCloneInfo) string {
	if clone.Name != "" && len(MatchRepo(clone.Name, mainClones)) == 1 {
		return clone.Name + ":" + bookmark
	}
	if s := clone.Slug(); s != "" && len(MatchRepo(s, mainClones)) == 1 {
		return s + ":" + bookmark
	}
	return clone.Path + ":" + bookmark
}

func dedupe(sorted []string) []string {
	var out []string
	for i, s := range sorted {
		if i == 0 || s != sorted[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// ResolveMainClone finds the main clone for a workspace.
// If the workspace is a worktree, return its main repo path.
// Otherwise, return the workspace path itself.
func ResolveMainClone(ws discovery.DiscoveredRepo) string {
	if ws.IsWorktree && ws.MainRepoPath != "" {
		return ws.MainRepoPath
	}
	return ws.Path
}

// NormalizeBookmarkRef creates a normalized bookmark reference using the main
// clone path. It is the same format as store.AnnotationKey.
func NormalizeBookmarkRef(mainClonePath, bookmark string) string {
	return store.AnnotationKey(mainClonePath, bookmark)
}

// MainClones returns the distinct main clones behind the discovered repos,
// sorted by path. Owner and repo come from the origin remote when it is a
// GitHub URL, else from the <owner>/<repo> directory layout.
func MainClones(jjc jj.Client, repos []discovery.DiscoveredRepo) []MainCloneInfo {
	clones, _ := MainClonesWithDiagnostics(jjc, repos)
	return clones
}

// MainClonesWithDiagnostics is MainClones that also reports each clone whose
// remotes could not be read. Such a clone falls back to the directory layout
// for its owner/repo slug, which may be a guess, so owner/repo references to
// it deserve a second look.
func MainClonesWithDiagnostics(jjc jj.Client, repos []discovery.DiscoveredRepo) ([]MainCloneInfo, []string) {
	var diags []string
	seen := map[string]bool{}
	var clones []MainCloneInfo
	for _, r := range repos {
		path := filepath.Clean(ResolveMainClone(r))
		if seen[path] {
			continue
		}
		seen[path] = true
		clone := MainCloneInfo{
			Path:  path,
			Name:  filepath.Base(path),
			Owner: filepath.Base(filepath.Dir(path)),
			Repo:  filepath.Base(path),
		}
		if jjc != nil {
			if remotes, err := jjc.RemoteURLs(path); err == nil {
				if owner, repo, ok := strings.Cut(gh.SlugFromRemoteURL(remotes["origin"]), "/"); ok {
					clone.Owner, clone.Repo = owner, repo
				}
			}
		}
		clones = append(clones, clone)
	}
	sort.Slice(clones, func(i, j int) bool { return clones[i].Path < clones[j].Path })
	sort.Strings(diags)
	return clones, diags
}
