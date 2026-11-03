package dash

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strings"

	"github.com/virtru/wgo/internal/review"
)

// liveBoot is the bootstrap the live page reads from its
// <script id="wgo-boot"> element.
type liveBoot struct {
	Mode   string `json:"mode"`
	API    string `json:"api"`
	Links  string `json:"links,omitempty"`
	PollMS int64  `json:"poll_ms"`
	// Focus is a dash node ID to select once the first snapshot arrives;
	// Entity is the identifier the user asked /lookup for.
	Focus  string `json:"focus,omitempty"`
	Entity string `json:"entity,omitempty"`
	// Token is the per-launch action token, sent back in TokenHeader.
	// ActionAPI and AckAPI are set when actions and Mark seen are enabled;
	// Resume names the configured resume tool (empty hides Resume).
	Token     string `json:"token,omitempty"`
	ActionAPI string `json:"action_api,omitempty"`
	AckAPI    string `json:"ack_api,omitempty"`
	Resume    string `json:"resume,omitempty"`
	// Fields is the query field registry and Filters the saved filters
	// (gh-100). Me and People are what @me and @people mean in the
	// snapshot being served (queryEnv); Extract is the Markdown route.
	Fields  *RegistryJSON `json:"fields,omitempty"`
	Filters []SavedFilter `json:"filters,omitempty"`
	Me      string        `json:"me,omitempty"`
	People  []QueryPerson `json:"people,omitempty"`
	Extract string        `json:"extract,omitempty"`
}

// liveAddon is one live page addon (spec/gh-100.md, requirement 27):
//
//   - Script, web/live-<name>.js under internal/review/web/, is inlined
//     after live.js and registers views or panels through
//     window.WGOLive.registerView/registerPanel before the page starts.
//     It follows live.js's rules: no markup from strings, no eval, no
//     storage, no POST and no token (TestLiveAddonsFollowTheLiveAppRules).
//   - Style, optional, web/live-<name>.css, is inlined in its own <style>
//     element after style.css and live.css, so an addon never edits the
//     shared stylesheet. Scope its selectors to the addon's own classes.
//
// Every inline script and stylesheet is hashed into the page's
// Content-Security-Policy (script-src and style-src): nothing needs
// 'unsafe-inline', and only the listed files are allowed.
//
// A view's render(ctx) reads the query result as ctx.query: matches,
// rows, vis (the visible closure, null without a filter), and rowKinds,
// the node kinds the query's kind: clauses allow (null without one), so a
// view need not parse the query to learn which kinds its rows are.
type liveAddon struct {
	Script string
	Style  string
}

// liveAddons are the live page's addons, inlined in this order. gh-101 to
// gh-105 append theirs.
var liveAddons = []liveAddon{
	{Script: "web/live-table.js", Style: "web/live-table.css"}, // gh-101: the analysis table, replacing the built-in one
}

// liveAddonFiles returns the addons' scripts and stylesheets, in order.
func liveAddonFiles(addons []liveAddon) (scripts, styles []string) {
	for _, a := range addons {
		scripts = append(scripts, a.Script)
		if a.Style != "" {
			styles = append(styles, a.Style)
		}
	}
	return scripts, styles
}

// page is a rendered explorer page with its Content-Security-Policy. The
// bootstrap element is held apart (prefix, suffix) so a page can be re-served
// with a different bootstrap without re-rendering; the bootstrap is JSON, not
// script, so it needs no hash and changing it keeps the policy valid.
type page struct {
	prefix, suffix []byte
	csp            string
}

const bootOpen = `<script id="wgo-boot" type="application/json">`

// newPage renders p (which must carry a Boot) and computes its policy.
func newPage(p review.Page) (*page, error) {
	b, err := review.RenderPage(p)
	if err != nil {
		return nil, err
	}
	i := bytes.Index(b, []byte(bootOpen))
	if i < 0 {
		return nil, fmt.Errorf("rendered %s page has no bootstrap element", p.Mode)
	}
	j := bytes.Index(b[i:], []byte("</script>"))
	if j < 0 {
		return nil, fmt.Errorf("rendered %s page has an unterminated bootstrap element", p.Mode)
	}
	return &page{prefix: b[:i], suffix: b[i+j+len("</script>"):], csp: contentSecurityPolicy(b)}, nil
}

// bytes returns the page with boot as its bootstrap.
func (p *page) bytes(boot any) ([]byte, error) {
	j, err := json.Marshal(boot) // escapes <, > and &, so it cannot close the element
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(p.prefix)+len(bootOpen)+len(j)+len("</script>")+len(p.suffix))
	out = append(out, p.prefix...)
	out = append(out, bootOpen...)
	out = append(out, j...)
	out = append(out, "</script>"...)
	return append(out, p.suffix...), nil
}

var (
	inlineScript = regexp.MustCompile(`(?s)<script>(.*?)</script>`)
	inlineStyle  = regexp.MustCompile(`(?s)<style>(.*?)</style>`)
)

// contentSecurityPolicy allows exactly the page's own inline scripts and
// styles (by hash), same-origin fetches, same-origin and data: images, and
// nothing else: no eval, no
// remote code, no framing, no form posts.
func contentSecurityPolicy(page []byte) string {
	// WARNING: every bare <script> (and <style>) in the rendered page is
	// hashed and so allowlisted, whatever it contains. Untrusted data
	// (snapshot fields, paths, branch names) must never be rendered into
	// one; it belongs in the JSON bootstrap or the /api responses.
	hashes := func(re *regexp.Regexp) string {
		var out []string
		for _, m := range re.FindAllSubmatch(page, -1) {
			sum := sha256.Sum256(m[1])
			out = append(out, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
		}
		if len(out) == 0 {
			return "'none'"
		}
		return strings.Join(out, " ")
	}
	return strings.Join([]string{
		"default-src 'none'",
		"script-src " + hashes(inlineScript),
		"style-src " + hashes(inlineStyle),
		"connect-src 'self'",
		"img-src 'self' data:",
		"base-uri 'none'",
		"form-action 'none'",
		"frame-ancestors 'none'",
	}, "; ")
}

// staticPage is a small self-contained, script-free notice page, such as
// the /lookup and review-run-not-found notices.
func staticPage(title, heading, message, linkText, linkHref string) []byte {
	esc := html.EscapeString
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head><meta charset=\"utf-8\"><meta name=\"color-scheme\" content=\"light dark\"><title>")
	b.WriteString(esc(title))
	b.WriteString("</title></head>\n<body>\n<h1>")
	b.WriteString(esc(heading))
	b.WriteString("</h1>\n<p>")
	b.WriteString(esc(message))
	b.WriteString("</p>\n")
	if linkHref != "" {
		b.WriteString("<p><a href=\"")
		b.WriteString(esc(linkHref))
		b.WriteString("\">")
		b.WriteString(esc(linkText))
		b.WriteString("</a></p>\n")
	}
	b.WriteString("</body>\n</html>\n")
	return []byte(b.String())
}

// staticCSP is the policy for staticPage: nothing executes or loads.
const staticCSP = "default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
