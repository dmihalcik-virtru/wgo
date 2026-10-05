//go:build linux

package proc

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type systemInspector struct{}

// Lookup parses /proc/<pid>/stat. The comm field is parenthesised and may
// itself contain spaces or parentheses, so fields are split after the last ')'.
func (systemInspector) Lookup(pid int) (Info, error) {
	if pid <= 0 {
		return Info{}, ErrNotFound
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return Info{}, ErrNotFound
	}
	return parseStat(pid, string(data))
}

func parseStat(pid int, s string) (Info, error) {
	open := strings.IndexByte(s, '(')
	closeIdx := strings.LastIndexByte(s, ')')
	if open < 0 || closeIdx < open {
		return Info{}, ErrNotFound
	}
	name := s[open+1 : closeIdx]
	// Fields after comm start at field 3 (state).
	fields := strings.Fields(s[closeIdx+1:])
	if len(fields) < 20 {
		return Info{}, ErrNotFound
	}
	if fields[0] == "Z" || fields[0] == "X" {
		return Info{}, ErrNotFound
	}
	ppid, err := strconv.Atoi(fields[1]) // field 4
	if err != nil {
		return Info{}, ErrNotFound
	}
	start, err := strconv.ParseInt(fields[19], 10, 64) // field 22: starttime
	if err != nil {
		return Info{}, ErrNotFound
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
