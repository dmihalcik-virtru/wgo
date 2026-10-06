//go:build linux

package proc

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"syscall"
)

type systemInspector struct{}

// Lookup parses /proc/<pid>/stat. The comm field is parenthesised and may
// itself contain spaces or parentheses, so fields are split after the last ')'.
// Only a missing /proc entry (or ESRCH, when the process exits mid-read) means
// the process is gone; anything else, such as EACCES under hidepid, is an
// inspection failure.
func (systemInspector) Lookup(pid int) (Info, error) {
	if pid <= 0 {
		return Info{}, ErrNotFound
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ESRCH):
		return Info{}, ErrNotFound
	case err != nil:
		return Info{}, err
	}
	return parseStat(pid, string(data))
}

func parseStat(pid int, s string) (Info, error) {
	malformed := fmt.Errorf("/proc/%d/stat: unexpected format", pid)
	open := strings.IndexByte(s, '(')
	closeIdx := strings.LastIndexByte(s, ')')
	if open < 0 || closeIdx < open {
		return Info{}, malformed
	}
	name := s[open+1 : closeIdx]
	// Fields after comm start at field 3 (state).
	fields := strings.Fields(s[closeIdx+1:])
	if len(fields) < 20 {
		return Info{}, malformed
	}
	if fields[0] == "Z" || fields[0] == "X" {
		return Info{}, ErrNotFound
	}
	ppid, err := strconv.Atoi(fields[1]) // field 4
	if err != nil {
		return Info{}, malformed
	}
	start, err := strconv.ParseInt(fields[19], 10, 64) // field 22: starttime
	if err != nil {
		return Info{}, malformed
	}
	return Info{PID: pid, PPID: ppid, Start: start, Name: name}, nil
}

func (systemInspector) Args(pid int) ([]string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return nil, ErrNotFound
	}
	data = bytes.TrimRight(data, "\x00")
	if len(data) == 0 {
		return nil, nil
	}
	return strings.Split(string(data), "\x00"), nil
}
