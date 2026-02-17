package github

import (
	"testing"
)

func TestParseGitHubURL(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		wantOwner  string
		wantRepo   string
		wantType   URLType
		wantIdent  string
		wantErr    bool
	}{
		{
			name:      "PR URL",
			url:       "https://github.com/virtru/wgo/pull/42",
			wantOwner: "virtru",
			wantRepo:  "wgo",
			wantType:  URLTypePR,
			wantIdent: "42",
		},
		{
			name:      "branch URL",
			url:       "https://github.com/virtru/wgo/tree/main",
			wantOwner: "virtru",
			wantRepo:  "wgo",
			wantType:  URLTypeBranch,
			wantIdent: "main",
		},
		{
			name:      "branch with slashes",
			url:       "https://github.com/virtru/wgo/tree/feature/auth/oauth",
			wantOwner: "virtru",
			wantRepo:  "wgo",
			wantType:  URLTypeBranch,
			wantIdent: "feature/auth/oauth",
		},
		{
			name:      "issue URL",
			url:       "https://github.com/virtru/wgo/issues/123",
			wantOwner: "virtru",
			wantRepo:  "wgo",
			wantType:  URLTypeIssue,
			wantIdent: "123",
		},
		{
			name:      "repo-only URL",
			url:       "https://github.com/virtru/wgo",
			wantOwner: "virtru",
			wantRepo:  "wgo",
			wantType:  URLTypeBranch,
			wantIdent: "",
		},
		{
			name:      "repo URL with .git suffix",
			url:       "https://github.com/virtru/wgo.git",
			wantOwner: "virtru",
			wantRepo:  "wgo",
			wantType:  URLTypeBranch,
			wantIdent: "",
		},
		{
			name:      "www.github.com",
			url:       "https://www.github.com/virtru/wgo/pull/1",
			wantOwner: "virtru",
			wantRepo:  "wgo",
			wantType:  URLTypePR,
			wantIdent: "1",
		},
		{
			name:    "not GitHub",
			url:     "https://gitlab.com/virtru/wgo",
			wantErr: true,
		},
		{
			name:    "missing repo",
			url:     "https://github.com/virtru",
			wantErr: true,
		},
		{
			name:    "invalid PR number",
			url:     "https://github.com/virtru/wgo/pull/abc",
			wantErr: true,
		},
		{
			name:    "invalid issue number",
			url:     "https://github.com/virtru/wgo/issues/abc",
			wantErr: true,
		},
		{
			name:    "unsupported URL type",
			url:     "https://github.com/virtru/wgo/actions",
			wantErr: true,
		},
		{
			name:    "PR URL missing number",
			url:     "https://github.com/virtru/wgo/pull",
			wantErr: true,
		},
		{
			name:    "tree URL missing branch",
			url:     "https://github.com/virtru/wgo/tree",
			wantErr: true,
		},
		{
			name:    "issues URL missing number",
			url:     "https://github.com/virtru/wgo/issues",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseGitHubURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseGitHubURL() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got.Owner != tt.wantOwner {
				t.Errorf("Owner = %q, want %q", got.Owner, tt.wantOwner)
			}
			if got.Repo != tt.wantRepo {
				t.Errorf("Repo = %q, want %q", got.Repo, tt.wantRepo)
			}
			if got.Type != tt.wantType {
				t.Errorf("Type = %d, want %d", got.Type, tt.wantType)
			}
			if got.Identifier != tt.wantIdent {
				t.Errorf("Identifier = %q, want %q", got.Identifier, tt.wantIdent)
			}
		})
	}
}

func TestSanitizeBranch(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"main", "main"},
		{"feature/auth", "feature-auth"},
		{"feature/auth/oauth", "feature-auth-oauth"},
		{"fix--double--dash", "fix-double-dash"},
		{"has spaces", "has-spaces"},
		{"special!@#chars", "special-chars"},
		{"-leading-dash", "leading-dash"},
		{"trailing-dash-", "trailing-dash"},
		{"a/very/long/branch/name/that/exceeds/the/sixty/character/limit/by/quite/a/bit", "a-very-long-branch-name-that-exceeds-the-sixty-character-lim"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := SanitizeBranch(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeBranch(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestIssueBranchName(t *testing.T) {
	tests := []struct {
		number int
		title  string
		want   string
	}{
		{123, "Add auth to API", "123-add-auth-to-api"},
		{42, "Fix: login bug!", "42-fix-login-bug"},
		{1, "Simple", "1-simple"},
		{99, "  Trim  spaces  ", "99-trim-spaces"},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			got := IssueBranchName(tt.number, tt.title)
			if got != tt.want {
				t.Errorf("IssueBranchName(%d, %q) = %q, want %q", tt.number, tt.title, got, tt.want)
			}
		})
	}
}

func TestRepoCloneURL(t *testing.T) {
	got := RepoCloneURL("virtru", "wgo")
	want := "https://github.com/virtru/wgo.git"
	if got != want {
		t.Errorf("RepoCloneURL() = %q, want %q", got, want)
	}
}
