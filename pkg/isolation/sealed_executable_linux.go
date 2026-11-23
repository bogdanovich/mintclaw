//go:build linux

package isolation

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

const sealedExecutablePath = "/proc/self/fd/3"

var ErrSealedExecutableIdentity = errors.New("sealed executable identity is not admitted")

// NewSealedExecutableCommand copies an already opened executable into an
// immutable Linux file descriptor. The caller is responsible for admitting the
// source file and for configuring the command's environment and sandbox. Close
// the returned snapshot after Start or if the command is abandoned.
func NewSealedExecutableCommand(
	name string, source *os.File, expectedSHA256 string, maximumBytes int64, arguments ...string,
) (*exec.Cmd, *os.File, error) {
	if source == nil || maximumBytes <= 0 || maximumBytes == math.MaxInt64 {
		return nil, nil, ErrSealedExecutableIdentity
	}
	fd, err := unix.MemfdCreate(name, unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, nil, err
	}
	snapshot := os.NewFile(uintptr(fd), name)
	if snapshot == nil {
		_ = unix.Close(fd)
		return nil, nil, errors.New("could not open executable snapshot")
	}
	fail := func(err error) (*exec.Cmd, *os.File, error) {
		_ = snapshot.Close()
		return nil, nil, err
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(snapshot, hash), io.LimitReader(source, maximumBytes+1))
	if err != nil || written <= 0 || written > maximumBytes || hex.EncodeToString(hash.Sum(nil)) != expectedSHA256 {
		return fail(ErrSealedExecutableIdentity)
	}
	if _, err = snapshot.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	seals := unix.F_SEAL_SEAL | unix.F_SEAL_SHRINK | unix.F_SEAL_GROW | unix.F_SEAL_WRITE
	if _, err = unix.FcntlInt(snapshot.Fd(), unix.F_ADD_SEALS, seals); err != nil {
		return fail(err)
	}
	command := exec.Command(sealedExecutablePath, arguments...)
	command.ExtraFiles = []*os.File{snapshot}
	return command, snapshot, nil
}
