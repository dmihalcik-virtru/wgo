package cmd

import (
	"fmt"
	"os"
)

// repoFlag is bound to the root command's persistent -C/--repo flag. When set,
// commands operate as if wgo were started in that directory instead of the
// process working directory.
var repoFlag string

// resolveCwd returns the directory context commands should operate on: the
// -C/--repo flag when set (validated to exist and be a directory), otherwise
// the process working directory.
func resolveCwd() (string, error) {
	if repoFlag == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get current directory: %w", err)
		}
		return cwd, nil
	}
	info, err := os.Stat(repoFlag)
	if err != nil {
		return "", fmt.Errorf("--repo %q: %w", repoFlag, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("--repo %q is not a directory", repoFlag)
	}
	return repoFlag, nil
}
