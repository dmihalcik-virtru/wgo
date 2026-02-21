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
	UI        UIConfig        `mapstructure:"ui"`
	Status    StatusConfig    `mapstructure:"status"`
	Hooks     HooksConfig     `mapstructure:"hooks"`
}

// HooksConfig contains git hooks configuration.
type HooksConfig struct {
	Enabled         bool     `mapstructure:"enabled"`
	AutoPlan        bool     `mapstructure:"auto_plan"`
	ExcludeBranches []string `mapstructure:"exclude_branches"`
}

// StatusConfig contains status dashboard configuration.
type StatusConfig struct {
	DefaultSort     string `mapstructure:"default_sort"`
	StaleDays       int    `mapstructure:"stale_days"`
	RefreshInterval int    `mapstructure:"refresh_interval"`
}

// DiscoveryConfig contains directory discovery configuration.
type DiscoveryConfig struct {
	BaseDirs         []string `mapstructure:"base_dirs"`
	ScanDepth        int      `mapstructure:"scan_depth"`
	ExcludePatterns  []string `mapstructure:"exclude_patterns"`
}

// UIConfig contains UI-related configuration.
type UIConfig struct {
	Icons    bool `mapstructure:"icons"`
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

	return nil
}

// setDefaults sets default configuration values.
func setDefaults() {
	home, _ := os.UserHomeDir()

	// Default author to git user.email, falling back to user.name
	viper.SetDefault("author", gitConfigAuthor())

	viper.SetDefault("discovery.base_dirs", []string{filepath.Join(home, "Documents", "GitHub")})
	viper.SetDefault("discovery.scan_depth", 4)
	viper.SetDefault("discovery.exclude_patterns", []string{
		"node_modules", ".cache", "vendor", "dist",
	})
	viper.SetDefault("ui.icons", false)
	viper.SetDefault("ui.tilde_home", true)
	viper.SetDefault("status.default_sort", "activity")
	viper.SetDefault("status.stale_days", 14)
	viper.SetDefault("status.refresh_interval", 5)
	viper.SetDefault("hooks.enabled", true)
	viper.SetDefault("hooks.auto_plan", true)
	viper.SetDefault("hooks.exclude_branches", []string{"main", "master", "develop", "release/*"})
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

// expandPaths expands ~ in paths.
func expandPaths(paths []string) []string {
	home, _ := os.UserHomeDir()
	expanded := make([]string, len(paths))

	for i, path := range paths {
		if strings.HasPrefix(path, "~") {
			expanded[i] = filepath.Join(home, path[1:])
		} else {
			expanded[i] = path
		}
	}

	return expanded
}

// Get returns the current configuration.
func Get() *Config {
	return cfg
}
