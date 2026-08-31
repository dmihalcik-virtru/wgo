package rig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/virtru/wgo/internal/jj"
)

// minAbbrev is the shortest commit-id prefix treated as identifying a commit.
// Below it a shared prefix is coincidence, not identity.
const minAbbrev = 7

// Pinned is the jj surface the health check needs.
//
// Narrow like Workspaces and RepoLocator, and for the same reason: it keeps
// the classification testable without nine jj workspaces on disk.
type Pinned interface {
	Log(repo, revset string) ([]jj.LogEntry, error)
}

// Health classifies one checkout against the commit its manifest pins.
type Health string

const (
	// HealthOK is a checkout sitting on its pinned commit. Uncommitted edits
	// do not change this: a rig is a place to hack on pinned source, so a
	// dirty working copy is the expected state, not a fault.
	HealthOK Health = "ok"
	// HealthMissing is a checkout whose directory is gone.
	HealthMissing Health = "missing"
	// HealthMoved is a checkout whose working copy no longer descends from the
	// pin — someone ran `jj new`, `jj edit` or `jj rebase` in it.
	HealthMoved Health = "moved"
	// HealthUnreadable is a checkout jj could not report on. Distinct from
	// HealthOK on purpose: "we could not tell" is not "it is fine".
	HealthUnreadable Health = "unreadable"
)

// Condition is one checkout's health, with the context needed to name a fix.
type Condition struct {
	// Rig is the manifest's name.
	Rig string
	// Checkout is the manifest entry this condition is about.
	Checkout Checkout
	// Path is the checkout's absolute directory.
	Path   string
	Health Health
	// At is the commit the working copy actually sits on, set for HealthMoved.
	At string
	// Detail is the underlying error, set for HealthUnreadable.
	Detail string
}

// OK reports whether the checkout is on its pin.
func (c Condition) OK() bool { return c.Health == HealthOK }

// Inspect classifies every checkout of a rig, in manifest order.
//
// The question is descent, not depth. A rig is a place to hack on pinned
// source, so the expected state after a session's work is a stack of commits
// sitting on top of the pin — `@--`, `@---`, as deep as the user got. Asking
// only `@ | @-` would report every one of those as drift, which is why the
// revset also intersects the pin with `::@`: the checkout is on its pin when
// the pin is `@`, is `@-`, or is any ancestor of `@`.
//
// Drift is the pin dropping out of `@`'s ancestry — someone ran `jj edit` or
// `jj new` onto an unrelated commit, or rebased the stack off the pin. That is
// the one condition that makes a rig quietly stop reproducing the artifact:
// the go.work still resolves, the build still succeeds, and the source is no
// longer what shipped.
//
// Every checkout is classified — callers filter for the ones that are not OK —
// so a caller can report "9 checkouts, all on their pins" without re-walking.
func Inspect(p Pinned, m *Manifest, rigRoot string) []Condition {
	out := make([]Condition, 0, len(m.Checkouts))
	for _, c := range m.Checkouts {
		out = append(out, inspectOne(p, m.Name, c, filepath.Join(rigRoot, SrcDir, c.Dir)))
	}
	return out
}

func inspectOne(p Pinned, rigName string, c Checkout, dest string) Condition {
	cond := Condition{Rig: rigName, Checkout: c, Path: dest}

	if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
		cond.Health = HealthMissing
		return cond
	}
	revset := pinRevset(c.Commit)
	entries, err := p.Log(dest, revset)
	if err != nil {
		cond.Health, cond.Detail = HealthUnreadable, err.Error()
		return cond
	}
	if len(entries) == 0 {
		// jj reported neither a working copy nor a parent. Not a drifted
		// checkout, but not a readable one either.
		cond.Health, cond.Detail = HealthUnreadable, "jj reported no commits for "+revset
		return cond
	}

	for _, e := range entries {
		if sameCommit(e.CommitID, c.Commit) {
			cond.Health = HealthOK
			return cond
		}
	}
	cond.Health, cond.At = HealthMoved, movedTo(entries)
	return cond
}

// pinRevset asks jj for the working copy, its parent, and the pin itself if
// the pin is an ancestor of the working copy.
//
// present() keeps a pin jj cannot resolve — abandoned, or mistyped into a
// hand-edited rig.toml — from turning a drifted checkout into an unreadable
// one; the empty result just fails the ancestry test, which is the right
// answer. The pin is only spliced in when it is a plausible commit id, so
// nothing else from rig.toml reaches jj's revset parser.
func pinRevset(commit string) string {
	const base = "@ | @-"
	if !isCommitID(commit) {
		return base
	}
	return base + " | (::@ & present(" + commit + "))"
}

// isCommitID reports whether s is safe to splice into a revset as a commit-id
// prefix: hex, and long enough that sameCommit would accept it anyway.
func isCommitID(s string) bool {
	if len(s) < minAbbrev {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// movedTo names the commit a drifted checkout now sits on.
//
// The working copy wins when it holds something: `jj edit <other commit>`
// leaves the user on that commit, and naming its parent instead would report a
// place they are not. An empty working copy is the fresh anonymous change jj
// makes for a `jj new`, whose hash says nothing — there the parent is the
// commit they actually moved to.
func movedTo(entries []jj.LogEntry) string {
	var wc, parent string
	for _, e := range entries {
		switch {
		case e.CurrentWorkingCopy && !e.Empty:
			return e.CommitID
		case e.CurrentWorkingCopy:
			wc = e.CommitID
		case parent == "":
			parent = e.CommitID
		}
	}
	if parent != "" {
		return parent
	}
	return wc
}

// sameCommit compares two commit ids, tolerating one being an abbreviation of
// the other. rig.toml is meant to be hand-editable, and a commit pasted in from
// `jj log` is abbreviated.
func sameCommit(a, b string) bool {
	if len(a) < minAbbrev || len(b) < minAbbrev {
		return false
	}
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}
