package urlhandler

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// LogPath is the log wgo open appends to, beside the approvals.
func LogPath(storeDir string) string { return filepath.Join(storeDir, "url-handler.log") }

// MaxLogSize is the size past which OpenLog starts a new log, keeping the
// previous one as url-handler.log.1.
const MaxLogSize = 256 << 10

// Log is the url-handler log. The applet runs wgo open through AppleScript's
// do shell script, which throws stderr away when wgo succeeds, so this file
// is the only place a warning from a link that did open can be read later.
// Writes never fail: a link must not stop because its log cannot be written.
// A Log is not safe for concurrent use.
type Log struct {
	f   *os.File
	now func() time.Time
	buf []byte
}

// OpenLog opens the log at path for appending, creating its directory and
// rotating it once it passes MaxLogSize. A failed rotation keeps appending
// to path and returns the error with a working Log, so the caller can
// record it there; any other error returns a Log that discards everything.
func OpenLog(path string, now func() time.Time) (*Log, error) {
	if now == nil {
		now = time.Now
	}
	l := &Log{now: now}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return l, fmt.Errorf("could not create %s: %w", filepath.Dir(path), err)
	}
	var rotErr error
	if fi, err := os.Stat(path); err == nil && fi.Size() > MaxLogSize {
		// ErrNotExist means another wgo open rotated it first.
		if err := os.Rename(path, path+".1"); err != nil && !errors.Is(err, fs.ErrNotExist) {
			rotErr = fmt.Errorf("could not rotate %s, so it keeps growing: %w", path, err)
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return l, fmt.Errorf("could not open %s: %w", path, err)
	}
	l.f = f
	return l, rotErr
}

// Writing reports whether the log is writing to its file.
func (l *Log) Writing() bool { return l.f != nil }

// Write implements io.Writer: each complete line is written with a
// timestamp, and control characters are replaced so a message cannot
// rewrite a terminal that tails the log.
func (l *Log) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		l.line(string(l.buf[:i]))
		l.buf = l.buf[i+1:]
	}
	return len(p), nil
}

// Printf writes one formatted line.
func (l *Log) Printf(format string, args ...any) {
	fmt.Fprintf(l, format+"\n", args...)
}

func (l *Log) line(s string) {
	if l.f == nil {
		return
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return '?'
		}
		return r
	}, s)
	fmt.Fprintf(l.f, "%s %s\n", l.now().Format(time.RFC3339), s)
}

// Close writes any unfinished line and closes the file.
func (l *Log) Close() error {
	if len(l.buf) > 0 {
		l.line(string(l.buf))
		l.buf = nil
	}
	if l.f == nil {
		return nil
	}
	return l.f.Close()
}
