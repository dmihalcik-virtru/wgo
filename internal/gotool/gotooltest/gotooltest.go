// Package gotooltest provides test helpers for exercising the real `go`
// toolchain hermetically.
//
// Tests that need `go` must not depend on the developer's module cache, the
// network, or a proxy: results would vary by machine and CI would be flaky or
// offline-broken. Proxy builds a file:// GOPROXY containing exactly the modules
// a test declares, and Env returns the environment that isolates a run to it.
package gotooltest

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

// RequireGo skips the test unless a `go` binary is on PATH.
func RequireGo(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not found on PATH")
	}
	return path
}

// Module is one module version to publish into a test proxy. Files maps
// module-relative paths to contents and must include a "go.mod".
type Module struct {
	Path    string
	Version string
	Files   map[string]string
}

// Proxy writes mods into a module proxy directory laid out per the GOPROXY
// protocol and returns its path. Use Env to point a Client at it.
func Proxy(t *testing.T, mods ...Module) string {
	t.Helper()
	root := t.TempDir()

	for _, m := range mods {
		if _, ok := m.Files["go.mod"]; !ok {
			t.Fatalf("gotooltest: module %s@%s has no go.mod", m.Path, m.Version)
		}
		mv := module.Version{Path: m.Path, Version: m.Version}

		// The zip requires a real directory tree on disk, and its entries must
		// be prefixed with <path>@<version>, which CreateFromDir handles.
		src := t.TempDir()
		for name, content := range m.Files {
			dst := filepath.Join(src, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				t.Fatalf("gotooltest: mkdir %s: %v", dst, err)
			}
			if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
				t.Fatalf("gotooltest: write %s: %v", dst, err)
			}
		}

		escaped, err := module.EscapePath(m.Path)
		if err != nil {
			t.Fatalf("gotooltest: escape %s: %v", m.Path, err)
		}
		dir := filepath.Join(root, filepath.FromSlash(escaped), "@v")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("gotooltest: mkdir %s: %v", dir, err)
		}

		info, err := json.Marshal(map[string]string{
			"Version": m.Version,
			// A fixed timestamp keeps proxy contents byte-identical across runs.
			"Time": "2020-01-01T00:00:00Z",
		})
		if err != nil {
			t.Fatalf("gotooltest: marshal info: %v", err)
		}
		write(t, filepath.Join(dir, m.Version+".info"), info)
		write(t, filepath.Join(dir, m.Version+".mod"), []byte(m.Files["go.mod"]))

		zf, err := os.Create(filepath.Join(dir, m.Version+".zip"))
		if err != nil {
			t.Fatalf("gotooltest: create zip: %v", err)
		}
		if err := modzip.CreateFromDir(zf, mv, src); err != nil {
			_ = zf.Close()
			t.Fatalf("gotooltest: zip %s@%s: %v", m.Path, m.Version, err)
		}
		if err := zf.Close(); err != nil {
			t.Fatalf("gotooltest: close zip: %v", err)
		}

		// @v/list must accumulate across versions of the same module.
		listPath := filepath.Join(dir, "list")
		f, err := os.OpenFile(listPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			t.Fatalf("gotooltest: open list: %v", err)
		}
		if _, err := f.WriteString(m.Version + "\n"); err != nil {
			_ = f.Close()
			t.Fatalf("gotooltest: write list: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("gotooltest: close list: %v", err)
		}
	}
	return root
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("gotooltest: write %s: %v", path, err)
	}
}

// Env returns KEY=VALUE settings that confine `go` to proxyDir: no network, no
// checksum database, and a scratch module cache that dies with the test.
// Suitable for gotool.Client.WithEnv.
func Env(t *testing.T, proxyDir string) []string {
	t.Helper()
	return []string{
		"GOPROXY=" + proxyURL(proxyDir),
		// Empty so a developer's GOPRIVATE/GONOPROXY cannot route a test
		// module around the file proxy and out to the network.
		"GONOPROXY=",
		"GONOSUMDB=",
		"GOPRIVATE=",
		"GOSUMDB=off",
		// Never download a toolchain mid-test; the go on PATH is the one
		// under test.
		"GOTOOLCHAIN=local",
		"GOMODCACHE=" + modCache(t),
	}
}

// modCache returns a throwaway module cache directory.
//
// The go command extracts modules read-only, so t.TempDir's own cleanup fails
// with "permission denied". Registering after TempDir means this runs first
// (cleanups are LIFO) and restores write permission so the removal succeeds.
func modCache(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			mode := os.FileMode(0o644)
			if d.IsDir() {
				mode = 0o755
			}
			_ = os.Chmod(path, mode)
			return nil
		})
	})
	return dir
}

// proxyURL converts a directory to a file:// URL the go command accepts,
// including on Windows where the path needs a leading slash.
func proxyURL(dir string) string {
	p := filepath.ToSlash(dir)
	if len(p) > 0 && p[0] != '/' {
		p = "/" + p
	}
	return "file://" + p
}
