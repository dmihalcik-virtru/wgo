package dash

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/virtru/wgo/internal/launch"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/spec"
)

// Browser action endpoints. Both accept only POST.
const (
	ActionPath = "/api/action"
	AckPath    = "/api/ack"
	// TokenHeader carries the per-launch action token. The token is only
	// ever sent in this header: never in a URL, a cookie or a log line.
	TokenHeader = "X-Wgo-Token"
	// maxActionBody bounds an action or ack request body.
	maxActionBody = 4 << 10
)

// Action kinds a browser may request. Nothing else is accepted.
const (
	ActionTerminal = "terminal"
	ActionResume   = "resume"
	ActionEditor   = "editor"
	ActionReveal   = "reveal"
	ActionPlan     = "plan"
	ActionSpec     = "spec"
)

var actionKinds = map[string]bool{
	ActionTerminal: true, ActionResume: true, ActionEditor: true,
	ActionReveal: true, ActionPlan: true, ActionSpec: true,
}

// newToken is NewToken; tests replace it to simulate a failure.
var newToken = NewToken

// NewToken returns a fresh random action token (256 bits, URL-safe).
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Resolver resolves a workspace ID against the currently discovered
// workspaces. Dash implements it.
type Resolver interface {
	Resolve(id string) (Target, error)
}

// Acker records a snapshot generation as seen. Dash implements it.
type Acker interface {
	Acknowledge(gen uint64) error
}

// ActionOptions enables POST /api/action.
type ActionOptions struct {
	// Resolver resolves workspace IDs afresh for every request.
	Resolver Resolver
	// Launcher opens the tools. Tests inject a fake.
	Launcher launch.Launcher
	// Roots are the configured discovery roots. A workspace must lie
	// beneath one of them after resolving symlinks.
	Roots []string
	// PlanPath is the plan file the Plan action opens.
	PlanPath string
	// Bookmark returns a workspace's current nearest bookmark, read fresh
	// (jj, read-only). It locates the plan entry and the ticket's spec; nil
	// disables both lookups.
	Bookmark func(ctx context.Context, root string) (string, error)
	// Resume is the allowlisted resume tool from local config, or empty to
	// disable the Resume action.
	Resume string
}

// actionRequest is the only body /api/action accepts.
type actionRequest struct {
	Kind        string `json:"kind"`
	WorkspaceID string `json:"workspace_id"`
}

// ackRequest is the only body /api/ack accepts.
type ackRequest struct {
	Generation uint64 `json:"generation"`
}

// actionResponse reports a completed action.
type actionResponse struct {
	OK          bool   `json:"ok"`
	Kind        string `json:"kind"`
	WorkspaceID string `json:"workspace_id"`
	launch.Result
}

// httpError is a rejected request: a status and a message for the page.
type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func reject(status int, format string, args ...any) *httpError {
	return &httpError{status: status, msg: fmt.Sprintf(format, args...)}
}

func (s *server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.opts.Logf("write a %d JSON response: %v", status, err)
	}
}

func (s *server) writeError(w http.ResponseWriter, e *httpError) {
	s.writeJSON(w, e.status, map[string]any{"ok": false, "error": e.msg})
}

// checkPost enforces everything a state-changing request must satisfy
// beyond the exact Host (checked for every request) and POST: an Origin
// that is exactly this server, a same-origin fetch, the action token in its
// header, and a JSON content type. It then decodes the bounded body into
// dst, rejecting unknown fields and trailing data.
func (s *server) checkPost(w http.ResponseWriter, r *http.Request, dst any) *httpError {
	origins := r.Header.Values("Origin")
	if len(origins) != 1 || origins[0] != "http://"+s.opts.Host {
		return reject(http.StatusForbidden, "forbidden: actions are only accepted from the wgo dash page itself")
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return reject(http.StatusForbidden, "forbidden: cross-site request")
	}
	tokens := r.Header.Values(TokenHeader)
	if s.token == "" || len(tokens) != 1 || subtle.ConstantTimeCompare([]byte(tokens[0]), []byte(s.token)) != 1 {
		return reject(http.StatusForbidden, "forbidden: missing or wrong action token; reload the wgo dash page (each wgo dash launch issues a new token)")
	}
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		return reject(http.StatusUnsupportedMediaType, "actions must be sent as application/json")
	}
	for k, v := range params {
		if k != "charset" || !strings.EqualFold(v, "utf-8") {
			return reject(http.StatusUnsupportedMediaType, "unsupported content type parameter %q", k)
		}
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxActionBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return reject(http.StatusRequestEntityTooLarge, "request body is larger than %d bytes", maxActionBody)
		}
		return reject(http.StatusBadRequest, "malformed request: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return reject(http.StatusRequestEntityTooLarge, "request body is larger than %d bytes", maxActionBody)
		}
		return reject(http.StatusBadRequest, "malformed request: unexpected data after the JSON object")
	}
	return nil
}

