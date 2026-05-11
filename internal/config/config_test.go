package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveProject(t *testing.T) {
	rules := []JiraProjectRule{
		{Repo: "virtru/*", Project: "DSPX", Type: "Story"},
		{Repo: "virtru/monorepo", Path: "packages/billing", Project: "BILL"},
		{Path: "packages/auth", Project: "AUTH"},
	}
	cfg := &JiraConfig{
		DefaultProject: "DEFAULT",
		DefaultType:    "Task",
		ProjectRules:   rules,
	}

	tests := []struct {
		name      string
		ownerRepo string
		cwd       string
		wantProj  string
		wantType  string
	}{
		{
			name:      "repo glob matches virtru/wgo",
			ownerRepo: "virtru/wgo",
			cwd:       "/home/user/work",
			wantProj:  "DSPX",
			wantType:  "Story",
		},
		{
			name:      "repo glob matches virtru/platform",
			ownerRepo: "virtru/platform",
			cwd:       "/home/user/work",
			wantProj:  "DSPX",
			wantType:  "Story",
		},
		{
			name:      "repo glob does not match personal/hobby",
			ownerRepo: "personal/hobby",
			cwd:       "/home/user/work",
			wantProj:  "DEFAULT",
			wantType:  "Task",
		},
		{
			name:      "path match for auth package",
			ownerRepo: "personal/hobby",
			cwd:       "/home/user/work/packages/auth/service",
			wantProj:  "AUTH",
			wantType:  "Task", // no type in rule, falls back to DefaultType
		},
		{
			name:      "union: second rule fires on path when repo does not match",
			ownerRepo: "other/org",
			cwd:       "/home/user/work/packages/billing/api",
			wantProj:  "BILL",
			wantType:  "Task",
		},
		{
			name:      "union: second rule fires on repo even when path does not match",
			ownerRepo: "virtru/monorepo",
			cwd:       "/home/user/work/packages/unrelated",
			wantProj:  "DSPX", // first rule matches repo glob "virtru/*" first
			wantType:  "Story",
		},
		{
			name:      "first rule wins over later matches",
			ownerRepo: "virtru/monorepo",
			cwd:       "/home/user/work/packages/billing",
			wantProj:  "DSPX", // virtru/* matches before the billing rule
			wantType:  "Story",
		},
		{
			name:      "no match returns defaults",
			ownerRepo: "",
			cwd:       "/home/user/unrelated",
			wantProj:  "DEFAULT",
			wantType:  "Task",
		},
		{
			name:      "empty ownerRepo only path rules can match",
			ownerRepo: "",
			cwd:       "/home/user/packages/auth/cmd",
			wantProj:  "AUTH",
			wantType:  "Task",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proj, typ := cfg.ResolveProject(tt.ownerRepo, tt.cwd)
			assert.Equal(t, tt.wantProj, proj)
			assert.Equal(t, tt.wantType, typ)
		})
	}
}

func TestResolveProject_NoRules(t *testing.T) {
	cfg := &JiraConfig{DefaultProject: "FALLBACK", DefaultType: "Bug"}
	proj, typ := cfg.ResolveProject("any/repo", "/any/path")
	assert.Equal(t, "FALLBACK", proj)
	assert.Equal(t, "Bug", typ)
}

func TestResolveProject_InvalidGlob(t *testing.T) {
	cfg := &JiraConfig{
		DefaultProject: "FALLBACK",
		DefaultType:    "Task",
		ProjectRules: []JiraProjectRule{
			{Repo: "[invalid", Project: "NEVER"}, // malformed glob
			{Repo: "good/*", Project: "GOOD"},
		},
	}
	// Bad pattern is skipped, next rule still evaluated.
	proj, _ := cfg.ResolveProject("good/repo", "/cwd")
	assert.Equal(t, "GOOD", proj)
}

func TestResolveProject_RuleWithNoFields(t *testing.T) {
	cfg := &JiraConfig{
		DefaultProject: "FALLBACK",
		DefaultType:    "Task",
		ProjectRules: []JiraProjectRule{
			{Project: ""}, // no repo, no path, no project — should be skipped
			{Repo: "x/*", Project: "X"},
		},
	}
	proj, _ := cfg.ResolveProject("x/repo", "/cwd")
	assert.Equal(t, "X", proj)
}
