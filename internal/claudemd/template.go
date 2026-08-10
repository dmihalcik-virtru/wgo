// Package claudemd renders the CLAUDE.md scaffold for a multi-repo wgo
// workspace, onboarding a coding agent to the repos involved and the spec
// that defines the goal. Callers (see internal/cmd) are responsible for
// writing the rendered result to the shared workspace root.
package claudemd

import (
	_ "embed"
	"fmt"
	"strings"
	"text/template"
)

// defaultTemplate is a text/template over TemplateData. The newlines around
// its {{ range }}/{{ end }} and {{ if }}/{{ end }} actions are load-bearing —
// they produce the blank lines between sections — so reformatting the file
// changes the generated output.
//
//go:embed default_template.md
var defaultTemplate string

// Marker identifies a CLAUDE.md that this package generated. It is the first
// line of every rendered file, and callers use it to tell their own output
// apart from a hand-written CLAUDE.md they must not overwrite.
const Marker = "<!-- wgo:generated-claude-md -->"

// RepoEntry describes one repo checkout under the shared workspace root.
type RepoEntry struct {
	Dir string // subdirectory name under the shared root

	// Label names the repo in prose, normally as "owner/repo". It degrades to
	// a bare directory name when no remote resolves, so it is for display
	// only — do not parse it or assume it contains a slash.
	Label string
}

// TemplateData holds the values RenderTemplate substitutes into the embedded
// CLAUDE.md template.
type TemplateData struct {
	Ticket string // may be empty (e.g. joining a non-ticket branch)

	// Description and SpecPath both feed the "Goal" section: SpecPath renders
	// when non-empty, Description is the fallback, and callers may set both.
	// If both are empty the section renders a visible placeholder telling the
	// agent to ask, rather than rendering an empty heading.
	Description string
	SpecPath    string // relative to the shared root, slash-separated

	Repos []RepoEntry // rendered in the order given
}

// RenderTemplate renders d into the markdown body of a shared CLAUDE.md. The
// result always begins with Marker. Callers are responsible for supplying a
// meaningful Repos list; rendering succeeds regardless of which fields are set.
func RenderTemplate(d TemplateData) ([]byte, error) {
	tmpl, err := template.New("claudemd").Parse(defaultTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, d); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}

	return []byte(buf.String()), nil
}
