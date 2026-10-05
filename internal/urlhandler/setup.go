package urlhandler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/virtru/wgo/internal/launch"
)

// The installed applet.
const (
	// AppName is the applet bundle created in ~/Applications.
	AppName = "wgo URL Handler.app"
	// BundleID is the applet's CFBundleIdentifier.
	BundleID = "com.virtru.wgo.url-handler"
	// MarkerKey and MarkerValue are an Info.plist entry that marks the
	// applet as created by wgo; --uninstall removes nothing without it.
	MarkerKey   = "WgoURLHandler"
	MarkerValue = "wgo setup url-handler v1"
	// LSRegister is the LaunchServices registration tool.
	LSRegister = "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"
)

// urlTypesJSON is the CFBundleURLTypes value: the wgo scheme only.
const urlTypesJSON = `[{"CFBundleURLName":"` + BundleID + `","CFBundleURLSchemes":["` + Scheme + `"]}]`

// defaultPathEnv is the PATH the applet uses when none was recorded.
const defaultPathEnv = "/usr/bin:/bin:/usr/sbin:/sbin"

// AppPath is where the applet lives for a user whose home is home.
func AppPath(home string) string { return filepath.Join(home, "Applications", AppName) }

// AppleScriptString returns s as an AppleScript string literal. Backslashes
// and double quotes are escaped; control characters (including newlines)
// are rejected, since no executable path or PATH needs them.
func AppleScriptString(s string) (string, error) {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%q contains a control character", s)
		}
	}
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`, nil
}

// AppletSource is the applet's AppleScript. wgoPath (absolute) and pathEnv
// are fixed at install time as string literals. A received URL is handed to
// `wgo open` as exactly one shell-quoted argument; wgo decides everything
// else. A failure (a rejected link, an unknown workspace) is shown in an
// alert.
func AppletSource(wgoPath, pathEnv string) (string, error) {
	if !filepath.IsAbs(wgoPath) {
		return "", fmt.Errorf("the wgo executable path %q is not absolute", wgoPath)
	}
	if pathEnv == "" {
		pathEnv = defaultPathEnv
	}
	wq, err := AppleScriptString(wgoPath)
	if err != nil {
		return "", fmt.Errorf("the wgo executable path cannot be embedded: %w", err)
	}
	pq, err := AppleScriptString(pathEnv)
	if err != nil {
		return "", fmt.Errorf("PATH cannot be embedded: %w", err)
	}
	// Locals, not properties: an applet saves changed properties back into
	// its bundle, which would break its signature.
	return `-- Created by wgo setup url-handler. Remove with: wgo setup url-handler --uninstall
on open location theURL
	set wgoPath to ` + wq + `
	set envPath to ` + pq + `
	try
		do shell script "PATH=" & quoted form of envPath & " " & quoted form of wgoPath & " open " & quoted form of theURL
	on error errMsg
		display alert "wgo did not open the link" message errMsg as critical
	end try
end open location

on run
	display dialog "This applet opens wgo:// links with wgo. Remove it with: wgo setup url-handler --uninstall" with title "wgo URL Handler" buttons {"OK"} default button "OK"
