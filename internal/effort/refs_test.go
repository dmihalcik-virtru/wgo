package effort

import (
	"strings"
	"testing"
)

func TestNormalizeBookmarkRef(t *testing.T) {
	ref := NormalizeBookmarkRef("/path/to/repo", "feature-branch")
	expected := "/path/to/repo:feature-branch"

	if ref != expected {
		t.Errorf("expected %s, got %s", expected, ref)
	}
}

func TestDisplayRef(t *testing.T) {
	clones := []MainCloneInfo{
		{Path: "/m/a/wgo", Name: "wgo", Owner: "a", Repo: "wgo"},
		{Path: "/m/b/wgo", Name: "wgo", Owner: "b", Repo: "wgo"},
		{Path: "/m/a/api", Name: "api", Owner: "a", Repo: "api"},
		{Path: "/x/api", Name: "api", Owner: "a", Repo: "api"},
	}
	cases := []struct {
		clone MainCloneInfo
		want  string
	}{
		{clones[0], "a/wgo:feat"},    // name shared, slug unique
		{clones[2], "/m/a/api:feat"}, // name and slug shared
		{MainCloneInfo{Path: "/m/c/cli", Name: "cli", Owner: "c", Repo: "cli"}, "cli:feat"},
	}
	all := append(clones, cases[2].clone)
	for _, tc := range cases {
		got := DisplayRef(tc.clone, "feat", all)
		if got != tc.want {
			t.Errorf("DisplayRef(%s) = %q, want %q", tc.clone.Path, got, tc.want)
		}
		// Every display form must resolve back to the same clone.
		c, bm, err := ResolveRef(got, all)
		if err != nil || c.Path != tc.clone.Path || bm != "feat" {
			t.Errorf("ResolveRef(%q) = %v, %q, %v", got, c, bm, err)
		}
	}
}

func TestResolveRefErrors(t *testing.T) {
	clones := []MainCloneInfo{
		{Path: "/m/a/wgo", Name: "wgo", Owner: "a", Repo: "wgo"},
		{Path: "/m/b/wgo", Name: "wgo", Owner: "b", Repo: "wgo"},
	}
	_, _, err := ResolveRef("wgo:feat", clones)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") ||
		!strings.Contains(err.Error(), "a/wgo (/m/a/wgo)") || !strings.Contains(err.Error(), "b/wgo (/m/b/wgo)") ||
		!strings.Contains(err.Error(), "owner/repo") {
		t.Errorf("ambiguous error should list candidates and the fix, got %v", err)
	}
	_, _, err = ResolveRef("nope:feat", clones)
	if err == nil || !strings.Contains(err.Error(), "discovered repos: wgo") {
		t.Errorf("unknown repo error should list known repos, got %v", err)
	}
	_, _, err = ResolveRef("nope:feat", nil)
	if err == nil || !strings.Contains(err.Error(), "discovery.base_dirs") {
		t.Errorf("no-clones error should point at discovery config, got %v", err)
	}
	if _, _, err = ResolveRef("no-colon", clones); err == nil {
		t.Error("expected an error for a reference without a colon")
	}
	c, bm, err := ResolveRef("B/WGO:feat", clones)
	if err != nil || c.Path != "/m/b/wgo" || bm != "feat" {
		t.Errorf("owner/repo match should be case-insensitive: %v %q %v", c, bm, err)
	}
}
