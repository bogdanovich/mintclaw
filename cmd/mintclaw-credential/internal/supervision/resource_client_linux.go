//go:build linux

package supervision

import (
	"context"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/internal/credentialipc"
)

// ResourceWorker owns one allocation connection and its received endpoints.
// Trusted broker integration must bind the original context to its actual
// LoginLease, close adopted proxy/channel copies on Done, and qualify the real
// browser before retrieving a secret. This handle supplies no grant authority.
type ResourceWorker struct {
	mu         sync.Mutex
	rpc        sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
	connection *net.UnixConn
	peer       credentialipc.Peer
	files      []*os.File
	exported   bool
	started    bool
	retiring   atomic.Bool
	once       sync.Once
	done       chan struct{}
	result     error
}

// AllocateResources uses only the fixed endpoint and dedicated current broker
// role. The allocation context is retained, never replaced by a later call.
func AllocateResources(ctx context.Context) (*ResourceWorker, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, errUnavailable
	}
	uid, gid, err := resourceBrokerRole()
	if err != nil || os.Getuid() != int(uid) || os.Geteuid() != int(uid) ||
		os.Getgid() != int(gid) || os.Getegid() != int(gid) {
		return nil, errUnavailable
	}
	return allocateResources(ctx, resourceDirectoryPath+"/"+resourceSocketName)
}

func allocateResources(ctx context.Context, path string) (result *ResourceWorker, resultErr error) {
	resourceCtx, cancel := context.WithTimeout(ctx, 20*time.Hour)
	callCtx, endCall := context.WithTimeout(resourceCtx, 5*time.Second)
	defer endCall()
	connection, err := (&net.Dialer{}).DialContext(callCtx, "unixpacket", path)
	if err != nil {
		cancel()
		return nil, errUnavailable
	}
	packet, ok := connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		cancel()
		return nil, errUnavailable
	}
	worker := &ResourceWorker{ctx: resourceCtx, cancel: cancel, connection: packet, done: make(chan struct{})}
	defer func() {
		if resultErr != nil {
			worker.finish(errUnavailable)
		}
	}()
	if prepareResourceConnection(packet) != nil {
		return nil, errUnavailable
	}
	worker.peer, err = credentialipc.ConnectedPeer(packet)
	if err != nil || worker.peer.UID != 0 || worker.peer.GID != 0 {
		return nil, errUnavailable
	}
	go worker.watch()
	if writeResourcePacket(callCtx, packet, worker.peer, allocateOperation, 0, nil) != nil {
		return nil, errUnavailable
	}
	files, err := readResourcePacket(callCtx, packet, worker.peer, allocateOperation, 1, 2)
	if err != nil {
		return nil, errUnavailable
	}
	worker.mu.Lock()
	if resourceCtx.Err() != nil || !qualifyResourceEndpoints(files, worker.peer) {
		worker.mu.Unlock()
		for _, file := range files {
			_ = file.Close()
		}
		return nil, errUnavailable
	}
	worker.files = files
	worker.mu.Unlock()
	if resourceCtx.Err() != nil || !worker.peer.Alive() {
		return nil, errUnavailable
	}
	return worker, nil
}

func prepareResourceConnection(connection *net.UnixConn) error {
	raw, err := connection.SyscallConn()
	if err != nil {
		return errUnavailable
	}
	var socketErr error
	err = raw.Control(func(fd uintptr) {
		kind, kindErr := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_TYPE)
		if kindErr != nil || kind != unix.SOCK_SEQPACKET {
			socketErr = errUnavailable
			return
		}
		socketErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PASSCRED, 1)
	})
	if err != nil || socketErr != nil {
		return errUnavailable
	}
	return nil
}

func qualifyResourceEndpoints(files []*os.File, peer credentialipc.Peer) bool {
	if len(files) != 2 || files[0] == nil || files[1] == nil {
		return false
	}
	for _, file := range files {
		flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
		kind, kindErr := unix.GetsockoptInt(int(file.Fd()), unix.SOL_SOCKET, unix.SO_TYPE)
		if err != nil || flags&unix.FD_CLOEXEC == 0 || kindErr != nil || kind != unix.SOCK_STREAM {
			return false
		}
	}
	listener := int(files[0].Fd())
	address, err := unix.Getsockname(listener)
	loopback, ok := address.(*unix.SockaddrInet4)
	listening, listenErr := unix.GetsockoptInt(listener, unix.SOL_SOCKET, unix.SO_ACCEPTCONN)
	if err != nil || !ok || loopback.Addr != [4]byte{127, 0, 0, 1} || loopback.Port == 0 ||
		listenErr != nil || listening != 1 {
		return false
	}
	control := int(files[1].Fd())
	creator, err := unix.GetsockoptUcred(control, unix.SOL_SOCKET, unix.SO_PEERCRED)
	return err == nil && creator.Pid == peer.PID && creator.Uid == 0 && creator.Gid == 0 &&
		unnamedUnixEndpoint(control, unix.SYS_GETSOCKNAME) && unnamedUnixEndpoint(control, unix.SYS_GETPEERNAME)
}

