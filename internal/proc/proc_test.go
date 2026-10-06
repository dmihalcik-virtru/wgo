package proc

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeInspector is a static process table.
type fakeInspector struct {
	procs map[int]Info
	args  map[int][]string
}

func (f fakeInspector) Lookup(pid int) (Info, error) {
	if p, ok := f.procs[pid]; ok {
		return p, nil
	}
	return Info{}, ErrNotFound
}

func (f fakeInspector) Args(pid int) ([]string, error) {
	if a, ok := f.args[pid]; ok {
		return a, nil
	}
	return nil, ErrNotFound
}

func TestCheckRequiresMatchingStart(t *testing.T) {
	in := fakeInspector{procs: map[int]Info{42: {PID: 42, Start: 1000}}}
	assert.Equal(t, Running, Check(in, 42, 1000))
	assert.Equal(t, Exited, Check(in, 42, 999), "a reused PID has a different start token")
	assert.Equal(t, Exited, Check(in, 43, 1000), "a missing PID has exited")
	assert.Equal(t, Unknown, Check(in, 42, 0), "no start token means no identity")
	assert.Equal(t, Unknown, Check(in, 0, 1000))
	assert.Equal(t, Unknown, Check(nil, 42, 1000), "no inspector verifies nothing")
}

// failingInspector fails every lookup without saying the process is gone, as a
// sandboxed sysctl or a hidepid /proc does.
type failingInspector struct{ err error }

func (f failingInspector) Lookup(int) (Info, error)   { return Info{}, f.err }
func (f failingInspector) Args(int) ([]string, error) { return nil, f.err }

func TestCheckInspectionFailureIsUnknown(t *testing.T) {
	denied := failingInspector{err: errors.New("permission denied")}
	assert.Equal(t, Unknown, Check(denied, 42, 1000))
	assert.Equal(t, Unknown, Check(failingInspector{err: ErrUnsupported}, 42, 1000))
}

func TestFindAncestorReportsFailedWalk(t *testing.T) {
	denied := failingInspector{err: errors.New("permission denied")}
	_, ok, err := FindAncestor(denied, 30, 8, func(Info) bool { return true })
	assert.False(t, ok)
	assert.ErrorContains(t, err, "pid 30: permission denied")
}

func TestFindAncestorWalksPastShells(t *testing.T) {
	in := fakeInspector{
		procs: map[int]Info{
			30: {PID: 30, PPID: 20, Name: "sh"},
			20: {PID: 20, PPID: 10, Name: "zsh"},
			10: {PID: 10, PPID: 1, Name: "claude", Start: 7},
		},
	}
	match := func(i Info) bool { return MatchesTool(in, i, "claude") }
	got, ok, err := FindAncestor(in, 30, 8, match)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 10, got.PID)

	_, ok, _ = FindAncestor(in, 30, 2, match)
	assert.False(t, ok, "depth is bounded")

	_, ok, err = FindAncestor(in, 30, 8, func(i Info) bool { return MatchesTool(in, i, "codex") })
	assert.False(t, ok, "no matching tool means no identity")
	assert.NoError(t, err, "a walk that reaches PID 1 is a definite miss")
}

func TestMatchesToolScriptRuntime(t *testing.T) {
	in := fakeInspector{args: map[int][]string{
		5: {"node", "/usr/local/lib/node_modules/@anthropic-ai/claude-code/cli.js"},
		6: {"node", "/usr/local/bin/claude"},
		7: {"node", "server.js"},
		8: {"claude"},
		9: {"/bin/zsh", "-c", "claude"},
	}}
	assert.True(t, MatchesTool(in, Info{PID: 8, Name: "2.1.285"}, "claude"), "native install: comm is the version, argv[0] the tool")
	assert.False(t, MatchesTool(in, Info{PID: 9, Name: "zsh"}, "claude"), "a shell running the tool is not the tool")
	assert.True(t, MatchesTool(in, Info{PID: 5, Name: "node"}, "claude"))
	assert.True(t, MatchesTool(in, Info{PID: 6, Name: "node"}, "claude"))
	assert.False(t, MatchesTool(in, Info{PID: 7, Name: "node"}, "claude"))
	assert.True(t, MatchesTool(in, Info{Name: "Cursor Helper"}, "cursor"))
	assert.False(t, MatchesTool(in, Info{Name: "claudette"}, "claude"))
	assert.False(t, MatchesTool(in, Info{Name: "claude"}, ""))
}

// TestSystemInspectorSelf checks the real implementation against this test
// process: it exists, its start token is stable, and its parent is reported.
func TestSystemInspectorSelf(t *testing.T) {
	in := System()
	self, err := in.Lookup(os.Getpid())
	if err != nil {
		t.Skipf("process inspection unsupported here: %v", err)
	}
	assert.Equal(t, os.Getpid(), self.PID)
	assert.Equal(t, os.Getppid(), self.PPID)
	assert.NotZero(t, self.Start)
	assert.NotEmpty(t, self.Name)
	assert.Equal(t, Running, Check(in, self.PID, self.Start))
	assert.Equal(t, Exited, Check(in, self.PID, self.Start+1))

	args, err := in.Args(os.Getpid())
	require.NoError(t, err)
	require.NotEmpty(t, args)
}

// TestSystemInspectorMissingPID checks that a PID with no process is reported
// as ErrNotFound (gone), not as an inspection failure.
func TestSystemInspectorMissingPID(t *testing.T) {
	in := System()
	if _, err := in.Lookup(os.Getpid()); err != nil {
		t.Skipf("process inspection unsupported here: %v", err)
	}
	_, err := in.Lookup(99_999_999)
	assert.ErrorIs(t, err, ErrNotFound)
}
