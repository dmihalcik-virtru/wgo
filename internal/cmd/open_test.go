package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtru/wgo/internal/dash"
	"github.com/virtru/wgo/internal/launch"
	"github.com/virtru/wgo/internal/urlhandler"
)

func TestRunOpenRejectsBeforeDiscovery(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out, errOut bytes.Buffer
	for _, raw := range []string{
		"wgo://open?ws=ws-0123456789abcdef&command=x",
		"wgo://open?ws=ws-0123456789abcdef&resume=1",
		"wgo://open/Users/me?ws=",
	} {
		err := runOpen(context.Background(), raw, &out, &errOut)
		if err == nil || !strings.Contains(err.Error(), "rejected wgo:// link") {
			t.Errorf("runOpen(%q) = %v, want a rejection", raw, err)
		}
	}
}

type declineConfirmer struct{}

func (declineConfirmer) Confirm(context.Context, urlhandler.Prompt) (bool, error) {
	return false, nil
}

type noLauncher struct{ t *testing.T }

func (l noLauncher) Launch(context.Context, launch.Action) launch.Result {
	l.t.Fatal("launched after a decline")
	return launch.Result{}
}

type oneResolver dash.Target

func (r oneResolver) Resolve(id string) (dash.Target, error) {
	if id == r.ID {
		return dash.Target(r), nil
	}
	return dash.Target{}, dash.ErrUnknownWorkspace
}

func TestOpenWithDeclineIsNotAnError(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	if err := os.MkdirAll(filepath.Join(ws, ".jj"), 0o755); err != nil {
		t.Fatal(err)
	}
	id := dash.WorkspaceID(ws, ws)
	var out bytes.Buffer
	err := openWith(context.Background(), urlhandler.OpenURL(id), &out, urlhandler.OpenOptions{
		Resolver:  oneResolver{ID: id, Root: ws, MainClone: ws},
		Roots:     []string{root},
		Launcher:  noLauncher{t},
		Confirmer: declineConfirmer{},
		Approvals: urlhandler.NewApprovals(filepath.Join(t.TempDir(), "a.json")),
	})
	if err != nil || !strings.Contains(out.String(), "declined") {
		t.Fatalf("err = %v, out = %q", err, out.String())
	}
}
