package cleanup

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gitDiff builds a zero-context git diff section for path from hunk specs of
// the form "@@ -a,b +c,d @@" followed by -/+ lines.
func gitDiff(path string, extra string, body ...string) string {
	head := "diff --git a/" + path + " b/" + path + "\n" + extra
	return head + "--- a/" + path + "\n+++ b/" + path + "\n" + strings.Join(body, "\n") + "\n"
}

func mustParse(t *testing.T, out string) map[string]*fileDiff {
	t.Helper()
	files, err := parseGitDiff(out)
	require.NoError(t, err)
	return files
}

func TestParseGitDiff(t *testing.T) {
	out := gitDiff("a.go", "index 1..2 100644\n",
		"@@ -2 +2 @@ func x() {",
		"-old",
		"+new",
		"@@ -5,0 +6,2 @@",
		"+ins1",
		"+ins2",
	) + "diff --git a/new.txt b/new.txt\nnew file mode 100644\n--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+hello\n\\ No newline at end of file\n" +
		"diff --git a/img.png b/img.png\nBinary files a/img.png and b/img.png differ\n"
	files := mustParse(t, out)

	a := files["a.go"]
	require.NotNil(t, a)
	require.Len(t, a.hunks, 2)
	assert.Equal(t, hunk{start: 1, oldLines: []string{"old"}, newLines: []string{"new"}}, a.hunks[0])
	assert.Equal(t, hunk{start: 5, newLines: []string{"ins1", "ins2"}}, a.hunks[1], "an insertion after old line 5 goes before index 5")

	n := files["new.txt"]
	require.NotNil(t, n)
	assert.True(t, n.created)
	assert.Equal(t, []string{"hello" + noEOL}, n.hunks[0].newLines)

	assert.True(t, files["img.png"].opaque)
}

func TestHeaderPath(t *testing.T) {
	assert.Equal(t, "dir/a b.go", headerPath("a/dir/a b.go b/dir/a b.go"))
	assert.Empty(t, headerPath("a/old.go b/new.go"), "a rename has two paths")
	assert.Empty(t, headerPath(`"a/tab\there" "b/tab\there"`), "quoted paths are not modelled")
}

// Old file for the containment cases: lines 1..6.
func TestHunksContained(t *testing.T) {
	change := func(start int, old, new string) hunk {
		h := hunk{start: start}
		if old != "" {
			h.oldLines = strings.Split(old, "|")
		}
		if new != "" {
			h.newLines = strings.Split(new, "|")
		}
		return h
	}
	tests := []struct {
		name      string
		trunk     []hunk
		work      []hunk
		contained bool
	}{
		{"same change", []hunk{change(1, "2", "B")}, []hunk{change(1, "2", "B")}, true},
		{"same change plus unrelated trunk change",
			[]hunk{change(1, "2", "B"), change(4, "5", "E")}, []hunk{change(1, "2", "B")}, true},
		{"trunk changed the work's lines again later",
			[]hunk{change(1, "2|3", "B2")}, []hunk{change(1, "2", "B")}, false},
		{"work-only change", []hunk{change(4, "5", "E")}, []hunk{change(1, "2", "B")}, false},
		{"different change to the same line", []hunk{change(1, "2", "X")}, []hunk{change(1, "2", "B")}, false},
		{"same insertion at one point", []hunk{change(3, "", "new")}, []hunk{change(3, "", "new")}, true},
		{"trunk inserted more at the same point", []hunk{change(3, "", "new|more")}, []hunk{change(3, "", "new")}, false},
		{"work insertion trunk lacks", nil, []hunk{change(3, "", "new")}, false},
		{"same deletion", []hunk{change(2, "3|4", "")}, []hunk{change(2, "3|4", "")}, true},
		{"trunk-only changes", []hunk{change(0, "1", "A")}, nil, true},
		{"adjacent: same change next to a trunk-only change",
			[]hunk{change(1, "2", "B"), change(2, "3", "C")}, []hunk{change(1, "2", "B")}, true},
		{"insertion and change at one point equal trunk's combined hunk",
			[]hunk{change(3, "4", "new|D")}, []hunk{change(3, "", "new"), change(3, "4", "D")}, true},
		{"work inserts before a line trunk changed",
			[]hunk{change(3, "4", "D")}, []hunk{change(3, "", "new")}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.contained, hunksContained(tt.trunk, tt.work))
		})
	}
}

func TestFileContained(t *testing.T) {
	work := &fileDiff{path: "f", hunks: []hunk{{start: 0, oldLines: []string{"a"}, newLines: []string{"b"}}}}
	ok, why := fileContained(nil, work)
	assert.False(t, ok)
	assert.Equal(t, "unchanged on trunk", why)

	created := &fileDiff{path: "f", created: true, hunks: []hunk{{start: 0, newLines: []string{"x"}}}}
	ok, why = fileContained(nil, created)
	assert.False(t, ok)
	assert.Equal(t, "missing on trunk", why)

	ok, why = fileContained(&fileDiff{path: "f", deleted: true, hunks: []hunk{{start: 0, oldLines: []string{"a"}}}}, work)
	assert.False(t, ok)
	assert.Equal(t, "deleted on trunk", why)

	ok, _ = fileContained(&fileDiff{path: "f", hunks: work.hunks}, work)
	assert.True(t, ok)

	ok, why = fileContained(&fileDiff{path: "f", opaque: true}, work)
	assert.False(t, ok)
	assert.Contains(t, why, "binary")

	ok, why = fileContained(&fileDiff{path: "f", hunks: work.hunks}, nil)
	assert.False(t, ok, "a file missing from the work's diff is never contained")
	assert.Equal(t, "not in the work's diff", why)
}
