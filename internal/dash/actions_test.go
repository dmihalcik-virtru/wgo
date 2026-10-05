package dash

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/launch"
)

const testToken = "test-token-0123456789abcdefghijklmnopqrstuv"

// fakeLauncher records launches and never starts a process.
type fakeLauncher struct {
	mu   sync.Mutex
	acts []launch.Action
}

func (f *fakeLauncher) Launch(_ context.Context, a launch.Action) launch.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acts = append(f.acts, a)
	return launch.Result{Launched: true, Method: "fake", Message: "faked."}
}

func (f *fakeLauncher) launches() []launch.Action {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]launch.Action(nil), f.acts...)
}

// fakeResolver maps IDs to targets; stale IDs report ErrStaleWorkspace.
type fakeResolver struct {
	targets map[string]Target
	stale   map[string]bool
}

func (r fakeResolver) Resolve(id string) (Target, error) {
	if r.stale[id] {
		return Target{}, ErrStaleWorkspace
	}
	if t, ok := r.targets[id]; ok {
		return t, nil
	}
	return Target{}, ErrUnknownWorkspace
}

type fakeAcker struct {
	mu   sync.Mutex
	gens []uint64
}

func (a *fakeAcker) Acknowledge(gen uint64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.gens = append(a.gens, gen)
	return nil
}

func (a *fakeAcker) count() int { a.mu.Lock(); defer a.mu.Unlock(); return len(a.gens) }

// actionFixture is a discovery root with workspaces, a plan and a spec.
type actionFixture struct {
	root, main, ws, nasty, outside, link string
	planPath                             string
	ids                                  map[string]string // name -> workspace ID
	resolver                             fakeResolver
	launcher                             *fakeLauncher
	acker                                *fakeAcker
	srv                                  *httptest.Server
}

func mkWorkspace(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".jj"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newActionFixture(t *testing.T, resume string) *actionFixture {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &actionFixture{
		root:     filepath.Join(tmp, "roots", "GitHub"),
		outside:  filepath.Join(tmp, "elsewhere", "ws"),
		planPath: filepath.Join(tmp, "home", ".wgo", "plan.md"),
		ids:      map[string]string{},
		launcher: &fakeLauncher{},
		acker:    &fakeAcker{},
	}
	f.main = filepath.Join(f.root, "mains", "acme", "wgo")
	f.ws = filepath.Join(f.root, "worktrees", "gh-70-dash", "acme", "wgo")
	f.nasty = filepath.Join(f.root, "worktrees", "it's a \"ws\" $(touch pwned)", "wgo")
	f.link = filepath.Join(f.root, "worktrees", "sneaky", "wgo")
	for _, d := range []string{f.main, f.ws, f.nasty, f.outside} {
		mkWorkspace(t, d)
	}
	if err := os.MkdirAll(filepath.Dir(f.link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.outside, f.link); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(f.ws, "spec", "gh-70.md"), "# gh-70\n")
	writeFile(t, filepath.Join(f.nasty, "spec", "gh-70.md"), "# gh-70\n")
	// A spec that is a symlink out of the workspace must not be opened.
	writeFile(t, filepath.Join(f.outside, "secret.md"), "secret\n")
	mkWorkspace(t, filepath.Join(f.root, "worktrees", "gh-99-x", "wgo"))
	if err := os.MkdirAll(filepath.Join(f.root, "worktrees", "gh-99-x", "wgo", "spec"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.outside, "secret.md"), filepath.Join(f.root, "worktrees", "gh-99-x", "wgo", "spec", "gh-99.md")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, f.planPath, "# Plan\n\n## Active Branches\n\n- **acme/other:gh-70-dash** — another repo\n- **acme/wgo:gh-70-dash** — the dashboard\n\n## Notes\n")

	targets := map[string]Target{}
	add := func(name, root string) {
		id := WorkspaceID(f.main, root)
		f.ids[name] = id
		targets[id] = Target{ID: id, Root: root, MainClone: f.main}
	}
	add("ws", f.ws)
	add("nasty", f.nasty)
	add("main", f.main)
	add("link", f.link)
	add("outside", f.outside)
	add("specLink", filepath.Join(f.root, "worktrees", "gh-99-x", "wgo"))
	add("gone", filepath.Join(f.root, "worktrees", "deleted", "wgo"))
	f.ids["stale"] = WorkspaceID(f.main, "/stale")
	f.resolver = fakeResolver{targets: targets, stale: map[string]bool{f.ids["stale"]: true}}

	bookmarks := map[string]string{f.ws: "gh-70-dash", f.nasty: "gh-70-dash", f.main: "main", filepath.Join(f.root, "worktrees", "gh-99-x", "wgo"): "gh-99-x"}
	f.srv = newServerOpts(t, HandlerOptions{
		Source: emptySource{},
		Token:  testToken,
		Ack:    f.acker,
		Actions: &ActionOptions{
			Resolver: f.resolver,
			Launcher: f.launcher,
			Roots:    []string{filepath.Join(tmp, "roots", "missing"), f.root},
			PlanPath: f.planPath,
			Resume:   resume,
			Bookmark: func(_ context.Context, root string) (string, error) {
				bm, ok := bookmarks[root]
				if !ok {
					return "", errors.New("no such workspace in the fake")
				}
				return bm, nil
			},
		},
	})
	return f
}

// post sends a well-formed action request; mod may break it.
func (f *actionFixture) post(t *testing.T, path, body string, mod func(*http.Request)) (int, http.Header, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, f.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", f.srv.URL)
	req.Header.Set(TokenHeader, testToken)
	req.Header.Set("Content-Type", "application/json")
	if mod != nil {
		mod(req)
	}
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		out = map[string]any{"raw": string(raw)}
	}
	return resp.StatusCode, resp.Header, out
}

