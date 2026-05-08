// Package discovery provides filesystem-based repository and worktree discovery.
package discovery

import (
	"os"
	"path/filepath"
	"strings"
)

// DiscoveredRepo represents a discovered repository.
type DiscoveredRepo struct {
	Path         string
	Name         string
	IsWorktree   bool
	MainRepoPath string // For worktrees, points to main repo
}

// Discovery discovers repositories and worktrees in configured base directories.
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

// DiscoverAll discovers all repositories and worktrees.
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
		// Skip hidden directories except .git
		if strings.HasPrefix(entry.Name(), ".") && entry.Name() != ".git" {
			continue
		}

		// Skip excluded patterns
		if d.isExcluded(entry.Name()) {
			continue
		}

		fullPath := filepath.Join(dir, entry.Name())

		// Check if it's a .git directory (bare repo or regular repo)
		if entry.Name() == ".git" && entry.IsDir() {
			isWorktree := d.isWorktree(fullPath)
			if isWorktree {
				// Extract main repo path from .git file
				mainRepo := d.getMainRepoPath(fullPath)
				repos = append(repos, DiscoveredRepo{
					Path:         dir,
					Name:         filepath.Base(dir),
					IsWorktree:   true,
					MainRepoPath: mainRepo,
				})
			} else {
				// Regular repo
				repos = append(repos, DiscoveredRepo{
					Path:       dir,
					Name:       filepath.Base(dir),
					IsWorktree: false,
				})
			}
			// Don't recurse into .git
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

// isWorktree checks if a .git directory is a worktree (file) rather than a directory.
func (d *Discovery) isWorktree(gitPath string) bool {
	info, err := os.Stat(gitPath)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// getMainRepoPath extracts the main repo path from a worktree's .git file.
func (d *Discovery) getMainRepoPath(gitPath string) string {
	// .git might be a file for worktrees containing "gitdir: /path/to/repo/.git/worktrees/name"
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return ""
	}

	content := string(data)
	if strings.HasPrefix(content, "gitdir: ") {
		path := strings.TrimPrefix(content, "gitdir: ")
		path = strings.TrimSpace(path)

		// Extract the main repo path
		if strings.Contains(path, ".git/worktrees") {
			idx := strings.Index(path, ".git/worktrees")
			return path[:idx]
		}
	}

	return ""
}

// IsRepo checks if a directory is a git repository.
func IsRepo(path string) bool {
	gitPath := filepath.Join(path, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return false
	}

	// Could be a directory (regular repo) or file (worktree)
	return info.IsDir() || info.Mode().IsRegular()
}
