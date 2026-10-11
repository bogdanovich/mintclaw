//go:build linux

package intent

import (
	"context"
	"net"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/internal/credentialipc"
)

// Client is configured by the trusted runtime. The socket path and dedicated
// server identity must be protected by qualified deployment. Call performs one
// attempt and never automatically replays a failed or uncertain operation.
type Client struct {
	SocketPath string
	ServerUID  uint32
	ServerGID  uint32
}

func (client Client) Call(ctx context.Context, request Request) (Response, error) {
	if ctx == nil || ctx.Err() != nil || client.ServerUID == 0 || client.ServerGID == 0 {
		return Response{}, ErrUnavailable
	}
	data, err := EncodeRequest(request)
	if err != nil {
		return Response{}, ErrInvalid
	}
	defer clear(data)
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(callCtx, "unixpacket", client.SocketPath)
	if err != nil {
		return Response{}, ErrUnavailable
	}
	defer func() { _ = connection.Close() }()
	packet, ok := connection.(*net.UnixConn)
	if !ok {
		return Response{}, ErrUnavailable
	}
	peer, err := credentialipc.ConnectedPeer(packet)
	if err != nil {
		return Response{}, ErrUnavailable
	}
	defer peer.Close()
	if peer.UID != client.ServerUID || peer.GID != client.ServerGID {
		return Response{}, ErrUnavailable
	}
	deadline, _ := callCtx.Deadline()
	if packet.SetDeadline(deadline) != nil {
		return Response{}, ErrUnavailable
	}
	finished := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-callCtx.Done():
			_ = packet.Close()
		case <-finished:
		}
	}()
	defer func() { close(finished); <-watchDone }()
	count, _, err := packet.WriteMsgUnix(data, nil, nil)
	if err != nil || count != len(data) {
		return Response{}, ErrUnavailable
	}
	buffer := make([]byte, MaxPacketBytes+1)
	defer clear(buffer)
	count, _, flags, err := credentialipc.ReadPacket(packet, buffer, nil)
	if err != nil || count == 0 || count > MaxPacketBytes || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 ||
		callCtx.Err() != nil || !peer.Alive() {
		return Response{}, ErrUnavailable
	}
	response, err := DecodeResponse(request.Operation, buffer[:count])
	if err != nil {
		return Response{}, ErrUnavailable
	}
	return response, nil
}
