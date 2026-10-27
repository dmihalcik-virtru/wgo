package cleanup

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// This file answers one question for the content proof: given the diff from a
// workspace's fork point O to some trunk commit C, and the diff from O to the
// workspace's @ (W), does C already contain every change W makes? It is a
// three-way comparison like diff3, computed from two git diffs with no context
// rather than from file contents, so jj's own line diff does the alignment.
//
// The answer is sound for any alignment: a file is reported contained only
// when, in every region either diff touches, W's version equals O's (W did
// not change it) or equals C's (W made the same change). Merging W's changes
// into C then gives back C exactly, so nothing in W is missing from C. A
// poorer alignment can only make the answer "not contained" more often.

// hunk is one change in a zero-context git diff, in old-file coordinates.
type hunk struct {
	start    int      // 0-based index of the first old line replaced; for a pure insertion, the old index the new lines go before
	oldLines []string // removed lines
	newLines []string // added lines
}

func (h hunk) end() int { return h.start + len(h.oldLines) }

// fileDiff is one file's section of a git diff.
type fileDiff struct {
	path    string
	created bool // the file is absent from the old side
	deleted bool // the file is absent from the new side
	// mode is the new side's mode when the diff states one ("new file mode"),
	// such as 100755 for an executable or 120000 for a symlink. It is empty
	// when the file keeps the old side's mode.
	mode string
	// opaque marks a section whose content the hunks do not describe: a
	// binary file, a rename or copy, or a mode change.
	opaque bool
	hunks  []hunk
}

// noEOL marks a line that has no trailing newline, so a change to the final
// newline still counts as a difference.
const noEOL = "\x00\\ No newline at end of file"

