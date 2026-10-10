package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/virtru/wgo/internal/dash"
)

func TestDashRequiresJSON(t *testing.T) {
	old := dashJSON
	t.Cleanup(func() { dashJSON = old })
	dashJSON = false
	err := runDash(context.Background(), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "wgo dash --json") {
		t.Fatalf("want an error suggesting --json, got %v", err)
	}
}

// TestDashFetchersAbsentIntegrations: with no GitHub credentials, no gh and no
// acli, --refresh has nothing to fetch with, so it skips those lookups instead
// of failing every run.
func TestDashFetchersAbsentIntegrations(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	f := dashFetchers()
	if f.PR != nil || f.Issue != nil || f.Jira != nil {
		t.Fatalf("fetchers without any integration: %+v", f)
	}
	want := map[dash.JobKind]string{
		dash.JobPR:    "no GitHub token or gh",
		dash.JobIssue: "no GitHub token or gh",
		dash.JobJira:  "acli not on PATH",
	}
	for k, why := range want {
		if f.Missing[k] != why {
			t.Fatalf("Missing[%s] = %q, want %q", k, f.Missing[k], why)
		}
	}
}
