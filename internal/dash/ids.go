package dash

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
)

// WorkspaceID returns the deterministic, opaque ID of the workspace rooted at
// root in the repository whose main clone is mainClone. Both paths are
// canonicalized (cleaned, symlinks resolved), so the same workspace reached
// through different spellings gets the same ID. The ID reveals neither path.
func WorkspaceID(mainClone, root string) string {
	return "ws-" + shortHash(canonicalPath(mainClone), canonicalPath(root))
}

var workspaceIDRe = regexp.MustCompile(`^ws-[0-9a-f]{16}$`)

// ValidWorkspaceID reports whether id is shaped like a WorkspaceID.
func ValidWorkspaceID(id string) bool { return workspaceIDRe.MatchString(id) }

func bookmarkID(mainClone, name string) string {
	return "bm-" + shortHash(canonicalPath(mainClone), name)
}

func effortNodeID(key string) string { return "effort:" + key }

func prNodeID(repo string, number int) string { return fmt.Sprintf("pr:%s#%d", repo, number) }

func agentNodeID(session string) string { return "agent:" + session }

func jiraTicketID(key string) string { return "ticket:jira:" + key }

func githubTicketID(ownerRepo string, number int) string {
	return fmt.Sprintf("ticket:gh:%s#%d", ownerRepo, number)
}

func shortHash(parts ...string) string {
	h := sha256.New()
	for i, p := range parts {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// canonicalPath cleans p and resolves symlinks when the path exists, so
// /var/... and /private/var/... (macOS) index the same workspace.
func canonicalPath(p string) string {
	if p == "" {
		return ""
	}
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