// parseGitDiff splits the output of `jj diff --git --context 0` into files,
// keyed by path. A section whose path cannot be read is dropped, which the
// caller treats as "not contained".
func parseGitDiff(out string) (map[string]*fileDiff, error) {
	files := map[string]*fileDiff{}
	var cur *fileDiff
	var h *hunk
	lastAdded := false
	flush := func() {
		if cur != nil && h != nil {
			cur.hunks = append(cur.hunks, *h)
		}
		h = nil
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			cur = &fileDiff{path: headerPath(strings.TrimPrefix(line, "diff --git "))}
			if cur.path != "" {
				files[cur.path] = cur
			}
		case cur == nil:
			continue
		case strings.HasPrefix(line, "@@ "):
			flush()
			nh, err := parseHunkHeader(line)
			if err != nil {
				return nil, err
			}
			h = &nh
		case h != nil && strings.HasPrefix(line, "-"):
			h.oldLines = append(h.oldLines, line[1:])
			lastAdded = false
		case h != nil && strings.HasPrefix(line, "+"):
			h.newLines = append(h.newLines, line[1:])
			lastAdded = true
		case h != nil && strings.HasPrefix(line, `\`):
			if lastAdded && len(h.newLines) > 0 {
				h.newLines[len(h.newLines)-1] += noEOL
			} else if !lastAdded && len(h.oldLines) > 0 {
				h.oldLines[len(h.oldLines)-1] += noEOL
			}
		case h != nil && strings.HasPrefix(line, " "):
			return nil, fmt.Errorf("unexpected context line in a zero-context diff: %q", line)
		case strings.HasPrefix(line, "--- "):
			if strings.TrimPrefix(line, "--- ") == "/dev/null" {
				cur.created = true
			}
		case strings.HasPrefix(line, "+++ "):
			if strings.TrimPrefix(line, "+++ ") == "/dev/null" {
				cur.deleted = true
			}
		case strings.HasPrefix(line, "new file mode "):
			cur.created = true
			cur.mode = strings.TrimPrefix(line, "new file mode ")
		case strings.HasPrefix(line, "deleted file mode"):
			cur.deleted = true
		case strings.HasPrefix(line, "Binary files"), strings.HasPrefix(line, "GIT binary patch"),
			strings.HasPrefix(line, "rename "), strings.HasPrefix(line, "copy "),
			strings.HasPrefix(line, "similarity index"), strings.HasPrefix(line, "old mode"),
			strings.HasPrefix(line, "new mode"):
			cur.opaque = true
		}
	}
	flush()
	return files, nil
}

// headerPath reads the path from the "a/<path> b/<path>" part of a
// `diff --git` header. It returns "" for a quoted header, or for one whose two
// paths differ (a rename), neither of which the containment check models.
func headerPath(rest string) string {
	if strings.HasPrefix(rest, `"`) || !strings.HasPrefix(rest, "a/") {
		return ""
	}
	n := (len(rest) - len("a/ b/")) / 2
	if n <= 0 || len(rest) != len("a/ b/")+2*n {
		return ""
	}
	a, b := rest[2:2+n], rest[2+n:]
	if b != " b/"+a {
		return ""
	}
	return a
}

// parseHunkHeader parses "@@ -a[,b] +c[,d] @@ ...". A side with a count of 0
// names the line the change follows, so a pure insertion after old line a goes
// before 0-based old index a.
func parseHunkHeader(line string) (hunk, error) {
	fields := strings.Fields(line)
	if len(fields) < 3 || !strings.HasPrefix(fields[1], "-") {
		return hunk{}, fmt.Errorf("bad hunk header %q", line)
	}
	start, count, err := parseRange(strings.TrimPrefix(fields[1], "-"))
	if err != nil {
		return hunk{}, fmt.Errorf("bad hunk header %q: %w", line, err)
	}
	if count > 0 {
		start--
	}
	return hunk{start: start}, nil
}

func parseRange(s string) (start, count int, err error) {
	first, rest, hasCount := strings.Cut(s, ",")
	if start, err = strconv.Atoi(first); err != nil {
		return 0, 0, err
	}
	count = 1
	if hasCount {
		if count, err = strconv.Atoi(rest); err != nil {
			return 0, 0, err
		}
	}
	return start, count, nil
}

// fileContained reports whether trunk (the diff from O to C) already contains
// the work's change to one file (the diff from O to W). toTrunk is nil when C
// leaves the file as it was at O. The reason says why not, for the evidence.
//
// The hunks describe only the lines, so whether the file exists and its mode
// are compared first: a file both sides create with the same bytes is not
// contained when one made it executable or a symlink and the other did not.
func fileContained(toTrunk, toWork *fileDiff) (bool, string) {
	switch {
	case toWork == nil:
		return false, "not in the work's diff"
	case toWork.opaque || (toTrunk != nil && toTrunk.opaque):
		return false, "binary, renamed or mode change"
	case toTrunk == nil && toWork.created:
		return false, "missing on trunk"
	case toTrunk == nil:
		return false, "unchanged on trunk"
	case toTrunk.deleted && !toWork.deleted:
		return false, "deleted on trunk"
	case toWork.deleted && !toTrunk.deleted:
		return false, "still on trunk"
	case toTrunk.mode != toWork.mode:
		return false, "mode " + toTrunk.mode + " on trunk, " + toWork.mode + " in the work"
	}
	if hunksContained(toTrunk.hunks, toWork.hunks) {
		return true, ""
	}
	return false, "differs from trunk"
}

// hunksContained is the three-way check over one file. Hunks from both diffs
// are grouped into regions of the old file: overlapping hunks share a region,
// and so do hunks that meet where one of them is a pure insertion, since two
// insertions at one point can only be compared together. In each region it
// rebuilds the old, trunk and work text and requires the work text to equal
// the old text or the trunk text.
func hunksContained(toTrunk, toWork []hunk) bool {
	type tagged struct {
		hunk
		work bool
	}
	all := make([]tagged, 0, len(toTrunk)+len(toWork))
	for _, h := range toTrunk {
		all = append(all, tagged{h, false})
	}
	for _, h := range toWork {
		all = append(all, tagged{h, true})
	}
	slices.SortStableFunc(all, func(a, b tagged) int {
		if a.start != b.start {
			return a.start - b.start
		}
		return a.end() - b.end()
	})

	for i := 0; i < len(all); {
		lo, hi := all[i].start, all[i].end()
		insertAtHi := all[i].end() == all[i].start
		j := i + 1
		for ; j < len(all); j++ {
			h := all[j]
			if h.start > hi || (h.start == hi && h.end() > h.start && !insertAtHi) {
				break
			}
			if h.end() > hi {
				hi = h.end()
				insertAtHi = false
			}
			if h.end() == h.start && h.start == hi {
				insertAtHi = true
			}
		}
		region := all[i:j]
		i = j

		old := make([]string, hi-lo)
		known := make([]bool, hi-lo)
		for _, h := range region {
			for k, l := range h.oldLines {
				idx := h.start - lo + k
				if known[idx] && old[idx] != l {
					return false // the two diffs disagree about the old file
				}
				old[idx], known[idx] = l, true
			}
		}
		if slices.Contains(known, false) {
			return false
		}
		var trunkHunks, workHunks []hunk
		for _, h := range region {
			if h.work {
				workHunks = append(workHunks, h.hunk)
			} else {
				trunkHunks = append(trunkHunks, h.hunk)
			}
		}
		work := applyHunks(old, lo, workHunks)
		if slices.Equal(work, old) {
			continue // the work leaves this region alone
		}
		if !slices.Equal(work, applyHunks(old, lo, trunkHunks)) {
			return false // a change trunk lacks, or a different change
		}
	}
	return true
}

// applyHunks rebuilds a region of the old file (old, starting at old index lo)
// with hunks applied. The hunks come from one diff, so they are disjoint and
// already in order.
func applyHunks(old []string, lo int, hunks []hunk) []string {
	var out []string
	at := 0
	for _, h := range hunks {
		s := h.start - lo
		out = append(out, old[at:s]...)
		out = append(out, h.newLines...)
		at = s + len(h.oldLines)
	}
	return append(out, old[at:]...)
}
