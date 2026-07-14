// Package config provides configuration management for wgo.
package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

// Config represents the wgo configuration.
type Config struct {
	Author    string          `mapstructure:"author"`
	Discovery DiscoveryConfig `mapstructure:"discovery"`
	Worktree  WorktreeConfig  `mapstructure:"worktree"`
	UI        UIConfig        `mapstructure:"ui"`
	Status    StatusConfig    `mapstructure:"status"`
	Doctor    DoctorConfig    `mapstructure:"doctor"`
	Pair      PairConfig      `mapstructure:"pair"`
	Jira      JiraConfig      `mapstructure:"jira"`
	Cache     CacheConfig     `mapstructure:"cache"`
}

// CacheConfig controls the cross-invocation on-disk caches under ~/.wgo/cache.
type CacheConfig struct {
	// PRTTL is how long (in seconds) a cached PR entry is considered fresh
	// before wgo statusline triggers a background refresh.
	PRTTL int `mapstructure:"pr_ttl"`
}

// JiraProjectRule maps a repo glob and/or CWD path substring to a Jira project.
// A rule matches when its repo glob matches owner/repo OR its path substring
// appears in the current working directory (either condition is sufficient).
type JiraProjectRule struct {
	Repo    string `mapstructure:"repo"` // filepath.Match glob vs "owner/repo"
	Path    string `mapstructure:"path"` // strings.Contains vs CWD
	Project string `mapstructure:"project"`
	Type    string `mapstructure:"type"` // optional issue-type override
}

// JiraConfig holds optional Jira integration settings.
// acli handles authentication; these fields are informational only except where noted.
type JiraConfig struct {
	Project         string            `mapstructure:"project"`          // informational project key; also board/backlog URL segment
	DefaultProject  string            `mapstructure:"default_project"`  // used by: wgo add --jira
	DefaultType     string            `mapstructure:"default_type"`     // used by: wgo add --jira (e.g. "Task")
	ProjectRules    []JiraProjectRule `mapstructure:"project_rules"`    // ordered; first match wins
	Board           int               `mapstructure:"board"`            // default board id for: wgo standup
	Site            string            `mapstructure:"site"`             // Jira host; auto-detected from acli when empty
	StandupStatuses []string          `mapstructure:"standup_statuses"` // in-flight statuses for: wgo standup
}

// StandupStatusesOrDefault returns the configured in-flight statuses, or the
// default set (In Progress / In Review / In QA) when none are configured.
func (c *JiraConfig) StandupStatusesOrDefault() []string {
	if len(c.StandupStatuses) > 0 {
		return c.StandupStatuses
	}
	return []string{"In Progress", "In Review", "In QA"}
}

// ResolveProject returns the Jira project key and issue type for the given repo
// and working directory. Rules are evaluated in declaration order; a rule matches
// when its repo glob matches ownerRepo OR its path substring appears in cwd.
// Falls back to DefaultProject / DefaultType when no rule matches.
func (c *JiraConfig) ResolveProject(ownerRepo, cwd string) (project, issueType string) {
	for _, rule := range c.ProjectRules {
		if rule.Project == "" {
			continue
		}
		repoMatch := rule.Repo != "" && globMatch(rule.Repo, ownerRepo)
		pathMatch := rule.Path != "" && strings.Contains(cwd, rule.Path)
		if repoMatch || pathMatch {
			t := rule.Type
			if t == "" {
				t = c.DefaultType
			}
			return rule.Project, t
		}
	}
	return c.DefaultProject, c.DefaultType
}

// globMatch wraps filepath.Match, treating pattern errors as non-matches with a warning.
func globMatch(pattern, s string) bool {
	matched, err := filepath.Match(pattern, s)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: jira project_rule has invalid repo pattern %q: %v\n", pattern, err)
		return false
	}
	return matched
}

// PairConfig holds configuration for a single pairing teammate.
// teammate should be the teammate's GitHub handle; it is used both for
// gh API calls and for matching spec frontmatter authors:.
type PairConfig struct {
	Teammate      string `mapstructure:"teammate"`
	TeammateJira  string `mapstructure:"teammate_jira"`
	DisplayName   string `mapstructure:"display_name"`
	TeammateEmail string `mapstructure:"teammate_email"`
}

