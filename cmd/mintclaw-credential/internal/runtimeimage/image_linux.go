//go:build linux

package runtimeimage

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const runtimePath = "/opt/mintclaw-credential/browser-runtime"

// Image owns a pinned read-only mount. Only qualified Root startup may borrow
// its descriptor for a private namespace; no model-facing path/launch API exists.
type Image struct {
	mu    sync.Mutex
	root  *os.File
	mount uint64
}

func rootProcess() bool {
	return os.Getuid() == 0 && os.Geteuid() == 0 && os.Getgid() == 0 && os.Getegid() == 0
}

func Open(ctx context.Context) (*Image, error) { return open(ctx, runtimePath) }

func open(ctx context.Context, pathname string) (*Image, error) {
	if !rootProcess() || ctx == nil || ctx.Err() != nil {
		return nil, errUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	root, err := protectedRoot(ctx, pathname)
	if err != nil {
		return nil, errUnavailable
	}
	keep := false
	defer func() {
		if !keep {
			_ = root.Close()
		}
	}()
	mount, err := readOnlyMount(int(root.Fd()))
	if err != nil {
		return nil, errUnavailable
	}
	manifest, err := openRegular(int(root.Fd()), manifestName, mount)
	if err != nil {
		return nil, errUnavailable
	}
	var manifestStat unix.Stat_t
	if unix.Fstat(int(manifest.Fd()), &manifestStat) != nil || manifestStat.Mode&0o7777 != 0o444 {
		_ = manifest.Close()
		return nil, errUnavailable
	}
	data, readErr := io.ReadAll(io.LimitReader(manifest, maxManifestBytes+1))
	closeErr := manifest.Close()
	if readErr != nil || closeErr != nil || ctx.Err() != nil {
		return nil, errUnavailable
	}
	entries, err := decodeManifest(data)
	if err != nil {
		return nil, errUnavailable
	}
	walker := inventory{entries: entries, mount: mount}
	if walker.directory(ctx, root, "", 0) != nil || len(entries) != 0 || ctx.Err() != nil {
		return nil, errUnavailable
	}
	keep = true
	return &Image{root: root, mount: mount}, nil
}

// BorrowRoot duplicates the already-admitted mount, never a later pathname
// lookup. The caller owns the returned descriptor and must retain it through
// mount-namespace setup. This does not admit any executable or launch arguments.
func (image *Image) BorrowRoot(ctx context.Context) (*os.File, error) {
	if image == nil || !rootProcess() || ctx == nil || ctx.Err() != nil {
		return nil, errUnavailable
	}
	image.mu.Lock()
	defer image.mu.Unlock()
	if image.root == nil {
		return nil, errUnavailable
	}
	mount, err := readOnlyMount(int(image.root.Fd()))
	if err != nil || mount != image.mount {
		return nil, errUnavailable
	}
	fd, err := unix.FcntlInt(image.root.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, errUnavailable
	}
	return os.NewFile(uintptr(fd), "private credential runtime mount"), nil
}

func (image *Image) Close() error {
	if image == nil {
		return nil
	}
	image.mu.Lock()
	defer image.mu.Unlock()
	if image.root == nil {
		return nil
	}
	file := image.root
	image.root = nil
	if file.Close() != nil {
		return errUnavailable
	}
	return nil
}

func protectedRoot(ctx context.Context, pathname string) (*os.File, error) {
	if !strings.HasPrefix(pathname, "/") || !validRelativePath(strings.TrimPrefix(pathname, "/")) {
		return nil, errUnavailable
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errUnavailable
	}
	for _, part := range strings.Split(strings.TrimPrefix(pathname, "/"), "/") {
		if protectedDirectory(fd, false) != nil || ctx.Err() != nil {
			_ = unix.Close(fd)
			return nil, errUnavailable
		}
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, errUnavailable
		}
		fd = next
	}
	if protectedDirectory(fd, true) != nil {
		_ = unix.Close(fd)
		return nil, errUnavailable
	}
	return os.NewFile(uintptr(fd), "private credential runtime mount"), nil
}

func noAttribute(fd int, name string) error {
	var value [1]byte
	_, err := unix.Fgetxattr(fd, name, value[:])
	if errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOTSUP) {
		return nil
	}
	return errUnavailable
}

func protectedDirectory(fd int, exact bool) error {
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != 0 || stat.Gid != 0 ||
		stat.Mode&0o7022 != 0 || (exact && stat.Mode&0o7777 != 0o555) ||
		noAttribute(fd, "system.posix_acl_access") != nil || noAttribute(fd, "system.posix_acl_default") != nil {
		return errUnavailable
	}
	return nil
}

