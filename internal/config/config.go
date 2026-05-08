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
	Hooks     HooksConfig     `mapstructure:"hooks"`
	Pair      PairConfig      `mapstructure:"pair"`
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

// HooksConfig contains git hooks configuration.
type HooksConfig struct {
	Enabled               bool     `mapstructure:"enabled"`
	AutoPlan              bool     `mapstructure:"auto_plan"`
	ExcludeBranches       []string `mapstructure:"exclude_branches"`
	SpecRequired          bool     `mapstructure:"spec_required"`
	SpecRequiredMinLines  int      `mapstructure:"spec_required_min_lines"`
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

	// Default author to git user.email, falling back to user.name
	viper.SetDefault("author", gitConfigAuthor())

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
	viper.SetDefault("hooks.enabled", true)
	viper.SetDefault("hooks.auto_plan", true)
	viper.SetDefault("hooks.exclude_branches", []string{"main", "master", "develop", "release/*"})
	viper.SetDefault("hooks.spec_required", false)
	viper.SetDefault("hooks.spec_required_min_lines", 5)
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

[hooks]
# Enable passive git hook monitoring
enabled = true

# Automatically add new branches to the plan file
auto_plan = true

# Branches to exclude from auto-plan (glob patterns)
exclude_branches = ["main", "master", "develop", "release/*"]

# Block commits on branches without a spec reference (opt-in)
spec_required = false
spec_required_min_lines = 5   # commits touching <= N lines bypass the check

# [pair]
# GitHub handle of your pairing teammate (enables pair features in today, pr, team)
# Use GitHub handles here — spec frontmatter authors: should match these values.
# teammate = "sujankota"
# display_name = "Sujan"
# teammate_jira = "sujan.kotakar"       # optional, defaults to teammate
# teammate_email = "sujan@example.com"  # optional, for git-author filtering in today --pair
`, filepath.Join(home, "Documents", "GitHub"))

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}

	return nil
}

// gitConfigAuthor returns the git user email or name for filtering commits.
func gitConfigAuthor() string {
	if out, err := exec.Command("git", "config", "--global", "user.email").Output(); err == nil {
		if v := strings.TrimSpace(string(out)); v != "" {
			return v
		}
	}
	if out, err := exec.Command("git", "config", "--global", "user.name").Output(); err == nil {
		if v := strings.TrimSpace(string(out)); v != "" {
			return v
		}
	}
	return ""
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
