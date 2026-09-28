//go:build linux

package companion

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func RunAuthorityBroker(
	ctx context.Context,
	config AuthorityBrokerConfig,
	executable string,
) error {
	if os.Geteuid() != 0 {
		return errors.New("authority broker must run as root")
	}
	if len(config.normalizedProfile) != MaxShellBrokerProfiles {
		return errors.New("authority broker config is not normalized")
	}
	runner, err := newAuthorityBrokerProcessRunner(executable)
	if err != nil {
		return err
	}
	identity, err := newAuthorityBrokerCgroupIdentity(config.CompanionCgroup)
	if err != nil {
		return err
	}
	defer func() { _ = identity.Close() }()
	server, err := newAuthorityBrokerServer(config, runner, identity)
	if err != nil {
		return err
	}
	directory, err := openAuthorityBrokerSocketDirectory(config.SocketPath)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	if prepareErr := directory.prepare(); prepareErr != nil {
		return prepareErr
	}
	listener, err := net.ListenUnix(
		"unix",
		&net.UnixAddr{Name: directory.descriptorPath(), Net: "unix"},
	)
	if err != nil {
		return fmt.Errorf("listen authority broker socket: %w", err)
	}
	listener.SetUnlinkOnClose(false)
	defer func() { _ = listener.Close() }()
	defer func() { _ = directory.unlink() }()
	if chownErr := unix.Fchownat(
		directory.descriptor,
		directory.name,
		0,
		int(config.AllowedGID),
		unix.AT_SYMLINK_NOFOLLOW,
	); chownErr != nil {
		return fmt.Errorf("own authority broker socket: %w", chownErr)
	}
	if chmodErr := unix.Fchmodat(
		directory.descriptor,
		directory.name,
		0o660,
		0,
	); chmodErr != nil {
		return fmt.Errorf("protect authority broker socket: %w", chmodErr)
	}
	return server.Serve(ctx, listener)
}

type authorityBrokerSocketDirectory struct {
	descriptor int
	name       string
}

func openAuthorityBrokerSocketDirectory(
	socketPath string,
) (*authorityBrokerSocketDirectory, error) {
	socketPath = filepath.Clean(socketPath)
	parent := filepath.Dir(socketPath)
	name := filepath.Base(socketPath)
	if !filepath.IsAbs(socketPath) ||
		name == "." ||
		name == string(filepath.Separator) {
		return nil, errors.New("authority broker socket path is invalid")
	}
	if err := verifyAuthorityBrokerDirectoryChain(parent); err != nil {
		return nil, err
	}
	descriptor, err := unix.Open(
		parent,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open authority broker socket directory: %w", err)
	}
	var opened unix.Stat_t
	if err := unix.Fstat(descriptor, &opened); err != nil {
		_ = unix.Close(descriptor)
		return nil, fmt.Errorf("stat authority broker socket directory: %w", err)
	}
	info, pathErr := os.Lstat(parent)
	pathStat, pathStatOK := pathInfoSyscallStat(info)
	if pathErr != nil ||
		!pathStatOK ||
		opened.Dev != pathStat.Dev ||
		opened.Ino != pathStat.Ino {
		_ = unix.Close(descriptor)
		return nil, errors.New("authority broker socket directory changed during validation")
	}
	return &authorityBrokerSocketDirectory{
		descriptor: descriptor, name: name,
	}, nil
}

func (directory *authorityBrokerSocketDirectory) descriptorPath() string {
	return fmt.Sprintf(
		"/proc/self/fd/%d/%s",
		directory.descriptor,
		directory.name,
	)
}

func (directory *authorityBrokerSocketDirectory) prepare() error {
	var stat unix.Stat_t
	err := unix.Fstatat(
		directory.descriptor,
		directory.name,
		&stat,
		unix.AT_SYMLINK_NOFOLLOW,
	)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	if stat.Uid != 0 || stat.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return errors.New("refusing to replace non-broker socket path")
	}
	if err := directory.unlink(); err != nil {
		return fmt.Errorf("remove stale authority broker socket: %w", err)
	}
	return nil
}

func (directory *authorityBrokerSocketDirectory) unlink() error {
	err := unix.Unlinkat(directory.descriptor, directory.name, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return err
}

func (directory *authorityBrokerSocketDirectory) Close() error {
	if directory == nil || directory.descriptor < 0 {
		return nil
	}
	err := unix.Close(directory.descriptor)
	directory.descriptor = -1
	return err
}

func prepareAuthorityBrokerSocket(path string) error {
	directory, err := openAuthorityBrokerSocketDirectory(path)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	if err := directory.prepare(); err != nil {
		return err
	}
	return nil
}
