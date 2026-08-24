//go:build darwin || linux

package companion

import (
	"errors"
	"os"
	"syscall"
)

func validateBrowserProfileDirectory(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("profile_directory must be owned by the companion account")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("profile_directory must not grant group or world access")
	}
	return nil
}

func validateBrowserDriverDirectory(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (stat.Uid != uint32(os.Geteuid()) && stat.Uid != 0) {
		return errors.New("driver directory must be owned by the companion account or root")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return errors.New("driver directory must not be group or world writable")
	}
	return nil
}

func validateBrowserExecutableFile(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (stat.Uid != uint32(os.Geteuid()) && stat.Uid != 0) || stat.Nlink != 1 {
		return errors.New("browser executable must have one trusted owner and link")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 {
		return errors.New("browser executable must be a non-writable executable regular file")
	}
	return nil
}
