//go:build linux || darwin

package coordinator

import (
	"os"
	"syscall"
)

func unixFileIdentity(info os.FileInfo) (links uint64, owner uint64, ok bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return unsigned64(stat.Nlink), unsigned64(stat.Uid), true
}