func actionBody(kind, id string) string {
	b, _ := json.Marshal(map[string]string{"kind": kind, "workspace_id": id})
	return string(b)
}

func assertNoCORS(t *testing.T, name string, h http.Header) {
	t.Helper()
	for k := range h {
		if strings.HasPrefix(strings.ToLower(k), "access-control-") {
			t.Errorf("%s: CORS header %s: %v", name, k, h[k])
		}
	}
}

// TestActionSecurityMatrix sends every kind of bad request and checks it is
// refused, sends no CORS allowance, and launches nothing.
func TestActionSecurityMatrix(t *testing.T) {
	f := newActionFixture(t, "claude")
	_, port, _ := strings.Cut(f.srv.Listener.Addr().String(), ":")
	good := actionBody("terminal", f.ids["ws"])
	cases := []struct {
		name   string
		method string
		path   string
		body   string
		mod    func(*http.Request)
		status int
	}{
		{"missing token", "", ActionPath, good, func(r *http.Request) { r.Header.Del(TokenHeader) }, 403},
		{"wrong token", "", ActionPath, good, func(r *http.Request) { r.Header.Set(TokenHeader, testToken[:len(testToken)-1]+"x") }, 403},
		{"token prefix", "", ActionPath, good, func(r *http.Request) { r.Header.Set(TokenHeader, testToken[:8]) }, 403},
		{"two tokens", "", ActionPath, good, func(r *http.Request) { r.Header.Add(TokenHeader, testToken) }, 403},
		{"token in query only", "", ActionPath + "?token=" + testToken + "&" + TokenHeader + "=" + testToken, good, func(r *http.Request) { r.Header.Del(TokenHeader) }, 403},
		{"token as cookie", "", ActionPath, good, func(r *http.Request) {
			r.Header.Del(TokenHeader)
			r.AddCookie(&http.Cookie{Name: TokenHeader, Value: testToken})
		}, 403},
		{"missing origin", "", ActionPath, good, func(r *http.Request) { r.Header.Del("Origin") }, 403},
		{"null origin", "", ActionPath, good, func(r *http.Request) { r.Header.Set("Origin", "null") }, 403},
		{"foreign origin", "", ActionPath, good, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, 403},
		{"localhost origin", "", ActionPath, good, func(r *http.Request) { r.Header.Set("Origin", "http://localhost:"+port) }, 403},
		{"origin with path", "", ActionPath, good, func(r *http.Request) { r.Header.Set("Origin", f.srv.URL+"/") }, 403},
		{"https origin", "", ActionPath, good, func(r *http.Request) { r.Header.Set("Origin", strings.Replace(f.srv.URL, "http:", "https:", 1)) }, 403},
		{"cross-site fetch metadata", "", ActionPath, good, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, 403},
		{"dns rebinding host", "", ActionPath, good, func(r *http.Request) { r.Host = "evil.example:" + port }, 403},
		{"dns rebinding host and origin", "", ActionPath, good, func(r *http.Request) {
			r.Host = "evil.example:" + port
			r.Header.Set("Origin", "http://evil.example:"+port)
		}, 403},
		{"text/plain", "", ActionPath, good, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, 415},
		{"form", "", ActionPath, "kind=terminal&workspace_id=" + f.ids["ws"], func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }, 415},
		{"multipart", "", ActionPath, good, func(r *http.Request) { r.Header.Set("Content-Type", "multipart/form-data; boundary=x") }, 415},
		{"no content type", "", ActionPath, good, func(r *http.Request) { r.Header.Del("Content-Type") }, 415},
		{"json with odd param", "", ActionPath, good, func(r *http.Request) { r.Header.Set("Content-Type", "application/json; foo=bar") }, 415},
		{"malformed json", "", ActionPath, `{"kind":"terminal",`, nil, 400},
		{"json array", "", ActionPath, `["terminal"]`, nil, 400},
		{"trailing data", "", ActionPath, good + `{"kind":"reveal"}`, nil, 400},
		{"unknown field path", "", ActionPath, `{"kind":"terminal","workspace_id":"` + f.ids["ws"] + `","path":"/etc"}`, nil, 400},
		{"unknown field command", "", ActionPath, `{"kind":"terminal","workspace_id":"` + f.ids["ws"] + `","command":"rm -rf ~"}`, nil, 400},
		{"unknown kind", "", ActionPath, actionBody("shell", f.ids["ws"]), nil, 400},
		{"empty kind", "", ActionPath, actionBody("", f.ids["ws"]), nil, 400},
		{"unknown workspace", "", ActionPath, actionBody("terminal", "ws-0000000000000000"), nil, 404},
		{"workspace path as id", "", ActionPath, actionBody("terminal", f.ws), nil, 404},
		{"traversal id", "", ActionPath, actionBody("terminal", "../../etc"), nil, 404},
		{"stale workspace", "", ActionPath, actionBody("terminal", f.ids["stale"]), nil, 404},
		{"deleted workspace dir", "", ActionPath, actionBody("terminal", f.ids["gone"]), nil, 404},
		{"symlink out of roots", "", ActionPath, actionBody("terminal", f.ids["link"]), nil, 404},
		{"outside roots", "", ActionPath, actionBody("reveal", f.ids["outside"]), nil, 404},
		{"spec symlinked out of workspace", "", ActionPath, actionBody("spec", f.ids["specLink"]), nil, 404},
		{"oversized body", "", ActionPath, `{"kind":"terminal","workspace_id":"` + strings.Repeat("a", maxActionBody+10) + `"}`, nil, 413},
		{"GET action", http.MethodGet, ActionPath, "", nil, 405},
		{"PUT action", http.MethodPut, ActionPath, good, nil, 405},
		{"DELETE action", http.MethodDelete, ActionPath, "", nil, 405},
		{"OPTIONS preflight", http.MethodOptions, ActionPath, "", func(r *http.Request) {
			r.Header.Set("Access-Control-Request-Method", "POST")
			r.Header.Set("Access-Control-Request-Headers", "x-wgo-token, content-type")
			r.Header.Set("Origin", "https://evil.example")
		}, 405},
		{"OPTIONS ack", http.MethodOptions, AckPath, "", nil, 405},
		{"GET ack", http.MethodGet, AckPath, "", nil, 405},
		{"ack without token", "", AckPath, `{"generation":1}`, func(r *http.Request) { r.Header.Del(TokenHeader) }, 403},
		{"ack foreign origin", "", AckPath, `{"generation":1}`, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, 403},
		{"ack text/plain", "", AckPath, `{"generation":1}`, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, 415},
		{"ack unknown field", "", AckPath, `{"generation":1,"all":true}`, nil, 400},
		{"ack dns rebinding", "", AckPath, `{"generation":1}`, func(r *http.Request) { r.Host = "evil.example:" + port }, 403},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mod := c.mod
			if c.method != "" {
				mod = func(r *http.Request) {
					r.Method = c.method
					if c.mod != nil {
						c.mod(r)
					}
				}
			}
			status, h, out := f.post(t, c.path, c.body, mod)
			if status != c.status {
				t.Errorf("status %d, want %d (%v)", status, c.status, out)
			}
			assertNoCORS(t, c.name, h)
			if status == 405 && h.Get("Allow") != "POST" {
				t.Errorf("Allow = %q", h.Get("Allow"))
			}
			if n := len(f.launcher.launches()); n != 0 {
				t.Fatalf("rejected request launched %d time(s): %+v", n, f.launcher.launches())
			}
			if f.acker.count() != 0 {
				t.Fatalf("rejected request acknowledged")
			}
			if status != 405 && status != 403 {
				if msg, _ := out["error"].(string); msg == "" {
					t.Errorf("no error message: %v", out)
				}
			}
		})
	}
}

