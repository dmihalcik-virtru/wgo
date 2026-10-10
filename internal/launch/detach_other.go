//go:build !unix

package launch

import "os/exec"

func detach(*exec.Cmd) {}
