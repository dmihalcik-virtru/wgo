package gotool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/virtru/wgo/internal/gomod"
	"github.com/virtru/wgo/internal/gotool/gotooltest"
)

func TestParsePackageModules(t *testing.T) {
	// `go list -deps -json` emits concatenated Package objects. Standard-library
	// packages carry no Module, and many packages share one module.
	const stream = `{
	"ImportPath": "fmt",
	"Standard": true
}
{
	"ImportPath": "github.com/opentdf/platform/service/policy",
	"Module": {"Path": "github.com/opentdf/platform/service", "Version": "v0.11.6"}
}
{
	"ImportPath": "github.com/opentdf/platform/service/authorization",
	"Module": {"Path": "github.com/opentdf/platform/service", "Version": "v0.11.6"}
}
{
	"ImportPath": "google.golang.org/grpc",
	"Module": {"Path": "google.golang.org/grpc", "Version": "v1.72.0"}
}
{
	"ImportPath": "command-line-arguments"
}
`
	mods, err := ParsePackageModules(strings.NewReader(stream))
	require.NoError(t, err)

	require.Len(t, mods, 2, "std, the duplicate and the module-less package are all dropped")
	assert.Equal(t, "github.com/opentdf/platform/service", mods[0].Path)
	assert.Equal(t, "v0.11.6", mods[0].Version)
	assert.Equal(t, "google.golang.org/grpc", mods[1].Path)
}

func TestParsePackageModulesDistinguishesVersions(t *testing.T) {
	// Two versions of one module can legitimately appear (e.g. across a
	// replace); dedup keys on path *and* version so neither is lost.
	const stream = `{"ImportPath":"a","Module":{"Path":"m","Version":"v1.0.0"}}
{"ImportPath":"b","Module":{"Path":"m","Version":"v1.1.0"}}
`
	mods, err := ParsePackageModules(strings.NewReader(stream))
	require.NoError(t, err)
	require.Len(t, mods, 2)
}

func TestParsePackageModulesEmpty(t *testing.T) {
	mods, err := ParsePackageModules(strings.NewReader(""))
	require.NoError(t, err)
	assert.Empty(t, mods)
}

func TestParsePackageModulesMalformed(t *testing.T) {
	_, err := ParsePackageModules(strings.NewReader(`{"ImportPath":"a"} not-json`))
	require.Error(t, err)
}

func TestWorkDefaultsToOff(t *testing.T) {
	// The zero value must never mean "search upward for a go.work" — that is
	// how a rig would silently adopt a checked-out repo's own workspace.
	c := &Client{}
	assert.Equal(t, WorkOff, c.work())

	env := c.environ(false)
	assert.Contains(t, env, "GOWORK=off")
}

func TestEnvironStripsInherited(t *testing.T) {
	t.Setenv("GOWORK", "/somewhere/else/go.work")
	t.Setenv("GOFLAGS", "-mod=mod")

	c := NewClient().WithWork("/rig/go.work")
	env := c.environ(true)

	assert.Contains(t, env, "GOWORK=/rig/go.work")
	assert.NotContains(t, env, "GOWORK=/somewhere/else/go.work")
	assert.Contains(t, env, "GOFLAGS=-mod=readonly")
	assert.NotContains(t, env, "GOFLAGS=-mod=mod")
}

func TestEnvironCallerOverridesWin(t *testing.T) {
	c := NewClient().WithEnv("GOFLAGS=-mod=mod")
	env := c.environ(true)
	// Later entries win in exec's environment, so an explicit WithEnv setting
	// must come after the readOnly default.
	assert.Greater(t, indexOf(env, "GOFLAGS=-mod=mod"), indexOf(env, "GOFLAGS=-mod=readonly"))
}

func indexOf(env []string, want string) int {
	for i, kv := range env {
		if kv == want {
			return i
		}
	}
	return -1
}

func TestWithWorkAbsolutizes(t *testing.T) {
	c := NewClient().In("/rigs/dsp").WithWork("go.work")
	assert.Equal(t, filepath.Join("/rigs/dsp", "go.work"), c.Work)

	// An absolute path and the sentinel both pass through untouched.
	assert.Equal(t, "/elsewhere/go.work", NewClient().In("/rigs/dsp").WithWork("/elsewhere/go.work").Work)
	assert.Equal(t, WorkOff, NewClient().In("/rigs/dsp").WithWork(WorkOff).Work)
}

func TestWithMethodsDoNotMutate(t *testing.T) {
	base := NewClient().In("/a")
	derived := base.In("/b").WithWork("/b/go.work").WithEnv("X=1")

	assert.Equal(t, "/a", base.Dir)
	assert.Equal(t, WorkOff, base.Work)
	assert.Empty(t, base.Env)
	assert.Equal(t, "/b", derived.Dir)
}

