package launch

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// fakeRunner records every process it is asked to run and never runs one.
type fakeRunner struct {
	calls   [][]string
	dirs    []string
	fail    map[string]bool // argv[0] (or "start:"+argv[0]) -> fail
	path    map[string]bool // executables LookPath finds
	noApp   bool
	appAsks []string
}

func (f *fakeRunner) Run(_ context.Context, dir string, argv []string) error {
	f.calls = append(f.calls, slices.Clone(argv))
	f.dirs = append(f.dirs, dir)
	if f.fail[argv[0]] {
		return errors.New(argv[0] + " failed")
	}
	return nil
}

func (f *fakeRunner) Start(dir string, argv []string) error {
	f.calls = append(f.calls, append([]string{"start:"}, argv...))
	f.dirs = append(f.dirs, dir)
	if f.fail["start:"+argv[0]] {
		return errors.New(argv[0] + " exited")
	}
	return nil
}

func (f *fakeRunner) LookPath(name string) (string, error) {
	if f.path[name] {
		return "/usr/local/bin/" + name, nil
	}
	return "", exec.ErrNotFound
}

func (f *fakeRunner) AppInstalled(name string) bool {
	f.appAsks = append(f.appAsks, name)
	return !f.noApp
}

const nasty = "/Users/me/work/it's a \"dir\" $(touch pwned) `x`\nline2"

func TestGhosttyArgvKeepsPathSeparate(t *testing.T) {
	for _, dir := range []string{"/tmp/plain", "/tmp/with space", nasty} {
		argv := GhosttyArgv(dir, "")
		if len(argv) != 4 || argv[0] != "osascript" || argv[1] != "-e" || argv[2] != ghosttyScript || argv[3] != dir {
			t.Fatalf("GhosttyArgv(%q) = %q", dir, argv)
		}
		if strings.Contains(argv[2], dir) {
			t.Fatalf("path leaked into the script source")
		}
	}
	argv := GhosttyArgv(nasty, "claude --continue\n")
	if len(argv) != 5 || argv[3] != nasty || argv[4] != "claude --continue\n" {
		t.Fatalf("resume argv = %q", argv)
	}
	// The script is a constant: it reads the path from argv and uses the
	// dictionary terms the spec names.
	for _, want := range []string{"on run argv", "item 1 of argv", "initial working directory", "new tab in front window with configuration", "new window with configuration", "select tab", "activate window"} {
		if !strings.Contains(ghosttyScript, want) {
			t.Errorf("ghosttyScript lacks %q", want)
		}
	}
}