// HasPair reports whether a teammate is configured.
func (c *Config) HasPair() bool { return c.Pair.Teammate != "" }

// PairDisplayName returns the display name for the teammate, falling back to
// the GitHub handle if display_name is not set.
func (c *Config) PairDisplayName() string {
	if c.Pair.DisplayName != "" {
		return c.Pair.DisplayName
	}
	return c.Pair.Teammate
}

// DoctorConfig contains settings for the active `wgo doctor` check that
// replaced the previous passive git-hook spec enforcement.
type DoctorConfig struct {
	// ExcludeBookmarks names bookmarks (glob patterns) that are exempt from
	// spec enforcement, typically default branches.
	ExcludeBookmarks []string `mapstructure:"exclude_bookmarks"`
	// SpecRequired causes `wgo doctor` to warn (or fail with --strict) on
	// workspaces whose current bookmark has no recorded spec file.
	SpecRequired bool `mapstructure:"spec_required"`
}

// StatusConfig contains status dashboard configuration.
type StatusConfig struct {
	DefaultSort     string `mapstructure:"default_sort"`
	StaleDays       int    `mapstructure:"stale_days"`
	RefreshInterval int    `mapstructure:"refresh_interval"`
	ShowSpecColumn  bool   `mapstructure:"show_spec_column"`
}

// DiscoveryConfig contains directory discovery configuration.
type DiscoveryConfig struct {
	BaseDirs        []string `mapstructure:"base_dirs"`
	ScanDepth       int      `mapstructure:"scan_depth"`
	ExcludePatterns []string `mapstructure:"exclude_patterns"`
}

// WorktreeConfig contains paths for clone and worktree roots.
type WorktreeConfig struct {
	MainsDir     string `mapstructure:"mains_dir"`
	WorktreesDir string `mapstructure:"worktrees_dir"`
}

// UIConfig contains UI-related configuration.
type UIConfig struct {
	Icons     bool `mapstructure:"icons"`
	TildeHome bool `mapstructure:"tilde_home"`
}

var (
	configFile string
	cfg        *Config
)

