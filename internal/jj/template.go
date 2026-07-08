package jj

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// TemplateSchemaVersion is bumped whenever any template constant or its
// matching parser changes shape. Used by SmokeCheck to fail loudly when jj
// upstream renames a template field we depend on.
const TemplateSchemaVersion = 2

// LogEntryTemplate renders a `jj log` record as a single line of JSON. Pair
// with `--no-graph` and ParseLogEntries to consume.
//
// We emit change_id in jj's canonical z-k encoding (the form `jj edit` and
// revset syntax accept), not normal-hex. The full 32-char form is rendered
// via `.short(32)` because ChangeId itself isn't a Stringify; only its
// String-typed projections are.
//
// Fields:
//
//	change_id            32-char z-k change id (the form jj revsets accept)
//	commit_id            full 40-char git commit id
//	description          raw description string
//	bookmarks            local bookmark names pointing at the change
//	parents              parent change-ids (z-k form)
//	author_email_local   local part of author email (jj exposes Email as
//	                     {local, domain}; we rejoin in ParseLogEntries
//	                     because Template-typed concatenations cannot be
//	                     escape_json'd directly)
//	author_email_domain  domain part of author email
//	author_timestamp     ISO 8601 ("%+") author timestamp
//	empty                true if the change touches no files
//	current_working_copy true if this is the workspace's @
const LogEntryTemplate = `"{\"change_id\":" ++ change_id.short(32).escape_json() ` +
	`++ ",\"commit_id\":" ++ commit_id.short(40).escape_json() ` +
	`++ ",\"description\":" ++ description.escape_json() ` +
	`++ ",\"bookmarks\":[" ++ bookmarks.map(|b| b.name().escape_json()).join(",") ` +
	`++ "],\"parents\":[" ++ parents.map(|p| p.change_id().short(32).escape_json()).join(",") ` +
	`++ "],\"author_email_local\":" ++ author.email().local().escape_json() ` +
	`++ ",\"author_email_domain\":" ++ author.email().domain().escape_json() ` +
	`++ ",\"author_timestamp\":" ++ author.timestamp().format("%+").escape_json() ` +
	`++ ",\"empty\":" ++ if(empty, "true", "false") ` +
	`++ ",\"current_working_copy\":" ++ if(current_working_copy, "true", "false") ` +
	`++ "}\n"`

// BookmarkListTemplate renders a `jj bookmark list` record as one line of
// JSON. Each row is a single bookmark (local or remote depending on the
// list options used).
//
// Fields:
//
//	name       local bookmark name
//	remote     remote name ("" for purely local bookmarks)
//	change_id  change id of the bookmark's target (or "")
//	commit_id  commit id of the bookmark's target (or "")
//	tracked    true if a tracked remote pair exists
//	conflict   true if the bookmark has conflicting targets
//	present    true if the bookmark points to any commit
const BookmarkListTemplate = `"{\"name\":" ++ self.name().escape_json() ` +
	`++ ",\"remote\":" ++ if(self.remote(), self.remote().escape_json(), "\"\"") ` +
	`++ ",\"change_id\":" ++ if(self.normal_target(), self.normal_target().change_id().short(32).escape_json(), "\"\"") ` +
	`++ ",\"commit_id\":" ++ if(self.normal_target(), self.normal_target().commit_id().short(40).escape_json(), "\"\"") ` +
	`++ ",\"tracked\":" ++ if(self.tracked(), "true", "false") ` +
	`++ ",\"conflict\":" ++ if(self.conflict(), "true", "false") ` +
	`++ ",\"present\":" ++ if(self.present(), "true", "false") ` +
	`++ "}\n"`

// WorkspaceListTemplate renders a `jj workspace list` record as one line of
// JSON.
//
// Fields:
//
//	name       workspace name (e.g. "default")
//	root       absolute path to the workspace's root
//	change_id  change id of the workspace's @
//	commit_id  commit id of the workspace's @
const WorkspaceListTemplate = `"{\"name\":" ++ self.name().escape_json() ` +
	`++ ",\"root\":" ++ self.root().escape_json() ` +
	`++ ",\"change_id\":" ++ self.target().change_id().short(32).escape_json() ` +
	`++ ",\"commit_id\":" ++ self.target().commit_id().short(40).escape_json() ` +
	`++ "}\n"`

