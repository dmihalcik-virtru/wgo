package rig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/virtru/wgo/internal/jj"
)

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
// A rig checkout is created as a fresh working-copy commit on top of the pin,
// so the pin is normally `@-`. It is `@` instead when the user has run
// `jj edit` on the pinned commit directly, and either is intact — hence the
// `@ | @-` revset rather than a parent lookup. Anything else means the working
// copy has been moved off the commit the manifest promises, which is the one
// condition that makes a rig quietly stop reproducing the artifact: the go.work
// still resolves, the build still succeeds, and the source is no longer what
// shipped.
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
	entries, err := p.Log(dest, "@ | @-")
	if err != nil {
		cond.Health, cond.Detail = HealthUnreadable, err.Error()
		return cond
	}
	if len(entries) == 0 {
		// jj reported neither a working copy nor a parent. Not a drifted
		// checkout, but not a readable one either.
		cond.Health, cond.Detail = HealthUnreadable, "jj reported no commits for @ | @-"
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

// movedTo names the commit a drifted checkout now sits on, preferring the
// working copy's parent over the working copy itself: the working copy of a
// checkout the user has merely worked in is a fresh empty change whose hash
// says nothing, while its parent is the commit they actually moved to.
func movedTo(entries []jj.LogEntry) string {
	for _, e := range entries {
		if !e.CurrentWorkingCopy {
			return e.CommitID
		}
	}
	return entries[0].CommitID
}

// sameCommit compares two commit ids, tolerating one being an abbreviation of
// the other. rig.toml is meant to be hand-editable, and a commit pasted in from
// `jj log` is abbreviated.
func sameCommit(a, b string) bool {
	const minAbbrev = 7
	if len(a) < minAbbrev || len(b) < minAbbrev {
		return false
	}
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}
