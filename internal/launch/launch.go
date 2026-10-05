// Package launch opens local tools at a workspace: a terminal tab (Ghostty on
// macOS, else a configured terminal), an agent resume, an editor, or Finder.
//
// It is the shared launcher behind `wgo dash` browser actions (gh-70) and is
// meant to be reused by #27's launch affordances. Callers hand it a validated
// absolute path; it never receives, builds or runs a shell string. Every
// process is started with an explicit argv through os/exec, and the one
// AppleScript it runs is a fixed constant that reads the path from its
// arguments (`on run argv`), so a path can never become script source.
//
// When the preferred method is unavailable it falls back in a fixed order and
// always ends with something the user can copy (a shell-quoted `cd` command
// or the path), reporting which method was used and why earlier ones failed.
// A launch never changes jj, plan or agent state.
package launch

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Kind is what to open.
type Kind string

// Action kinds.
const (
	// KindTerminal opens a terminal tab whose working directory is Dir.
	KindTerminal Kind = "terminal"
	// KindResume opens a terminal tab in Dir and resumes the configured
	// agent tool there.
	KindResume Kind = "resume"
	// KindEditor opens Dir in an editor.
	KindEditor Kind = "editor"
	// KindReveal reveals Dir in Finder.
	KindReveal Kind = "reveal"
	// KindFile opens File in an editor, at Line when the editor supports it.
	KindFile Kind = "file"
)

// Action is one launch request. Paths are absolute, already validated by the
// caller; nothing in an Action comes from a browser or a URL.
type Action struct {
	Kind Kind
	// Dir is the workspace directory (terminal, resume, editor, reveal).
	Dir string
	// File is the file to open (KindFile), and Line its 1-based line, or 0.
	File string
	Line int
}

// Methods reported in Result.Method and Result.Fallbacks.
const (
	MethodGhostty         = "ghostty"
	MethodITerm           = "iterm"
	MethodTerminalApp     = "terminal"
	MethodTerminalCommand = "terminal_command"
	MethodEditor          = "editor" // the configured dash.editor
	MethodCode            = "code"
	MethodXed             = "xed"
	MethodFinder          = "finder"
	MethodCopy            = "copy" // nothing launched: Copy holds what to run or open
)

// Result reports what a launch did.
type Result struct {
	// Launched is true when a process was started successfully; false when
	// the result is only something to copy.
	Launched bool `json:"launched"`
	// Method is the method that succeeded, or MethodCopy.
	Method string `json:"method"`
	// Message is a human-readable account of what happened.
	Message string `json:"message"`
	// Copy is text the user can copy and run or open: a shell-quoted cd
	// command or a path. It is set whenever the action could not be fully
	// carried out (and for resume outside Ghostty).
	Copy string `json:"copy,omitempty"`
	// Fallbacks lists the methods tried before Method, each with why it
	// failed, in the order they were tried.
	Fallbacks []string `json:"fallbacks,omitempty"`
}

// Launcher opens tools for actions. System is the real implementation;
// tests inject fakes.
type Launcher interface {
	Launch(ctx context.Context, a Action) Result
}

// Terminal choices for Config.Terminal.
const (
	TerminalGhostty = "ghostty"
	TerminalITerm   = "iterm"
	TerminalApp     = "terminal"
	TerminalCommand = "command"
)

// WorkspacePlaceholder is replaced by the workspace directory in
// Config.TerminalCommand. Only an argv element that is exactly the
// placeholder is replaced.
const WorkspacePlaceholder = "{workspace}"

// resumeTools is the fixed allowlist of agent tools Resume can start, and
// the argv each one runs. Only trusted local config selects among them.
var resumeTools = map[string][]string{
	"claude": {"claude", "--continue"},
	"codex":  {"codex", "resume", "--last"},
}