// EndpointFiles exports each owned File once. Any duplicates made while
// adopting them remain the broker's responsibility and must follow Done.
func (worker *ResourceWorker) EndpointFiles(ctx context.Context) (*os.File, *os.File, error) {
	if worker == nil || worker.ctx == nil || ctx == nil || ctx.Err() != nil {
		return nil, nil, errUnavailable
	}
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.ctx.Err() != nil || worker.exported || len(worker.files) != 2 {
		return nil, nil, errUnavailable
	}
	worker.exported = true
	return worker.files[0], worker.files[1], nil
}

func (worker *ResourceWorker) Start(ctx context.Context) error {
	if worker == nil || worker.ctx == nil || ctx == nil || ctx.Err() != nil {
		return errUnavailable
	}
	worker.rpc.Lock()
	defer worker.rpc.Unlock()
	worker.mu.Lock()
	valid := worker.ctx.Err() == nil && worker.exported && !worker.started && !worker.retiring.Load()
	if valid {
		worker.started = true
	}
	worker.mu.Unlock()
	if !valid {
		return errUnavailable
	}
	if worker.call(ctx, startOperation) != nil {
		worker.finish(errUnavailable)
		return errUnavailable
	}
	return nil // This acknowledges initial exec, not Node/Chromium readiness.
}

// Retire requires the broker to close adopted proxy/channel copies first. Its
// successful reply proves Root verified forest retirement; no retries occur.
func (worker *ResourceWorker) Retire(ctx context.Context) error {
	if worker == nil || worker.ctx == nil || ctx == nil || ctx.Err() != nil {
		return errUnavailable
	}
	worker.rpc.Lock()
	defer worker.rpc.Unlock()
	worker.mu.Lock()
	valid := worker.ctx.Err() == nil && worker.started && !worker.retiring.Swap(true)
	for _, file := range worker.files {
		_ = file.Close()
	}
	worker.files = nil
	worker.mu.Unlock()
	if !valid {
		worker.finish(errUnavailable)
		return errUnavailable
	}
	err := worker.call(ctx, retireOperation)
	worker.finish(err)
	<-worker.done
	return worker.result
}

func (worker *ResourceWorker) call(ctx context.Context, operation byte) error {
	callCtx, endCall := context.WithTimeout(worker.ctx, 5*time.Second)
	defer endCall()
	unwatch := context.AfterFunc(ctx, func() { endCall(); worker.finish(errUnavailable) })
	defer unwatch()
	if writeResourcePacket(callCtx, worker.connection, worker.peer, operation, 0, nil) != nil {
		return errUnavailable
	}
	_, err := readResourcePacket(callCtx, worker.connection, worker.peer, operation, 1, 0)
	return err
}

func (worker *ResourceWorker) watch() {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-worker.ctx.Done():
			worker.finish(errUnavailable)
			return
		case <-ticker.C:
			// Explicit retirement owns the final reply and closes the connection.
			if !worker.peer.Alive() || !worker.retiring.Load() && !credentialipc.ConnectionAlive(worker.connection) {
				worker.finish(errUnavailable)
				return
			}
		}
	}
}

func (worker *ResourceWorker) finish(result error) {
	worker.once.Do(func() {
		worker.cancel()
		_ = worker.connection.Close()
		worker.mu.Lock()
		for _, file := range worker.files {
			_ = file.Close()
		}
		worker.files = nil
		worker.result = result
		worker.mu.Unlock()
		worker.peer.Close()
		close(worker.done)
	})
}

// Close withdraws this allocation immediately. The server must verify cleanup
// after EOF; a caller needing its explicit acknowledgment uses Retire instead.
func (worker *ResourceWorker) Close() {
	if worker != nil && worker.ctx != nil {
		worker.finish(errUnavailable)
	}
}

func (worker *ResourceWorker) Done() <-chan struct{} {
	if worker == nil {
		return nil
	}
	return worker.done
}
