package launch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// RunTimeout bounds every Run call (osascript, open, an editor). It is this
// long because osascript can wait on the macOS Automation consent prompt.
const RunTimeout = 2 * time.Minute

// startGrace is how long Start waits for a detached process to fail fast
// (a bad flag, a missing display) before reporting it as running.
var startGrace = 1500 * time.Millisecond

// ExecRunner runs processes with os/exec: argv only, never a shell.
type ExecRunner struct {
	// Logf, when set, receives failures nobody else can report: a process
	// Start already reported as running that later exits non-zero.
	Logf func(format string, args ...any)
}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, dir string, argv []string) error {
	if len(argv) == 0 {
		return errors.New("empty command")
	}
	ctx, cancel := context.WithTimeout(ctx, RunTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	stderr := newCapture()
	defer stderr.close()
	stderr.attach(cmd)
	if err := cmd.Run(); err != nil {
		return describe(err, stderr.head())
	}
	return nil
}

// Start implements Runner. The process gets its own process group and a
// file, not a pipe, for stderr, so stopping wgo dash (or wgo open exiting)
// neither signals the terminal it opened nor breaks its stderr.
func (e ExecRunner) Start(dir string, argv []string) error {
	if len(argv) == 0 {
		return errors.New("empty command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	stderr := newCapture()
	stderr.attach(cmd)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		stderr.close()
		return describe(err, "")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		defer stderr.close()
		if err != nil {
			return describe(err, stderr.head())
		}
		return nil
	case <-time.After(startGrace):
		// Still running, so it is reported as opened. A later failure has
		// no request left to answer; log it.
		go func() {
			defer stderr.close()
			if err := <-done; err != nil && e.Logf != nil {
				e.Logf("%s exited after it was reported as opened: %v", argv[0], describe(err, stderr.tail()))
			}
		}()
		return nil
	}
}

// captureLimit bounds how much of a child's stderr is read back.
const captureLimit = 4096

// capture gives a child an unlinked temp file as stderr. os/exec would
// connect a Buffer through a pipe whose reader lives in wgo: a terminal
// that outlives wgo would then get SIGPIPE or EPIPE on its next stderr
// write, a chatty one would grow the Buffer for its whole life, and Wait
// would block on any forked descendant (a GUI editor) still holding the
// pipe. A file has none of those problems. If no temp file can be made,
// the child's stderr goes to the null device and failures carry only the
// exit status.
type capture struct {
	f        *os.File
	unlinked bool
}

func newCapture() *capture {
	f, err := os.CreateTemp("", "wgo-launch-*.stderr")
	if err != nil {
		return &capture{}
	}
	// Unlinking an open file works on unix; elsewhere close removes it.
	return &capture{f: f, unlinked: os.Remove(f.Name()) == nil}
}

func (c *capture) attach(cmd *exec.Cmd) {
	if c.f != nil {
		cmd.Stderr = c.f
	}
}

// head returns the start of what the child wrote: a fast failure's reason.
func (c *capture) head() string { return c.read(0) }

// tail returns the end of what the child wrote: after a long run the
// reason it finally exited, not its startup chatter.
func (c *capture) tail() string {
	if c.f == nil {
		return ""
	}
	fi, err := c.f.Stat()
	if err != nil {
		return ""
	}
	s := c.read(max(0, fi.Size()-captureLimit))
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return s
}

func (c *capture) read(off int64) string {
	if c.f == nil {
		return ""
	}
	buf := make([]byte, captureLimit)
	n, _ := c.f.ReadAt(buf, off)
	return string(buf[:n])
}

func (c *capture) close() {
	if c.f == nil {
		return
	}
	c.f.Close()
	if !c.unlinked {
		os.Remove(c.f.Name())
	}
}

// LookPath implements Runner.
func (ExecRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }

// AppInstalled implements Runner: it looks for name.app in the standard
// application folders.
func (ExecRunner) AppInstalled(name string) bool {
	dirs := []string{"/Applications"}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "Applications"))
	}
	for _, d := range dirs {
		if fi, err := os.Stat(filepath.Join(d, name+".app")); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}

// describe turns an exec failure into a short message with the first line
// of stderr.
func describe(err error, stderr string) error {
	msg := strings.TrimSpace(stderr)
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	if msg != "" {
		return fmt.Errorf("%v: %s", err, msg)
	}
	return err
}
