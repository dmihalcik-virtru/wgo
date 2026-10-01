package review

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// RunsDir is where the year-in-review skill writes run directories, relative
// to the wgo base directory (~/.wgo).
func RunsDir(wgoDir string) string {
	return filepath.Join(wgoDir, "cache", "review", "runs")
}

// Resolve turns the user's argument into a run directory: an existing path is
// used as-is, otherwise it names a run under RunsDir, and an empty argument
// picks the most recently modified run.
func Resolve(wgoDir, arg string) (string, error) {
	if arg != "" {
		if fi, err := os.Stat(arg); err == nil && fi.IsDir() {
			return arg, nil
		}
		dir := filepath.Join(RunsDir(wgoDir), arg)
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return dir, nil
		}
		return "", fmt.Errorf("no run %q: not a directory and not under %s; list runs with `ls %s`", arg, RunsDir(wgoDir), RunsDir(wgoDir))
	}

	entries, err := os.ReadDir(RunsDir(wgoDir))
	if err != nil {
		return "", fmt.Errorf("no year-in-review runs found in %s: run the /year-in-review skill first", RunsDir(wgoDir))
	}
	type run struct {
		name string
		mod  int64
	}
	var runs []run
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if fi, err := e.Info(); err == nil {
			runs = append(runs, run{e.Name(), fi.ModTime().UnixNano()})
		}
	}
	if len(runs) == 0 {
		return "", fmt.Errorf("no year-in-review runs found in %s: run the /year-in-review skill first", RunsDir(wgoDir))
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].mod > runs[j].mod })
	return filepath.Join(RunsDir(wgoDir), runs[0].name), nil
}
