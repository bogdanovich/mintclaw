//go:build !unix && !windows

package thread

import "os"

func tryAcquireAttachmentGCQuarantineFile(file *os.File) error {
	return tryAcquireThreadLeaseFile(file)
}

func releaseAttachmentGCQuarantineFile(file *os.File) error {
	return releaseThreadLeaseFile(file)
}
