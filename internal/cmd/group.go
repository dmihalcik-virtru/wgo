package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/links"
	"github.com/virtru/wgo/internal/rig"
	"github.com/virtru/wgo/internal/spec"
	"github.com/virtru/wgo/models"
)

// groupMemberCap bounds how many workspaces a group summary resolves. Each
// costs jj calls on the statusline's hot path, and a line has no room for
// more anyway.
const groupMemberCap = 6

// groupWorkspaces returns the jj workspaces directly inside dir, or nil when
// there are none. Such a folder is where `wgo add` with several repos, and
// `wgo rig`, write a CLAUDE.md, so it is where an agent is often started. A
// rig root (one with a rig.toml) keeps its checkouts under src/.
//
// It reads one directory level and stats each entry, so it is cheap enough
// for the statusline and hooks. Callers check first that dir is not inside a
// workspace: every subdirectory of a repository with nested jj repos would
// otherwise look like a group.
func groupWorkspaces(dir string) []string {
	parent := dir
	if _, err := os.Stat(rig.ManifestPath(dir)); err == nil {
		parent = filepath.Join(dir, rig.SrcDir)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		ws := filepath.Join(parent, e.Name())
		if info, err := os.Stat(filepath.Join(ws, ".jj")); err == nil && info.IsDir() {
			out = append(out, ws)
		}
	}
	return out
}

// buildGroupContext summarizes the workspaces of the group at dir: each one's
// bookmark and PRs, plus the group's ticket (from the folder name, as `wgo
// add` names it), rig and agent. Like the statusline it serves, it degrades
// rather than fails: a workspace jj cannot read is listed by name alone.
func buildGroupContext(dir string, members []string, opts contextOptions) *models.Context {
	g := &models.GroupRef{Name: filepath.Base(dir), Path: dir}
	if len(members) > groupMemberCap {
		g.Overflow = len(members) - groupMemberCap
		members = members[:groupMemberCap]
	}
	// One budget for every workspace, as lookupWorkspace has, so a wedged jj
	// cannot hold up the prompt.
	jctx, cancel := context.WithTimeout(context.Background(), workspaceLookupTimeout)
	defer cancel()
	jjc := jj.NewCLI().ReadOnly().WithContext(jctx)
	g.Workspaces = make([]models.GroupWorkspace, len(members))
	var wg sync.WaitGroup
	for i, ws := range members {
		wg.Go(func() { g.Workspaces[i] = groupWorkspace(jjc, ws, opts) })
	}
	wg.Wait()

	ctx := &models.Context{SchemaVersion: models.ContextSchemaVersion, Group: g}
	ctx.Rig = resolveRigRef(dir)
	// A rig is named for its pin (dsp-2.7.1), which would parse as a ticket.
	if ticket := spec.ParseTicketFromBranch(g.Name); ticket != "" && ctx.Rig == nil {
		ctx.Ticket = ticket
		var autoSite string
		ctx.JiraStatus, ctx.JiraAssignee, autoSite = resolveJiraStatus(ticket, opts)
		ctx.TicketURL = ticketURL(ticket, groupRepoURL(g), autoSite)
	}
	// No repo or bookmark: a hook-managed session keeps the ones it recorded.
	heartbeatAgent(dir, "", "", false)
	ctx.Agent = resolveAgent(dir)
	return ctx
}

// groupWorkspace resolves one workspace of a group with read-only jj calls.
func groupWorkspace(jjc *jj.CLIClient, ws string, opts contextOptions) models.GroupWorkspace {
	gw := models.GroupWorkspace{Name: filepath.Base(ws), Path: ws}
	remote := ""
	if remotes, err := jjc.RemoteURLs(ws); err == nil {
		remote = remotes["origin"]
	} else {
		debugf("group: remotes for %s: %v", ws, err)
	}
	gw.RepoURL = links.RepoURL(remote)
	gw.Branch, _ = bookmarkOf(jjc, ws)
	if gw.Branch != "" {
		gw.BranchURL = links.BranchURL(remote, gw.Branch)
		if remote != "" {
			gw.PRs, _ = prLookup(resolvePRs(ws, remote, gw.Branch, opts))
		}
	}
	return gw
}

// groupRepoURL is the first workspace's GitHub URL, which a GH-<n> ticket
// links against.
func groupRepoURL(g *models.GroupRef) string {
	for _, ws := range g.Workspaces {
		if ws.RepoURL != "" {
			return ws.RepoURL
		}
	}
	return ""
}
