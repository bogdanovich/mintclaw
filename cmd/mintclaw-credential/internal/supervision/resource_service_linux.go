//go:build linux

package supervision

import (
	"context"
	"net"
	"os"
	"os/user"
	"strconv"
	"time"

	"github.com/bogdanovich/mintclaw/internal/credentialipc"
)

func resourceBrokerRole() (uint32, uint32, error) {
	broker, err := user.Lookup("mintclaw-credential")
	if err != nil {
		return 0, 0, errUnavailable
	}
	uid, uidErr := strconv.ParseUint(broker.Uid, 10, 32)
	gid, gidErr := strconv.ParseUint(broker.Gid, 10, 32)
	if uidErr != nil || gidErr != nil || uid == 0 || gid == 0 ||
		uid == uint64(^uint32(0)) || gid == uint64(^uint32(0)) ||
		strconv.FormatUint(uid, 10) != broker.Uid || strconv.FormatUint(gid, 10) != broker.Gid {
		return 0, 0, errUnavailable
	}
	return uint32(uid), uint32(gid), nil
}

// ServeResources exposes only the fixed allocate/start/retire protocol to the
// dedicated broker. It opens no provider, token, approval policy or model tool.
// Root resource lifetime is an upper bound, never authorization to use secrets.
func ServeResources(ctx context.Context) (resultErr error) {
	if !root() || ctx == nil || ctx.Err() != nil {
		return errUnavailable
	}
	launcher, err := OpenLauncher(ctx)
	if err != nil {
		return errUnavailable
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if launcher.Close(cleanupCtx) != nil {
			resultErr = errUnavailable
		}
	}()
	uid, gid, err := resourceBrokerRole()
	if err != nil || uid == launcher.uid || gid == launcher.gid {
		return errUnavailable
	}
	endpoint, err := openResourceListener(resourceDirectoryPath, gid)
	if err != nil {
		return errUnavailable
	}
	defer func() {
		if endpoint.close() != nil {
			resultErr = errUnavailable
		}
	}()
	return serveResourceConnections(ctx, endpoint.listener, launcher, uid, gid)
}

func serveResourceConnections(ctx context.Context, listener *net.UnixListener, launcher *Launcher,
	brokerUID, brokerGID uint32,
) error {
	watch := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer watch()
	for ctx.Err() == nil {
		connection, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return errUnavailable
		}
		peer, peerErr := credentialipc.ConnectedPeer(connection)
		if peerErr == nil && peer.UID == brokerUID && peer.GID == brokerGID {
			serveResourceConnection(ctx, connection, peer, launcher)
		}
		_ = connection.Close()
		peer.Close()
		launcher.mu.Lock()
		failed := launcher.quarantined
		launcher.mu.Unlock()
		if failed {
			return errUnavailable
		}
	}
	return nil
}

func serveResourceConnection(ctx context.Context, connection *net.UnixConn, peer credentialipc.Peer,
	launcher *Launcher,
) {
	resourceCtx, cancel := context.WithTimeout(ctx, 20*time.Hour)
	defer cancel()
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-resourceCtx.Done():
				_ = connection.Close()
				return
			case <-ticker.C:
				if !peer.Alive() {
					cancel()
				}
			}
		}
	}()
	defer func() { cancel(); <-watchDone }()
	callCtx, endCall := context.WithTimeout(resourceCtx, 5*time.Second)
	_, err := readResourcePacket(callCtx, connection, peer, allocateOperation, 0, 0)
	endCall()
	if err != nil {
		return
	}
	worker, err := launcher.Prepare(resourceCtx)
	if err != nil {
		return
	}
	defer func() {
		cleanupCtx, endCleanup := context.WithTimeout(context.Background(), 3*time.Second)
		defer endCleanup()
		_ = worker.Retire(cleanupCtx)
	}()
	listener, control, err := worker.Endpoints(resourceCtx)
	if err != nil {
		return
	}
	callCtx, endCall = context.WithTimeout(resourceCtx, 5*time.Second)
	err = writeResourcePacket(callCtx, connection, peer, allocateOperation, 1, []*os.File{listener, control})
	_ = listener.Close()
	_ = control.Close()
	endCall()
	if err != nil {
		return
	}
	callCtx, endCall = context.WithTimeout(resourceCtx, 5*time.Second)
	_, err = readResourcePacket(callCtx, connection, peer, startOperation, 0, 0)
	if err == nil {
		err = worker.Start(callCtx)
	}
	if err == nil {
		err = writeResourcePacket(callCtx, connection, peer, startOperation, 1, nil)
	}
	endCall()
	if err != nil {
		return
	}
	stopWorkerWatch, workerWatchDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(workerWatchDone)
		select {
		case <-worker.Done():
			cancel()
		case <-resourceCtx.Done():
		case <-stopWorkerWatch:
		}
	}()
	_, err = readResourcePacket(resourceCtx, connection, peer, retireOperation, 0, 0)
	close(stopWorkerWatch)
	<-workerWatchDone
	if err != nil {
		return
	}
	cleanupCtx, endCleanup := context.WithTimeout(context.Background(), 3*time.Second)
	err = worker.Retire(cleanupCtx)
	endCleanup()
	if err != nil {
		return
	}
	callCtx, endCall = context.WithTimeout(resourceCtx, time.Second)
	defer endCall()
	_ = writeResourcePacket(callCtx, connection, peer, retireOperation, 1, nil)
}
