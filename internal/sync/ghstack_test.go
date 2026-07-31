package sync

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newFakeLinker builds a cliLinker with injectable exec seams.
func newFakeLinker(ghPresent bool, extOut, versionOut string, colocated bool, capture *[][]string) *cliLinker {
	return &cliLinker{
		lookPath: func(string) (string, error) {
			if ghPresent {
				return "/usr/bin/gh", nil
			}
			return "", fmt.Errorf("not found")
		},
		run: func(dir, name string, args ...string) (string, error) {
			if capture != nil {
				*capture = append(*capture, append([]string{name}, args...))
			}
			switch {
			case len(args) >= 2 && args[0] == "extension" && args[1] == "list":
				return extOut, nil
			case len(args) >= 1 && args[0] == "--version":
				return versionOut, nil
			case len(args) >= 2 && args[0] == "stack" && args[1] == "link":
				return "", nil
			}
			return "", nil
		},
		isColocated: func(string) bool { return colocated },
	}
}

func TestLinkerAvailable_GatingMatrix(t *testing.T) {
	const goodVer = "gh version 2.90.0 (2024-01-01)\nhttps://github.com/cli/cli"
	const goodExt = "gh stack   github/gh-stack  v1.0.0"
	tests := []struct {
		name               string
		ghPresent          bool
		extOut, versionOut string
		colocated          bool
		want               bool
	}{
		{"all good", true, goodExt, goodVer, true, true},
		{"no gh", false, goodExt, goodVer, true, false},
		{"no extension", true, "some-other/ext v1", goodVer, true, false},
		{"gh too old", true, goodExt, "gh version 2.89.9 (2024-01-01)", true, false},
		{"not colocated", true, goodExt, goodVer, false, false},
		{"exactly min version", true, goodExt, "gh version 2.90.0 (x)", true, true},
		{"newer major", true, goodExt, "gh version 3.0.0 (x)", true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := newFakeLinker(tc.ghPresent, tc.extOut, tc.versionOut, tc.colocated, nil)
			assert.Equal(t, tc.want, l.Available("/tmp/repo"))
		})
	}
}

func TestLinker_LinkRunsOnlyLinkSubcommand(t *testing.T) {
	var calls [][]string
	l := newFakeLinker(true, "github/gh-stack", "gh version 2.90.0 (x)", true, &calls)

	err := l.Link("/tmp/repo", []string{"a", "b", "c"})
	require.NoError(t, err)

	// Find the stack invocation and assert it is exactly `gh stack link a b c`.
	var stackCall []string
	for _, c := range calls {
		if len(c) >= 2 && c[0] == "gh" && c[1] == "stack" {
			stackCall = c
		}
	}
	require.NotNil(t, stackCall, "expected a gh stack invocation")
	assert.Equal(t, []string{"gh", "stack", "link", "a", "b", "c"}, stackCall)

	// Ensure no banned subcommand ever appears in any gh stack invocation.
	banned := map[string]bool{"init": true, "add": true, "rebase": true, "sync": true, "modify": true, "submit": true}
	for _, c := range calls {
		if len(c) >= 3 && c[0] == "gh" && c[1] == "stack" {
			assert.False(t, banned[c[2]], "banned gh stack subcommand invoked: %q", c[2])
		}
	}
}

func TestLinker_LinkRequiresTwoBranches(t *testing.T) {
	l := newFakeLinker(true, "github/gh-stack", "gh version 2.90.0 (x)", true, nil)
	err := l.Link("/tmp/repo", []string{"only-one"})
	require.Error(t, err)
}

func TestParseGHVersion(t *testing.T) {
	cases := map[string]struct {
		maj, min int
		ok       bool
	}{
		"gh version 2.90.0 (2024-01-01)": {2, 90, true},
		"gh version 2.4 (x)":             {2, 4, true},
		"garbage":                        {0, 0, false},
		"gh version notanumber.x":        {0, 0, false},
	}
	for in, want := range cases {
		maj, min, ok := parseGHVersion(in)
		assert.Equal(t, want.ok, ok, in)
		if want.ok {
			assert.Equal(t, want.maj, maj, in)
			assert.Equal(t, want.min, min, in)
		}
	}
}