// rawLogEntry is the wire-format struct emitted by LogEntryTemplate.
type rawLogEntry struct {
	ChangeID           string   `json:"change_id"`
	CommitID           string   `json:"commit_id"`
	Description        string   `json:"description"`
	Bookmarks          []string `json:"bookmarks"`
	Parents            []string `json:"parents"`
	AuthorEmailLocal   string   `json:"author_email_local"`
	AuthorEmailDomain  string   `json:"author_email_domain"`
	AuthorTimestamp    string   `json:"author_timestamp"`
	Empty              bool     `json:"empty"`
	CurrentWorkingCopy bool     `json:"current_working_copy"`
}

// rawBookmark is the wire-format struct emitted by BookmarkListTemplate.
type rawBookmark struct {
	Name     string `json:"name"`
	Remote   string `json:"remote"`
	ChangeID string `json:"change_id"`
	CommitID string `json:"commit_id"`
	Tracked  bool   `json:"tracked"`
	Conflict bool   `json:"conflict"`
	Present  bool   `json:"present"`
}

// rawWorkspace is the wire-format struct emitted by WorkspaceListTemplate.
type rawWorkspace struct {
	Name     string `json:"name"`
	Root     string `json:"root"`
	ChangeID string `json:"change_id"`
	CommitID string `json:"commit_id"`
}

// ParseLogEntries parses line-delimited JSON produced by `jj log -T LogEntryTemplate`.
// Blank lines are tolerated; malformed lines abort the parse with a wrapped
// error that includes the offending payload.
func ParseLogEntries(stdout []byte) ([]LogEntry, error) {
	lines := bytes.Split(bytes.TrimRight(stdout, "\n"), []byte{'\n'})
	out := make([]LogEntry, 0, len(lines))
	for i, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var raw rawLogEntry
		if err := json.Unmarshal(line, &raw); err != nil {
			return nil, fmt.Errorf("parse log entry %d: %w: %s", i, err, string(line))
		}
		ts, err := parseTimestamp(raw.AuthorTimestamp)
		if err != nil {
			return nil, fmt.Errorf("parse log entry %d timestamp %q: %w", i, raw.AuthorTimestamp, err)
		}
		email := raw.AuthorEmailLocal
		if raw.AuthorEmailDomain != "" {
			email = raw.AuthorEmailLocal + "@" + raw.AuthorEmailDomain
		}
		out = append(out, LogEntry{
			ChangeID:           raw.ChangeID,
			CommitID:           raw.CommitID,
			Description:        raw.Description,
			Bookmarks:          raw.Bookmarks,
			Parents:            raw.Parents,
			AuthorEmail:        email,
			AuthorTimestamp:    ts,
			Empty:              raw.Empty,
			CurrentWorkingCopy: raw.CurrentWorkingCopy,
		})
	}
	return out, nil
}

// ParseBookmarks parses line-delimited JSON produced by
// `jj bookmark list -T BookmarkListTemplate`.
func ParseBookmarks(stdout []byte) ([]Bookmark, error) {
	lines := bytes.Split(bytes.TrimRight(stdout, "\n"), []byte{'\n'})
	out := make([]Bookmark, 0, len(lines))
	for i, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var raw rawBookmark
		if err := json.Unmarshal(line, &raw); err != nil {
			return nil, fmt.Errorf("parse bookmark %d: %w: %s", i, err, string(line))
		}
		out = append(out, Bookmark{
			Name:     raw.Name,
			ChangeID: raw.ChangeID,
			CommitID: raw.CommitID,
			Remote:   raw.Remote,
			Tracked:  raw.Tracked,
			Conflict: raw.Conflict,
			Present:  raw.Present,
		})
	}
	return out, nil
}

// ParseWorkspaces parses line-delimited JSON produced by
// `jj workspace list -T WorkspaceListTemplate`.
func ParseWorkspaces(stdout []byte) ([]Workspace, error) {
	lines := bytes.Split(bytes.TrimRight(stdout, "\n"), []byte{'\n'})
	out := make([]Workspace, 0, len(lines))
	for i, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var raw rawWorkspace
		if err := json.Unmarshal(line, &raw); err != nil {
			return nil, fmt.Errorf("parse workspace %d: %w: %s", i, err, string(line))
		}
		out = append(out, Workspace{
			Name:     raw.Name,
			Path:     raw.Root,
			ChangeID: raw.ChangeID,
			CommitID: raw.CommitID,
		})
	}
	return out, nil
}

// parseTimestamp accepts both jj's "%+" output (e.g. 2026-06-15T12:30:02-04:00)
// and the RFC3339 variant used for the root commit's zero timestamp.
func parseTimestamp(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05-07:00",
		time.RFC3339Nano,
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp layout")
}