// ResumeTools lists the allowlisted resume tool names, sorted.
func ResumeTools() []string {
	out := make([]string, 0, len(resumeTools))
	for k := range resumeTools {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ResumeArgv returns the argv for an allowlisted resume tool, or an error
// naming the allowed tools.
func ResumeArgv(tool string) ([]string, error) {
	argv, ok := resumeTools[tool]
	if !ok {
		return nil, fmt.Errorf("resume tool %q is not supported; set dash.resume to one of %s, or leave it empty to hide Resume", tool, strings.Join(ResumeTools(), ", "))
	}
	return slices.Clone(argv), nil
}

// Config is the trusted local launch configuration (the [dash] section).
type Config struct {
	// Terminal is ghostty (the default when empty), iterm, terminal or
	// command.
	Terminal string
	// TerminalCommand is the argv for Terminal = "command", and the fallback
	// after Ghostty when set. WorkspacePlaceholder elements are replaced.
	TerminalCommand []string
	// Resume is an allowlisted tool name, or empty to disable Resume.
	Resume string
	// Editor is an editor executable to prefer over code and xed.
	Editor string
}

// Validate reports every problem with c. A zero Config is valid.
func (c Config) Validate() error {
	var errs []error
	switch c.Terminal {
	case "", TerminalGhostty, TerminalITerm, TerminalApp:
	case TerminalCommand:
		if len(c.TerminalCommand) == 0 {
			errs = append(errs, errors.New(`dash.terminal = "command" needs dash.terminal_command, e.g. ["wezterm", "start", "--cwd", "{workspace}"]`))
		}
	default:
		errs = append(errs, fmt.Errorf("dash.terminal = %q is not supported; use ghostty, iterm, terminal or command", c.Terminal))
	}
	for i, a := range c.TerminalCommand {
		if a != WorkspacePlaceholder && strings.Contains(a, WorkspacePlaceholder) {
			errs = append(errs, fmt.Errorf("dash.terminal_command[%d] = %q: %s is only replaced as a whole argument; split it into its own element", i, a, WorkspacePlaceholder))
		}
	}
	if len(c.TerminalCommand) > 0 && strings.TrimSpace(c.TerminalCommand[0]) == "" {
		errs = append(errs, errors.New("dash.terminal_command: the first element must name the terminal executable"))
	}
	if c.Resume != "" {
		if _, err := ResumeArgv(c.Resume); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ShellQuote quotes s for a POSIX shell: it wraps s in single quotes and
// writes each embedded single quote as quote, backslash, quote, quote
// (close, escaped quote, reopen). Any byte, including a newline, is literal
// inside the quotes.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// CDCommand is the copyable command that changes to dir.
func CDCommand(dir string) string { return "cd " + ShellQuote(dir) }

// ghosttyScript is the fixed AppleScript that opens a Ghostty tab. It takes
// the working directory as argument 1 and optional initial input (a resume
// command from the allowlist, with a trailing newline) as argument 2. Paths
// are never interpolated into it. It needs Ghostty 1.3+ with
// macos-applescript enabled (the default).
const ghosttyScript = `on run argv
	set wsDir to item 1 of argv
	set startInput to ""
	if (count of argv) > 1 then set startInput to item 2 of argv
	tell application "Ghostty"
		activate
		if startInput is "" then
			set cfg to {initial working directory:wsDir}
		else
			set cfg to {initial working directory:wsDir, initial input:startInput}
		end if
		if (count of windows) is 0 then
			new window with configuration cfg
		else
			set t to new tab in front window with configuration cfg
			select tab t
			activate window front window
		end if
	end tell
end run`

// GhosttyArgv is the osascript argv that opens a Ghostty tab at dir, typing
// input (if any) into it. dir and input are separate argv elements.
func GhosttyArgv(dir, input string) []string {
	argv := []string{"osascript", "-e", ghosttyScript, dir}
	if input != "" {
		argv = append(argv, input)
	}
	return argv
}

// TerminalCommandArgv substitutes dir for every element of tmpl that is
// exactly WorkspacePlaceholder. Nothing else is changed or split.
func TerminalCommandArgv(tmpl []string, dir string) []string {
	out := make([]string, len(tmpl))
	for i, a := range tmpl {
		if a == WorkspacePlaceholder {
			a = dir
		}
		out[i] = a
	}
	return out
}

// editorFlavor is how an editor takes a line number.
type editorFlavor int

const (
	flavorPlain  editorFlavor = iota // editor <path>
	flavorVSCode                     // code -g <file>:<line>
	flavorXed                        // xed -l <line> <file>
)

func flavorOf(exe string) editorFlavor {
	switch strings.TrimSuffix(strings.ToLower(filepath.Base(exe)), ".exe") {
	case "code", "code-insiders", "cursor", "windsurf", "codium", "vscodium":
		return flavorVSCode
	case "xed":
		return flavorXed
	}
	return flavorPlain
}

// EditorArgv opens path (a file or directory) in exe, at line when line > 0
// and the editor supports it. lineOK reports whether the line was passed.
func EditorArgv(exe, path string, line int) (argv []string, lineOK bool) {
	if line > 0 {
		switch flavorOf(exe) {
		case flavorVSCode:
			return []string{exe, "-g", path + ":" + strconv.Itoa(line)}, true
		case flavorXed:
			return []string{exe, "-l", strconv.Itoa(line), path}, true
		}
	}
	return []string{exe, path}, false
}

// RevealArgv reveals path in Finder.
func RevealArgv(path string) []string { return []string{"open", "-R", path} }

// Runner runs processes. The real one uses os/exec; tests inject a fake.
type Runner interface {
	// Run runs argv in dir (when set) and waits for it to exit.
	Run(ctx context.Context, dir string, argv []string) error
	// Start starts argv in dir detached from wgo and returns once it is
	// clearly running or has failed.
	Start(dir string, argv []string) error
	// LookPath finds an executable on PATH.
	LookPath(name string) (string, error)
	// AppInstalled reports whether a macOS application bundle is installed.
	AppInstalled(name string) bool
}

// System is the real Launcher.
type System struct {
	cfg  Config
	run  Runner
	goos string
}

// NewSystem returns a Launcher for cfg using r (ExecRunner when nil) on the
// current OS.
func NewSystem(cfg Config, r Runner) *System {
	return newSystem(cfg, r, runtime.GOOS)
}

func newSystem(cfg Config, r Runner, goos string) *System {
	if r == nil {
		r = ExecRunner{}
	}
	return &System{cfg: cfg, run: r, goos: goos}
}

// Launch implements Launcher.
func (s *System) Launch(ctx context.Context, a Action) Result {
	switch a.Kind {
	case KindTerminal:
		return s.terminal(ctx, a.Dir, nil)
	case KindResume:
		argv, err := ResumeArgv(s.cfg.Resume)
		if s.cfg.Resume == "" {
			err = errors.New("resume is not configured; set dash.resume = \"claude\" in ~/.wgo/config.toml")
		}
		if err != nil {
			return Result{Method: MethodCopy, Message: err.Error()}
		}
		return s.terminal(ctx, a.Dir, argv)
	case KindEditor:
		return s.editor(ctx, a.Dir, 0)
	case KindReveal:
		return s.reveal(ctx, a.Dir)
	case KindFile:
		return s.editor(ctx, a.File, a.Line)
	}
	return Result{Method: MethodCopy, Message: fmt.Sprintf("unknown launch kind %q", a.Kind)}
}

type attempt struct {
	method string
	try    func() error
}

// first runs attempts in order and returns the first that succeeds, with
// the failures before it.
func first(attempts []attempt) (method string, fallbacks []string, ok bool) {
	for _, at := range attempts {
		err := at.try()
		if err == nil {
			return at.method, fallbacks, true
		}
		fallbacks = append(fallbacks, at.method+": "+err.Error())
	}
	return "", fallbacks, false
}

// TerminalPlan lists the terminal methods tried, in order, for cfg on goos.
// The copyable cd command always follows them.
func TerminalPlan(cfg Config, goos string) []string {
	var out []string
	switch cfg.Terminal {
	case "", TerminalGhostty:
		if goos == "darwin" {
			out = append(out, MethodGhostty)
		}
		if len(cfg.TerminalCommand) > 0 {
			out = append(out, MethodTerminalCommand)
		}
	case TerminalITerm:
		if goos == "darwin" {
			out = append(out, MethodITerm)
		}
	case TerminalApp:
		if goos == "darwin" {
			out = append(out, MethodTerminalApp)
		}
	case TerminalCommand:
		if len(cfg.TerminalCommand) > 0 {
			out = append(out, MethodTerminalCommand)
		}
	}
	return out
}

// automationHint explains a macOS Automation (TCC) denial, which osascript
// reports as error -1743, and how to undo it.
func automationHint(err error) error {
	if err == nil {
		return nil
	}
	if msg := err.Error(); strings.Contains(msg, "-1743") || strings.Contains(strings.ToLower(msg), "not authorized to send apple events") {
		return fmt.Errorf("%w (macOS denied wgo control of Ghostty: allow it under System Settings > Privacy & Security > Automation, or run tccutil reset AppleEvents and try again to be asked anew)", err)
	}
	return err
}

// terminal opens a tab at dir through the configured chain, typing resume
// (an allowlisted argv) into it when the method supports that.
func (s *System) terminal(ctx context.Context, dir string, resume []string) Result {
	input := ""
	if resume != nil {
		input = strings.Join(resume, " ") + "\n"
	}
	var attempts []attempt
	for _, m := range TerminalPlan(s.cfg, s.goos) {
		switch m {
		case MethodGhostty:
			attempts = append(attempts, attempt{m, func() error {
				if !s.run.AppInstalled("Ghostty") {
					return errors.New("Ghostty.app is not installed")
				}
				return automationHint(s.run.Run(ctx, "", GhosttyArgv(dir, input)))
			}})
		case MethodITerm:
			attempts = append(attempts, attempt{m, func() error {
				return s.run.Run(ctx, "", []string{"open", "-a", "iTerm", dir})
			}})
		case MethodTerminalApp:
			attempts = append(attempts, attempt{m, func() error {
				return s.run.Run(ctx, "", []string{"open", "-a", "Terminal", dir})
			}})
		case MethodTerminalCommand:
			attempts = append(attempts, attempt{m, func() error {
				return s.run.Start(dir, TerminalCommandArgv(s.cfg.TerminalCommand, dir))
			}})
		}
	}
	cd := CDCommand(dir)
	resumeCopy := ""
	if resume != nil {
		resumeCopy = cd + " && " + strings.Join(resume, " ")
	}
	method, fallbacks, ok := first(attempts)
	if !ok {
		r := Result{Method: MethodCopy, Copy: cd, Fallbacks: fallbacks,
			Message: "Could not open a terminal; copy the cd command instead."}
		if len(attempts) == 0 {
			r.Message = "No terminal launcher is available here (Ghostty needs macOS; set dash.terminal_command to use another terminal); copy the cd command instead."
		}
		if resume != nil {
			r.Copy = resumeCopy
			r.Message = strings.Replace(r.Message, "copy the cd command", "copy the resume command", 1)
		}
		return r
	}
	r := Result{Launched: true, Method: method, Fallbacks: fallbacks}
	switch {
	case resume == nil:
		r.Message = "Opened a " + methodName(method) + " tab at the workspace."
	case method == MethodGhostty:
		r.Message = "Opened a Ghostty tab at the workspace running " + strings.Join(resume, " ") + "."
	default:
		// Only Ghostty can type the command; elsewhere the user pastes it.
		r.Message = "Opened " + methodName(method) + " at the workspace; it cannot run the resume command itself, so paste it there."
		r.Copy = strings.Join(resume, " ")
	}
	return r
}

func methodName(m string) string {
	switch m {
	case MethodGhostty:
		return "Ghostty"
	case MethodITerm:
		return "iTerm"
	case MethodTerminalApp:
		return "Terminal"
	case MethodTerminalCommand:
		return "configured terminal"
	case MethodFinder:
		return "Finder"
	}
	return m
}

// editor opens path in the configured editor, then code, then xed, then
// reveals it in Finder (macOS), else returns the path to copy.
func (s *System) editor(ctx context.Context, path string, line int) Result {
	var exes []struct{ method, exe string }
	if s.cfg.Editor != "" {
		exes = append(exes, struct{ method, exe string }{MethodEditor, s.cfg.Editor})
	}
	exes = append(exes, struct{ method, exe string }{MethodCode, "code"})
	if s.goos == "darwin" {
		exes = append(exes, struct{ method, exe string }{MethodXed, "xed"})
	}
	var attempts []attempt
	lineUsed := false
	for _, e := range exes {
		attempts = append(attempts, attempt{e.method, func() error {
			exe, err := s.run.LookPath(e.exe)
			if err != nil {
				return fmt.Errorf("%s not found on PATH", e.exe)
			}
			argv, ok := EditorArgv(exe, path, line)
			if err := s.run.Run(ctx, "", argv); err != nil {
				return err
			}
			lineUsed = ok
			return nil
		}})
	}
	if s.goos == "darwin" {
		attempts = append(attempts, attempt{MethodFinder, func() error { return s.run.Run(ctx, "", RevealArgv(path)) }})
	}
	method, fallbacks, ok := first(attempts)
	name := filepath.Base(path)
	if !ok {
		return Result{Method: MethodCopy, Copy: path, Fallbacks: fallbacks,
			Message: "No editor could be opened (set dash.editor, or install code or xed); copy the path instead."}
	}
	r := Result{Launched: true, Method: method, Fallbacks: fallbacks}
	switch {
	case method == MethodFinder:
		r.Message = "No editor could be opened, so " + name + " is revealed in Finder."
		r.Copy = path
	case line > 0 && lineUsed:
		r.Message = fmt.Sprintf("Opened %s at line %d in %s.", name, line, method)
	case line > 0:
		r.Message = fmt.Sprintf("Opened %s in %s; it cannot jump to a line, so look for line %d.", name, method, line)
	default:
		r.Message = "Opened " + name + " in " + method + "."
	}
	return r
}

// reveal shows path in Finder on macOS; elsewhere it returns the path.
func (s *System) reveal(ctx context.Context, path string) Result {
	if s.goos != "darwin" {
		return Result{Method: MethodCopy, Copy: path, Message: "Revealing in a file manager is only supported on macOS; copy the path instead."}
	}
	if err := s.run.Run(ctx, "", RevealArgv(path)); err != nil {
		return Result{Method: MethodCopy, Copy: path, Fallbacks: []string{MethodFinder + ": " + err.Error()},
			Message: "Finder could not reveal the workspace; copy the path instead."}
	}
	return Result{Launched: true, Method: MethodFinder, Message: "Revealed " + filepath.Base(path) + " in Finder."}
}
