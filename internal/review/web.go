package review

import (
	"embed"
	"encoding/json"
	"fmt"
	"html"
	"io/fs"
	"sort"
	"strings"
)

// webFS holds the self-contained explorer: HTML shells plus the styles, apps
// and vendored scripts that RenderPage inlines into them.
//
//go:embed web
var webFS embed.FS

// Mode selects what the shared explorer renders.
type Mode string

const (
	// ModeReview renders the historical influence graph (web/app.js). It is
	// what `wgo review graph` exports.
	ModeReview Mode = "review"
	// ModeLive renders the `wgo dash` snapshot (web/live.js), which the page
	// fetches from the serving dashboard.
	ModeLive Mode = "live"
)

// Page describes one explorer page.
type Page struct {
	Mode Mode
	// Title is the page title suffix; review mode defaults to the graph label.
	Title string
	// Graph is the review-mode data. Live mode fetches its data instead.
	Graph *Graph
	// Boot, when non-nil, is JSON-encoded into a
	// <script id="wgo-boot" type="application/json"> element that tells the
	// scripts how they are being served. Review exports carry no Boot.
	Boot any
	// Addons names embedded web/*.js scripts inlined after the mode's app,
	// such as web/served.js for review pages served by `wgo dash`.
	Addons []string
}

// Render returns a single self-contained HTML page that explores g. All
// styles, scripts and the graph data are inlined; the page loads nothing
// from the network.
func Render(g *Graph) ([]byte, error) {
	return RenderPage(Page{Mode: ModeReview, Graph: g})
}

// RenderPage renders p. A review page with no Boot and no Addons is exactly
// Render's offline export: the extra elements are only appended when asked
// for, so the exported HTML is unaffected by the dashboard's needs.
func RenderPage(p Page) ([]byte, error) {
	switch p.Mode {
	case ModeReview:
		if p.Graph == nil {
			return nil, fmt.Errorf("review page needs a graph")
		}
		return renderShell("web/index.html", []string{"web/style.css"}, "web/app.js", p, p.Graph)
	case ModeLive:
		return renderShell("web/live.html", []string{"web/style.css", "web/live.css"}, "web/live.js", p, nil)
	default:
		return nil, fmt.Errorf("unknown explorer mode %q", p.Mode)
	}
}

func renderShell(shellName string, styleNames []string, appName string, p Page, g *Graph) ([]byte, error) {
	shell, err := webFile(shellName)
	if err != nil {
		return nil, err
	}
	var style strings.Builder
	for i, name := range styleNames {
		b, err := webFile(name)
		if err != nil {
			return nil, err
		}
		if i > 0 {
			style.WriteString("\n")
		}
		style.WriteString(b)
	}
	app, err := webFile(appName)
	if err != nil {
		return nil, err
	}
	vendor, err := vendorScripts()
	if err != nil {
		return nil, err
	}

	if containsFold(style.String(), "</style") {
		return nil, fmt.Errorf("an explorer stylesheet contains a literal </style; rewrite it (e.g. \"<\\/style\") so the page stays well-formed")
	}
	if containsFold(app, "</script") {
		return nil, fmt.Errorf("%s contains a literal </script; rewrite it as \"<\\/script\" so the page stays well-formed", appName)
	}
	if containsFold(vendor, "</script") {
		return nil, fmt.Errorf("a web/vendor/*.js file contains a literal </script; rewrite it as \"<\\/script\" so the page stays well-formed")
	}

	// json.Marshal escapes <, >, & and U+2028/U+2029, so the payload cannot
	// terminate the surrounding <script> element.
	data := []byte("null")
	title := p.Title
	if g != nil {
		data, err = json.Marshal(g)
		if err != nil {
			return nil, err
		}
		if title == "" {
			title = g.Label
		}
	}

	var extra strings.Builder
	if p.Boot != nil {
		boot, err := json.Marshal(p.Boot)
		if err != nil {
			return nil, err
		}
		extra.WriteString(`<script id="wgo-boot" type="application/json">`)
		extra.Write(boot)
		extra.WriteString("</script>\n")
	}
	for _, name := range p.Addons {
		if !strings.HasPrefix(name, "web/") || !strings.HasSuffix(name, ".js") || strings.Contains(name, "/vendor/") {
			return nil, fmt.Errorf("explorer addon %q is not a web/*.js script", name)
		}
		b, err := webFile(name)
		if err != nil {
			return nil, err
		}
		if containsFold(b, "</script") {
			return nil, fmt.Errorf("%s contains a literal </script; rewrite it as \"<\\/script\"", name)
		}
		extra.WriteString("<script>\n")
		extra.WriteString(b)
		extra.WriteString("\n</script>\n")
	}

	// NewReplacer makes a single pass, so placeholder-like text inside the
	// substituted content (e.g. in the data) is never re-expanded.
	r := strings.NewReplacer(
		"/*WGO_TITLE*/", html.EscapeString(title),
		"/*WGO_STYLE*/", style.String(),
		"/*WGO_VENDOR*/", vendor,
		"/*WGO_APP*/", app,
		"/*WGO_DATA*/", string(data),
	)
	out := r.Replace(shell)
	if extra.Len() > 0 {
		i := strings.LastIndex(out, "</body>")
		if i < 0 {
			return nil, fmt.Errorf("%s has no </body>", shellName)
		}
		out = out[:i] + extra.String() + out[i:]
	}
	return []byte(out), nil
}

func vendorScripts() (string, error) {
	names, err := fs.Glob(webFS, "web/vendor/*.js")
	if err != nil {
		return "", err
	}
	sort.Strings(names)
	var vendor strings.Builder
	for _, name := range names {
		b, err := webFile(name)
		if err != nil {
			return "", err
		}
		vendor.WriteString(b)
		vendor.WriteString("\n")
	}
	return vendor.String(), nil
}

func webFile(name string) (string, error) {
	b, err := webFS.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("embedded %s: %w", name, err)
	}
	return string(b), nil
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), sub)
}
