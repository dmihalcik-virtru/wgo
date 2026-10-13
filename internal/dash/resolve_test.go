package dash

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/jj"
)

func TestResolveWorkspace(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "src")
	ws := filepath.Join(root, "w")
	out := filepath.Join(base, "out")
	for _, d := range []string{ws, out} {
		if err := os.MkdirAll(filepath.Join(d, ".jj"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	id, outID := WorkspaceID(ws, ws), WorkspaceID(out, out)
	other := WorkspaceID(ws, "/other")
	r := fakeResolver{targets: map[string]Target{
		id:    {ID: id, Root: ws, MainClone: ws},
		outID: {ID: outID, Root: out, MainClone: out},
		other: {ID: id, Root: ws, MainClone: ws}, // a resolver answering for the wrong ID
	}}
	_, dir, err := ResolveWorkspace(r, id, []string{root})
	realWS, _ := filepath.EvalSymlinks(ws)
	if err != nil || dir != realWS {
		t.Fatalf("ResolveWorkspace = %q, %v; want %q", dir, err, realWS)
	}
	for name, tc := range map[string]struct {
		id   string
		want func(error) bool
	}{
		"malformed":  {"../etc", func(e error) bool { return errors.Is(e, ErrUnknownWorkspace) }},
		"unknown":    {"ws-ffffffffffffffff", func(e error) bool { return errors.Is(e, ErrUnknownWorkspace) }},
		"mismatched": {other, func(e error) bool { return errors.Is(e, ErrUnknownWorkspace) }},
		"not under root": {outID, func(e error) bool {
			var nc *NotContainedError
			return errors.As(e, &nc)
		}},
	} {
		t.Run(name, func(t *testing.T) {
			_, dir, err := ResolveWorkspace(r, tc.id, []string{root})
			if dir != "" || !tc.want(err) {
				t.Fatalf("= %q, %v", dir, err)
			}
		})
	}
}

func TestResolveWorkspaceRequiresMatchingID(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "w")
	if err := os.MkdirAll(filepath.Join(ws, ".jj"), 0o755); err != nil {
		t.Fatal(err)
	}
	id := WorkspaceID(ws, ws)
	// A resolver that answers without an ID has not said it resolved id.
	r := fakeResolver{targets: map[string]Target{id: {Root: ws, MainClone: ws}}}
	if _, dir, err := ResolveWorkspace(r, id, []string{root}); dir != "" || !errors.Is(err, ErrUnknownWorkspace) {
		t.Fatalf("= %q, %v; want ErrUnknownWorkspace", dir, err)
	}
}

func TestCollectorResolve(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "w")
	gone := filepath.Join(root, "gone")
	for _, d := range []string{ws, gone} {
		if err := os.MkdirAll(filepath.Join(d, ".jj"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	id, goneID := WorkspaceID(ws, ws), WorkspaceID(gone, gone)
	var calls int
	discover := func() ([]discovery.DiscoveredRepo, error) {
		calls++
		return []discovery.DiscoveredRepo{{Path: ws, Name: "w"}, {Path: gone, Name: "gone"}}, nil
	}
	c := NewCollector(Config{Discover: discover})
	if err := os.RemoveAll(filepath.Join(gone, ".jj")); err != nil {
		t.Fatal(err)
	}

	if tg, err := c.Resolve(id); err != nil || tg.ID != id || tg.Root != ws {
		t.Fatalf("Resolve(discovered) = %+v, %v", tg, err)
	}
	for name, tc := range map[string]string{
		"undiscovered": "ws-ffffffffffffffff",
		"no .jj":       goneID,
	} {
		if _, err := c.Resolve(tc); err != ErrUnknownWorkspace {
			t.Errorf("%s: err = %v; want ErrUnknownWorkspace", name, err)
		}
	}
	calls = 0
	if _, err := c.Resolve("../etc"); err != ErrUnknownWorkspace || calls != 0 {
		t.Fatalf("malformed: err = %v after %d discoveries; want ErrUnknownWorkspace and none", err, calls)
	}

	boom := errors.New("boom")
	failing := NewCollector(Config{Discover: func() ([]discovery.DiscoveredRepo, error) { return nil, boom }})
	if _, err := failing.Resolve(id); !errors.Is(err, boom) || errors.Is(err, ErrUnknownWorkspace) {
		t.Fatalf("discovery error = %v; want it passed through, not mapped to unknown", err)
	}
}

// TestCollectorResolveReportsDiscoveryProblems: when jj cannot list a
// repo's workspaces (here, the repo is not a real jj repo, or jj is
// missing), a missing ID says so instead of looking deleted.
func TestCollectorResolveReportsDiscoveryProblems(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".jj"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := NewCollector(Config{
		JJ:       jj.NewCLI(),
		Discover: func() ([]discovery.DiscoveredRepo, error) { return []discovery.DiscoveredRepo{{Path: ws}}, nil },
	})
	_, err := c.Resolve("ws-ffffffffffffffff")
	var di *DiscoveryIncompleteError
	if !errors.As(err, &di) || !errors.Is(err, ErrUnknownWorkspace) {
		t.Fatalf("err = %v; want a *DiscoveryIncompleteError that is ErrUnknownWorkspace", err)
	}
	if len(di.Diagnostics) == 0 || !strings.Contains(di.Diagnostics[0], "could not list workspaces") {
		t.Fatalf("diagnostics = %q", di.Diagnostics)
	}
}