func TestResumeDisabledLaunchesNothing(t *testing.T) {
	f := newActionFixture(t, "")
	status, _, out := f.post(t, ActionPath, actionBody("resume", f.ids["ws"]), nil)
	if status != 400 || !strings.Contains(out["error"].(string), "not configured") || len(f.launcher.launches()) != 0 {
		t.Fatalf("resume disabled: %d %v %v", status, out, f.launcher.launches())
	}
}

// TestActionHappyPaths checks the launcher receives exactly the paths the
// server derived, including one with spaces, quotes and $(...).
func TestActionHappyPaths(t *testing.T) {
	f := newActionFixture(t, "claude")
	cases := []struct {
		kind, ws string
		want     launch.Action
		note     string
	}{
		{"terminal", "ws", launch.Action{Kind: launch.KindTerminal, Dir: f.ws}, ""},
		{"terminal", "nasty", launch.Action{Kind: launch.KindTerminal, Dir: f.nasty}, ""},
		{"resume", "ws", launch.Action{Kind: launch.KindResume, Dir: f.ws}, ""},
		{"editor", "nasty", launch.Action{Kind: launch.KindEditor, Dir: f.nasty}, ""},
		{"reveal", "ws", launch.Action{Kind: launch.KindReveal, Dir: f.ws}, ""},
		{"plan", "ws", launch.Action{Kind: launch.KindFile, File: f.planPath, Line: 6}, ""},
		{"plan", "main", launch.Action{Kind: launch.KindFile, File: f.planPath}, "No Active Branches entry for wgo:main"},
		{"spec", "ws", launch.Action{Kind: launch.KindFile, File: filepath.Join(f.ws, "spec", "gh-70.md")}, ""},
		{"spec", "nasty", launch.Action{Kind: launch.KindFile, File: filepath.Join(f.nasty, "spec", "gh-70.md")}, ""},
	}
	for i, c := range cases {
		status, h, out := f.post(t, ActionPath, actionBody(c.kind, f.ids[c.ws]), nil)
		if status != 200 || out["ok"] != true || out["method"] != "fake" || out["kind"] != c.kind {
			t.Fatalf("%s %s: %d %v", c.kind, c.ws, status, out)
		}
		assertNoCORS(t, c.kind, h)
		got := f.launcher.launches()
		if len(got) != i+1 || got[i] != c.want {
			t.Fatalf("%s %s: launched %+v, want %+v", c.kind, c.ws, got[len(got)-1], c.want)
		}
		if c.note != "" && !strings.Contains(out["message"].(string), c.note) {
			t.Errorf("%s %s: message %q lacks %q", c.kind, c.ws, out["message"], c.note)
		}
	}
	// The plan line really is the entry.
	content, _ := os.ReadFile(f.planPath)
	if line := strings.Split(string(content), "\n")[6-1]; !strings.Contains(line, "acme/wgo:gh-70-dash") {
		t.Fatalf("line 6 is %q", line)
	}
}

