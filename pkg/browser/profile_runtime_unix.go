//go:build darwin || linux

package browser

import (
	"errors"
	"os"
	"syscall"
)

func validateBrowserRuntimeOwner(info os.FileInfo, directory bool) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("browser runtime path has an unexpected owner")
	}
	if directory && info.Mode().Perm()&0o077 != 0 {
		return errors.New("browser runtime directory grants group or world access")
	}
	if !directory && info.Mode().Perm()&0o077 != 0 {
		return errors.New("browser runtime file grants group or world access")
	}
	return nil
}
