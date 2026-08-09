//go:build windows

package tools

import (
	"fmt"
	"os/exec"
	"strconv"
)

func killProcessGroup(pid int) error {
	return killProcessGroupWith(pid, func(target int) error {
		return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(target)).Run()
	})
}

func killProcessGroupWith(pid int, killTree func(int) error) error {
	if err := killTree(pid); err != nil {
		return fmt.Errorf("taskkill process tree %d: %w", pid, err)
	}
	return nil
}