// Init initializes the configuration.
func Init() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	wgoDir := filepath.Join(home, ".wgo")
	configFile = filepath.Join(wgoDir, "config.toml")

	// Ensure directory exists
	if err := os.MkdirAll(wgoDir, 0o755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Set defaults
	setDefaults()

	// Read config if it exists
	if _, err := os.Stat(configFile); err == nil {
		viper.SetConfigFile(configFile)
		viper.SetConfigType("toml")

		if err := viper.ReadInConfig(); err != nil {
			return fmt.Errorf("failed to read config file: %w", err)
		}
	} else {
		// Create default config file
		if err := createDefaultConfig(configFile); err != nil {
			return fmt.Errorf("failed to create default config: %w", err)
		}
	}

	cfg = &Config{}
	if err := viper.Unmarshal(cfg); err != nil {
		return fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Expand tilde in paths
	cfg.Discovery.BaseDirs = expandPaths(cfg.Discovery.BaseDirs)
	cfg.Worktree.MainsDir = expandPath(cfg.Worktree.MainsDir)
	cfg.Worktree.WorktreesDir = expandPath(cfg.Worktree.WorktreesDir)

	return nil
}

// setDefaults sets default configuration values.
func setDefaults() {
	home, _ := os.UserHomeDir()

	// Default author to jj user.email, falling back to user.name or env vars.
	viper.SetDefault("author", defaultAuthor())

	viper.SetDefault("discovery.base_dirs", []string{filepath.Join(home, "Documents", "GitHub")})
	viper.SetDefault("worktree.mains_dir", filepath.Join(home, "Documents", "GitHub", "mains"))
	viper.SetDefault("worktree.worktrees_dir", filepath.Join(home, "Documents", "GitHub", "worktrees"))
	viper.SetDefault("discovery.scan_depth", 4)
	viper.SetDefault("discovery.exclude_patterns", []string{
		"node_modules", ".cache", "vendor", "dist",
	})
	viper.SetDefault("ui.icons", false)
	viper.SetDefault("ui.tilde_home", true)
	viper.SetDefault("status.default_sort", "activity")
	viper.SetDefault("status.stale_days", 14)
	viper.SetDefault("status.refresh_interval", 5)
	viper.SetDefault("status.show_spec_column", true)
	viper.SetDefault("doctor.exclude_bookmarks", []string{"main", "master", "develop", "release/*"})
	viper.SetDefault("doctor.spec_required", false)
	viper.SetDefault("cache.pr_ttl", 120)
}

// createDefaultConfig creates a default config file.
func createDefaultConfig(path string) error {
	home, _ := os.UserHomeDir()

	content := fmt.Sprintf(`# wgo configuration

[discovery]
# Base directories to scan for repositories
base_dirs = ["%s"]

# Maximum depth to scan (0 = unlimited)
scan_depth = 4

# Patterns to exclude from discovery
exclude_patterns = ["node_modules", ".cache", "vendor", "dist"]

[worktree]
# Where to clone main branches (owner/repo layout)
mains_dir = "~/Documents/GitHub/mains"

# Where to create feature worktrees (branch/repo layout)
worktrees_dir = "~/Documents/GitHub/worktrees"

[ui]
# Display icons in output
icons = false

# Display home directory as ~ in output
tilde_home = true

[doctor]
# Bookmarks (glob patterns) exempt from spec enforcement, typically default branches.
exclude_bookmarks = ["main", "master", "develop", "release/*"]

# When true, "wgo doctor" warns (or fails with --strict) on workspaces whose
# current bookmark has no recorded spec file. Opt-in.
spec_required = false

[cache]
# How long (seconds) a cached PR entry stays fresh before "wgo statusline"
# triggers a background refresh. Used by the ~/.wgo/cache/pr on-disk cache.
pr_ttl = 120

# [pair]
# GitHub handle of your pairing teammate (enables pair features in today, pr, team)
# Use GitHub handles here — spec frontmatter authors: should match these values.
# teammate = "sujankota"
# display_name = "Sujan"
# teammate_jira = "sujan.kotakar"       # optional, defaults to teammate
# teammate_email = "sujan@example.com"  # optional, for git-author filtering in today --pair

# [jira]
# project         = "WGO"    # informational project key; also board/backlog URL segment
# default_project = "WGO"    # fallback project used by: wgo add --jira
# default_type    = "Task"   # fallback issue type used by: wgo add --jira
# board           = 305      # default board id used by: wgo standup (override with --board)
# site            = "your-org.atlassian.net"  # optional; auto-detected from acli when omitted
# standup_statuses = ["In Progress", "In Review", "In QA"]  # in-flight states for: wgo standup
#
# Per-repo/path rules — first match wins; either field is sufficient
# [[jira.project_rules]]
# repo    = "myorg/*"          # filepath.Match glob against "owner/repo"
# project = "PROJ"
# type    = "Story"            # optional; overrides default_type
#
# [[jira.project_rules]]
# repo    = "myorg/monorepo"   # repo OR path — either triggers the rule
# path    = "packages/billing" # substring of current working directory
# project = "BILL"
`, filepath.Join(home, "Documents", "GitHub"))

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}

	return nil
}

// defaultAuthor returns the user's email (preferred) or display name from jj
// config for filtering commits. Falls back to the JJ_EMAIL / JJ_USER env vars
// so containers and CI without `jj config get` still pick up identity.
func defaultAuthor() string {
	if v := jjConfigValue("user.email"); v != "" {
		return v
	}
	if v := jjConfigValue("user.name"); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("JJ_EMAIL")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("JJ_USER")); v != "" {
		return v
	}
	return ""
}

// jjConfigValue returns the value of a jj config key, stripping any
// surrounding quotes that `jj config get` may add.
func jjConfigValue(key string) string {
	out, err := exec.Command("jj", "config", "get", key).Output()
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(out))
	v = strings.Trim(v, `"`)
	return v
}

// expandPath expands a leading ~ to the user's home directory.
func expandPath(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[1:])
	}
	return path
}

// expandPaths expands ~ in a slice of paths.
func expandPaths(paths []string) []string {
	expanded := make([]string, len(paths))
	for i, p := range paths {
		expanded[i] = expandPath(p)
	}
	return expanded
}

// Get returns the current configuration.
func Get() *Config {
	return cfg
}
