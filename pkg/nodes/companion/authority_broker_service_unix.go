//go:build linux || darwin

package companion

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/pkg/nodes/internal/jsonstrict"
)

const MaxAuthorityBrokerConfigBytes = 1024 * 1024

func LoadAuthorityBrokerConfig(path string) (AuthorityBrokerConfig, error) {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return AuthorityBrokerConfig{}, errors.New("authority broker config path must be absolute")
	}
	if err := verifyAuthorityBrokerDirectoryChain(filepath.Dir(path)); err != nil {
		return AuthorityBrokerConfig{}, err
	}
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return AuthorityBrokerConfig{}, fmt.Errorf("open authority broker config: %w", err)
	}
	file := os.NewFile(uintptr(descriptor), path)
	if file == nil {
		_ = unix.Close(descriptor)
		return AuthorityBrokerConfig{}, errors.New("open authority broker config: invalid descriptor")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return AuthorityBrokerConfig{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	pathInfo, pathErr := os.Lstat(path)
	pathStat, pathStatOK := pathInfoSyscallStat(pathInfo)
	if !ok ||
		pathErr != nil ||
		!pathStatOK ||
		stat.Dev != pathStat.Dev ||
		stat.Ino != pathStat.Ino ||
		stat.Uid != 0 ||
		!info.Mode().IsRegular() ||
		info.Mode().Perm()&0o022 != 0 {
		return AuthorityBrokerConfig{}, errors.New(
			"authority broker config must be a root-owned non-writable regular file",
		)
	}
	raw, err := io.ReadAll(io.LimitReader(file, MaxAuthorityBrokerConfigBytes+1))
	if err != nil {
		return AuthorityBrokerConfig{}, fmt.Errorf("read authority broker config: %w", err)
	}
	if len(raw) > MaxAuthorityBrokerConfigBytes {
		return AuthorityBrokerConfig{}, errors.New("authority broker config exceeds size limit")
	}
	if _, err := jsonstrict.Decode(raw); err != nil {
		return AuthorityBrokerConfig{}, fmt.Errorf("validate authority broker config: %w", err)
	}
	var config AuthorityBrokerConfig
	if err := decodeStrictJSON(raw, &config); err != nil {
		return AuthorityBrokerConfig{}, fmt.Errorf("decode authority broker config: %w", err)
	}
	return NormalizeAuthorityBrokerConfig(config, filepath.Dir(path))
}

func verifyAuthorityBrokerDirectoryChain(path string) error {
	for {
		info, err := os.Lstat(path)
		stat, statOK := pathInfoSyscallStat(info)
		if err != nil ||
			!statOK ||
			stat.Uid != 0 ||
			!info.IsDir() ||
			info.Mode().Perm()&0o022 != 0 {
			return errors.New(
				"authority broker directory chain must be root-owned and non-writable",
			)
		}
		if path == string(filepath.Separator) {
			return nil
		}
		parent := filepath.Dir(path)
		if parent == path {
			return errors.New("authority broker directory chain is invalid")
		}
		path = parent
	}
}

func pathInfoSyscallStat(info os.FileInfo) (*syscall.Stat_t, bool) {
	if info == nil {
		return nil, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return stat, ok
}
