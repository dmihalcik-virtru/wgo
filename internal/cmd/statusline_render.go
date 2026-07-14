package cmd

import (
	"fmt"
	"io"
	"strings"
	"text/template"

	"github.com/virtru/wgo/internal/links"
	"github.com/virtru/wgo/models"
)

// ANSI SGR color codes used by the default statusline. An empty code means
// "no color", so colorize is a no-op for it.
const (
	colReset   = "\033[0m"
	colDim     = "2"
	colRed     = "31"
	colGreen   = "32"
	colYellow  = "33"
	colMagenta = "35"
	colCyan    = "36"
)

// colorNames maps friendly names accepted by the {{color}} template func to
// ANSI codes.
var colorNames = map[string]string{
	"dim": colDim, "red": colRed, "green": colGreen,
	"yellow": colYellow, "magenta": colMagenta, "cyan": colCyan,
	"blue": "34", "bold": "1",
}

// colorize wraps s in an ANSI SGR code when rich is true and code is non-empty.
func colorize(s, code string, rich bool) string {
	if !rich || code == "" || s == "" {
		return s
	}
	return "\033[" + code + "m" + s + colReset
}

// styleLink colors text and, when rich, wraps it in an OSC8 hyperlink.
func styleLink(url, text, code string, rich bool) string {
	return links.Link(url, colorize(text, code, rich), rich)
}

// prStateColor returns the color for a PR state word (case-insensitive).
func prStateColor(state string) string {
	switch strings.ToLower(state) {
	case "open":
		return colGreen
	case "merged":
		return colMagenta
	case "closed":
		return colRed
	case "draft":
		return colDim
	default:
		return ""
	}
}

// prStateWord returns the display state token for a PR: "draft" when it is a
// draft, otherwise the lowercased PR state (open/merged/closed). Shared by the
// text and statusline renderers so a draft reads the same in both.
func prStateWord(pr models.PRRef) string {
	if pr.IsDraft {
		return "draft"
	}
	return strings.ToLower(pr.State)
}

// reviewGlyph maps a review decision to a compact marker: ✓ approved, ✗ changes
// requested, "" otherwise. Plain (uncolored); callers colorize as needed.
func reviewGlyph(decision string) string {
	switch strings.ToUpper(decision) {
	case "APPROVED":
		return "✓"
	case "CHANGES_REQUESTED":
		return "✗"
	}
	return ""
}

// reviewColor returns the statusline color for a review decision.
func reviewColor(decision string) string {
	switch strings.ToUpper(decision) {
	case "APPROVED":
		return colGreen
	case "CHANGES_REQUESTED":
		return colRed
	}
	return ""
}

// ciStateColor returns the statusline color for a CI rollup state (the dot).
func ciStateColor(state string) string {
	switch strings.ToLower(state) {
	case "success":
		return colGreen
	case "failure":
		return colRed
	case "pending":
		return colYellow
	}
	return ""
}

// renderStatuslineLine writes the built-in default single line: repo, branch
// (with a dirty marker), ticket, ahead/behind, and each PR. When rich, segments
// are colored and hyperlinked (repo/branch/PR → GitHub, ticket → Jira/GitHub).
// Empty segments are omitted, so a cache miss simply drops the PR part.
func renderStatuslineLine(w io.Writer, c *models.Context, rich bool) error {
	var parts []string

	if c.Repo != "" {
		parts = append(parts, styleLink(c.RepoURL, c.Repo, colCyan, rich))
	}

	branch := styleLink(c.BranchURL, c.Branch, colGreen, rich)
	if c.Dirty {
		branch += colorize("*", colRed, rich)
	}
	parts = append(parts, branch)

	if c.Ticket != "" {
		label := "[" + c.Ticket + "]"
		if c.JiraStatus != "" {
			label = "[" + c.Ticket + " " + c.JiraStatus + "]"
		}
		parts = append(parts, styleLink(c.TicketURL, label, colYellow, rich))
	}

	if ab := aheadBehind(c.Ahead, c.Behind, rich); ab != "" {
		parts = append(parts, ab)
	}

	for _, pr := range c.PRs {
		state := prStateWord(pr)
		code := prStateColor(state)
		num := styleLink(pr.URL, fmt.Sprintf("#%d", pr.Number), code, rich)
		seg := num + " " + colorize(state, code, rich)
		if g := reviewGlyph(pr.ReviewDecision); g != "" {
			seg += " " + colorize(g, reviewColor(pr.ReviewDecision), rich)
		}
		if pr.Checks.State != "" && pr.Checks.State != "none" {
			seg += " " + styleLink(pr.Checks.URL, "●", ciStateColor(pr.Checks.State), rich)
		}
		parts = append(parts, seg)
	}

	if c.Agent != nil {
		parts = append(parts, colorize("🤖 "+c.Agent.Name, colMagenta, rich))
	}

	_, err := fmt.Fprintln(w, strings.Join(parts, " "))
	return err
}

// aheadBehind formats the ↑ahead ↓behind segment, dim-colored, or "" when the
// branch is level with its remote.
func aheadBehind(ahead, behind int, rich bool) string {
	var b strings.Builder
	if ahead > 0 {
		fmt.Fprintf(&b, "↑%d", ahead)
	}
	if behind > 0 {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "↓%d", behind)
	}
	if b.Len() == 0 {
		return ""
	}
	return colorize(b.String(), colDim, rich)
}

// renderStatuslineFormat renders a user-supplied Go text/template over the
// context. Template funcs: upper, lower, color (by name), link (OSC8). color
// and link honor the rich flag. A bad template returns a non-nil error.
func renderStatuslineFormat(w io.Writer, c *models.Context, tmplText string, rich bool) error {
	funcs := template.FuncMap{
		"upper": strings.ToUpper,
		"lower": strings.ToLower,
		"color": func(name, s string) string { return colorize(s, colorNames[name], rich) },
		"link":  func(url, text string) string { return links.Link(url, text, rich) },
	}
	t, err := template.New("statusline").Funcs(funcs).Parse(tmplText)
	if err != nil {
		return fmt.Errorf("invalid --format template: %w", err)
	}
	var b strings.Builder
	if err := t.Execute(&b, c); err != nil {
		return fmt.Errorf("rendering statusline: %w", err)
	}
	_, err = fmt.Fprintln(w, strings.TrimRight(b.String(), "\n"))
	return err
}
