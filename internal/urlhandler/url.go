// Package urlhandler implements the optional wgo:// URL handler (gh-70):
// the strict grammar of the only URL it accepts, the first-use confirmation
// and its remembered approvals, `wgo open`, and the macOS applet that
// `wgo setup url-handler` installs and removes.
//
// A wgo:// URL is hostile input: any web page can navigate to one. The only
// accepted form is wgo://open?ws=<workspace-id>. The ID is resolved through
// current discovery and the dashboard's containment check, the first open
// of each workspace path is confirmed by the user, and the only thing it
// can do is open a terminal tab there. Nothing in the URL is ever run.
package urlhandler

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/virtru/wgo/internal/dash"
)

// Scheme is the URL scheme the handler registers.
const Scheme = "wgo"

// openHost is the only accepted URL host (the action).
const openHost = "open"

// MaxURLLen bounds an accepted URL. The canonical form is 30 bytes.
const MaxURLLen = 256

// OpenURL is the canonical wgo:// URL that opens workspace id.
func OpenURL(id string) string {
	u := url.URL{Scheme: Scheme, Host: openHost, RawQuery: url.Values{"ws": {id}}.Encode()}
	return u.String()
}

// AcceptedForm is the only wgo:// link wgo open accepts, for messages.
const AcceptedForm = "wgo://open?ws=<workspace-id>"

// ParseOpenURL accepts exactly wgo://open?ws=<id>, where id has the shape of
// a dashboard workspace ID (ws- and 16 lowercase hex digits), and returns
// the ID. Everything else is rejected with a reason: other schemes, hosts,
// ports, user info, paths, fragments, extra, empty or duplicate query
// parameters, percent-encoding, '+', control characters and spaces. As a
// final guard the input must equal the re-serialized canonical form, so no
// url.Parse leniency can widen what is accepted.
//
// Error messages are fixed text: they never quote any part of raw, because
// the applet shows them to the user and a web page chooses raw.
func ParseOpenURL(raw string) (string, error) {
	if len(raw) > MaxURLLen {
		return "", errors.New("the link is too long")
	}
	for _, r := range raw {
		if r <= ' ' || r >= 0x7f {
			return "", errors.New("the link contains a space, control or non-ASCII character")
		}
	}
	switch {
	case strings.Contains(raw, "%"):
		return "", errors.New("percent-encoded characters are not accepted in a wgo:// link")
	case strings.Contains(raw, "+"):
		return "", errors.New("'+' is not accepted in a wgo:// link")
	case strings.Contains(raw, "#"):
		return "", errors.New("a wgo:// link cannot have a fragment (#...)")
	case strings.Contains(raw, ";"):
		return "", errors.New("';' is not accepted in a wgo:// link")
	case !strings.HasPrefix(raw, Scheme+":"):
		return "", fmt.Errorf("only lowercase %s:// links are accepted", Scheme)
	case !strings.HasPrefix(raw, Scheme+"://"):
		return "", fmt.Errorf("the link must start with %s://", Scheme)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("the link is not a valid URL")
	}
	switch {
	case u.Opaque != "":
		return "", fmt.Errorf("the link must start with %s://", Scheme)
	case u.User != nil:
		return "", errors.New("a wgo:// link cannot carry user info (user@)")
	case strings.Contains(u.Host, ":"):
		return "", errors.New("a wgo:// link cannot carry a port")
	case u.Host != openHost:
		return "", errors.New("unsupported link host")
	case u.Path != "" || u.RawPath != "":
		return "", errors.New("a wgo:// link cannot carry a path")
	case u.RawQuery == "":
		return "", errors.New("the link names no workspace")
	}
	id := ""
	seen := false
	for _, p := range strings.Split(u.RawQuery, "&") {
		if p == "" {
			return "", errors.New("the link has an empty query parameter (a stray or trailing '&')")
		}
		k, v, _ := strings.Cut(p, "=")
		if k != "ws" {
			return "", errors.New("unexpected query parameter; a link can never carry a command")
		}
		if seen {
			return "", errors.New("the link names ws more than once")
		}
		seen = true
		id = v
	}
	if id == "" {
		return "", errors.New("the ws parameter is empty")
	}
	if !dash.ValidWorkspaceID(id) {
		return "", errors.New("ws is not a workspace ID (ws- followed by 16 lowercase hex digits, as shown by wgo dash --json)")
	}
	if raw != OpenURL(id) {
		return "", errors.New("the link is not in canonical form")
	}
	return id, nil
}
