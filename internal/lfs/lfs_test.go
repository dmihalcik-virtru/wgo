package lfs_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtru/wgo/internal/lfs"
)

func TestParsePointer(t *testing.T) {
	valid := "version https://git-lfs.github.com/spec/v1\n" +
		"oid sha256:8abc1683484d2cacc1c5d0fb1d0e36d888b950ecec73c4ec15836d0ba9c07607\n" +
		"size 12345\n"

	cases := []struct {
		name string
		data string
		want lfs.Pointer
		ok   bool
	}{
		{
			name: "valid pointer",
			data: valid,
			want: lfs.Pointer{OID: "sha256:8abc1683484d2cacc1c5d0fb1d0e36d888b950ecec73c4ec15836d0ba9c07607", Size: 12345},
			ok:   true,
		},
		{
			name: "valid pointer with unknown extension line",
			data: valid + "x-custom-key value\n",
			want: lfs.Pointer{OID: "sha256:8abc1683484d2cacc1c5d0fb1d0e36d888b950ecec73c4ec15836d0ba9c07607", Size: 12345},
			ok:   true,
		},
		{
			name: "real content is not a pointer",
			data: "this is a fake binary blob, pretend it's large\n",
		},
		{
			name: "empty file",
			data: "",
		},
		{
			name: "wrong version header",
			data: "version https://git-lfs.github.com/spec/v0\noid sha256:8abc1683484d2cacc1c5d0fb1d0e36d888b950ecec73c4ec15836d0ba9c07607\nsize 1\n",
		},
		{
			name: "missing size",
			data: "version https://git-lfs.github.com/spec/v1\noid sha256:8abc1683484d2cacc1c5d0fb1d0e36d888b950ecec73c4ec15836d0ba9c07607\n",
		},
		{
			name: "missing oid",
			data: "version https://git-lfs.github.com/spec/v1\nsize 12345\n",
		},
		{
			name: "malformed oid (too short)",
			data: "version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 12345\n",
		},
		{
			name: "oversized file",
			data: valid + strings.Repeat("x", lfs.MaxPointerSize),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := lfs.ParsePointer([]byte(tc.data))
			if ok != tc.ok {
				t.Fatalf("ParsePointer() ok = %v, want %v", ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Fatalf("ParsePointer() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestObjectPath(t *testing.T) {
	oid := "8abc1683484d2cacc1c5d0fb1d0e36d888b950ecec73c4ec15836d0ba9c07607"
	got := lfs.ObjectPath("/repo/.git/lfs/objects", oid)
	want := filepath.Join("/repo/.git/lfs/objects", "8a", "bc", oid)
	if got != want {
		t.Fatalf("ObjectPath() = %q, want %q", got, want)
	}
}

func requireGitLFS(t *testing.T) {
	t.Helper()
	if !lfs.Available() {
		t.Skip("git or git-lfs not found on PATH; skipping LFS integration test")
	}
}

// runOK runs name with args in dir, failing the test on non-zero exit.
func runOK(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=wgo-test", "GIT_AUTHOR_EMAIL=wgo-test@example.com",
		"GIT_COMMITTER_NAME=wgo-test", "GIT_COMMITTER_EMAIL=wgo-test@example.com",
		"JJ_USER=wgo-test", "JJ_EMAIL=wgo-test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v (in %s): %v\n%s", name, args, dir, err, out)
	}
	return string(out)
}

// seedLFSRemote creates a bare git remote seeded (via plain git, not jj)
// with one LFS-tracked file on main, returning the remote path and the
// file's real content.
func seedLFSRemote(t *testing.T, root string) (remote, content string) {
	t.Helper()
	remote = filepath.Join(root, "remote.git")
	runOK(t, "", "git", "init", "--bare", "-q", remote)

	seed := filepath.Join(root, "seed")
	runOK(t, "", "git", "init", "-q", seed)
	runOK(t, seed, "git", "lfs", "install", "--local")
	runOK(t, seed, "git", "lfs", "track", "*.bin")
	content = "real lfs content for the wgo integration test\n"
	if err := os.WriteFile(filepath.Join(seed, "asset.bin"), []byte(content), 0o644); err != nil {
		t.Fatalf("write asset.bin: %v", err)
	}
	runOK(t, seed, "git", "add", ".gitattributes", "asset.bin")
	runOK(t, seed, "git", "commit", "-q", "-m", "add lfs asset")
	runOK(t, seed, "git", "remote", "add", "origin", remote)
	runOK(t, seed, "git", "push", "-q", "origin", "HEAD:main")
	return remote, content
}

func TestHydrateWorkspace(t *testing.T) {
	requireGitLFS(t)
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj binary not found on PATH; skipping LFS integration test")
	}
	root := t.TempDir()
	remote, content := seedLFSRemote(t, root)

	mainCheckout := filepath.Join(root, "main-checkout")
	runOK(t, "", "jj", "git", "clone", "--colocate", remote, mainCheckout)
	runOK(t, mainCheckout, "jj", "new", "main@origin")

	assetPath := filepath.Join(mainCheckout, "asset.bin")
	before, err := os.ReadFile(assetPath)
	if err != nil {
		t.Fatalf("read asset.bin: %v", err)
	}
	if _, ok := lfs.ParsePointer(before); !ok {
		t.Fatalf("expected asset.bin to be a raw LFS pointer before hydration, got: %q", before)
	}

	sha := strings.TrimSpace(runOK(t, mainCheckout, "git", "rev-parse", "origin/main"))

	c := lfs.NewClient()
	result, err := c.HydrateWorkspace(mainCheckout, mainCheckout, "origin", sha)
	if err != nil {
		t.Fatalf("HydrateWorkspace: %v", err)
	}
	if len(result.Hydrated) != 1 || result.Hydrated[0] != "asset.bin" {
		t.Fatalf("expected asset.bin to be hydrated, got %+v", result)
	}
	if len(result.Missing) != 0 {
		t.Fatalf("expected no missing objects, got %+v", result.Missing)
	}

	info, err := os.Lstat(assetPath)
	if err != nil {
		t.Fatalf("lstat asset.bin: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected asset.bin to become a symlink")
	}
	got, err := os.ReadFile(assetPath)
	if err != nil {
		t.Fatalf("read hydrated asset.bin: %v", err)
	}
	if string(got) != content {
		t.Fatalf("hydrated content mismatch: got %q want %q", got, content)
	}

	scan, err := lfs.Scan(mainCheckout)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(scan.Pointers) != 0 || len(scan.Hydrated) != 1 {
		t.Fatalf("expected Scan to report 1 hydrated file and 0 pointers, got %+v", scan)
	}

	// Idempotent: a second run finds nothing left to hydrate.
	result2, err := c.HydrateWorkspace(mainCheckout, mainCheckout, "origin", sha)
	if err != nil {
		t.Fatalf("HydrateWorkspace (second run): %v", err)
	}
	if len(result2.Hydrated) != 0 {
		t.Fatalf("expected second run to be a no-op, got %+v", result2)
	}
}
