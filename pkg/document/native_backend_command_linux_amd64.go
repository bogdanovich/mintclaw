//go:build linux && amd64

package document

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

const (
	popplerStderrLimit     = 8 * 1024
	verifiedExecutablePath = "/proc/self/fd/3"
)

func popplerBackendAvailable() bool {
	return nativeBackendAvailable(PopplerBackendName)
}

func newVerifiedPopplerCommand(executable, expectedSHA256 string, arguments ...string) (*exec.Cmd, *os.File, error) {
	return newVerifiedDocumentCommand("mintclaw-poppler", executable, expectedSHA256, arguments...)
}

func newVerifiedDocumentCommand(
	snapshotName string,
	executable string,
	expectedSHA256 string,
	arguments ...string,
) (*exec.Cmd, *os.File, error) {
	source, err := openSourceNoFollow(executable)
	if err != nil {
		return nil, nil, errors.New("document native backend is unavailable")
	}
	defer func() { _ = source.Close() }()
	descriptor, err := unix.MemfdCreate(snapshotName, unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, nil, errors.New("document native backend cannot be isolated")
	}
	snapshot := os.NewFile(uintptr(descriptor), snapshotName)
	if snapshot == nil {
		_ = unix.Close(descriptor)
		return nil, nil, errors.New("document native backend cannot be isolated")
	}
	fail := func(message string) (*exec.Cmd, *os.File, error) {
		_ = snapshot.Close()
		return nil, nil, errors.New(message)
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(snapshot, hash), io.LimitReader(source, maximumExecutableBytes+1))
	if err != nil || written <= 0 || written > maximumExecutableBytes ||
		hex.EncodeToString(hash.Sum(nil)) != expectedSHA256 {
		return fail("document native backend identity is not admitted")
	}
	if _, err = snapshot.Seek(0, io.SeekStart); err != nil {
		return fail("document native backend cannot be isolated")
	}
	seals := unix.F_SEAL_SEAL | unix.F_SEAL_SHRINK | unix.F_SEAL_GROW | unix.F_SEAL_WRITE
	if _, err = unix.FcntlInt(snapshot.Fd(), unix.F_ADD_SEALS, seals); err != nil {
		return fail("document native backend cannot be isolated")
	}
	command := exec.Command(verifiedExecutablePath, arguments...)
	command.ExtraFiles = []*os.File{snapshot}
	return command, snapshot, nil
}

func documentBackendEnvironment() []string {
	return []string{"HOME=.", "TMPDIR=.", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "PATH="}
}
