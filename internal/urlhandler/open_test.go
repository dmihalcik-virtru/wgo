package urlhandler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/dash"
	"github.com/virtru/wgo/internal/launch"
)

type fakeLauncher struct {
	acts []launch.Action
	res  *launch.Result
}

func (f *fakeLauncher) Launch(_ context.Context, a launch.Action) launch.Result {
	f.acts = append(f.acts, a)
	if f.res != nil {
		return *f.res
	}
	return launch.Result{Launched: true, Method: "fake", Message: "faked."}
}

type fakeResolver map[string]dash.Target

func (r fakeResolver) Resolve(id string) (dash.Target, error) {
	if t, ok := r[id]; ok {
		return t, nil
	}
	return dash.Target{}, dash.ErrUnknownWorkspace
}

type fakeConfirmer struct {
	answer  bool
	err     error
	prompts []Prompt
}

func (f *fakeConfirmer) Confirm(_ context.Context, p Prompt) (bool, error) {
	f.prompts = append(f.prompts, p)
	return f.answer, f.err
}

type openFixture struct {
	root, ws, outside string
	wsID, outsideID   string
	resolver          fakeResolver
	launcher          *fakeLauncher
	confirmer         *fakeConfirmer
	approvals         *Approvals
	lock              func() (func(), error)
	warnf             func(string, ...any)
}

func mkWorkspace(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".jj"), 0o755); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func newOpenFixture(t *testing.T) *openFixture {
	t.Helper()
	base := t.TempDir()
	f := &openFixture{root: filepath.Join(base, "src")}
	f.ws = mkWorkspace(t, filepath.Join(f.root, "worktrees", "it's a \"ws\"", "wgo"))
	f.outside = mkWorkspace(t, filepath.Join(base, "elsewhere", "wgo"))
	main := filepath.Join(f.root, "mains", "wgo")
	f.wsID = dash.WorkspaceID(main, f.ws)
	f.outsideID = dash.WorkspaceID(main, f.outside)
	f.resolver = fakeResolver{
		f.wsID:      {ID: f.wsID, Root: f.ws, MainClone: main},
		f.outsideID: {ID: f.outsideID, Root: f.outside, MainClone: main},
	}
	f.launcher = &fakeLauncher{}
	f.confirmer = &fakeConfirmer{answer: true}
	f.approvals = NewApprovals(filepath.Join(base, "home", ".wgo", ApprovalsFile))
	return f
}

func (f *openFixture) open(raw string) (launch.Result, error) {
	return Open(context.Background(), raw, OpenOptions{
		Resolver:  f.resolver,
		Roots:     []string{f.root},
		Launcher:  f.launcher,
		Confirmer: f.confirmer,
		Approvals: f.approvals,
		Lock:      f.lock,
		Warnf:     f.warnf,
		Now:       func() time.Time { return time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC) },
	})
}

func TestOpenRejectsBadURLWithoutResolving(t *testing.T) {
	f := newOpenFixture(t)
	for _, raw := range []string{
		OpenURL(f.wsID) + "&command=x",
		OpenURL(f.wsID) + "&resume=1",
		"wgo://open?ws=" + f.ws,
		OpenURL(f.wsID) + "#x",
	} {
		if _, err := f.open(raw); err == nil || !strings.Contains(err.Error(), "rejected") {
			t.Errorf("open(%q) err = %v, want a rejection", raw, err)
		}
	}
	if len(f.launcher.acts) != 0 || len(f.confirmer.prompts) != 0 {
		t.Fatalf("launches %v, prompts %v; want none", f.launcher.acts, f.confirmer.prompts)
	}
}

func TestOpenUnknownWorkspace(t *testing.T) {
	f := newOpenFixture(t)
	_, err := f.open(OpenURL("ws-ffffffffffffffff"))
	if err == nil || !strings.Contains(err.Error(), "unknown workspace") {
		t.Fatalf("err = %v, want unknown workspace", err)
	}
	if len(f.launcher.acts) != 0 || len(f.confirmer.prompts) != 0 {
		t.Fatal("an unknown workspace must not confirm or launch")
	}
}

func TestOpenOutsideDiscoveryRootsRejected(t *testing.T) {
	f := newOpenFixture(t)
	_, err := f.open(OpenURL(f.outsideID))
	if err == nil || !strings.Contains(err.Error(), "discovery root") {
		t.Fatalf("err = %v, want a containment rejection", err)
	}
	if len(f.launcher.acts) != 0 || len(f.confirmer.prompts) != 0 {
		t.Fatal("an uncontained workspace must not confirm or launch")
	}
}

