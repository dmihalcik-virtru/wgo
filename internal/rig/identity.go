// Package rig builds pinned multi-checkout Go workspaces: one editable,
// tag-pinned checkout per module of a build list, tied together with a
// generated go.work so a shipped artifact can be debugged from source.
package rig

import (
	"path/filepath"
	"strings"
)

// WorkspacePrefix is the prefix every jj workspace a rig creates is named
// with. It exists so a rig workspace is recognisable from the repo side —
// `jj workspace list` in a main clone — where the rig directory is not in view.
const WorkspacePrefix = "rig-"

// ManifestName is the file that records a rig's contents. Its presence is what
// makes a directory a rig.
const ManifestName = "rig.toml"

// IsWorkspace reports whether a jj workspace belongs to a rig.
//
// Both signals are checked because neither alone is sufficient. Path matching
// misses nothing but requires knowing rigDir, which callers enumerating
// workspaces from the repo side may not have configured; the name prefix
// travels with the workspace itself and survives a moved or reconfigured rig
// directory.
//
// This matters most for `wgo clean`, which reaches workspaces through
// `jj workspace list` on the main clone rather than through a filesystem walk,
// so excluding rigDir from discovery does not hide them. A rig workspace is
// pinned to a released tag and carries no bookmark, which is exactly the shape
// clean reads as an abandoned worktree.
func IsWorkspace(rigDir, wsPath, wsName string) bool {
	if strings.HasPrefix(wsName, WorkspacePrefix) {
		return true
	}
	return UnderDir(rigDir, wsPath)
}

// UnderDir reports whether path is at or beneath rigDir. An empty rigDir
// matches nothing.
func UnderDir(rigDir, path string) bool {
	if strings.TrimSpace(rigDir) == "" || strings.TrimSpace(path) == "" {
		return false
	}
	root := filepath.Clean(rigDir)
	p := filepath.Clean(path)
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}