func TestTerminalCommandArgvSubstitutesWholeElementsOnly(t *testing.T) {
	tmpl := []string{"wezterm", "start", "--cwd", "{workspace}", "--", "echo", "{workspace}x"}
	got := TerminalCommandArgv(tmpl, nasty)
	want := []string{"wezterm", "start", "--cwd", nasty, "--", "echo", "{workspace}x"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if tmpl[3] != "{workspace}" {
		t.Fatal("template was modified")
	}
}

func TestShellQuoteRoundTrip(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	for _, s := range []string{"/tmp/plain", "/tmp/with space", nasty, "'", "''", "a'b'c", "-n", ""} {
		out, err := exec.Command(sh, "-c", "printf %s "+ShellQuote(s)).Output()
		if err != nil {
			t.Fatalf("sh -c printf %s: %v", ShellQuote(s), err)
		}
		if string(out) != s {
			t.Fatalf("round trip of %q gave %q", s, out)
		}
	}
	if got := CDCommand("/a b/it's"); got != `cd '/a b/it'\''s'` {
		t.Fatalf("CDCommand = %s", got)
	}
}

func TestResumeAllowlist(t *testing.T) {
	argv, err := ResumeArgv("claude")
	if err != nil || !slices.Equal(argv, []string{"claude", "--continue"}) {
		t.Fatalf("claude: %q %v", argv, err)
	}
	argv[0] = "mutated"
	if again, _ := ResumeArgv("claude"); again[0] != "claude" {
		t.Fatal("ResumeArgv returned the allowlist's own slice")
	}
	for _, bad := range []string{"bash", "claude --dangerously-skip-permissions", "rm -rf /", "Claude", "/usr/bin/claude"} {
		if _, err := ResumeArgv(bad); err == nil {
			t.Errorf("ResumeArgv(%q) accepted", bad)
		}
		if err := (Config{Resume: bad}).Validate(); err == nil {
			t.Errorf("Validate accepted resume %q", bad)
		}
	}
	// A non-allowlisted resume launches nothing.
	f := &fakeRunner{}
	r := newSystem(Config{Resume: "bash"}, f, "darwin").Launch(context.Background(), Action{Kind: KindResume, Dir: "/w"})
	if r.Launched || len(f.calls) != 0 || !strings.Contains(r.Message, "not supported") {
		t.Fatalf("bad resume: %+v calls %q", r, f.calls)
	}
	r = newSystem(Config{}, f, "darwin").Launch(context.Background(), Action{Kind: KindResume, Dir: "/w"})
	if r.Launched || len(f.calls) != 0 || !strings.Contains(r.Message, "not configured") {
		t.Fatalf("unconfigured resume: %+v calls %q", r, f.calls)
	}
}

func TestConfigValidate(t *testing.T) {
	good := []Config{{}, {Terminal: "ghostty"}, {Terminal: "iterm"}, {Terminal: "terminal"},
		{Terminal: "command", TerminalCommand: []string{"wezterm", "start", "--cwd", "{workspace}"}}, {Resume: "codex"}}
	for _, c := range good {
		if err := c.Validate(); err != nil {
			t.Errorf("%+v: %v", c, err)
		}
	}
	bad := []Config{{Terminal: "xterm"}, {Terminal: "command"}, {TerminalCommand: []string{"wezterm", "--cwd={workspace}"}}, {TerminalCommand: []string{""}}}
	for _, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
}

func TestTerminalFallbackOrder(t *testing.T) {
	ctx := context.Background()
	cmd := []string{"wezterm", "start", "--cwd", "{workspace}"}

	// Ghostty works: one osascript call, path as its own argument.
	f := &fakeRunner{}
	r := newSystem(Config{}, f, "darwin").Launch(ctx, Action{Kind: KindTerminal, Dir: nasty})
	if !r.Launched || r.Method != MethodGhostty || len(f.calls) != 1 || !slices.Equal(f.calls[0], GhosttyArgv(nasty, "")) {
		t.Fatalf("ghostty: %+v %q", r, f.calls)
	}

	// Ghostty missing -> terminal_command, with the workspace substituted.
	f = &fakeRunner{noApp: true}
	r = newSystem(Config{TerminalCommand: cmd}, f, "darwin").Launch(ctx, Action{Kind: KindTerminal, Dir: nasty})
	if !r.Launched || r.Method != MethodTerminalCommand || len(r.Fallbacks) != 1 || !strings.HasPrefix(r.Fallbacks[0], "ghostty: ") {
		t.Fatalf("command fallback: %+v", r)
	}
	if len(f.calls) != 1 || !slices.Equal(f.calls[0], []string{"start:", "wezterm", "start", "--cwd", nasty}) || f.dirs[0] != nasty {
		t.Fatalf("command argv: %q dirs %q", f.calls, f.dirs)
	}

	// Ghostty fails and terminal_command fails -> copyable cd.
	f = &fakeRunner{fail: map[string]bool{"osascript": true, "start:wezterm": true}}
	r = newSystem(Config{TerminalCommand: cmd}, f, "darwin").Launch(ctx, Action{Kind: KindTerminal, Dir: nasty})
	if r.Launched || r.Method != MethodCopy || r.Copy != CDCommand(nasty) || len(r.Fallbacks) != 2 {
		t.Fatalf("copy fallback: %+v", r)
	}
	if got := []string{f.calls[0][0], f.calls[1][1]}; !slices.Equal(got, []string{"osascript", "wezterm"}) {
		t.Fatalf("order: %q", f.calls)
	}

	// Not macOS and nothing configured: straight to the cd command.
	f = &fakeRunner{}
	r = newSystem(Config{}, f, "linux").Launch(ctx, Action{Kind: KindTerminal, Dir: "/w"})
	if r.Launched || r.Method != MethodCopy || r.Copy != "cd '/w'" || len(f.calls) != 0 {
		t.Fatalf("linux: %+v %q", r, f.calls)
	}

	// terminal = iterm / terminal use `open -a` with the path as one argument.
	for term, app := range map[string]string{"iterm": "iTerm", "terminal": "Terminal"} {
		f = &fakeRunner{}
		r = newSystem(Config{Terminal: term}, f, "darwin").Launch(ctx, Action{Kind: KindTerminal, Dir: nasty})
		if !r.Launched || r.Method != term || !slices.Equal(f.calls[0], []string{"open", "-a", app, nasty}) {
			t.Fatalf("%s: %+v %q", term, r, f.calls)
		}
	}

	// terminal = command skips Ghostty entirely.
	f = &fakeRunner{}
	r = newSystem(Config{Terminal: "command", TerminalCommand: cmd}, f, "darwin").Launch(ctx, Action{Kind: KindTerminal, Dir: "/w"})
	if r.Method != MethodTerminalCommand || len(f.calls) != 1 || len(f.appAsks) != 0 {
		t.Fatalf("command only: %+v %q", r, f.calls)
	}
}

func TestResumeRunsAllowlistedCommandInTab(t *testing.T) {
	ctx := context.Background()
	f := &fakeRunner{}
	r := newSystem(Config{Resume: "claude"}, f, "darwin").Launch(ctx, Action{Kind: KindResume, Dir: nasty})
	if !r.Launched || r.Method != MethodGhostty || !slices.Equal(f.calls[0], GhosttyArgv(nasty, "claude --continue\n")) {
		t.Fatalf("resume: %+v %q", r, f.calls)
	}
	// Outside Ghostty the command is offered to paste.
	f = &fakeRunner{noApp: true}
	r = newSystem(Config{Resume: "claude", TerminalCommand: []string{"wezterm", "start", "--cwd", "{workspace}"}}, f, "darwin").Launch(ctx, Action{Kind: KindResume, Dir: "/w"})
	if !r.Launched || r.Method != MethodTerminalCommand || r.Copy != "claude --continue" {
		t.Fatalf("resume via command: %+v", r)
	}
	f = &fakeRunner{}
	r = newSystem(Config{Resume: "claude"}, f, "linux").Launch(ctx, Action{Kind: KindResume, Dir: "/w x"})
	if r.Launched || r.Copy != "cd '/w x' && claude --continue" {
		t.Fatalf("resume copy: %+v", r)
	}
}

func TestEditorArgv(t *testing.T) {
	cases := []struct {
		exe  string
		line int
		want []string
		ok   bool
	}{
		{"/usr/local/bin/code", 12, []string{"/usr/local/bin/code", "-g", nasty + ":12"}, true},
		{"/usr/bin/xed", 12, []string{"/usr/bin/xed", "-l", "12", nasty}, true},
		{"/usr/bin/subl", 12, []string{"/usr/bin/subl", nasty}, false},
		{"/usr/local/bin/code", 0, []string{"/usr/local/bin/code", nasty}, false},
	}
	for _, c := range cases {
		got, ok := EditorArgv(c.exe, nasty, c.line)
		if !slices.Equal(got, c.want) || ok != c.ok {
			t.Errorf("EditorArgv(%s, %d) = %q %v", c.exe, c.line, got, ok)
		}
	}
}

func TestEditorFallbackOrder(t *testing.T) {
	ctx := context.Background()
	// Configured editor wins.
	f := &fakeRunner{path: map[string]bool{"subl": true, "code": true}}
	r := newSystem(Config{Editor: "subl"}, f, "darwin").Launch(ctx, Action{Kind: KindFile, File: "/p/plan.md", Line: 7})
	if r.Method != MethodEditor || !slices.Equal(f.calls[0], []string{"/usr/local/bin/subl", "/p/plan.md"}) || !strings.Contains(r.Message, "line 7") {
		t.Fatalf("configured: %+v %q", r, f.calls)
	}
	// No code -> xed with -l.
	f = &fakeRunner{path: map[string]bool{"xed": true}}
	r = newSystem(Config{}, f, "darwin").Launch(ctx, Action{Kind: KindFile, File: "/p/plan.md", Line: 7})
	if r.Method != MethodXed || !slices.Equal(f.calls[0], []string{"/usr/local/bin/xed", "-l", "7", "/p/plan.md"}) || len(r.Fallbacks) != 1 {
		t.Fatalf("xed: %+v %q", r, f.calls)
	}
	// No editor -> Finder reveal, with the path to copy.
	f = &fakeRunner{}
	r = newSystem(Config{}, f, "darwin").Launch(ctx, Action{Kind: KindEditor, Dir: "/w"})
	if r.Method != MethodFinder || !slices.Equal(f.calls[0], []string{"open", "-R", "/w"}) || r.Copy != "/w" || len(r.Fallbacks) != 2 {
		t.Fatalf("finder: %+v %q", r, f.calls)
	}
	// Linux with no editor -> copy the path.
	f = &fakeRunner{}
	r = newSystem(Config{}, f, "linux").Launch(ctx, Action{Kind: KindEditor, Dir: "/w"})
	if r.Launched || r.Method != MethodCopy || r.Copy != "/w" || len(f.calls) != 0 {
		t.Fatalf("linux: %+v %q", r, f.calls)
	}
	// An editor that fails to start falls through to the next.
	f = &fakeRunner{path: map[string]bool{"code": true, "xed": true}, fail: map[string]bool{"/usr/local/bin/code": true}}
	r = newSystem(Config{}, f, "darwin").Launch(ctx, Action{Kind: KindEditor, Dir: "/w"})
	if r.Method != MethodXed || len(f.calls) != 2 {
		t.Fatalf("code fails: %+v %q", r, f.calls)
	}
}

func TestReveal(t *testing.T) {
	ctx := context.Background()
	f := &fakeRunner{}
	r := newSystem(Config{}, f, "darwin").Launch(ctx, Action{Kind: KindReveal, Dir: nasty})
	if !r.Launched || !slices.Equal(f.calls[0], []string{"open", "-R", nasty}) {
		t.Fatalf("reveal: %+v %q", r, f.calls)
	}
	f = &fakeRunner{}
	r = newSystem(Config{}, f, "linux").Launch(ctx, Action{Kind: KindReveal, Dir: "/w"})
	if r.Launched || r.Copy != "/w" || len(f.calls) != 0 || !strings.Contains(r.Message, "only supported on macOS") {
		t.Fatalf("linux reveal: %+v", r)
	}
	f = &fakeRunner{fail: map[string]bool{"open": true}}
	r = newSystem(Config{}, f, "darwin").Launch(ctx, Action{Kind: KindReveal, Dir: "/w"})
	if r.Launched || r.Copy != "/w" || len(r.Fallbacks) != 1 {
		t.Fatalf("reveal fails: %+v", r)
	}
}

func TestExecRunnerUsesArgvNotShell(t *testing.T) {
	// Run a harmless real process: the "$(...)" argument must arrive
	// verbatim, which it would not through a shell.
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("no true")
	}
	if err := (ExecRunner{}).Run(context.Background(), t.TempDir(), []string{"true", "$(exit 1)"}); err != nil {
		t.Fatal(err)
	}
	if err := (ExecRunner{}).Run(context.Background(), "", []string{"false"}); err == nil {
		t.Fatal("false succeeded")
	}
	if err := (ExecRunner{}).Start("", []string{"false"}); err == nil {
		t.Fatal("Start(false) should report the fast failure")
	}
	if err := (ExecRunner{}).Start("", []string{"wgo-no-such-terminal-xyz"}); err == nil {
		t.Fatal("Start of a missing binary succeeded")
	}
}

func TestAutomationHint(t *testing.T) {
	if automationHint(nil) != nil {
		t.Fatal("nil error gained a hint")
	}
	denied := errors.New("exit status 1: execution error: Not authorized to send Apple events to Ghostty. (-1743)")
	if got := automationHint(denied); !errors.Is(got, denied) || !strings.Contains(got.Error(), "tccutil reset AppleEvents") || !strings.Contains(got.Error(), "Privacy & Security > Automation") {
		t.Fatalf("hint = %v", got)
	}
	other := errors.New("exit status 1: syntax error")
	if automationHint(other) != other {
		t.Fatal("unrelated error gained a hint")
	}
}
