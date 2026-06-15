// Package discovery provides filesystem-based repository and workspace discovery.
package discovery

import (
	"os"
	"path/filepath"
	"strings"
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
}

// New creates a new Discovery with the given parameters.
func New(baseDirs []string, scanDepth int, excludePatterns []string) *Discovery {
	return &Discovery{
		baseDirs:        baseDirs,
		scanDepth:       scanDepth,
		excludePatterns: excludePatterns,
	}
}

// DiscoverAll discovers all repositories and workspaces.
func (d *Discovery) DiscoverAll() ([]DiscoveredRepo, error) {
	var repos []DiscoveredRepo

	for _, baseDir := range d.baseDirs {
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
