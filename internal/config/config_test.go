package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCacheTTLDefaults verifies the on-disk cache TTL defaults, including the
// WGO-134 jira_ttl (10m).
func TestCacheTTLDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, Init())

	cfg := Get()
	require.NotNil(t, cfg)
	assert.Equal(t, 120, cfg.Cache.PRTTL)
	assert.Equal(t, 600, cfg.Cache.JiraTTL)
}

// TestSyncDefaults verifies the [sync] section defaults.
func TestSyncDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, Init())

	cfg := Get()
	require.NotNil(t, cfg)
	assert.False(t, cfg.Sync.CreatePRs)
	assert.Equal(t, "auto", cfg.Sync.GHStack)
	assert.Equal(t, "auto", cfg.Sync.GHStackMode())
}

// TestRigDefaults verifies the [rig] section defaults, including that rig.dir
// is tilde-expanded like the other path settings — discovery compares it
// against absolute paths, so a literal "~" would silently exclude nothing.
func TestRigDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, Init())

	cfg := Get()
	require.NotNil(t, cfg)
	assert.Equal(t, filepath.Join(home, "Documents", "GitHub", "rigs"), cfg.Rig.Dir)
	assert.Empty(t, cfg.Rig.OrgPrefixes)
	assert.True(t, cfg.Rig.Sparse)
	assert.True(t, cfg.Rig.VerifyOnNew)
	assert.False(t, cfg.Rig.Freeze)
}

func TestGHStackModeDefault(t *testing.T) {
	assert.Equal(t, "auto", SyncConfig{}.GHStackMode(), "empty defaults to auto")
	assert.Equal(t, "off", SyncConfig{GHStack: "off"}.GHStackMode())
	assert.Equal(t, "on", SyncConfig{GHStack: "on"}.GHStackMode())
}

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

// writeConfig points HOME at a fresh directory holding toml as
// ~/.wgo/config.toml, and captures config warnings.
func writeConfig(t *testing.T, toml string) (warnings *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".wgo"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".wgo", "config.toml"), []byte(toml), 0o644))
	return captureWarnings(t)
}

func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	oldOut, oldWarned := warnOut, warned
	warnOut, warned = &buf, map[string]bool{}
	t.Cleanup(func() { warnOut, warned = oldOut, oldWarned })
	return &buf
}

// TestDashConfig verifies the optional [dash] section is read, and that the
// generated default config parses cleanly with [dash] unset.
func TestDashConfig(t *testing.T) {
	writeConfig(t, `[dash]
port = 8799
terminal = "command"
terminal_command = ["wezterm", "start", "--cwd", "{workspace}"]
resume = "claude"
editor = "~/bin/myedit"
refresh_seconds = 45
days = 21
`)
	require.NoError(t, Init())
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	assert.Equal(t, DashConfig{
		Port:            8799,
		Terminal:        "command",
		TerminalCommand: []string{"wezterm", "start", "--cwd", "{workspace}"},
		Resume:          "claude",
		Editor:          filepath.Join(home, "bin", "myedit"),
		RefreshSeconds:  45,
		Days:            21,
	}, Get().Dash)

	// A fresh HOME: Init writes the template and reads it back, so no earlier
	// read may leak in and the template itself must decode without warnings.
	t.Setenv("HOME", t.TempDir())
	warnings := captureWarnings(t)
	require.NoError(t, Init())
	assert.Equal(t, DashConfig{}, Get().Dash, "the default config leaves [dash] unset")
	assert.Empty(t, warnings.String(), "the default config has no unknown keys")
}

// TestInitRejectsMistypedValues verifies values are decoded strictly: a wrong
// type or a fractional integer is an error naming the file and the key, and
// Get falls back to the defaults rather than a partly decoded config.
func TestInitRejectsMistypedValues(t *testing.T) {
	for _, tc := range []struct{ name, toml, want string }{
		{"string for int", "[dash]\nport = \"abc\"\n", "dash.port"},
		{"numeric string for int", "[dash]\nport = \"9000\"\n", "dash.port"},
		{"bool for int", "[dash]\nrefresh_seconds = true\n", "dash.refresh_seconds"},
		{"fractional int", "[dash]\nport = 9000.7\n", "whole number"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeConfig(t, "[cache]\npr_ttl = 5\n"+tc.toml)
			err := Init()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Contains(t, err.Error(), filepath.Join(".wgo", "config.toml"))
			assert.Contains(t, err.Error(), "remove the key")
			assert.NotContains(t, err.Error(), "decoding failed", "the mapstructure preamble is dropped")
			require.NotNil(t, Get())
			assert.Equal(t, 120, Get().Cache.PRTTL, "a failed decode leaves the defaults, not a partial config")
		})
	}
}

// TestInitAcceptsLenientForms verifies the strict decoding still accepts a
// whole-number float and a single string for a list.
func TestInitAcceptsLenientForms(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, "[dash]\nport = 9000.0\n[discovery]\nbase_dirs = \""+home+"\"\n")
	require.NoError(t, Init())
	assert.Equal(t, 9000, Get().Dash.Port)
	assert.Equal(t, []string{home}, Get().Discovery.BaseDirs)
}

// TestInitWarnsOnUnknownKeys verifies a misspelled key or section is
// reported once on stderr and otherwise ignored.
func TestInitWarnsOnUnknownKeys(t *testing.T) {
	warnings := writeConfig(t, "[dash]\nrefresh_second = 10\nport = 9001\n[dashboard]\nport = 1\n")
	require.NoError(t, Init())
	require.NoError(t, Init())
	assert.Equal(t, 9001, Get().Dash.Port)
	out := warnings.String()
	assert.Equal(t, 1, strings.Count(out, `unknown key "dash.refresh_second"`), out)
	assert.Equal(t, 1, strings.Count(out, `unknown key "dashboard"`), out)
}

// TestInitIgnoresRetiredSections verifies the [hooks] section that older
// default templates wrote is ignored without a warning.
func TestInitIgnoresRetiredSections(t *testing.T) {
	warnings := writeConfig(t, "[hooks]\nenabled = true\nauto_plan = true\nexclude_branches = [\"main\"]\n")
	require.NoError(t, Init())
	assert.Empty(t, warnings.String())
}

// TestInitReportsBadTOML verifies a syntax error names the file and leaves
// the defaults in place.
func TestInitReportsBadTOML(t *testing.T) {
	writeConfig(t, "[dash\nport = 1\n")
	err := Init()
	require.Error(t, err)
	assert.Contains(t, err.Error(), filepath.Join(".wgo", "config.toml"))
	assert.Contains(t, err.Error(), "fix the TOML syntax")
	assert.Equal(t, 120, Get().Cache.PRTTL)
}
