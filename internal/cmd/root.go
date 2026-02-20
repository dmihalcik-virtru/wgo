// Package cmd provides CLI commands for wgo.
package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// rootCmd represents the base command when called without any subcommands.
var rootCmd = &cobra.Command{
	Use:     "wgo",
	Short:   "Track developer work context across repositories",
	Long:    getLongDescription(),
	Version: getVersionString(),
}

// figletFallback is the baked-in ASCII art for "what's going on?" in slant font.
var figletFallback = strings.Join([]string{
	`           __          __ _                      _                          ___ `,
	` _      __/ /_  ____ _/ /( )_____   ____ _____  (_)___  ____ _   ____  ____/__ \`,
	"| | /| / / __ \\/ __ `/ __/// ___/  / __ `/ __ \\/ / __ \\/ __ `/  / __ \\/ __ \\/ _/",
	`| |/ |/ / / / / /_/ / /_  (__  )  / /_/ / /_/ / / / / / /_/ /  / /_/ / / / /_/ `,
	`|__/|__/_/ /_/\__,_/\__/ /____/   \__, /\____/_/_/ /_/\__, /   \____/_/ /_(_)  `,
	`                                 /____/              /____/                      `,
}, "\n")

func getHeader() string {
	out, err := exec.Command("figlet", "-f", "slant", "-w", "120", "what's going on?").Output()
	if err == nil {
		return strings.TrimRight(string(out), "\n")
	}
	return figletFallback
}

func getLongDescription() string {
	return getHeader() + `

wgo tracks developer work context across the entire filesystem.
It maintains a human-readable plan file that maps branches to purpose to PR status
across repositories, helping you keep track of what you created, why, and where things are.`
}

// Execute adds all child commands to the root command and sets flags appropriately.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.CompletionOptions.DisableDefaultCmd = false
	rootCmd.CompletionOptions.HiddenDefaultCmd = false
}

// getVersionString returns a formatted version string using build info.
func getVersionString() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date)
	}

	buildVersion := version
	buildCommit := commit
	buildDate := date

	// Try to get version from module
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		buildVersion = info.Main.Version
	}

	// Try to get commit and date from VCS settings
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			if setting.Value != "" {
				buildCommit = setting.Value
				if len(buildCommit) > 7 {
					buildCommit = buildCommit[:7]
				}
			}
		case "vcs.time":
			if setting.Value != "" {
				buildDate = setting.Value
			}
		}
	}

	return fmt.Sprintf("%s (commit: %s, built: %s)", buildVersion, buildCommit, buildDate)
}
