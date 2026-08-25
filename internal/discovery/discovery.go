// Package discovery provides filesystem-based repository and workspace discovery.
package discovery

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/virtru/wgo/internal/config"
)

// DiscoveredRepo represents a discovered jj repository or workspace.
type DiscoveredRepo struct {
	Path         string
	Name         string
	IsWorktree   bool   // True for secondary jj workspaces (analogous to git worktrees).
	MainRepoPath string // For secondary workspaces, points to the main workspace.
}

// Discovery discovers repositories and workspaces in configured base directories.
type Discovery struct {
	baseDirs        []string
	scanDepth       int
	excludePatterns []string
	excludeRoots    []string
}

// Option customises a Discovery.
type Option func(*Discovery)

// ExcludeRoots skips any directory at or beneath one of dirs.
//
// This is a whole-subtree exclusion keyed on the path, deliberately distinct
// from excludePatterns, which is a substring match on a single path component:
// excluding "rigs" that way would also hide a legitimate repo named
// "mains/acme/rigsomething".
func ExcludeRoots(dirs ...string) Option {
	return func(d *Discovery) {
		for _, dir := range dirs {
			if dir = strings.TrimSpace(dir); dir != "" {
				d.excludeRoots = append(d.excludeRoots, filepath.Clean(dir))
			}
		}
	}
}

// New creates a new Discovery with the given parameters.
func New(baseDirs []string, scanDepth int, excludePatterns []string, opts ...Option) *Discovery {
	d := &Discovery{
		baseDirs:        baseDirs,
		scanDepth:       scanDepth,
		excludePatterns: excludePatterns,
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// FromConfig builds the Discovery every wgo command should use.
//
// Going through one constructor is what keeps rigs invisible: a rig holds
// many pinned, bookmark-less checkouts of the same repository, so any command
// that walked into rig.dir would report them as a heap of stale, untracked
// worktrees. Call this rather than New so no caller can forget the exclusion.
func FromConfig(cfg *config.Config) *Discovery {
	return New(
		cfg.Discovery.BaseDirs,
		cfg.Discovery.ScanDepth,
		cfg.Discovery.ExcludePatterns,
		ExcludeRoots(cfg.Rig.Dir),
	)
}

// DiscoverAll discovers all repositories and workspaces.
func (d *Discovery) DiscoverAll() ([]DiscoveredRepo, error) {
	var repos []DiscoveredRepo

	for _, baseDir := range d.baseDirs {
		if d.isExcludedRoot(baseDir) {
			continue
		}
		found, err := d.discoverInDir(baseDir, 0)
		if err != nil {
			// Log but continue with other directories
			continue
		}
		repos = append(repos, found...)
	}

	return repos, nil
}

// discoverInDir recursively discovers repos in a directory.
func (d *Discovery) discoverInDir(dir string, depth int) ([]DiscoveredRepo, error) {
	var repos []DiscoveredRepo

	// Check depth limit
	if d.scanDepth > 0 && depth >= d.scanDepth {
		return repos, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return repos, nil // Skip on error, continue discovery
	}

	for _, entry := range entries {
		// Skip hidden directories except .jj
		if strings.HasPrefix(entry.Name(), ".") && entry.Name() != ".jj" {
			continue
		}

		// Skip excluded patterns
		if d.isExcluded(entry.Name()) {
			continue
		}

		fullPath := filepath.Join(dir, entry.Name())

		if d.isExcludedRoot(fullPath) {
			continue
		}

		// Check if it's a .jj directory (a jj repo or workspace).
		if entry.Name() == ".jj" && entry.IsDir() {
			isWorktree := d.isSecondaryWorkspace(fullPath)
			if isWorktree {
				mainRepo := d.getMainRepoPath(fullPath)
				repos = append(repos, DiscoveredRepo{
					Path:         dir,
					Name:         filepath.Base(dir),
					IsWorktree:   true,
					MainRepoPath: mainRepo,
				})
			} else {
				repos = append(repos, DiscoveredRepo{
					Path:       dir,
					Name:       filepath.Base(dir),
					IsWorktree: false,
				})
			}
			// Don't recurse into .jj
			continue
		}

		// Recurse into directories
		if entry.IsDir() {
			found, _ := d.discoverInDir(fullPath, depth+1)
			repos = append(repos, found...)
		}
	}

	return repos, nil
}

// isExcluded checks if a path matches exclude patterns.
func (d *Discovery) isExcluded(path string) bool {
	for _, pattern := range d.excludePatterns {
		if pattern == path || strings.Contains(path, pattern) {
			return true
		}
	}
	return false
}

// isExcludedRoot reports whether path is at or beneath an ExcludeRoots entry.
func (d *Discovery) isExcludedRoot(path string) bool {
	if len(d.excludeRoots) == 0 {
		return false
	}
	clean := filepath.Clean(path)
	for _, root := range d.excludeRoots {
		if clean == root || strings.HasPrefix(clean, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// isSecondaryWorkspace reports whether the given .jj directory belongs to a
// secondary workspace (one created with `jj workspace add`). In a main
// workspace, `.jj/repo` is a directory holding the repo storage; in a
// secondary workspace, `.jj/repo` is a file containing the relative path to
// the main workspace's storage.
func (d *Discovery) isSecondaryWorkspace(jjPath string) bool {
	repoPath := filepath.Join(jjPath, "repo")
	info, err := os.Stat(repoPath)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// getMainRepoPath returns the path to the main workspace given a secondary
// workspace's .jj directory. The secondary workspace's `.jj/repo` file
// contains the relative path to the main workspace's `.jj/repo` directory
// (e.g. `../../main/.jj/repo`).
func (d *Discovery) getMainRepoPath(jjPath string) string {
	repoFile := filepath.Join(jjPath, "repo")
	data, err := os.ReadFile(repoFile)
	if err != nil {
		return ""
	}

	target := strings.TrimSpace(string(data))
	if target == "" {
		return ""
	}

	// Resolve relative to .jj/.
	if !filepath.IsAbs(target) {
		target = filepath.Join(jjPath, target)
	}

	// Strip trailing /.jj/repo to get the main workspace root.
	target = filepath.Clean(target)
	if strings.HasSuffix(target, "/.jj/repo") {
		return strings.TrimSuffix(target, "/.jj/repo")
	}
	return ""
}

// IsRepo reports whether the given directory contains a jj repository or
// workspace (i.e. has a `.jj/` directory).
func IsRepo(path string) bool {
	jjPath := filepath.Join(path, ".jj")
	info, err := os.Stat(jjPath)
	if err != nil {
		return false
	}
	return info.IsDir()
}