func TestActionMissingPlanAndSpec(t *testing.T) {
	f := newActionFixture(t, "")
	// main has bookmark "main": no ticket, so no spec.
	status, _, out := f.post(t, ActionPath, actionBody("spec", f.ids["main"]), nil)
	if status != 404 || !strings.Contains(out["error"].(string), "no ticket ID") {
		t.Fatalf("no ticket: %d %v", status, out)
	}
	if err := os.Remove(filepath.Join(f.ws, "spec", "gh-70.md")); err != nil {
		t.Fatal(err)
	}
	status, _, out = f.post(t, ActionPath, actionBody("spec", f.ids["ws"]), nil)
	if status != 404 || !strings.Contains(out["error"].(string), "wgo spec new GH-70") {
		t.Fatalf("missing spec: %d %v", status, out)
	}
	if err := os.Remove(f.planPath); err != nil {
		t.Fatal(err)
	}
	status, _, out = f.post(t, ActionPath, actionBody("plan", f.ids["ws"]), nil)
	if status != 404 || !strings.Contains(out["error"].(string), "no plan file yet") {
		t.Fatalf("missing plan: %d %v", status, out)
	}
	if n := len(f.launcher.launches()); n != 0 {
		t.Fatalf("launched %d", n)
	}
}

func TestActionsDisabled(t *testing.T) {
	srv := newServerOpts(t, HandlerOptions{Source: emptySource{}})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+ActionPath, strings.NewReader(actionBody("terminal", "ws-0000000000000000")))
	req.Header.Set("Origin", srv.URL)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(TokenHeader, "")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("no token configured: %d", resp.StatusCode)
	}
	m := bootRe.FindStringSubmatch(getBody(t, srv, "/"))
	if m == nil || strings.Contains(m[1], "token") || strings.Contains(m[1], "action_api") || strings.Contains(m[1], "ack_api") {
		t.Fatalf("a read-only page carries a token: %v", m)
	}
}

