//go:build linux || darwin

package provisioning

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const encryptionTool = "/usr/bin/systemd-creds"

func checkEncryptionTool(root *os.File, owner uint32) error {
	fd, err := unix.FcntlInt(root.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return ErrEncryption
	}
	for _, name := range []string{"usr", "bin"} {
		if !trustedDirectory(fd, owner, false) {
			_ = unix.Close(fd)
			return ErrEncryption
		}
		next, openErr := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return ErrEncryption
		}
		fd = next
	}
	if !trustedDirectory(fd, owner, false) {
		_ = unix.Close(fd)
		return ErrEncryption
	}
	tool, err := unix.Openat(fd, "systemd-creds", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	_ = unix.Close(fd)
	if err != nil {
		return ErrEncryption
	}
	defer func() { _ = unix.Close(tool) }()
	var info unix.Stat_t
	if unix.Fstat(tool, &info) != nil || info.Mode&unix.S_IFMT != unix.S_IFREG || info.Uid != owner ||
		info.Mode&0o022 != 0 || info.Mode&0o6000 != 0 || info.Mode&0o100 == 0 {
		return ErrEncryption
	}
	return nil
}

type boundedOutput struct {
	data []byte
}

func (output *boundedOutput) Write(data []byte) (int, error) {
	if len(data) > maxCiphertextBytes-len(output.data) {
		return 0, ErrEncryption
	}
	output.data = append(output.data, data...)
	return len(data), nil
}

func encryptToken(ctx context.Context, token []byte) ([]byte, error) {
	return runEncryption(encryptionCommand(ctx, token))
}

func runEncryption(command *exec.Cmd) ([]byte, error) {
	output := &boundedOutput{}
	command.Stdout = output
	if err := command.Run(); err != nil {
		clear(output.data)
		return nil, ErrEncryption
	}
	return output.data, nil
}

func encryptionCommand(ctx context.Context, token []byte) *exec.Cmd {
	command := exec.CommandContext(ctx, encryptionTool,
		"encrypt", "--name="+credentialName, "--with-key=auto", "-", "-")
	command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "SYSTEMD_LOG_TARGET=null", "SYSTEMD_LOG_LEVEL=err"}
	command.Stdin = bytes.NewReader(token)
	command.Stderr = io.Discard
	command.WaitDelay = time.Second
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return unix.Kill(-command.Process.Pid, unix.SIGKILL) }
	return command
}
