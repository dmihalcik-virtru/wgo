package dash

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
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
