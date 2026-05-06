package cmd

import (
	"testing"

	"github.com/virtru/wgo/internal/config"
)

func TestIsJiraTicket(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"DSPX-2674", true},
		{"A-1", true},
		{"FOO-123", true},
		{"dspx-2674", false},
		{"DSPX-", false},
		{"2674", false},
		{"DSPX2674", false},
		{"", false},
		{"DSPX-abc", false},
		{"dspx-ABC", false},
	}
	for _, tt := range tests {
		got := isJiraTicket(tt.input)
		if got != tt.want {
			t.Errorf("isJiraTicket(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestSlugTicketBranch(t *testing.T) {
	tests := []struct {
		ticket string
		desc   string
		want   string
	}{
		{"DSPX-123", "", "DSPX-123"},
		{"DSPX-123", "remove volume directive", "DSPX-123-remove-volume-directive"},
		{"DSPX-123", "fix the login bug", "DSPX-123-fix-the-login-bug"},
		// result must never end in a dash, capped at 60 chars
		{"DSPX-123", "a very long description that will be truncated at the sixty character limit", "DSPX-123-a-very-long-description-that-will-be-truncated-at-t"},
		// special characters are sanitized
		{"DSPX-1", "hello world!", "DSPX-1-hello-world"},
	}
	for _, tt := range tests {
		got := slugTicketBranch(tt.ticket, tt.desc)
		if got != tt.want {
			t.Errorf("slugTicketBranch(%q, %q) = %q, want %q", tt.ticket, tt.desc, got, tt.want)
		}
		if len(got) > 0 && got[len(got)-1] == '-' {
			t.Errorf("slugTicketBranch(%q, %q) = %q ends in dash", tt.ticket, tt.desc, got)
		}
	}
}

func TestTruncateSlug(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 30, "short"},
		{"exactly-ten", 11, "exactly-ten"},
		{"DSPX-123-remove-volume-directive", 20, "DSPX-123-remove"},
		// truncates at last dash boundary
		{"abc-def-ghi", 8, "abc-def"},
		// no dash in range: raw truncation
		{"abcdefghij", 5, "abcde"},
		// trailing dash trimmed after raw truncation
		{"abc-defgh", 4, "abc"},
	}
	for _, tt := range tests {
		got := truncateSlug(tt.input, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncateSlug(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
		}
	}
}

func TestParseSpecRepoFlag(t *testing.T) {
	specs := []repoSpec{
		{owner: "virtru", repo: "wgo"},
		{owner: "virtru", repo: "api"},
	}

	spec, err := parseSpecRepoFlag("virtru/api", specs)
	if err != nil {
		t.Fatalf("parseSpecRepoFlag failed: %v", err)
	}
	if spec.String() != "virtru/api" {
		t.Fatalf("expected virtru/api, got %s", spec.String())
	}

	if _, err := parseSpecRepoFlag("virtru/other", specs); err == nil {
		t.Fatalf("expected invalid spec repo to fail")
	}
}

func TestSpecAuthorsIncludesPair(t *testing.T) {
	cfg := &config.Config{
		Author: "dmihalcik",
		Pair: config.PairConfig{
			Teammate: "sujan",
		},
	}

	authors := specAuthors(cfg)
	if len(authors) != 2 {
		t.Fatalf("expected 2 authors, got %d", len(authors))
	}
	if authors[0] != "dmihalcik" || authors[1] != "sujan" {
		t.Fatalf("unexpected authors: %v", authors)
	}
}
