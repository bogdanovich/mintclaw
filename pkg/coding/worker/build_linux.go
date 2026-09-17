//go:build linux

package worker

import "os"

func openRunningExecutable() (*os.File, error) {
	return os.Open("/proc/self/exe")
}