func getBody(t *testing.T, srv *httptest.Server, path string) string {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestPageEmbedsTokenOnlyInBody(t *testing.T) {
	f := newActionFixture(t, "claude")
	resp, err := f.srv.Client().Get(f.srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	m := bootRe.FindSubmatch(b)
	if m == nil {
		t.Fatal("no boot element")
	}
	var boot liveBoot
	if err := json.Unmarshal(m[1], &boot); err != nil {
		t.Fatal(err)
	}
	if boot.Token != testToken || boot.ActionAPI != ActionPath || boot.AckAPI != AckPath || boot.Resume != "claude" {
		t.Fatalf("boot = %+v", boot)
	}
	for k, v := range resp.Header {
		if strings.Contains(strings.Join(v, " "), testToken) {
			t.Fatalf("token in header %s", k)
		}
	}
	if resp.Header.Get("Referrer-Policy") != "no-referrer" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("headers: %v", resp.Header)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "connect-src 'self'", "form-action 'none'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP lacks %q: %s", want, csp)
		}
	}
	// The scraped token works, with the exact origin.
	status, _, out := f.post(t, ActionPath, actionBody("reveal", f.ids["ws"]), func(r *http.Request) { r.Header.Set(TokenHeader, boot.Token) })
	if status != 200 || out["ok"] != true {
		t.Fatalf("scraped token: %d %v", status, out)
	}
}