func readOnlyMount(fd int) (uint64, error) {
	var fs unix.Statfs_t
	var stat unix.Statx_t
	if unix.Fstatfs(fd, &fs) != nil || (fs.Type != unix.TMPFS_MAGIC && fs.Type != unix.SQUASHFS_MAGIC) ||
		fs.Flags&unix.ST_RDONLY == 0 || fs.Flags&unix.ST_NOEXEC != 0 ||
		unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &stat) != nil ||
		stat.Mask&unix.STATX_MNT_ID == 0 || stat.Mnt_id == 0 {
		return 0, errUnavailable
	}
	return stat.Mnt_id, nil
}

// O_PATH inspects type before opening data, preventing FIFO/device access.
// The data-open follows only this live pin, then verifies its identity again.
func openRegular(parent int, name string, mount uint64) (*os.File, error) {
	pin, err := unix.Openat(parent, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errUnavailable
	}
	defer func() { _ = unix.Close(pin) }()
	var original unix.Stat_t
	if unix.Fstat(pin, &original) != nil || !regularMetadata(original) {
		return nil, errUnavailable
	}
	fd, err := unix.Open("/proc/self/fd/"+strconv.Itoa(pin), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, errUnavailable
	}
	var stat unix.Stat_t
	actual, mountErr := readOnlyMount(fd)
	if unix.Fstat(fd, &stat) != nil || !regularMetadata(stat) || original.Dev != stat.Dev || original.Ino != stat.Ino ||
		mountErr != nil || actual != mount || noAttribute(fd, "system.posix_acl_access") != nil ||
		noAttribute(fd, "security.capability") != nil {
		_ = unix.Close(fd)
		return nil, errUnavailable
	}
	return os.NewFile(uintptr(fd), "private credential runtime file"), nil
}

func regularMetadata(stat unix.Stat_t) bool {
	mode := stat.Mode & 0o7777
	return stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Uid == 0 && stat.Gid == 0 && stat.Nlink == 1 &&
		(mode == 0o444 || mode == 0o555) && stat.Size >= 0 && stat.Size <= maxFileBytes
}

type inventory struct {
	entries map[string]entry
	mount   uint64
	count   int
}

func (walker *inventory) directory(ctx context.Context, directory *os.File, prefix string, depth int) error {
	fd := int(directory.Fd())
	mount, err := readOnlyMount(fd)
	if depth > 24 || mount != walker.mount || err != nil || protectedDirectory(fd, true) != nil {
		return errUnavailable
	}
	for {
		names, readErr := directory.Readdirnames(128)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return errUnavailable
		}
		for _, name := range names {
			walker.count++
			if walker.count > maxFiles*2 || ctx.Err() != nil || !validRelativePath(prefix+name) {
				return errUnavailable
			}
			if prefix == "" && name == manifestName {
				continue
			}
			var stat unix.Stat_t
			if unix.Fstatat(fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW) != nil {
				return errUnavailable
			}
			if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
				child, openErr := unix.Openat(
					fd,
					name,
					unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
					0,
				)
				if openErr != nil {
					return errUnavailable
				}
				file := os.NewFile(uintptr(child), "private credential runtime directory")
				walkErr := walker.directory(ctx, file, prefix+name+"/", depth+1)
				closeErr := file.Close()
				if walkErr != nil || closeErr != nil {
					return errUnavailable
				}
				continue
			}
			value, found := walker.entries[prefix+name]
			if !found {
				return errUnavailable
			}
			file, openErr := openRegular(fd, name, walker.mount)
			if openErr != nil {
				return errUnavailable
			}
			verifyErr := verifyFile(ctx, file, prefix+name, value)
			closeErr := file.Close()
			if verifyErr != nil || closeErr != nil {
				return errUnavailable
			}
			delete(walker.entries, prefix+name)
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
	}
}

func verifyFile(ctx context.Context, file *os.File, name string, value entry) error {
	var before, after unix.Stat_t
	fd := int(file.Fd())
	if unix.Fstat(fd, &before) != nil || before.Mode&0o7777 != value.mode || before.Size != value.size {
		return errUnavailable
	}
	hash := sha256.New()
	buffer := make([]byte, 32768)
	var count int64
	for {
		if ctx.Err() != nil {
			return errUnavailable
		}
		n, err := file.Read(buffer)
		if n > 0 {
			if count == 0 && (name == NodePath || name == ChromiumPath) &&
				(n < 4 || string(buffer[:4]) != "\x7fELF") {
				return errUnavailable
			}
			count += int64(n)
			if count > value.size {
				return errUnavailable
			}
			_, _ = hash.Write(buffer[:n])
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || n == 0 {
			return errUnavailable
		}
	}
	if count != value.size || string(hash.Sum(nil)) != string(value.digest[:]) ||
		unix.Fstat(fd, &after) != nil || before != after || ctx.Err() != nil {
		return errUnavailable
	}
	return nil
}
