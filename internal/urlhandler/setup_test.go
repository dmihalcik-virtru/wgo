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
	} {
		if !strings.Contains(src, want) {
			t.Errorf("source lacks %q:\n%s", want, src)
		}
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
	return &Installer{GOOS: "darwin", Home: t.TempDir(), Executable: "/usr/local/bin/wgo", PathEnv: "/usr/bin", Run: r, Out: &out}, &out
}

func TestInstallRunsStepsInOrder(t *testing.T) {
	r := &fakeRunner{}
	in, out := newInstaller(t, r)
	if err := in.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	app := AppPath(in.Home)
	var names []string
	for _, c := range r.calls {
		names = append(names, c[0])
	}
	want := []string{"osacompile", "plutil", "plutil", "plutil", "codesign", LSRegister}
	if !slices.Equal(names, want) {
		t.Fatalf("ran %q, want %q", names, want)
	}
	if c := r.calls[0]; c[2] != app {
		t.Errorf("osacompile -o %q, want %q", c[2], app)
	}
	if c := r.calls[5]; !slices.Equal(c, []string{LSRegister, "-f", app}) {
		t.Errorf("lsregister argv = %q", c)
	}
	if fi, err := os.Stat(filepath.Dir(app)); err != nil || !fi.IsDir() {
		t.Errorf("~/Applications not created: %v", err)
	}
	if !strings.Contains(out.String(), "--uninstall") {
		t.Errorf("output does not name the uninstall command: %s", out.String())
	}
}

func TestInstallFailureRemovesPartialApplet(t *testing.T) {
	r := &fakeRunner{err: map[string]error{"codesign": errors.New("boom")}}
	in, _ := newInstaller(t, r)
	app := AppPath(in.Home)
	var removed []string
	in.RemoveAll = func(p string) error { removed = append(removed, p); return nil }
	if err := in.Install(context.Background()); err == nil {
		t.Fatal("want an error")
	}
	if !slices.Equal(removed, []string{app}) {
		t.Fatalf("removed %q, want the partial applet", removed)
	}
	for _, c := range r.calls {
		if c[0] == LSRegister {
			t.Fatal("must not register after a failed step")
		}
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