func TestFindWorkFile(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	nested := filepath.Join(repo, "test", "integration")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	// No go.work anywhere: the walk stops at the repo root and reports nothing,
	// rather than continuing into the user's home directory the way `go` does.
	assert.Equal(t, "", FindWorkFile(nested, repo))

	// A go.work above the stop directory is out of bounds.
	outside := filepath.Join(root, "go.work")
	require.NoError(t, os.WriteFile(outside, []byte("go 1.24\n"), 0o644))
	assert.Equal(t, "", FindWorkFile(nested, repo))

	// One at the repo root is found from a nested module.
	inside := filepath.Join(repo, "go.work")
	require.NoError(t, os.WriteFile(inside, []byte("go 1.24\n"), 0o644))
	assert.Equal(t, inside, FindWorkFile(nested, repo))

	// The nearest one wins.
	nearer := filepath.Join(repo, "test", "go.work")
	require.NoError(t, os.WriteFile(nearer, []byte("go 1.24\n"), 0o644))
	assert.Equal(t, nearer, FindWorkFile(nested, repo))

	// startDir == stopDir still checks that directory.
	assert.Equal(t, inside, FindWorkFile(repo, repo))
}

func TestFindWorkFileIgnoresDirectory(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(repo, "go.work"), 0o755))
	assert.Equal(t, "", FindWorkFile(repo, repo))
}

func TestFindWorkFileUnrelatedStopDir(t *testing.T) {
	// A stopDir that is not an ancestor must terminate at the filesystem root
	// rather than loop forever.
	dir := t.TempDir()
	assert.Equal(t, "", FindWorkFile(dir, filepath.Join(dir, "not-an-ancestor")))
}

func TestExitErrorTruncatesButKeepsFullStderr(t *testing.T) {
	full := strings.Repeat("x", maxErrSummary*2)
	err := &ExitError{Args: []string{"build", "./..."}, Stderr: full}

	assert.Contains(t, err.Error(), "truncated")
	assert.Less(t, len(err.Error()), len(full))
	assert.Equal(t, full, err.Stderr, "callers can still print the whole failure")
}

func TestExitErrorWithoutStderr(t *testing.T) {
	err := &ExitError{Args: []string{"list", "-m"}, Err: os.ErrNotExist}
	assert.Contains(t, err.Error(), "go list -m")
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// --- tests that need a real toolchain ---

func TestVersionAndEnvVar(t *testing.T) {
	gotooltest.RequireGo(t)

	c := NewClient().In(t.TempDir())
	v, err := c.Version()
	require.NoError(t, err)
	assert.Contains(t, v, "go version")

	// The whole point of the package: GOWORK is whatever we said it was.
	gowork, err := c.EnvVar("GOWORK")
	require.NoError(t, err)
	assert.Equal(t, "off", gowork)
}

// A workspace must be selected explicitly. Running inside a directory that has
// its own go.work must not join it unless the caller asked.
func TestGoWorkIsNotInherited(t *testing.T) {
	gotooltest.RequireGo(t)

	dir := t.TempDir()
	own := filepath.Join(dir, "go.work")
	require.NoError(t, os.WriteFile(own, []byte("go 1.21\n\nuse .\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.21\n"), 0o644))

	c := NewClient().In(dir)
	gowork, err := c.EnvVar("GOWORK")
	require.NoError(t, err)
	assert.Equal(t, "off", gowork, "the ancestor walk must not find the repo's own go.work")

	withWork := c.WithWork(own)
	gowork, err = withWork.EnvVar("GOWORK")
	require.NoError(t, err)
	assert.Equal(t, withWork.Work, gowork)
	// WithWork resolves symlinks (macOS /var -> /private/var), so compare
	// against the resolved form rather than the path as written.
	assert.Equal(t, "go.work", filepath.Base(gowork))
}

func TestListModulesAndBuild(t *testing.T) {
	gotooltest.RequireGo(t)

	// A dependency published only through a file:// proxy: no network, and no
	// dependence on the developer's module cache.
	proxy := gotooltest.Proxy(t, gotooltest.Module{
		Path:    "example.com/dep",
		Version: "v1.0.0",
		Files: map[string]string{
			"go.mod":  "module example.com/dep\n\ngo 1.21\n",
			"dep.go":  "package dep\n\nfunc Answer() int { return 42 }\n",
			"LICENSE": "MIT\n",
		},
	})

	main := t.TempDir()
	writeFile(t, main, "go.mod", "module example.com/app\n\ngo 1.21\n\nrequire example.com/dep v1.0.0\n")
	writeFile(t, main, "main.go", `package main

import (
	"fmt"

	"example.com/dep"
)

func main() { fmt.Println(dep.Answer()) }
`)

	c := NewClient().In(main).WithEnv(gotooltest.Env(t, proxy)...)

	// GONOSUMDB/GOSUMDB=off keep this from needing sum.golang.org, but go.sum
	// is still required for a module build, so let the toolchain write one.
	_, err := c.WithEnv("GOFLAGS=-mod=mod").run(false, "mod", "tidy")
	require.NoError(t, err)

	mods, err := c.ListModules()
	require.NoError(t, err)
	require.NotEmpty(t, mods)
	assert.True(t, mods[0].Main, "the main module sorts first")
	assert.Equal(t, "example.com/app", mods[0].Path)
	assert.Contains(t, modulePaths(mods), "example.com/dep")

	pkgMods, err := c.ListPackageModules(".")
	require.NoError(t, err)
	paths := modulePaths(pkgMods)
	assert.Contains(t, paths, "example.com/app")
	assert.Contains(t, paths, "example.com/dep")
	assert.NotContains(t, paths, "fmt", "standard library packages carry no module")

	out := t.TempDir()
	require.NoError(t, c.Build(out, "./..."))
	entries, err := os.ReadDir(out)
	require.NoError(t, err)
	assert.NotEmpty(t, entries, "-o directs the binary away from the checkout")

	entries, err = os.ReadDir(main)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotEqual(t, "app", e.Name(), "the checkout must stay clean")
	}
}

