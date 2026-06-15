package links

import (
	"fmt"
	"os/exec"
	"runtime"
)

// OpenInBrowser opens url in the user's default browser. Cross-platform
// equivalent of `gh ... --web`: shells out to `open` on macOS, `xdg-open`
// on Linux, and `cmd /c start` on Windows.
//
// We deliberately do not wait for the spawned process or check for a
// success exit code — the browser launching is fire-and-forget by design.
func OpenInBrowser(url string) error {
	if url == "" {
		return fmt.Errorf("OpenInBrowser: empty url")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