func TestOpenDeletedWorkspaceRejected(t *testing.T) {
	f := newOpenFixture(t)
	if err := os.RemoveAll(f.ws); err != nil {
		t.Fatal(err)
	}
	if _, err := f.open(OpenURL(f.wsID)); err == nil {
		t.Fatal("a deleted workspace must be rejected")
	}
	if len(f.launcher.acts) != 0 || len(f.confirmer.prompts) != 0 {
		t.Fatal("a deleted workspace must not confirm or launch")
	}
}

func TestOpenDeclineLaunchesNothingAndStoresNothing(t *testing.T) {
	f := newOpenFixture(t)
	f.confirmer.answer = false
	_, err := f.open(OpenURL(f.wsID))
	if !errors.Is(err, ErrDeclined) {
		t.Fatalf("err = %v, want ErrDeclined", err)
	}
	if len(f.confirmer.prompts) != 1 || f.confirmer.prompts[0].Path != f.ws {
		t.Fatalf("prompts = %+v, want one with the full path %q", f.confirmer.prompts, f.ws)
	}
	if len(f.launcher.acts) != 0 {
		t.Fatal("a declined link must not launch")
	}
	if _, err := os.Stat(f.approvals.Path()); !os.IsNotExist(err) {
		t.Fatalf("approvals file exists after a decline (err %v)", err)
	}
}

func TestOpenConfirmErrorLaunchesNothing(t *testing.T) {
	f := newOpenFixture(t)
	f.confirmer.err = errors.New("no GUI")
	if _, err := f.open(OpenURL(f.wsID)); err == nil {
		t.Fatal("want the confirmer's error")
	}
	if len(f.launcher.acts) != 0 {
		t.Fatal("must not launch without a confirmation")
	}
}

func TestOpenAcceptLaunchesStoresAndSkipsConfirmNextTime(t *testing.T) {
	f := newOpenFixture(t)
	res, err := f.open(OpenURL(f.wsID))
	if err != nil || !res.Launched {
		t.Fatalf("open = %+v, %v", res, err)
	}
	want := launch.Action{Kind: launch.KindTerminal, Dir: f.ws}
	if len(f.launcher.acts) != 1 || f.launcher.acts[0] != want {
		t.Fatalf("launches = %+v, want [%+v]", f.launcher.acts, want)
	}
	if ok, err := f.approvals.Approved(f.wsID, f.ws); !ok || err != nil {
		t.Fatalf("Approved = %v, %v; want stored", ok, err)
	}
	if fi, err := os.Stat(f.approvals.Path()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("approvals file: %v, %v", fi, err)
	}

	if _, err := f.open(OpenURL(f.wsID)); err != nil {
		t.Fatal(err)
	}
	if len(f.confirmer.prompts) != 1 {
		t.Fatalf("second open prompted again: %d prompts", len(f.confirmer.prompts))
	}
	if len(f.launcher.acts) != 2 {
		t.Fatalf("launches = %d, want 2", len(f.launcher.acts))
	}
}

func TestOpenPathChangeConfirmsAgain(t *testing.T) {
	f := newOpenFixture(t)
	if _, err := f.open(OpenURL(f.wsID)); err != nil {
		t.Fatal(err)
	}
	// The same ID now resolves to another path (e.g. a symlinked root was
	// repointed): the approval no longer covers it.
	moved := mkWorkspace(t, filepath.Join(f.root, "worktrees", "moved", "wgo"))
	tg := f.resolver[f.wsID]
	tg.Root = moved
	f.resolver[f.wsID] = tg
	f.confirmer.answer = false
	if _, err := f.open(OpenURL(f.wsID)); !errors.Is(err, ErrDeclined) {
		t.Fatalf("err = %v, want ErrDeclined", err)
	}
	if len(f.confirmer.prompts) != 2 || f.confirmer.prompts[1].Path != moved {
		t.Fatalf("prompts = %+v, want a second prompt for %s", f.confirmer.prompts, moved)
	}
	if len(f.launcher.acts) != 1 {
		t.Fatalf("launches = %d, want only the first", len(f.launcher.acts))
	}
	f.confirmer.answer = true
	if _, err := f.open(OpenURL(f.wsID)); err != nil {
		t.Fatal(err)
	}
	if ok, _ := f.approvals.Approved(f.wsID, f.ws); ok {
		t.Fatal("the old path should no longer be approved")
	}
	if ok, _ := f.approvals.Approved(f.wsID, moved); !ok {
		t.Fatal("the new path should be approved")
	}
}

