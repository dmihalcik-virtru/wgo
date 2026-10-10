package dash

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// deltaFromServer GETs /api/snapshot and returns its delta.
func deltaFromServer(t *testing.T, srv *httptest.Server) Delta {
	t.Helper()
	var out struct {
		Delta *Delta `json:"delta"`
	}
	if err := json.Unmarshal([]byte(getBody(t, srv, "/api/snapshot")), &out); err != nil || out.Delta == nil {
		t.Fatalf("snapshot has no delta: %v", err)
	}
	return *out.Delta
}

// TestAckSurfacesBaselineWriteFailure: a failed last-seen.json write is a 5xx
// JSON error (never ok:true), the served delta does not move, and a retry once
// the obstruction is gone succeeds and advances the delta.
func TestAckSurfacesBaselineWriteFailure(t *testing.T) {
	dir := t.TempDir()
	d := openDash(t, dir)
	g := publish(t, d, synth(snapOpts{changesA: []string{"a1"}}))
	f := &actionFixture{logs: &logBuf{}}
	f.srv = newServerOpts(t, HandlerOptions{Source: d, Token: testToken, Ack: d, Logf: f.logs.logf})

	body := `{"generation":` + itoa(g) + `}`
	if dl := deltaFromServer(t, f.srv); dl.Status != DeltaNoPreviousLook {
		t.Fatalf("setup delta: %+v", dl)
	}

	path := filepath.Join(dir, BaselineFile)
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	status, hdr, out := f.post(t, AckPath, body, nil)
	if status < 500 || status > 599 {
		t.Fatalf("ack with an unwritable baseline = %d %v, want 5xx", status, out)
	}
	if out["ok"] != false {
		t.Fatalf("failed ack must report ok:false: %v", out)
	}
	msg, _ := out["error"].(string)
	if msg == "" || !strings.Contains(msg, "last-seen baseline") {
		t.Fatalf("error body should explain the failure: %v", out)
	}
	if strings.Contains(msg, dir) || strings.Contains(msg, BaselineFile) {
		t.Fatalf("error leaks the filesystem path: %q", msg)
	}
	if !strings.Contains(f.logs.String(), "last-seen") {
		t.Fatalf("the detailed error should be logged, got %q", f.logs.String())
	}
	if strings.Contains(msg, testToken) {
		t.Fatalf("error leaks the action token: %q", msg)
	}
	if ct := hdr.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("error content type = %q", ct)
	}
	if dl := deltaFromServer(t, f.srv); dl.Status != DeltaNoPreviousLook {
		t.Fatalf("failed ack changed the served delta: %+v", dl)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	status, _, out = f.post(t, AckPath, body, nil)
	if status != http.StatusOK || out["ok"] != true || out["generation"] != float64(g) {
		t.Fatalf("retry = %d %v", status, out)
	}
	if dl := deltaFromServer(t, f.srv); dl.Status != DeltaCompared || dl.BaselineGeneration != g {
		t.Fatalf("delta after a good ack: %+v", dl)
	}
}

type countingResolver struct{ n atomic.Int32 }

func (r *countingResolver) Resolve(string) (Target, error) {
	r.n.Add(1)
	return Target{}, ErrUnknownWorkspace
}

// TestGetRoutesNeverResolve: only POST /api/action resolves a workspace.
func TestGetRoutesNeverResolve(t *testing.T) {
	res := &countingResolver{}
	srv := newServerOpts(t, HandlerOptions{
		Source: emptySource{},
		Token:  testToken,
		Ack:    &fakeAcker{},
		Actions: &ActionOptions{
			Resolver: res,
			Launcher: &fakeLauncher{},
			Roots:    []string{t.TempDir()},
			PlanPath: filepath.Join(t.TempDir(), "plan.md"),
			Resume:   "claude",
		},
	})
	for _, p := range []string{"/", "/api/snapshot", ActionPath, AckPath} {
		resp, err := srv.Client().Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	if n := res.n.Load(); n != 0 {
		t.Fatalf("GET routes called Resolve %d times", n)
	}
	f := &actionFixture{srv: srv}
	f.post(t, ActionPath, actionBody("terminal", "ws-0000000000000000"), nil)
	if n := res.n.Load(); n != 1 {
		t.Fatalf("POST /api/action called Resolve %d times, want 1", n)
	}
}

func itoa(g uint64) string { b, _ := json.Marshal(g); return string(b) }