func TestBuildInfoRoundTrip(t *testing.T) {
	gotooltest.RequireGo(t)

	main := t.TempDir()
	writeFile(t, main, "go.mod", "module example.com/app\n\ngo 1.21\n")
	writeFile(t, main, "main.go", "package main\n\nfunc main() {}\n")

	c := NewClient().In(main)
	out := t.TempDir()
	require.NoError(t, c.Build(out, "."))

	bi, err := c.BuildInfo(filepath.Join(out, "app"))
	require.NoError(t, err)
	assert.Equal(t, "example.com/app", bi.Main.Path)
	assert.True(t, strings.HasPrefix(bi.GoVersion, "go1."))
	assert.NotEmpty(t, bi.Settings["GOARCH"])
}

func TestBuildInfoOnNonBinary(t *testing.T) {
	gotooltest.RequireGo(t)

	dir := t.TempDir()
	writeFile(t, dir, "notabinary.txt", "hello\n")

	_, err := NewClient().In(dir).BuildInfo(filepath.Join(dir, "notabinary.txt"))
	require.Error(t, err)
}

func TestWorkEdit(t *testing.T) {
	gotooltest.RequireGo(t)

	root := t.TempDir()
	memberDir := filepath.Join(root, "member")
	require.NoError(t, os.Mkdir(memberDir, 0o755))
	writeFile(t, memberDir, "go.mod", "module example.com/member\n\ngo 1.21\n")
	writeFile(t, memberDir, "m.go", "package member\n")

	work := filepath.Join(root, "go.work")
	// Deliberately unformatted, to prove -fmt actually ran: a single-member
	// block normalizes to the one-line form.
	require.NoError(t, os.WriteFile(work, []byte("go 1.21\nuse (\n./member\n)\n"), 0o644))

	c := NewClient().In(root).WithWork(work)
	require.NoError(t, c.WorkEditFmt())
	assert.Equal(t, "go 1.21\n\nuse ./member\n", readFile(t, work))

	require.NoError(t, c.WorkEditReplace("example.com/dep", "", "example.com/dep", "v1.0.0"))
	assert.Contains(t, readFile(t, work), "replace example.com/dep => example.com/dep v1.0.0")

	require.NoError(t, c.WorkEditDropReplace("example.com/dep", ""))
	assert.NotContains(t, readFile(t, work), "replace example.com/dep")

	// go.work is the only file touched; the member's go.mod is untouched.
	assert.Equal(t, "module example.com/member\n\ngo 1.21\n", readFile(t, filepath.Join(memberDir, "go.mod")))
}

func TestWorkUse(t *testing.T) {
	gotooltest.RequireGo(t)

	root := t.TempDir()
	memberDir := filepath.Join(root, "member")
	require.NoError(t, os.Mkdir(memberDir, 0o755))
	writeFile(t, memberDir, "go.mod", "module example.com/member\n\ngo 1.21\n")

	work := filepath.Join(root, "go.work")
	require.NoError(t, os.WriteFile(work, []byte("go 1.21\n"), 0o644))

	c := NewClient().In(root).WithWork(work)
	require.NoError(t, c.WorkUse("./member"))
	assert.Contains(t, readFile(t, work), "./member")

	// A no-op call must not shell out at all.
	require.NoError(t, c.WorkUse())
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

func modulePaths(mods []gomod.Module) []string {
	paths := make([]string, 0, len(mods))
	for _, m := range mods {
		paths = append(paths, m.Path)
	}
	return paths
}
