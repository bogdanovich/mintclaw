//go:build darwin

package coordinator

import "os/exec"

func configureChildProcess(*exec.Cmd) {}
