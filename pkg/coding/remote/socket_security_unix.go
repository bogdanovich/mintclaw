//go:build darwin || linux

package remote

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func validateSocketPathSyntax(socketPath string) error {
	if socketPath == "" || !filepath.IsAbs(socketPath) || filepath.Clean(socketPath) != socketPath ||
		len(socketPath) > 100 || filepath.Base(socketPath) == string(filepath.Separator) {
		return errors.New("coding remote socket path is invalid")
	}
	return nil
}

func prepareOwnerSocketPath(socketPath string) error {
	if err := validateSocketPathSyntax(socketPath); err != nil {
		return err
	}
	parent := filepath.Dir(socketPath)
	if _, err := os.Lstat(parent); errors.Is(err, os.ErrNotExist) {
		grandparent := filepath.Dir(parent)
		if err = validateCanonicalDirectory(grandparent, false); err != nil {
			return fmt.Errorf("validate coding remote socket parent: %w", err)
		}
		if err = os.Mkdir(parent, 0o700); err != nil {
			return fmt.Errorf("create coding remote socket directory: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("inspect coding remote socket directory: %w", err)
	}
	if err := validateCanonicalDirectory(parent, true); err != nil {
		return fmt.Errorf("validate coding remote socket directory: %w", err)
	}
	info, err := os.Lstat(socketPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect coding remote socket: %w", err)
	}
	if err = validateOwnerSocketInfo(info); err != nil {
		return errors.New("refusing to replace unknown coding remote socket path")
	}
	connection, dialErr := net.DialTimeout("unix", socketPath, 100*time.Millisecond)
	if dialErr == nil {
		_ = connection.Close()
		return errors.New("coding remote socket is already in use")
	}
	if !errors.Is(dialErr, syscall.ECONNREFUSED) && !errors.Is(dialErr, os.ErrNotExist) {
		return errors.New("coding remote socket liveness is uncertain")
	}
	if err = os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale coding remote socket: %w", err)
	}
	return nil
}

func validateOwnerSocketEndpoint(socketPath string) error {
	if err := validateSocketPathSyntax(socketPath); err != nil {
		return err
	}
	if err := validateCanonicalDirectory(filepath.Dir(socketPath), true); err != nil {
		return fmt.Errorf("validate coding remote socket directory: %w", err)
	}
	info, err := os.Lstat(socketPath)
	if err != nil {
		return fmt.Errorf("inspect coding remote socket: %w", err)
	}
	if err = validateOwnerSocketInfo(info); err != nil {
		return err
	}
	return nil
}

func validateCanonicalDirectory(path string, private bool) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if resolved != path {
		return errors.New("socket directory path contains a symbolic link")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("socket directory is not a direct directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("socket directory has an unexpected owner")
	}
	if private && info.Mode().Perm()&0o077 != 0 {
		return errors.New("socket directory grants group or world access")
	}
	return nil
}

func validateOwnerSocketInfo(info os.FileInfo) error {
	if info == nil || info.Mode()&os.ModeSocket == 0 || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o077 != 0 {
		return errors.New("coding remote endpoint is not an owner-only socket")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("coding remote endpoint has an unexpected owner")
	}
	return nil
}
