//go:build linux

package supervision

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const parentPath = "/sys/fs/cgroup/mintclaw-credential-browser.slice"

// Supervisor owns one worker at a time. A failed retirement permanently denies
// new work on this instance; a fresh instance also refuses any leftover group.
type Supervisor struct {
	mu          sync.Mutex
	parent      *os.File
	active      *Forest
	closed      bool
	quarantined bool
}

// Forest holds a newly created, pinned cgroup, not a caller-selected PID or
// process group. Start attaches the initial process atomically with clone3.
type Forest struct {
	supervisor *Supervisor
	group      *os.File
	name       string
	exited     chan struct{}
	done       chan struct{}
	once       sync.Once
	result     error
	unwatch    func() bool
}

func root() bool {
	return os.Getuid() == 0 && os.Geteuid() == 0 && os.Getgid() == 0 && os.Getegid() == 0
}

// Open requires an already configured, empty, protected cgroup-v2 parent.
// The operator, not this API, installs its systemd slice and delegation policy.
func Open() (*Supervisor, error) { return open(parentPath) }

func open(path string) (*Supervisor, error) {
	if !root() {
		return nil, errUnavailable
	}
	parent, err := protectedDirectory(path)
	if err != nil {
		return nil, errUnavailable
	}
	ok := false
	defer func() {
		if !ok {
			_ = parent.Close()
		}
	}()
	var filesystem unix.Statfs_t
	if unix.Fstatfs(int(parent.Fd()), &filesystem) != nil || filesystem.Type != unix.CGROUP2_SUPER_MAGIC {
		return nil, errUnavailable
	}
	current, err := os.ReadFile("/proc/self/cgroup")
	if err != nil || len(current) > 4096 {
		return nil, errUnavailable
	}
	location := strings.TrimSuffix(strings.TrimPrefix(string(current), "0::"), "\n")
	relative := strings.TrimPrefix(path, "/sys/fs/cgroup")
	if !strings.HasPrefix(string(current), "0::/") || strings.Contains(location, "\n") ||
		location == relative || strings.HasPrefix(location, relative+"/") {
		return nil, errUnavailable
	}
	typeData, err := readControl(parent, "cgroup.type")
	if err != nil || typeData != "domain\n" {
		return nil, errUnavailable
	}
	data, err := readControl(parent, "cgroup.events")
	live, parseErr := populated(data)
	if err != nil || parseErr != nil || live {
		return nil, errUnavailable
	}
	entries, err := parent.ReadDir(4097)
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > 4096 {
		return nil, errUnavailable
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, errUnavailable
		}
	}
	ok = true
	return &Supervisor{parent: parent}, nil
}

