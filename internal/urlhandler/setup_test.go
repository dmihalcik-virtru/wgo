package urlhandler

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestAppleScriptString(t *testing.T) {
	cases := map[string]string{
		"/usr/local/bin/wgo":         `"/usr/local/bin/wgo"`,
		`/Users/a "b"/wgo`:           `"/Users/a \"b\"/wgo"`,
		`/Users/a\b/wgo`:             `"/Users/a\\b/wgo"`,
		`/x/\"; do shell script "rm`: `"/x/\\\"; do shell script \"rm"`,
		"/Users/it's/wgo":            `"/Users/it's/wgo"`,
	}
	for in, want := range cases {
		got, err := AppleScriptString(in)
		if err != nil || got != want {
			t.Errorf("AppleScriptString(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"/a\nb", "/a\rb", "/a\x00b", "/a\tb", "/a\x7fb"} {
		if _, err := AppleScriptString(bad); err == nil {
			t.Errorf("AppleScriptString(%q) accepted a control character", bad)
		}
	}
}

func TestAppletSource(t *testing.T) {
	src, err := AppletSource(`/Users/me/my "bin"/wgo`, "/opt/homebrew/bin:/usr/bin")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`set wgoPath to "/Users/me/my \"bin\"/wgo"`,
		`set envPath to "/opt/homebrew/bin:/usr/bin"`,
		`on open location theURL`,
		`do shell script "PATH=" & quoted form of envPath & " " & quoted form of wgoPath & " open " & quoted form of theURL`,
		`display notification errMsg with title "wgo did not open the link"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("source lacks %q:\n%s", want, src)
		}
	}
	if strings.Contains(src, "display alert") {
		t.Error("link failures must not raise a modal alert")
	}
	if strings.Contains(src, "property ") {
		t.Error("the applet must not use properties (they are saved back into the bundle)")
	}
	// theURL appears only as the quoted argument.
	if n := strings.Count(src, "theURL"); n != 2 {
		t.Errorf("theURL used %d times, want 2 (handler parameter and one quoted argument)", n)
	}
	if _, err := AppletSource("wgo", ""); err == nil {
		t.Error("a relative wgo path must be rejected")
	}
	if _, err := AppletSource("/a\n/wgo", ""); err == nil {
		t.Error("a newline in the wgo path must be rejected")
	}
	if src, _ := AppletSource("/bin/wgo", ""); !strings.Contains(src, `"`+defaultPathEnv+`"`) {
		t.Error("an empty PATH must fall back to the default")
	}
}

func TestOsacompileArgvOneLinePerE(t *testing.T) {
	argv := OsacompileArgv("/h/Applications/x.app", "a\nb \"c\"\n")
	want := []string{"osacompile", "-o", "/h/Applications/x.app", "-e", "a", "-e", `b "c"`}
	if !slices.Equal(argv, want) {
		t.Fatalf("argv = %q, want %q", argv, want)
	}
}

func TestPlistArgvs(t *testing.T) {
	app := "/h/Applications/wgo URL Handler.app"
	plist := app + "/Contents/Info.plist"
	want := [][]string{
		{"plutil", "-replace", "CFBundleIdentifier", "-string", "com.virtru.wgo.url-handler", plist},
		{"plutil", "-replace", "CFBundleURLTypes", "-json", `[{"CFBundleURLName":"com.virtru.wgo.url-handler","CFBundleURLSchemes":["wgo"]}]`, plist},
		{"plutil", "-replace", "WgoURLHandler", "-string", "wgo setup url-handler v1", plist},
	}
	got := PlistArgvs(app)
	if len(got) != len(want) {
		t.Fatalf("got %d argvs", len(got))
	}
	for i := range want {
		if !slices.Equal(got[i], want[i]) {
			t.Errorf("argv %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func newInstaller(t *testing.T, r *fakeRunner) (*Installer, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	if r.before == nil {
		r.before = compileLeavesApplet
	}
	return &Installer{GOOS: "darwin", Home: t.TempDir(), Executable: "/usr/local/bin/wgo", PathEnv: "/usr/bin", Run: r, Out: &out}, &out
}

// compileLeavesApplet makes the fake osacompile leave a bundle at its -o
// path, as the real one does.
func compileLeavesApplet(argv []string) {
	if argv[0] == "osacompile" {
		_ = os.MkdirAll(filepath.Join(argv[2], "Contents"), 0o755)
	}
}

func TestInstallRunsStepsInOrder(t *testing.T) {
	r := &fakeRunner{}
	in, out := newInstaller(t, r)
	if err := in.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	app := AppPath(in.Home)
	built := filepath.Join(StagingDir(in.Home), AppName)
	var names []string
	for _, c := range r.calls {
		names = append(names, c[0])
	}
	want := []string{"osacompile", "plutil", "plutil", "plutil", "codesign", LSRegister}
	if !slices.Equal(names, want) {
		t.Fatalf("ran %q, want %q", names, want)
	}
	if c := r.calls[0]; c[2] != built {
		t.Errorf("osacompile -o %q, want the staging path %q", c[2], built)
	}
	if c := r.calls[4]; c[len(c)-1] != built {
		t.Errorf("codesign argv = %q, want the staged applet", c)
	}
	if c := r.calls[5]; !slices.Equal(c, []string{LSRegister, "-f", app}) {
		t.Errorf("lsregister argv = %q", c)
	}
	if fi, err := os.Stat(filepath.Join(app, "Contents")); err != nil || !fi.IsDir() {
		t.Errorf("applet not moved into place: %v", err)
	}
	if _, err := os.Lstat(StagingDir(in.Home)); !os.IsNotExist(err) {
		t.Errorf("staging folder left behind: %v", err)
	}
	if !strings.Contains(out.String(), "--uninstall") {
		t.Errorf("output does not name the uninstall command: %s", out.String())
	}
}

// mkOurApplet leaves an installed applet whose Info.plist the fake runner
// reports as ours, with a file to tell it apart from a new build.
func mkOurApplet(t *testing.T, in *Installer, r *fakeRunner) string {
	t.Helper()
	app := AppPath(in.Home)
	if err := os.MkdirAll(filepath.Join(app, "Contents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "old"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r.extract = map[string]string{MarkerKey: MarkerValue, "CFBundleIdentifier": BundleID}
	return app
}

func TestInstallReplacesOwnApplet(t *testing.T) {
	r := &fakeRunner{}
	in, _ := newInstaller(t, r)
	app := mkOurApplet(t, in, r)
	if err := in.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(app, "Contents", "old")); !os.IsNotExist(err) {
		t.Fatalf("previous applet was not replaced: %v", err)
	}
	if _, err := os.Stat(filepath.Join(app, "Contents")); err != nil {
		t.Fatalf("new applet missing: %v", err)
	}
	if _, err := os.Lstat(StagingDir(in.Home)); !os.IsNotExist(err) {
		t.Errorf("staging folder left behind: %v", err)
	}
}

// A failed rebuild must not cost the user the handler they already have.
func TestInstallFailureKeepsPreviousApplet(t *testing.T) {
	for _, step := range []string{"osacompile", "plutil", "codesign"} {
		t.Run(step, func(t *testing.T) {
			r := &fakeRunner{err: map[string]error{step: errors.New("boom")}}
			in, _ := newInstaller(t, r)
			app := mkOurApplet(t, in, r)
			err := in.Install(context.Background())
			if err == nil || !strings.Contains(err.Error(), "partial build was removed") || !strings.Contains(err.Error(), "previous wgo URL handler was left in place") {
				t.Fatalf("err = %v", err)
			}
			if _, err := os.Stat(filepath.Join(app, "Contents", "old")); err != nil {
				t.Fatalf("previous applet lost: %v", err)
			}
			if _, err := os.Lstat(StagingDir(in.Home)); !os.IsNotExist(err) {
				t.Errorf("partial build left behind: %v", err)
			}
			for _, c := range r.calls {
				if c[0] == LSRegister {
					t.Fatal("must not register after a failed step")
				}
			}
		})
	}
}

func TestInstallRestoresPreviousWhenSwapFails(t *testing.T) {
	r := &fakeRunner{}
	in, _ := newInstaller(t, r)
	app := mkOurApplet(t, in, r)
	in.Rename = func(oldpath, newpath string) error {
		if newpath == app && strings.HasPrefix(oldpath, StagingDir(in.Home)) && filepath.Base(oldpath) == AppName {
			return errors.New("cross-device link")
		}
		return os.Rename(oldpath, newpath)
	}
	if err := in.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "left in place") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(app, "Contents", "old")); err != nil {
		t.Fatalf("previous applet not restored: %v", err)
	}
}

// TestInstallKeepsBackupWhenRestoreFails: if the new applet cannot be moved
// into place and the previous one cannot be moved back, the previous one
// is the copy in staging, so staging must not be removed.
func TestInstallKeepsBackupWhenRestoreFails(t *testing.T) {
	r := &fakeRunner{}
	in, _ := newInstaller(t, r)
	app := mkOurApplet(t, in, r)
	in.Rename = func(oldpath, newpath string) error {
		if newpath == app {
			return errors.New("cross-device link")
		}
		return os.Rename(oldpath, newpath)
	}
	err := in.Install(context.Background())
	previous := filepath.Join(StagingDir(in.Home), "previous.app")
	if err == nil || !strings.Contains(err.Error(), previous) || !strings.Contains(err.Error(), "run wgo setup url-handler again") {
		t.Fatalf("err = %v; want it to name %s and the fix", err, previous)
	}
	if _, err := os.Stat(filepath.Join(previous, "Contents", "old")); err != nil {
		t.Fatalf("the only copy of the previous applet was lost: %v", err)
	}
	for _, c := range r.calls {
		if c[0] == LSRegister {
			t.Fatal("must not register after a failed swap")
		}
	}
}

func TestInstallMoveAsideFailureKeepsPreviousApplet(t *testing.T) {
	r := &fakeRunner{}
	in, _ := newInstaller(t, r)
	app := mkOurApplet(t, in, r)
	in.Rename = func(oldpath, newpath string) error {
		if oldpath == app {
			return errors.New("permission denied")
		}
		return os.Rename(oldpath, newpath)
	}
	err := in.Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "moving the previous applet aside") || !strings.Contains(err.Error(), "left in place") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(app, "Contents", "old")); err != nil {
		t.Fatalf("previous applet lost: %v", err)
	}
	if _, err := os.Lstat(StagingDir(in.Home)); !os.IsNotExist(err) {
		t.Errorf("staging folder left behind: %v", err)
	}
	for _, c := range r.calls {
		if c[0] == LSRegister {
			t.Fatal("must not register after a failed move")
		}
	}
}

func TestUninstallUnregisterFailureKeepsApplet(t *testing.T) {
	r := &fakeRunner{
		extract: map[string]string{MarkerKey: MarkerValue, "CFBundleIdentifier": BundleID},
		err:     map[string]error{LSRegister: errors.New("boom")},
	}
	in, _ := newInstaller(t, r)
	app := AppPath(in.Home)
	if err := os.MkdirAll(filepath.Join(app, "Contents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := in.Uninstall(context.Background()); err == nil || !strings.Contains(err.Error(), "not removed") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(app); err != nil {
		t.Fatalf("applet removed although unregistering failed: %v", err)
	}
}

func TestInstallRegisterFailureKeepsNewApplet(t *testing.T) {
	r := &fakeRunner{err: map[string]error{LSRegister: errors.New("boom")}}
	in, _ := newInstaller(t, r)
	app := AppPath(in.Home)
	if err := in.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "is installed, but registering") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(app, "Contents")); err != nil {
		t.Fatalf("signed applet was removed: %v", err)
	}
}

func TestInstallClearsLeftoverStaging(t *testing.T) {
	r := &fakeRunner{}
	in, _ := newInstaller(t, r)
	leftover := filepath.Join(StagingDir(in.Home), AppName, "Contents", "stale")
	if err := os.MkdirAll(filepath.Dir(leftover), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(leftover, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := in.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(AppPath(in.Home), "Contents", "stale")); !os.IsNotExist(err) {
		t.Fatalf("leftover build was installed: %v", err)
	}
}

func TestInstallRefusesSymlinkOrFile(t *testing.T) {
	for name, mk := range map[string]func(app, target string) error{
		"symlink": func(app, target string) error { return os.Symlink(target, app) },
		"file":    func(app, _ string) error { return os.WriteFile(app, nil, 0o644) },
	} {
		t.Run(name, func(t *testing.T) {
			r := &fakeRunner{extract: map[string]string{MarkerKey: MarkerValue, "CFBundleIdentifier": BundleID}}
			in, _ := newInstaller(t, r)
			app := AppPath(in.Home)
			target := filepath.Join(in.Home, "elsewhere")
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(app), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := mk(app, target); err != nil {
				t.Fatal(err)
			}
			in.RemoveAll = func(p string) error { t.Fatalf("removed %s", p); return nil }
			if err := in.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "left alone") {
				t.Fatalf("err = %v, want a refusal", err)
			}
			if len(r.calls) != 0 {
				t.Fatalf("ran %q after refusing", r.calls)
			}
			if _, err := os.Stat(target); err != nil {
				t.Fatalf("target lost: %v", err)
			}
		})
	}
}

func TestInstallRefusesToReplaceForeignApp(t *testing.T) {
	r := &fakeRunner{extract: map[string]string{"CFBundleIdentifier": "com.example.other"}}
	in, _ := newInstaller(t, r)
	app := AppPath(in.Home)
	if err := os.MkdirAll(filepath.Join(app, "Contents"), 0o755); err != nil {
		t.Fatal(err)
	}
	in.RemoveAll = func(p string) error { t.Fatalf("removed %s", p); return nil }
	if err := in.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "left alone") {
		t.Fatalf("err = %v, want a refusal", err)
	}
	for _, c := range r.calls {
		if c[0] != "plutil" {
			t.Fatalf("ran %q after refusing", c)
		}
	}
}

func TestUninstall(t *testing.T) {
	ours := map[string]string{MarkerKey: MarkerValue, "CFBundleIdentifier": BundleID}
	t.Run("ours", func(t *testing.T) {
		r := &fakeRunner{extract: ours}
		in, _ := newInstaller(t, r)
		app := AppPath(in.Home)
		if err := os.MkdirAll(filepath.Join(app, "Contents"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := in.Uninstall(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(app); !os.IsNotExist(err) {
			t.Fatalf("applet still exists: %v", err)
		}
		last := r.calls[len(r.calls)-1]
		if !slices.Equal(last, []string{LSRegister, "-u", app}) {
			t.Fatalf("last call %q, want lsregister -u", last)
		}
	})
	foreign := map[string]map[string]string{
		"no marker":    {"CFBundleIdentifier": BundleID},
		"wrong marker": {MarkerKey: "something else", "CFBundleIdentifier": BundleID},
		"wrong id":     {MarkerKey: MarkerValue, "CFBundleIdentifier": "com.example.app"},
	}
	for name, ex := range foreign {
		t.Run(name, func(t *testing.T) {
			r := &fakeRunner{extract: ex}
			in, _ := newInstaller(t, r)
			app := AppPath(in.Home)
			if err := os.MkdirAll(filepath.Join(app, "Contents"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := in.Uninstall(context.Background()); err == nil {
				t.Fatal("want a refusal")
			}
			if _, err := os.Stat(app); err != nil {
				t.Fatalf("a foreign app was removed: %v", err)
			}
			for _, c := range r.calls {
				if c[0] == LSRegister {
					t.Fatal("a foreign app was unregistered")
				}
			}
		})
	}
	t.Run("remove fails after unregister", func(t *testing.T) {
		r := &fakeRunner{extract: ours}
		in, _ := newInstaller(t, r)
		app := AppPath(in.Home)
		if err := os.MkdirAll(filepath.Join(app, "Contents"), 0o755); err != nil {
			t.Fatal(err)
		}
		in.RemoveAll = func(string) error { return errors.New("permission denied") }
		err := in.Uninstall(context.Background())
		if err == nil || !strings.Contains(err.Error(), "unregistered") || !strings.Contains(err.Error(), "remove it manually") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		r := &fakeRunner{extract: ours}
		in, _ := newInstaller(t, r)
		app := AppPath(in.Home)
		target := filepath.Join(in.Home, "precious")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(app), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, app); err != nil {
			t.Fatal(err)
		}
		if err := in.Uninstall(context.Background()); err == nil {
			t.Fatal("want a refusal for a symlink")
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatal("the symlink target was removed")
		}
	})
	t.Run("not installed", func(t *testing.T) {
		r := &fakeRunner{}
		in, out := newInstaller(t, r)
		if err := in.Uninstall(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(r.calls) != 0 || !strings.Contains(out.String(), "nothing to remove") {
			t.Fatalf("calls %q, out %q", r.calls, out.String())
		}
	})
}

func TestDryRunRunsNothing(t *testing.T) {
	r := &fakeRunner{}
	in, out := newInstaller(t, r)
	in.DryRun = true
	in.RemoveAll = func(p string) error { t.Fatalf("removed %s", p); return nil }
	in.MkdirAll = func(p string, _ os.FileMode) error { t.Fatalf("created %s", p); return nil }
	if err := in.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Dry run", "osacompile", "CFBundleURLTypes", "codesign", "lsregister' '-f'"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry run output lacks %q:\n%s", want, out.String())
		}
	}
	// An existing applet: uninstall dry run also runs nothing.
	if err := os.MkdirAll(AppPath(in.Home), 0o755); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := in.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := in.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 0 {
		t.Fatalf("dry run ran %q", r.calls)
	}
	if !strings.Contains(out.String(), "lsregister' '-u'") || !strings.Contains(out.String(), "remove ") {
		t.Errorf("uninstall dry run output:\n%s", out.String())
	}
}

func TestNonDarwinUnavailable(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		r := &fakeRunner{}
		in := &Installer{GOOS: goos, Home: t.TempDir(), Executable: "/usr/local/bin/wgo", Run: r}
		for _, f := range []func(context.Context) error{in.Install, in.Uninstall} {
			err := f(context.Background())
			if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "URL-handler setup is unavailable on "+goos) {
				t.Fatalf("err = %v", err)
			}
		}
		if len(r.calls) != 0 {
			t.Fatalf("ran %q on %s", r.calls, goos)
		}
	}
}

// TestAppletCompiles compiles the generated source into a temp dir with the
// real osacompile when it exists. It never registers anything.
func TestAppletCompiles(t *testing.T) {
	if _, err := exec.LookPath("osacompile"); err != nil {
		t.Skip("osacompile not available")
	}
	src, err := AppletSource(`/tmp/odd "dir"\with/wgo`, "/usr/bin:/bin")
	if err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(t.TempDir(), "t.app")
	argv := OsacompileArgv(app, src)
	if out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("osacompile: %v\n%s", err, out)
	}
	if _, err := exec.LookPath("osadecompile"); err != nil {
		return
	}
	dec, err := exec.Command("osadecompile", filepath.Join(app, "Contents", "Resources", "Scripts", "main.scpt")).Output()
	if err != nil {
		t.Fatalf("osadecompile: %v", err)
	}
	if want := `set wgoPath to "/tmp/odd \"dir\"\\with/wgo"`; !strings.Contains(string(dec), want) {
		t.Fatalf("decompiled applet lacks %s:\n%s", want, dec)
	}
}

// TestDialogScriptCompiles checks the confirmation script's syntax with the
// real osacompile when it exists, without showing the dialog.
func TestDialogScriptCompiles(t *testing.T) {
	if _, err := exec.LookPath("osacompile"); err != nil {
		t.Skip("osacompile not available")
	}
	out := filepath.Join(t.TempDir(), "dialog.scpt")
	if b, err := exec.Command("osacompile", "-o", out, "-e", dialogScript).CombinedOutput(); err != nil {
		t.Fatalf("osacompile: %v\n%s", err, b)
	}
}

// An applet wgo cannot read is not called foreign: the error says plutil
// failed, so the user knows it may be a broken wgo applet.
func TestInstallUnreadablePlistIsNotForeign(t *testing.T) {
	r := &fakeRunner{err: map[string]error{"plutil": errors.New("plutil: exit status 1: Info.plist: (The file couldn't be opened because there is no such file.)")}}
	in, _ := newInstaller(t, r)
	app := mkOurApplet(t, in, r)
	r.extractErr = true
	in.RemoveAll = func(p string) error { t.Fatalf("removed %s", p); return nil }
	err := in.Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "could not read "+MarkerKey) || strings.Contains(err.Error(), "was not created by wgo") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(app, "Contents", "old")); err != nil {
		t.Fatalf("applet touched: %v", err)
	}
}

// TestDialogScriptDefaultsToCancel pins the dialog's safe defaults: Return
// or Escape cancels, a timeout cancels, and the path comes only from argv.
func TestDialogScriptDefaultsToCancel(t *testing.T) {
	for _, want := range []string{`default button "Cancel"`, `cancel button "Cancel"`, "giving up after ", `if gave up of r then return "Cancel"`, "set wsPath to item 1 of argv"} {
		if !strings.Contains(dialogScript, want) {
			t.Errorf("dialog script lacks %q", want)
		}
	}
	if strings.Contains(dialogScript, `default button "Open"`) {
		t.Error("Open must not be the default button")
	}
}
