package rig

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUnderDir(t *testing.T) {
	tests := []struct {
		name   string
		rigDir string
		path   string
		want   bool
	}{
		{"direct child", "/home/u/rigs", "/home/u/rigs/dsp-2.7.1", true},
		{"deep child", "/home/u/rigs", "/home/u/rigs/dsp-2.7.1/src/platform-service-v0.11.6", true},
		{"the dir itself", "/home/u/rigs", "/home/u/rigs", true},
		{"trailing slash on root", "/home/u/rigs/", "/home/u/rigs/dsp", true},
		{"uncleaned path", "/home/u/rigs", "/home/u/rigs/dsp/../dsp/src", true},
		// The prefix must land on a separator: "rigsomething" is a sibling.
		{"sibling sharing a prefix", "/home/u/rigs", "/home/u/rigsomething", false},
		{"sibling sharing a prefix, deep", "/home/u/rigs", "/home/u/rigsomething/repo", false},
		{"unrelated", "/home/u/rigs", "/home/u/mains/acme/widget", false},
		{"parent of the rig dir", "/home/u/rigs", "/home/u", false},
		// An unconfigured rig.dir must never match; filepath.Clean("") is ".",
		// which would otherwise prefix-match every relative path.
		{"empty rig dir", "", "/home/u/rigs/dsp", false},
		{"blank rig dir", "   ", "/home/u/rigs/dsp", false},
		{"empty path", "/home/u/rigs", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, UnderDir(filepath.FromSlash(tt.rigDir), filepath.FromSlash(tt.path)))
		})
	}
}

func TestIsWorkspace(t *testing.T) {
	tests := []struct {
		name   string
		rigDir string
		path   string
		wsName string
		want   bool
	}{
		{
			name:   "both signals",
			rigDir: "/home/u/rigs",
			path:   "/home/u/rigs/dsp-2.7.1/src/platform-service-v0.11.6",
			wsName: "rig-dsp-2.7.1-service",
			want:   true,
		},
		{
			// A rig moved out from under a reconfigured rig.dir is still a rig.
			name:   "name only",
			rigDir: "/home/u/rigs",
			path:   "/tmp/scratch/platform-service-v0.11.6",
			wsName: "rig-dsp-2.7.1-service",
			want:   true,
		},
		{
			// Callers enumerating from the repo side may have no rig.dir at all.
			name:   "name only, rig dir unset",
			rigDir: "",
			path:   "/home/u/rigs/dsp-2.7.1/src/x",
			wsName: "rig-dsp-2.7.1-service",
			want:   true,
		},
		{
			// A hand-made workspace parked in the rig tree still must not be
			// swept by wgo clean: the rig owns that directory.
			name:   "path only",
			rigDir: "/home/u/rigs",
			path:   "/home/u/rigs/dsp-2.7.1/src/manual",
			wsName: "WGO-136-something",
			want:   true,
		},
		{
			name:   "ordinary worktree",
			rigDir: "/home/u/rigs",
			path:   "/home/u/worktrees/WGO-136/widget",
			wsName: "WGO-136-add-rig",
			want:   false,
		},
		{
			name:   "workspace merely starting with rig",
			rigDir: "/home/u/rigs",
			path:   "/home/u/worktrees/rigging/widget",
			wsName: "rigging-refactor",
			want:   false,
		},
		{
			name:   "default workspace of a main clone",
			rigDir: "/home/u/rigs",
			path:   "/home/u/mains/opentdf/platform",
			wsName: "default",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsWorkspace(filepath.FromSlash(tt.rigDir), filepath.FromSlash(tt.path), tt.wsName)
			assert.Equal(t, tt.want, got)
		})
	}
}