end run
`, nil
}

// OsacompileArgv compiles source into the applet at app, one -e per line,
// so the source never passes through a file or a shell.
func OsacompileArgv(app, source string) []string {
	argv := []string{"osacompile", "-o", app}
	for _, line := range strings.Split(strings.TrimRight(source, "\n"), "\n") {
		argv = append(argv, "-e", line)
	}
	return argv
}

// PlistArgvs are the plutil edits to the applet's Info.plist: its bundle
// ID, the wgo URL scheme and the wgo marker.
func PlistArgvs(app string) [][]string {
	plist := infoPlist(app)
	return [][]string{
		{"plutil", "-replace", "CFBundleIdentifier", "-string", BundleID, plist},
		{"plutil", "-replace", "CFBundleURLTypes", "-json", urlTypesJSON, plist},
		{"plutil", "-replace", MarkerKey, "-string", MarkerValue, plist},
	}
}

func infoPlist(app string) string { return filepath.Join(app, "Contents", "Info.plist") }

// Installer installs and removes the applet. Every process goes through
// Run, so tests never run osacompile, plutil, codesign or lsregister.
type Installer struct {
	GOOS string
	Home string
	// Executable is the absolute, symlink-resolved path of wgo to embed.
	Executable string
	// PathEnv is the PATH the applet gives wgo (jj must be on it).
	PathEnv string
	Run     Runner
	Out     io.Writer
	// DryRun prints the steps and runs nothing.
	DryRun bool
	// RemoveAll and MkdirAll default to the os functions.
	RemoveAll func(string) error
	MkdirAll  func(string, os.FileMode) error
}

// ErrUnsupported reports a non-macOS build.
var ErrUnsupported = errors.New("URL-handler setup is unavailable")

func (in *Installer) check() error {
	if in.GOOS != "darwin" {
		return fmt.Errorf("%w on %s: the wgo:// handler is a macOS applet. wgo dash works without it", ErrUnsupported, in.GOOS)
	}
	if in.Home == "" || !filepath.IsAbs(in.Home) {
		return errors.New("cannot find your home directory")
	}
	if in.Run == nil {
		in.Run = ExecRunner{}
	}
	if in.Out == nil {
		in.Out = io.Discard
	}
	if in.RemoveAll == nil {
		in.RemoveAll = os.RemoveAll
	}
	if in.MkdirAll == nil {
		in.MkdirAll = os.MkdirAll
	}
	return nil
}

func quoteArgv(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = launch.ShellQuote(a)
	}
	return strings.Join(q, " ")
}

func (in *Installer) run(ctx context.Context, argv []string) error {
	if in.DryRun {
		fmt.Fprintf(in.Out, "  %s\n", quoteArgv(argv))
		return nil
	}
	_, err := in.Run.Output(ctx, argv)
	return err
}

// verifyOurs checks app is a real directory (not a symlink) whose
// Info.plist carries the wgo marker and bundle ID.
func (in *Installer) verifyOurs(ctx context.Context, app string) error {
	fi, err := os.Lstat(app)
	if err != nil {
		return err
	}
	if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("%s is not an application folder wgo created; it was left alone", app)
	}
	for _, kv := range [][2]string{{MarkerKey, MarkerValue}, {"CFBundleIdentifier", BundleID}} {
		out, err := in.Run.Output(ctx, []string{"plutil", "-extract", kv[0], "raw", "-o", "-", infoPlist(app)})
		if err != nil || strings.TrimSpace(out) != kv[1] {
			return fmt.Errorf("%s was not created by wgo setup url-handler (its Info.plist has no %s = %q); it was left alone. Remove it yourself if you mean to", app, kv[0], kv[1])
		}
	}
	return nil
}

// Install builds the applet in ~/Applications and registers it for wgo://.
// An existing applet is replaced only when it carries the wgo marker.
func (in *Installer) Install(ctx context.Context) error {
	if err := in.check(); err != nil {
		return err
	}
	if !filepath.IsAbs(in.Executable) {
		return fmt.Errorf("the wgo executable path %q is not absolute", in.Executable)
	}
	src, err := AppletSource(in.Executable, in.PathEnv)
	if err != nil {
		return err
	}
	app := AppPath(in.Home)
	if in.DryRun {
		fmt.Fprintf(in.Out, "Dry run: wgo setup url-handler would, running nothing now:\n")
	}
	_, statErr := os.Lstat(app)
	exists := statErr == nil
	if exists {
		if in.DryRun {
			fmt.Fprintf(in.Out, "  verify %s carries the %s marker, then remove it to replace it\n", app, MarkerKey)
		} else {
			if err := in.verifyOurs(ctx, app); err != nil {
				return err
			}
			if err := in.RemoveAll(app); err != nil {
				return fmt.Errorf("remove the previous %s: %w", app, err)
			}
		}
	}
	if in.DryRun {
		fmt.Fprintf(in.Out, "  create %s\n", filepath.Dir(app))
	} else if err := in.MkdirAll(filepath.Dir(app), 0o755); err != nil {
		return err
	}
	if err := in.run(ctx, OsacompileArgv(app, src)); err != nil {
		return fmt.Errorf("compile the applet: %w", err)
	}
	steps := append(PlistArgvs(app),
		// Editing Info.plist invalidates the applet's signature; re-sign it
		// ad hoc so macOS will launch it.
		[]string{"codesign", "--force", "--sign", "-", app},
		[]string{LSRegister, "-f", app},
	)
	for _, argv := range steps {
		if err := in.run(ctx, argv); err != nil {
			// The applet is ours and half-built: remove it rather than
			// leave a handler that does not work.
			if rerr := in.RemoveAll(app); rerr != nil {
				return fmt.Errorf("%s failed: %w (and removing the partial %s failed: %v)", argv[0], err, app, rerr)
			}
			return fmt.Errorf("%s failed: %w; the partial applet was removed", argv[0], err)
		}
	}
	if in.DryRun {
		return nil
	}
	fmt.Fprintf(in.Out, "Installed %s for wgo:// links.\nIt runs %s open <url>; the first link to each workspace asks before opening it.\nRemove it with: wgo setup url-handler --uninstall\n", app, in.Executable)
	return nil
}

// Uninstall unregisters and removes the applet, but only after verifying
// it is the one wgo created.
func (in *Installer) Uninstall(ctx context.Context) error {
	if err := in.check(); err != nil {
		return err
	}
	app := AppPath(in.Home)
	if _, err := os.Lstat(app); errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(in.Out, "No wgo URL handler is installed at %s; nothing to remove.\n", app)
		return nil
	}
	if in.DryRun {
		fmt.Fprintf(in.Out, "Dry run: wgo setup url-handler --uninstall would, running nothing now:\n")
		fmt.Fprintf(in.Out, "  verify %s carries the %s marker\n", app, MarkerKey)
		fmt.Fprintf(in.Out, "  %s\n", quoteArgv([]string{LSRegister, "-u", app}))
		fmt.Fprintf(in.Out, "  remove %s\n", app)
		return nil
	}
	if err := in.verifyOurs(ctx, app); err != nil {
		return err
	}
	if err := in.run(ctx, []string{LSRegister, "-u", app}); err != nil {
		return fmt.Errorf("unregister %s: %w; it was not removed", app, err)
	}
	if err := in.RemoveAll(app); err != nil {
		return fmt.Errorf("remove %s: %w", app, err)
	}
	fmt.Fprintf(in.Out, "Removed %s; wgo:// links no longer open anything.\n", app)
	return nil
}
