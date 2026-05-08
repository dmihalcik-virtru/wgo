package links

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
		assert.Equal(t, tt.want, got, "RepoURL(%q)", tt.remote)
	}
}

func TestBranchURL(t *testing.T) {
	got := BranchURL("git@github.com:virtru/wgo.git", "main")
	assert.Equal(t, "https://github.com/virtru/wgo/tree/main", got)
}

func TestCommitURL(t *testing.T) {
	got := CommitURL("git@github.com:virtru/wgo.git", "abc1234")
	assert.Equal(t, "https://github.com/virtru/wgo/commit/abc1234", got)
}

func TestPRURL(t *testing.T) {
	got := PRURL("https://github.com/virtru/wgo.git", 42)
	assert.Equal(t, "https://github.com/virtru/wgo/pull/42", got)
}

func TestIssueURL(t *testing.T) {
	got := IssueURL("https://github.com/virtru/wgo.git", 7)
	assert.Equal(t, "https://github.com/virtru/wgo/issues/7", got)
}

func TestNonGitHub(t *testing.T) {
	got := BranchURL("git@gitlab.com:o/r.git", "main")
	assert.Empty(t, got, "expected empty for non-GitHub")
}

func TestHyperlink(t *testing.T) {
	got := Hyperlink("https://example.com", "click")
	want := "\033]8;;https://example.com\033\\\033[4mclick\033[24m\033]8;;\033\\"
	assert.Equal(t, want, got)
}

func TestLink(t *testing.T) {
	// TTY mode
	got := Link("https://example.com", "click", true)
	assert.NotEqual(t, "click", got, "expected hyperlink in TTY mode")
	// Non-TTY mode
	got = Link("https://example.com", "click", false)
	assert.Equal(t, "click", got, "expected plain text in non-TTY mode")
	// Empty URL
	got = Link("", "click", true)
	assert.Equal(t, "click", got, "expected plain text for empty URL")
}