// serveAction handles POST /api/action.
func (s *server) serveAction(w http.ResponseWriter, r *http.Request) {
	var req actionRequest
	if e := s.checkPost(w, r, &req); e != nil {
		s.writeError(w, e)
		return
	}
	a := s.opts.Actions
	if a == nil || a.Resolver == nil || a.Launcher == nil {
		s.writeError(w, reject(http.StatusNotFound, "browser actions are not enabled in this wgo dash"))
		return
	}
	if !actionKinds[req.Kind] {
		s.writeError(w, reject(http.StatusBadRequest, "unknown action %q", req.Kind))
		return
	}
	if req.Kind == ActionResume && a.Resume == "" {
		s.writeError(w, reject(http.StatusBadRequest, `Resume is not configured: set resume = "claude" under [dash] in ~/.wgo/config.toml`))
		return
	}
	// The request was accepted; a disconnecting browser must not abort a
	// launch halfway (launch.RunTimeout still bounds it).
	ctx := context.WithoutCancel(r.Context())
	act, extra, e := s.planAction(ctx, req)
	if e != nil {
		s.writeError(w, e)
		return
	}
	res := a.Launcher.Launch(ctx, act)
	if extra != "" {
		res.Message = strings.TrimSpace(res.Message + " " + extra)
	}
	s.writeJSON(w, http.StatusOK, actionResponse{OK: true, Kind: req.Kind, WorkspaceID: req.WorkspaceID, Result: res})
}

// planAction resolves req to a launch: the workspace is looked up in
// current discovery and every path is derived here, never taken from the
// request. extra is a note to append to the launcher's message.
func (s *server) planAction(ctx context.Context, req actionRequest) (launch.Action, string, *httpError) {
	a := s.opts.Actions
	t, err := a.Resolver.Resolve(req.WorkspaceID)
	switch {
	case errors.Is(err, ErrUnknownWorkspace):
		return launch.Action{}, "", reject(http.StatusNotFound, "unknown workspace %q: it is not among the workspaces wgo dash discovers now", req.WorkspaceID)
	case errors.Is(err, ErrStaleWorkspace):
		return launch.Action{}, "", reject(http.StatusNotFound, "that workspace is no longer discovered (removed or moved); nothing was opened. The page updates on the next refresh")
	case err != nil:
		return launch.Action{}, "", reject(http.StatusInternalServerError, "could not run discovery: %v", err)
	}
	dir, err := containedWorkspace(t.Root, a.Roots)
	var ue *unreadableRootsError
	if errors.As(err, &ue) {
		s.opts.Logf("workspace %s (%s) is under no readable discovery root; unreadable: %s", req.WorkspaceID, t.Root, ue.list())
	}
	if err != nil {
		return launch.Action{}, "", reject(http.StatusNotFound, "workspace %q cannot be opened: %v", req.WorkspaceID, err)
	}
	switch req.Kind {
	case ActionTerminal:
		return launch.Action{Kind: launch.KindTerminal, Dir: dir}, "", nil
	case ActionResume:
		return launch.Action{Kind: launch.KindResume, Dir: dir}, "", nil
	case ActionEditor:
		return launch.Action{Kind: launch.KindEditor, Dir: dir}, "", nil
	case ActionReveal:
		return launch.Action{Kind: launch.KindReveal, Dir: dir}, "", nil
	case ActionPlan:
		return s.planFile(ctx, t, dir)
	case ActionSpec:
		return s.specFile(ctx, t, dir)
	}
	return launch.Action{}, "", reject(http.StatusBadRequest, "unknown action %q", req.Kind)
}

// planFile opens the plan at the workspace's Active Branches entry, or the
// whole plan when it has none.
func (s *server) planFile(ctx context.Context, t Target, dir string) (launch.Action, string, *httpError) {
	a := s.opts.Actions
	if a.PlanPath == "" {
		return launch.Action{}, "", reject(http.StatusNotFound, "no plan file is configured")
	}
	content, err := os.ReadFile(a.PlanPath)
	if errors.Is(err, fs.ErrNotExist) {
		return launch.Action{}, "", reject(http.StatusNotFound, "there is no plan file yet (%s); start one with: wgo plan add", a.PlanPath)
	}
	if err != nil {
		return launch.Action{}, "", reject(http.StatusInternalServerError, "could not read the plan: %v", err)
	}
	act := launch.Action{Kind: launch.KindFile, File: a.PlanPath}
	if a.Bookmark == nil {
		return act, "", nil
	}
	bm, err := a.Bookmark(ctx, dir)
	if err != nil {
		return act, fmt.Sprintf("(Could not read the workspace's bookmark, so the whole plan is open: %v.)", err), nil
	}
	if bm == "" {
		return act, "(The workspace has no bookmark, so it has no plan entry; the whole plan is open.)", nil
	}
	p, err := plan.Parse(string(content))
	if err != nil {
		return act, fmt.Sprintf("(The plan could not be parsed, so the whole plan is open: %v.)", err), nil
	}
	repo := filepath.Base(t.MainClone)
	_, line := p.FindBranchLine(func(e plan.BranchEntry) bool {
		return e.Branch == bm && (e.Repo == repo || path.Base(e.Repo) == repo)
	})
	if line == 0 {
		return act, fmt.Sprintf("(No Active Branches entry for %s:%s, so the whole plan is open. Add one with: wgo plan add)", repo, bm), nil
	}
	act.Line = line
	return act, "", nil
}