func TestOpenCorruptApprovalsConfirmsAgain(t *testing.T) {
	f := newOpenFixture(t)
	if err := os.MkdirAll(filepath.Dir(f.approvals.Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.approvals.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var warns []string
	f.warnf = func(format string, args ...any) { warns = append(warns, fmt.Sprintf(format, args...)) }
	if _, err := f.open(OpenURL(f.wsID)); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(warns, func(w string) bool { return strings.Contains(w, "approvals were lost") }) {
		t.Fatalf("warnings %q do not say the earlier approvals were lost", warns)
	}
	if len(f.confirmer.prompts) != 1 {
		t.Fatal("a corrupt approvals file must lead to a confirmation")
	}
	if ok, err := f.approvals.Approved(f.wsID, f.ws); !ok || err != nil {
		t.Fatalf("Approved = %v, %v after rewrite", ok, err)
	}
}

func TestOpenFallbackReturnsCopyableCD(t *testing.T) {
	f := newOpenFixture(t)
	f.launcher.res = &launch.Result{Method: launch.MethodCopy, Message: "Could not open a terminal.", Copy: launch.CDCommand(f.ws)}
	_, err := f.open(OpenURL(f.wsID))
	if err == nil || !strings.Contains(err.Error(), launch.CDCommand(f.ws)) {
		t.Fatalf("err = %v, want the cd command", err)
	}
}

func TestSystemConfirmer(t *testing.T) {
	ctx := context.Background()
	p := Prompt{WorkspaceID: goodID, Path: "/w/it's \"x\""}
	t.Run("dialog open", func(t *testing.T) {
		r := &fakeRunner{out: map[string]string{"osascript": "Open\n"}}
		ok, err := SystemConfirmer{GOOS: "darwin", Run: r}.Confirm(ctx, p)
		if !ok || err != nil {
			t.Fatalf("= %v, %v", ok, err)
		}
		argv := r.calls[0]
		if len(argv) != 4 || argv[0] != "osascript" || argv[2] != dialogScript || argv[3] != p.Path {
			t.Fatalf("argv = %q", argv)
		}
		if strings.Contains(dialogScript, p.Path) {
			t.Fatal("the path must not be in the script")
		}
	})
	t.Run("dialog cancel", func(t *testing.T) {
		r := &fakeRunner{err: map[string]error{"osascript": errors.New("execution error: User canceled. (-128)")}}
		ok, err := SystemConfirmer{GOOS: "darwin", Run: r}.Confirm(ctx, p)
		if ok || err != nil {
			t.Fatalf("= %v, %v; want a plain decline", ok, err)
		}
	})
	t.Run("dialog gave up", func(t *testing.T) {
		r := &fakeRunner{out: map[string]string{"osascript": "Cancel\n"}}
		if ok, _ := (SystemConfirmer{GOOS: "darwin", Run: r}).Confirm(ctx, p); ok {
			t.Fatal("want a decline")
		}
	})
	t.Run("no gui no tty refuses", func(t *testing.T) {
		r := &fakeRunner{err: map[string]error{"osascript": errors.New("No user interaction allowed. (-1713)")}}
		ok, err := SystemConfirmer{GOOS: "darwin", Run: r, Stdin: strings.NewReader("y\n")}.Confirm(ctx, p)
		if ok || err == nil {
			t.Fatalf("= %v, %v; want a refusal", ok, err)
		}
	})
	t.Run("linux no tty refuses", func(t *testing.T) {
		ok, err := SystemConfirmer{GOOS: "linux"}.Confirm(ctx, p)
		if ok || err == nil {
			t.Fatalf("= %v, %v; want a refusal", ok, err)
		}
	})
	for in, want := range map[string]bool{"y\n": true, "yes\n": true, "\n": false, "n\n": false, "": false, "sure\n": false} {
		t.Run("tty "+strings.TrimSpace(in), func(t *testing.T) {
			var prompt strings.Builder
			ok, err := SystemConfirmer{GOOS: "linux", Stdin: strings.NewReader(in), TTY: true, Prompt: &prompt}.Confirm(ctx, p)
			if ok != want || err != nil {
				t.Fatalf("= %v, %v; want %v", ok, err, want)
			}
			if !strings.Contains(prompt.String(), p.Path) || !strings.Contains(prompt.String(), "[y/N]") {
				t.Fatalf("prompt = %q", prompt.String())
			}
		})
	}
}

func TestApprovalsRejectsNewerVersion(t *testing.T) {
	f := newOpenFixture(t)
	if err := os.MkdirAll(filepath.Dir(f.approvals.Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	newer := []byte(`{"version": 99, "approvals": [], "future": true}` + "\n")
	if err := os.WriteFile(f.approvals.Path(), newer, 0o600); err != nil {
		t.Fatal(err)
	}
	err := f.approvals.Approve(f.wsID, f.ws, time.Now(), nil)
	if err == nil || !strings.Contains(err.Error(), "newer wgo") {
		t.Fatalf("Approve err = %v, want a newer-wgo error", err)
	}
	var warns []string
	f.warnf = func(format string, args ...any) { warns = append(warns, fmt.Sprintf(format, args...)) }
	if _, err := f.open(OpenURL(f.wsID)); err != nil {
		t.Fatal(err)
	}
	if len(f.confirmer.prompts) != 1 || len(f.launcher.acts) != 1 {
		t.Fatalf("prompts %d, launches %d; want 1 each", len(f.confirmer.prompts), len(f.launcher.acts))
	}
	if len(warns) == 0 {
		t.Fatal("want warnings about the unreadable and unwritable approvals")
	}
	if got, _ := os.ReadFile(f.approvals.Path()); !bytes.Equal(got, newer) {
		t.Fatalf("a newer approvals file was changed: %s", got)
	}
}

func TestOpenErrorsNeverQuoteTheURL(t *testing.T) {
	f := newOpenFixture(t)
	for _, raw := range []string{
		"wgo://Run_in_Terminal?ws=" + f.wsID,
		OpenURL(f.wsID) + "&Run_in_Terminal=1",
		"wgo://open?ws=Run_in_Terminal",
	} {
		_, err := f.open(raw)
		if err == nil || strings.Contains(err.Error(), "Run_in_Terminal") {
			t.Fatalf("open(%q) err = %v", raw, err)
		}
	}
	// Errors after the URL was accepted do not quote the ID either.
	for _, id := range []string{"ws-ffffffffffffffff", f.outsideID} {
		if _, err := f.open(OpenURL(id)); err == nil || strings.Contains(err.Error(), id) {
			t.Fatalf("open(%s) err = %v", id, err)
		}
	}
}

func TestOpenRefusesDisguisingPathCharacters(t *testing.T) {
	for name, seg := range map[string]string{
		"rlo":     "safe\u202Egpj.exe",
		"lri":     "a\u2066b\u2069",
		"lrm":     "a\u200Eb",
		"rlm":     "a\u200Fb",
		"alm":     "a\u061Cb",
		"zwsp":    "a\u200Bb",
		"newline": "a\nb",
		"tab":     "a\tb",
		"escape":  "a\x1b[31mb",
	} {
		t.Run(name, func(t *testing.T) {
			f := newOpenFixture(t)
			ws := mkWorkspace(t, filepath.Join(f.root, "worktrees", seg, "wgo"))
			id := dash.WorkspaceID(ws, ws)
			f.resolver[id] = dash.Target{ID: id, Root: ws, MainClone: ws}
			_, err := f.open(OpenURL(id))
			if err == nil || !strings.Contains(err.Error(), "formatting characters") {
				t.Fatalf("err = %v, want a refusal", err)
			}
			if strings.Contains(err.Error(), seg) {
				t.Fatal("the refusal must not echo the path")
			}
			if len(f.confirmer.prompts) != 0 || len(f.launcher.acts) != 0 {
				t.Fatal("a disguised path must not be confirmed or launched")
			}
		})
	}
	if !displaySafe("/Users/me/Документы/日本語/it's \"ok\"") {
		t.Fatal("ordinary non-ASCII paths must be allowed")
	}
}

// blockingConfirmer holds the first confirmation open until released.
type blockingConfirmer struct {
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	release chan struct{}
}

func (b *blockingConfirmer) Confirm(context.Context, Prompt) (bool, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	close(b.entered)
	<-b.release
	return true, nil
}

func TestOpenOneLinkAtATime(t *testing.T) {
	f := newOpenFixture(t)
	lockPath := LockPath(t.TempDir())
	f.lock = func() (func(), error) { return TryLock(lockPath) }
	bc := &blockingConfirmer{entered: make(chan struct{}), release: make(chan struct{})}
	opts := func() OpenOptions {
		return OpenOptions{Resolver: f.resolver, Roots: []string{f.root}, Launcher: f.launcher,
			Confirmer: bc, Approvals: f.approvals, Lock: f.lock}
	}
	first := make(chan error, 1)
	go func() {
		_, err := Open(context.Background(), OpenURL(f.wsID), opts())
		first <- err
	}()
	<-bc.entered // the first link's dialog is up, holding the lock
	start := time.Now()
	_, err := Open(context.Background(), OpenURL(f.wsID), opts())
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("second open err = %v, want ErrBusy", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("the second open waited for the lock")
	}
	close(bc.release)
	if err := <-first; err != nil {
		t.Fatalf("first open: %v", err)
	}
	if bc.calls != 1 || len(f.launcher.acts) != 1 {
		t.Fatalf("confirms %d, launches %d; want 1 each", bc.calls, len(f.launcher.acts))
	}
	// Released: the next link is handled (already approved, so no dialog).
	f.confirmer.answer = false
	if _, err := f.open(OpenURL(f.wsID)); err != nil {
		t.Fatalf("open after release: %v", err)
	}
}