func TestGeneratedTokenIsRandom(t *testing.T) {
	a, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewToken()
	if a == b || len(a) < 40 || strings.ContainsAny(a, "+/=") {
		t.Fatalf("tokens %q %q", a, b)
	}
	// A handler with actions but no explicit token generates one.
	srv := newServerOpts(t, HandlerOptions{Source: emptySource{}, Ack: &fakeAcker{}})
	m := bootRe.FindStringSubmatch(getBody(t, srv, "/"))
	var boot liveBoot
	if m == nil || json.Unmarshal([]byte(m[1]), &boot) != nil || len(boot.Token) < 40 || boot.AckAPI != AckPath || boot.ActionAPI != "" {
		t.Fatalf("generated boot: %v", m)
	}
}

func TestContainedWorkspace(t *testing.T) {
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	root := filepath.Join(tmp, "root")
	ws := filepath.Join(root, "a", "ws")
	mkWorkspace(t, ws)
	sibling := filepath.Join(tmp, "root-evil", "ws") // shares a prefix with root
	mkWorkspace(t, sibling)
	notJJ := filepath.Join(root, "plain")
	if err := os.MkdirAll(notJJ, 0o755); err != nil {
		t.Fatal(err)
	}
	// A symlinked root still contains its real workspaces.
	linkRoot := filepath.Join(tmp, "linkroot")
	if err := os.Symlink(root, linkRoot); err != nil {
		t.Fatal(err)
	}
	if got, err := containedWorkspace(ws, []string{root}); err != nil || got != ws {
		t.Fatalf("ws: %q %v", got, err)
	}
	if got, err := containedWorkspace(filepath.Join(linkRoot, "a", "ws"), []string{linkRoot}); err != nil || got != ws {
		t.Fatalf("via linked root: %q %v", got, err)
	}
	for name, p := range map[string]string{"prefix sibling": sibling, "not jj": notJJ, "relative": "a/ws", "missing": filepath.Join(root, "nope"), "dotdot": root + "/a/../../root-evil/ws"} {
		if _, err := containedWorkspace(p, []string{root}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := containedWorkspace(ws, nil); err == nil {
		t.Error("no roots accepted")
	}
}

// TestAckOnlyByPost checks the baseline advances only through a valid POST
// /api/ack, never by loading or reloading the page.
func TestAckOnlyByPost(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	s := &Snapshot{Schema: SchemaVersion, GeneratedAt: time.Now(), Days: DefaultDays, Sources: map[string]SourceStatus{}}
	if err := d.Publish(s); err != nil {
		t.Fatal(err)
	}
	srv := newServerOpts(t, HandlerOptions{Source: d, Ack: d, Token: testToken})
	for i := 0; i < 3; i++ {
		for _, p := range []string{"/", "/api/snapshot", "/lookup?entity=x"} {
			getBody(t, srv, p)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, BaselineFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("loading the page wrote a baseline: %v", err)
	}
	if d.Current().Delta.Status != DeltaNoPreviousLook {
		t.Fatalf("delta = %s", d.Current().Delta.Status)
	}
	post := func(body string, mod func(*http.Request)) (int, string) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+AckPath, bytes.NewReader([]byte(body)))
		req.Header.Set("Origin", srv.URL)
		req.Header.Set(TokenHeader, testToken)
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		if mod != nil {
			mod(req)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if st, _ := post(`{"generation":1}`, func(r *http.Request) { r.Header.Set(TokenHeader, "nope") }); st != 403 {
		t.Fatalf("bad token ack: %d", st)
	}
	if _, err := os.Stat(filepath.Join(dir, BaselineFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a rejected ack wrote a baseline")
	}
	if st, body := post(`{"generation":99}`, nil); st != 409 || !strings.Contains(body, "too old") {
		t.Fatalf("unknown generation: %d %s", st, body)
	}
	if st, body := post(`{"generation":1}`, nil); st != 200 || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("ack: %d %s", st, body)
	}
	if _, err := os.Stat(filepath.Join(dir, BaselineFile)); err != nil {
		t.Fatalf("ack wrote no baseline: %v", err)
	}
	if d.Current().Delta.Status == DeltaNoPreviousLook {
		t.Fatal("delta still has no previous look after ack")
	}
}