// Start accepts only a command constructed by protected Root startup. That
// caller must separately qualify the fixed executable/modules, worker role,
// environment, private endpoint, sandbox and network/filesystem boundary.
// This component never receives a model command, secret, or PID.
func (supervisor *Supervisor) Start(ctx context.Context, command *exec.Cmd) (*Forest, error) {
	if supervisor == nil || ctx == nil || ctx.Err() != nil || !root() || command == nil ||
		command.Process != nil || command.Stdin != nil || command.Stdout != nil || command.Stderr != nil ||
		command.Env == nil || !filepath.IsAbs(command.Path) {
		return nil, errUnavailable
	}
	seen := make(map[string]bool)
	for _, entry := range command.Env {
		name, value, found := strings.Cut(entry, "=")
		if !found || seen[name] || len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") {
			return nil, errUnavailable
		}
		seen[name] = true
		switch name {
		case "PATH":
			if value != "/usr/bin:/bin" {
				return nil, errUnavailable
			}
		case "LANG":
			if value != "C.UTF-8" {
				return nil, errUnavailable
			}
		case "TZ":
			if value != "UTC" {
				return nil, errUnavailable
			}
		case "HOME", "TMPDIR":
			if !filepath.IsAbs(value) || filepath.Clean(value) != value {
				return nil, errUnavailable
			}
		default:
			return nil, errUnavailable
		}
	}
	supervisor.mu.Lock()
	if ctx.Err() != nil || supervisor.closed || supervisor.quarantined ||
		supervisor.active != nil || supervisor.parent == nil {
		supervisor.mu.Unlock()
		return nil, errUnavailable
	}
	attributes := syscall.SysProcAttr{}
	if command.SysProcAttr != nil {
		attributes = *command.SysProcAttr
	}
	if attributes.UseCgroupFD || attributes.CgroupFD != 0 || attributes.PidFD != nil {
		supervisor.mu.Unlock()
		return nil, errUnavailable
	}
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		supervisor.mu.Unlock()
		return nil, errUnavailable
	}
	name := "worker-" + hex.EncodeToString(entropy[:])
	parentFD := int(supervisor.parent.Fd())
	if unix.Mkdirat(parentFD, name, 0o700) != nil {
		supervisor.mu.Unlock()
		return nil, errUnavailable
	}
	group, err := openDirectoryAt(parentFD, name)
	if err != nil {
		supervisor.quarantined = true
		supervisor.mu.Unlock()
		return nil, errUnavailable
	}
	forest := &Forest{
		supervisor: supervisor, group: group, name: name,
		exited: make(chan struct{}), done: make(chan struct{}),
	}
	supervisor.active = forest
	attributes.UseCgroupFD, attributes.CgroupFD = true, int(group.Fd())
	command.SysProcAttr = &attributes
	startErr := command.Start()
	if startErr == nil {
		forest.unwatch = context.AfterFunc(ctx, func() { forest.beginRetirement() })
	}
	supervisor.mu.Unlock()
	if startErr != nil {
		close(forest.exited)
		forest.beginRetirement()
		return nil, errUnavailable
	}
	go func() {
		_ = command.Wait() // Raw process diagnostics never leave this boundary.
		close(forest.exited)
		forest.beginRetirement()
	}()
	if ctx.Err() != nil {
		forest.beginRetirement()
		return nil, errUnavailable
	}
	return forest, nil
}

// Retire withdraws this forest permanently and bounds the caller's wait. Only
// cgroup.kill, populated=0, verified group removal, and leader reaping prove
// termination. The bounded cleanup continues if the caller cancels its wait.
func (forest *Forest) Retire(ctx context.Context) error {
	if forest == nil || ctx == nil {
		return errUnavailable
	}
	forest.beginRetirement()
	select {
	case <-forest.done:
		if ctx.Err() == nil {
			return forest.result
		}
	case <-ctx.Done():
	}
	return errUnavailable
}