// specFile opens spec/<ticket>.md in the workspace, the ticket coming from
// its current bookmark.
func (s *server) specFile(ctx context.Context, t Target, dir string) (launch.Action, string, *httpError) {
	a := s.opts.Actions
	if a.Bookmark == nil {
		return launch.Action{}, "", reject(http.StatusNotFound, "spec lookup is not available in this wgo dash")
	}
	bm, err := a.Bookmark(ctx, dir)
	if err != nil {
		return launch.Action{}, "", reject(http.StatusInternalServerError, "could not read the workspace's bookmark: %v", err)
	}
	ticket := spec.ParseTicketFromBranch(bm)
	if ticket == "" {
		name := bm
		if name == "" {
			name = "(none)"
		}
		return launch.Action{}, "", reject(http.StatusNotFound, "the workspace's bookmark %s carries no ticket ID, so it has no spec/<ticket>.md", name)
	}
	p, err := spec.FindByTicket(dir, ticket)
	if errors.Is(err, fs.ErrNotExist) {
		return launch.Action{}, "", reject(http.StatusNotFound, "no spec/%s.md in %s; write one with: wgo spec new %s", ticket, filepath.Base(t.MainClone), ticket)
	}
	if err != nil {
		return launch.Action{}, "", reject(http.StatusInternalServerError, "could not look for the spec: %v", err)
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil || !within(real, dir) {
		return launch.Action{}, "", reject(http.StatusNotFound, "spec/%s.md resolves outside the workspace; it was not opened", ticket)
	}
	return launch.Action{Kind: launch.KindFile, File: real}, "", nil
}

// containedWorkspace resolves root's symlinks and checks the result is an
// existing jj workspace beneath one of roots (also symlink-resolved). It
// returns the resolved directory, which is what gets launched.
func containedWorkspace(root string, roots []string) (string, error) {
	if root == "" || !filepath.IsAbs(root) {
		return "", errors.New("it has no absolute path")
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", errors.New("its directory no longer exists")
	}
	fi, err := os.Stat(filepath.Join(real, ".jj"))
	if err != nil || !fi.IsDir() {
		return "", errors.New("it is no longer a jj workspace")
	}
	var unreadable []string
	for _, r := range roots {
		if r == "" || !filepath.IsAbs(r) {
			continue
		}
		rr, err := filepath.EvalSymlinks(r)
		if err != nil {
			unreadable = append(unreadable, fmt.Sprintf("%s (%v)", r, unwrapPathError(err)))
			continue
		}
		if within(real, rr) {
			return real, nil
		}
	}
	if len(unreadable) > 0 {
		return "", &unreadableRootsError{roots: unreadable}
	}
	return "", errors.New("it is not under a configured discovery root (discovery.base_dirs)")
}

// unreadableRootsError is a containment failure where some discovery roots
// could not be resolved (an unmounted volume, a permission error), so the
// workspace may well be under one of them.
type unreadableRootsError struct{ roots []string }

func (e *unreadableRootsError) list() string { return strings.Join(e.roots, ", ") }

func (e *unreadableRootsError) Error() string {
	return "it is not under any readable discovery root, and these discovery.base_dirs could not be read (unmounted or inaccessible?): " + e.list()
}

// unwrapPathError drops the path from a *fs.PathError, which the caller
// already names.
func unwrapPathError(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// within reports whether p is base or beneath it. Both are clean and
// symlink-free.
func within(p, base string) bool {
	rel, err := filepath.Rel(base, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

// serveAck handles POST /api/ack (Mark seen): it makes the generation the
// page shows the since-last-look baseline.
func (s *server) serveAck(w http.ResponseWriter, r *http.Request) {
	var req ackRequest
	if e := s.checkPost(w, r, &req); e != nil {
		s.writeError(w, e)
		return
	}
	if s.opts.Ack == nil {
		s.writeError(w, reject(http.StatusNotFound, "Mark seen is not enabled in this wgo dash"))
		return
	}
	err := s.opts.Ack.Acknowledge(req.Generation)
	switch {
	case errors.Is(err, ErrUnknownGen):
		s.writeError(w, reject(http.StatusConflict, "generation %d is too old to mark seen; the page will catch up on its next refresh, then try again", req.Generation))
		return
	case errors.Is(err, ErrOldGeneration):
		s.writeError(w, reject(http.StatusConflict, "a newer generation has already been marked seen (perhaps in another tab)"))
		return
	case err != nil:
		// The path-bearing detail goes to the log, not to the browser.
		s.opts.Logf("save the last-seen baseline: %v", err)
		s.writeError(w, reject(http.StatusInternalServerError, "could not save the last-seen baseline; see the wgo dash log for details"))
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "generation": req.Generation})
}
