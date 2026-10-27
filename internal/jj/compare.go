package jj

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Read helpers that compare two arbitrary revisions rather than a revision
// and its parent. They back `wgo clean`'s workspace classification, which asks
// whether a workspace's work already exists somewhere else. All of them run
// inside repo (a workspace root), so `@` is that workspace's working copy and
// paths are relative to its root. They are not on Client: only the cleanup
// classifier needs them, and it takes a narrow interface of its own.

// FilesetPath returns a fileset expression matching exactly path, relative to
// the workspace root, quoted so spaces and glob characters stay literal.
func FilesetPath(path string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `root-file:"` + r.Replace(path) + `"`
}

// filesetArgs turns paths into trailing `-- <fileset>...` arguments, or none
// when paths is empty (meaning every file).
func filesetArgs(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	args := make([]string, 0, len(paths)+1)
	args = append(args, "--")
	for _, p := range paths {
		args = append(args, FilesetPath(p))
	}
	return args
}

// DiffSummaryBetween returns the per-file changes from revision from to
// revision to, parsed from `jj diff --summary`. Status is 'A', 'M' or 'D', or
// 'R'/'C' for a rename or copy, whose Path is jj's "{old => new}" form.
func (c *CLIClient) DiffSummaryBetween(repo, from, to string) ([]FileChange, error) {
	out, err := c.runIn(repo, "--color=never", "diff", "--from", from, "--to", to, "--summary")
	if err != nil {
		return nil, err
	}
	var changes []FileChange
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) < 3 || line[1] != ' ' {
			continue
		}
		changes = append(changes, FileChange{Status: rune(line[0]), Path: line[2:]})
	}
	return changes, nil
}

// DiffStatBetween returns the lines added and deleted from revision from to
// revision to, from the summary line of `jj diff --stat`.
func (c *CLIClient) DiffStatBetween(repo, from, to string) (added, deleted int, err error) {
	out, err := c.runIn(repo, "--color=never", "diff", "--from", from, "--to", to, "--stat")
	if err != nil {
		return 0, 0, err
	}
	return parseDiffStatSummary(out)
}

// DiffNamesBetween returns the paths whose content differs between revisions
// from and to, limited to paths when it is non-empty. A path absent from the
// result is identical in both, including absent from both.
func (c *CLIClient) DiffNamesBetween(repo, from, to string, paths []string) ([]string, error) {
	args := append([]string{"--color=never", "diff", "--from", from, "--to", to, "--name-only"}, filesetArgs(paths)...)
	out, err := c.runIn(repo, args...)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if f := strings.TrimRight(line, "\r"); f != "" {
			names = append(names, f)
		}
	}
	return names, nil
}

// DiffGitBetween returns the git-format diff from revision from to revision
// to with no context lines, limited to paths when it is non-empty. With no
// context every hunk header gives exact line ranges, which is what a
// three-way comparison of two such diffs needs.
func (c *CLIClient) DiffGitBetween(repo, from, to string, paths []string) (string, error) {
	args := append([]string{"--color=never", "diff", "--from", from, "--to", to, "--git", "--context", "0"}, filesetArgs(paths)...)
	return c.runIn(repo, args...)
}

// RefNames returns the names of the local bookmarks pointing at commits in
// revset, plus those of remote bookmarks when remote is set, deduplicated and
// sorted. A remote bookmark contributes its bare name; refs of the git
// pseudo-remote are skipped, since they only mirror local bookmarks.
func (c *CLIClient) RefNames(repo, revset string, remote bool) ([]string, error) {
	tmpl := `local_bookmarks.map(|b| b.name() ++ "\n").join("")`
	if remote {
		tmpl += ` ++ remote_bookmarks.filter(|b| b.remote() != "` + GitPseudoRemote + `").map(|b| b.name() ++ "\n").join("")`
	}
	out, err := c.runIn(repo, "--color=never", "log", "--no-graph", "-r", revset, "-T", tmpl)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		n := strings.TrimSpace(line)
		if n != "" && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names, nil
}

// Membership is a log entry plus the revsets that contain it.
type Membership struct {
	LogEntry
	// LocalBookmarks are the local bookmarks on the commit. Unlike
	// LogEntry.Bookmarks they never include a remote bookmark.
	LocalBookmarks []string
	// RemoteBookmarks are the bare names of the remote bookmarks on the
	// commit, skipping the git pseudo-remote.
	RemoteBookmarks []string
	// In[i] reports whether the i-th revset passed to LogMembership contains
	// the commit.
	In []bool
}

// LogMembership lists the commits in revset like Log does and reports, for
// each, which of sets contain it — one jj invocation however many sets there
// are, using the template method contained_in. It lets a caller that would
// otherwise run one `jj log` per question ask them all at once.
func (c *CLIClient) LogMembership(repo, revset string, sets []string) ([]Membership, error) {
	quote := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	in := make([]string, len(sets))
	for i, set := range sets {
		in[i] = `if(self.contained_in("` + quote.Replace(set) + `"), "true", "false")`
	}
	inList := `"[]"`
	if len(in) > 0 {
		inList = `"[" ++ ` + strings.Join(in, ` ++ "," ++ `) + ` ++ "]"`
	}
	tmpl := strings.TrimSuffix(LogEntryTemplate, `++ "}\n"`) +
		`++ ",\"local_bookmarks\":[" ++ local_bookmarks.map(|b| b.name().escape_json()).join(",") ++ "]" ` +
		`++ ",\"remote_bookmarks\":[" ++ remote_bookmarks.filter(|b| b.remote() != "` + GitPseudoRemote + `").map(|b| b.name().escape_json()).join(",") ++ "]" ` +
		`++ ",\"in\":" ++ ` + inList + ` ++ "}\n"`
	out, err := c.runR(repo, "--color=never", "log", "--no-graph", "-r", revset, "-T", tmpl)
	if err != nil {
		return nil, err
	}
	var entries []Membership
	for i, line := range bytes.Split([]byte(out), []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var raw struct {
			rawLogEntry
			LocalBookmarks  []string `json:"local_bookmarks"`
			RemoteBookmarks []string `json:"remote_bookmarks"`
			In              []bool   `json:"in"`
		}
		if err := json.Unmarshal(line, &raw); err != nil {
			return nil, fmt.Errorf("parse log entry %d: %w: %s", i, err, line)
		}
		e, err := raw.rawLogEntry.entry()
		if err != nil {
			return nil, fmt.Errorf("parse log entry %d: %w", i, err)
		}
		if len(raw.In) != len(sets) {
			return nil, fmt.Errorf("parse log entry %d: %d membership flags for %d revsets", i, len(raw.In), len(sets))
		}
		entries = append(entries, Membership{LogEntry: e, LocalBookmarks: raw.LocalBookmarks, RemoteBookmarks: raw.RemoteBookmarks, In: raw.In})
	}
	return entries, nil
}
