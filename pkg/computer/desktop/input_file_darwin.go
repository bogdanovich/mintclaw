//go:build darwin

package desktop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/pkg/computer"
)

// This OS-global, root-owned sticky directory deliberately does not depend on
// HOME, TMPDIR, a profile path, bundle container or Mach bootstrap namespace.
// The empty per-UID protocol lock is persistent: never unlink it on release or
// "recover" it by PID/age. flock releases the open-file-description lock on exit.
const darwinInputLockRoot = "/private/var/tmp"

type darwinFileLease struct {
	rootPath string
	name     string
	uid      uint32
	root     int
	file     int
	queue    int
	rootInfo unix.Stat_t
	fileInfo unix.Stat_t
	lost     bool
}

func acquireDarwinFileLease(ctx context.Context) (*darwinFileLease, error) {
	if os.Getuid() == 0 || os.Getuid() != os.Geteuid() {
		return nil, computer.ErrDenied
	}
	return openDarwinFileLease(ctx, darwinInputLockRoot, 0, 0o1777)
}

// Only acquireDarwinFileLease chooses the production namespace. This lower
// level constructor also permits real-process tests in an owned private folder;
// no directory or owner is supplied by a model, profile or environment variable.
// The caller serializes check/close; the containing native lease owns them.
func openDarwinFileLease(
	ctx context.Context, rootPath string, rootOwner uint32, rootMode uint16,
) (*darwinFileLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lease := &darwinFileLease{
		rootPath: rootPath, name: "io.github.bogdanovich.mintclaw.input-control." + strconv.Itoa(os.Getuid()) + ".lock",
		uid: uint32(os.Getuid()), root: -1, file: -1, queue: -1,
	}
	failed := true
	defer func() {
		if failed {
			_ = lease.close()
		}
	}()
	var err error
	lease.root, err = unix.Open(rootPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, computer.ErrUnavailable
	}
	var filesystem unix.Statfs_t
	if unix.Fstat(lease.root, &lease.rootInfo) != nil || lease.rootInfo.Uid != rootOwner ||
		lease.rootInfo.Mode&unix.S_IFMT != unix.S_IFDIR || lease.rootInfo.Mode&0o7777 != rootMode ||
		unix.Fstatfs(lease.root, &filesystem) != nil || filesystem.Flags&unix.MNT_LOCAL == 0 {
		return nil, computer.ErrDenied
	}
	lease.file, err = unix.Openat(lease.root, lease.name,
		unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, computer.ErrDenied
	}
	if unix.Fstat(lease.file, &lease.fileInfo) != nil || !lease.validFile(lease.fileInfo) {
		return nil, computer.ErrDenied
	}
	lease.queue, err = unix.Kqueue()
	if err != nil {
		return nil, ErrUnsupported
	}
	unix.CloseOnExec(lease.queue)
	changes := []unix.Kevent_t{
		{
			Ident: uint64(lease.root), Filter: unix.EVFILT_VNODE,
			Flags: unix.EV_ADD | unix.EV_ENABLE | unix.EV_CLEAR | unix.EV_RECEIPT,
			// Directory NOTE_ATTRIB also covers harmless timestamp changes.
			// Revalidate root identity/owner/mode below; only namespace loss
			// events revoke continuously. Keep all file tampering events.
			Fflags: unix.NOTE_DELETE | unix.NOTE_RENAME | unix.NOTE_REVOKE,
		},
		{
			Ident: uint64(lease.file), Filter: unix.EVFILT_VNODE,
			Flags: unix.EV_ADD | unix.EV_ENABLE | unix.EV_CLEAR | unix.EV_RECEIPT,
			Fflags: unix.NOTE_DELETE | unix.NOTE_RENAME | unix.NOTE_REVOKE | unix.NOTE_ATTRIB |
				unix.NOTE_LINK | unix.NOTE_WRITE | unix.NOTE_EXTEND,
		},
	}
	receipts := make([]unix.Kevent_t, len(changes))
	n, err := unix.Kevent(lease.queue, changes, receipts, nil)
	if err != nil || n != len(changes) {
		return nil, ErrUnsupported
	}
	for index, receipt := range receipts {
		if receipt.Ident != changes[index].Ident || receipt.Filter != changes[index].Filter ||
			receipt.Flags&unix.EV_ERROR == 0 || receipt.Data != 0 {
			return nil, ErrUnsupported
		}
	}
	if err = unix.Flock(lease.file, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, computer.ErrBusy
		}
		return nil, computer.ErrUnavailable
	}
	if err = lease.check(); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	failed = false
	return lease, nil
}

func (lease *darwinFileLease) validFile(info unix.Stat_t) bool {
	return info.Uid == lease.uid && info.Mode&unix.S_IFMT == unix.S_IFREG &&
		info.Mode&0o7777 == 0o600 && info.Nlink == 1 && info.Size == 0
}

func sameDarwinFile(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Gen == b.Gen
}

func (lease *darwinFileLease) check() (err error) {
	if lease == nil || lease.lost || lease.file < 0 || lease.queue < 0 || lease.root < 0 {
		return computer.ErrStale
	}
	defer func() {
		if err != nil {
			lease.lost = true
		}
	}()
	// Drain before and after identity checks. File delete/restore or chmod/restore
	// is terminal; matching numbers at the end must not resurrect authority.
	// Root timestamps are not authority; current root identity/owner/mode are.
	if err = lease.checkEvents(); err != nil {
		return err
	}
	var root, namedRoot, file, namedFile unix.Stat_t
	if unix.Fstat(lease.root, &root) != nil || !sameDarwinFile(root, lease.rootInfo) ||
		root.Uid != lease.rootInfo.Uid || root.Mode != lease.rootInfo.Mode ||
		unix.Fstatat(unix.AT_FDCWD, lease.rootPath, &namedRoot, unix.AT_SYMLINK_NOFOLLOW) != nil ||
		!sameDarwinFile(root, namedRoot) ||
		unix.Fstat(lease.file, &file) != nil || !lease.validFile(file) || !sameDarwinFile(file, lease.fileInfo) ||
		unix.Fstatat(lease.root, lease.name, &namedFile, unix.AT_SYMLINK_NOFOLLOW) != nil ||
		!lease.validFile(namedFile) || !sameDarwinFile(file, namedFile) {
		return fmt.Errorf("%w: native lock identity changed", computer.ErrStale)
	}
	return lease.checkEvents()
}

func (lease *darwinFileLease) checkEvents() error {
	var events [1]unix.Kevent_t
	n, err := unix.Kevent(lease.queue, nil, events[:], &unix.Timespec{})
	if err != nil {
		return fmt.Errorf("%w: native lock event read failed", computer.ErrStale)
	}
	if n != 0 {
		source := "file"
		if events[0].Ident == uint64(lease.root) {
			source = "directory"
		}
		// Safe native metadata only: no path, UID, app content or raw syscall
		// error. Public input checks still sanitize the result to ErrStale.
		return fmt.Errorf("%w: native lock %s event flags %#x", computer.ErrStale, source, events[0].Fflags)
	}
	return nil
}

func (lease *darwinFileLease) close() error {
	if lease == nil {
		return nil
	}
	lease.lost = true
	var result error
	for _, descriptor := range []*int{&lease.queue, &lease.file, &lease.root} {
		if *descriptor >= 0 {
			fd := *descriptor
			*descriptor = -1
			if unix.Close(fd) != nil {
				result = computer.ErrUnavailable
			}
		}
	}
	// No LOCK_UN, unlink, chmod, contents, stale-file cleanup or app operation.
	return result
}