func (forest *Forest) beginRetirement() {
	forest.once.Do(func() {
		go func() {
			supervisor := forest.supervisor
			supervisor.mu.Lock()
			unwatch := forest.unwatch
			supervisor.mu.Unlock()
			if unwatch != nil {
				unwatch()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
			defer cancel()
			forest.result = forest.killAndVerify(ctx)
			supervisor.mu.Lock()
			if forest.result != nil {
				supervisor.quarantined = true
			} else {
				supervisor.active = nil
			}
			if supervisor.closed && supervisor.active == nil && supervisor.parent != nil {
				if supervisor.parent.Close() != nil {
					forest.result, supervisor.quarantined = errUnavailable, true
				}
				supervisor.parent = nil
			}
			supervisor.mu.Unlock()
			close(forest.done)
		}()
	})
}

func (forest *Forest) killAndVerify(ctx context.Context) error {
	if writeControl(forest.group, "cgroup.kill", "1\n") != nil {
		return errUnavailable
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, err := readControl(forest.group, "cgroup.events")
		live, parseErr := populated(data)
		if err != nil || parseErr != nil || ctx.Err() != nil {
			return errUnavailable
		}
		if !live {
			break
		}
		select {
		case <-ctx.Done():
			return errUnavailable
		case <-ticker.C:
		}
	}
	select {
	case <-forest.exited:
	case <-ctx.Done():
		return errUnavailable
	}
	var owned, named unix.Stat_t
	parentFD := int(forest.supervisor.parent.Fd())
	if unix.Fstat(int(forest.group.Fd()), &owned) != nil ||
		unix.Fstatat(parentFD, forest.name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil ||
		owned.Dev != named.Dev || owned.Ino != named.Ino ||
		unix.Unlinkat(parentFD, forest.name, unix.AT_REMOVEDIR) != nil || ctx.Err() != nil {
		return errUnavailable
	}
	if forest.group.Close() != nil {
		return errUnavailable
	}
	return nil
}

func (supervisor *Supervisor) Close(ctx context.Context) error {
	if supervisor == nil || ctx == nil {
		return errUnavailable
	}
	supervisor.mu.Lock()
	supervisor.closed = true
	forest := supervisor.active
	if forest == nil {
		failed := supervisor.quarantined
		if supervisor.parent != nil {
			failed = supervisor.parent.Close() != nil || failed
			supervisor.parent = nil
		}
		supervisor.mu.Unlock()
		if failed || ctx.Err() != nil {
			return errUnavailable
		}
		return nil
	}
	supervisor.mu.Unlock()
	return forest.Retire(ctx)
}

func protectedDirectory(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(path) > 4096 ||
		!strings.HasPrefix(path, "/sys/fs/cgroup/") || strings.Count(path, "/") > 32 {
		return nil, errUnavailable
	}
	current, err := openDirectoryAt(unix.AT_FDCWD, "/")
	if err != nil {
		return nil, errUnavailable
	}
	for component := range strings.SplitSeq(strings.TrimPrefix(path, "/"), "/") {
		next, nextErr := openDirectoryAt(int(current.Fd()), component)
		closeErr := current.Close()
		if nextErr != nil || closeErr != nil {
			if next != nil {
				_ = next.Close()
			}
			return nil, errUnavailable
		}
		current = next
	}
	return current, nil
}

func openDirectoryAt(parent int, name string) (*os.File, error) {
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errUnavailable
	}
	var stat unix.Stat_t
	_, aclErr := unix.Fgetxattr(fd, "system.posix_acl_access", nil)
	if unix.Fstat(fd, &stat) != nil || stat.Uid != 0 || stat.Gid != 0 || stat.Mode&0o022 != 0 ||
		aclErr != unix.ENODATA && aclErr != unix.EOPNOTSUPP {
		_ = unix.Close(fd)
		return nil, errUnavailable
	}
	return os.NewFile(uintptr(fd), "private-cgroup"), nil
}

func controlFile(directory *os.File, name string, flags int) (*os.File, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errUnavailable
	}
	var stat unix.Stat_t
	var filesystem unix.Statfs_t
	if unix.Fstat(fd, &stat) != nil || stat.Uid != 0 || stat.Gid != 0 || stat.Mode&unix.S_IFMT != unix.S_IFREG ||
		stat.Mode&0o022 != 0 || unix.Fstatfs(fd, &filesystem) != nil ||
		filesystem.Type != unix.CGROUP2_SUPER_MAGIC {
		_ = unix.Close(fd)
		return nil, errUnavailable
	}
	return os.NewFile(uintptr(fd), "private-cgroup-control"), nil
}

func readControl(directory *os.File, name string) (string, error) {
	file, err := controlFile(directory, name, unix.O_RDONLY)
	if err != nil {
		return "", errUnavailable
	}
	data, readErr := io.ReadAll(io.LimitReader(file, 4097))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(data) > 4096 {
		return "", errUnavailable
	}
	return string(data), nil
}

func writeControl(directory *os.File, name, data string) error {
	file, err := controlFile(directory, name, unix.O_WRONLY)
	if err != nil {
		return errUnavailable
	}
	count, writeErr := file.WriteString(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil || count != len(data) {
		return errUnavailable
	}
	return nil
}
