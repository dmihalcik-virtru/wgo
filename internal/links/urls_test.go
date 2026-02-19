package links

import "testing"

func TestRepoURL(t *testing.T) {
	tests := []struct {
		remote string
		want   string
	}{
		{"git@github.com:virtru/wgo.git", "https://github.com/virtru/wgo"},
		{"https://github.com/virtru/wgo.git", "https://github.com/virtru/wgo"},
		{"https://github.com/virtru/wgo", "https://github.com/virtru/wgo"},
		{"git@github.com:owner/repo.git", "https://github.com/owner/repo"},
		{"https://gitlab.com/owner/repo.git", ""},
		{"not-a-url", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := RepoURL(tt.remote)
		if got != tt.want {
			t.Errorf("RepoURL(%q) = %q, want %q", tt.remote, got, tt.want)
		}
	}
}

func TestBranchURL(t *testing.T) {
	got := BranchURL("git@github.com:virtru/wgo.git", "main")
	want := "https://github.com/virtru/wgo/tree/main"
	if got != want {
		t.Errorf("BranchURL = %q, want %q", got, want)
	}
}

func TestCommitURL(t *testing.T) {
	got := CommitURL("git@github.com:virtru/wgo.git", "abc1234")
	want := "https://github.com/virtru/wgo/commit/abc1234"
	if got != want {
		t.Errorf("CommitURL = %q, want %q", got, want)
	}
}

func TestPRURL(t *testing.T) {
	got := PRURL("https://github.com/virtru/wgo.git", 42)
	want := "https://github.com/virtru/wgo/pull/42"
	if got != want {
		t.Errorf("PRURL = %q, want %q", got, want)
	}
}

func TestIssueURL(t *testing.T) {
	got := IssueURL("https://github.com/virtru/wgo.git", 7)
	want := "https://github.com/virtru/wgo/issues/7"
	if got != want {
		t.Errorf("IssueURL = %q, want %q", got, want)
	}
}

func TestNonGitHub(t *testing.T) {
	if got := BranchURL("git@gitlab.com:o/r.git", "main"); got != "" {
		t.Errorf("expected empty for non-GitHub, got %q", got)
	}
}

func TestHyperlink(t *testing.T) {
	got := Hyperlink("https://example.com", "click")
	want := "\033]8;;https://example.com\033\\click\033]8;;\033\\"
	if got != want {
		t.Errorf("Hyperlink = %q, want %q", got, want)
	}
}

func TestLink(t *testing.T) {
	// TTY mode
	got := Link("https://example.com", "click", true)
	if got == "click" {
		t.Error("expected hyperlink in TTY mode")
	}
	// Non-TTY mode
	got = Link("https://example.com", "click", false)
	if got != "click" {
		t.Errorf("expected plain text in non-TTY mode, got %q", got)
	}
	// Empty URL
	got = Link("", "click", true)
	if got != "click" {
		t.Errorf("expected plain text for empty URL, got %q", got)
	}
}
