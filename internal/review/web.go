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

// webFS holds the self-contained explorer: an HTML shell plus the style,
// app and vendored scripts that Render inlines into it.
//
//go:embed web
var webFS embed.FS

// Render returns a single self-contained HTML page that explores g. All
// styles, scripts and the graph data are inlined; the page loads nothing
// from the network.
func Render(g *Graph) ([]byte, error) {
	shell, err := webFile("web/index.html")
	if err != nil {
		return nil, err
	}
	style, err := webFile("web/style.css")
	if err != nil {
		return nil, err
	}
	app, err := webFile("web/app.js")
	if err != nil {
		return nil, err
	}

	names, err := fs.Glob(webFS, "web/vendor/*.js")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var vendor strings.Builder
	for _, name := range names {
		b, err := webFile(name)
		if err != nil {
			return nil, err
		}
		vendor.WriteString(b)
		vendor.WriteString("\n")
	}

	if containsFold(style, "</style") {
		return nil, fmt.Errorf("web/style.css contains a literal </style; rewrite it (e.g. \"<\\/style\") so the page stays well-formed")
	}
	if containsFold(app, "</script") {
		return nil, fmt.Errorf("web/app.js contains a literal </script; rewrite it as \"<\\/script\" so the page stays well-formed")
	}
	if containsFold(vendor.String(), "</script") {
		return nil, fmt.Errorf("a web/vendor/*.js file contains a literal </script; rewrite it as \"<\\/script\" so the page stays well-formed")
	}

	// json.Marshal escapes <, >, & and U+2028/U+2029, so the payload cannot
	// terminate the surrounding <script> element.
	data, err := json.Marshal(g)
	if err != nil {
		return nil, err
	}

	// NewReplacer makes a single pass, so placeholder-like text inside the
	// substituted content (e.g. in the data) is never re-expanded.
	r := strings.NewReplacer(
		"/*WGO_TITLE*/", html.EscapeString(g.Label),
		"/*WGO_STYLE*/", style,
		"/*WGO_VENDOR*/", vendor.String(),
		"/*WGO_APP*/", app,
		"/*WGO_DATA*/", string(data),
	)
	return []byte(r.Replace(shell)), nil
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
