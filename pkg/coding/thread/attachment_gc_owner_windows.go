//go:build windows

package thread

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

const attachmentGCQuarantineLockOffset = MaxAttachmentBytes + 64*1024 + 1

func tryAcquireAttachmentGCQuarantineFile(file *os.File) error {
	overlapped := attachmentGCQuarantineOverlapped()
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&overlapped,
	)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrLeaseBusy
	}
	return err
}

func releaseAttachmentGCQuarantineFile(file *os.File) error {
	overlapped := attachmentGCQuarantineOverlapped()
	return windows.UnlockFileEx(
		windows.Handle(file.Fd()),
		0,
		1,
		0,
		&overlapped,
	)
}

func attachmentGCQuarantineOverlapped() windows.Overlapped {
	return windows.Overlapped{Offset: uint32(attachmentGCQuarantineLockOffset)}
}
