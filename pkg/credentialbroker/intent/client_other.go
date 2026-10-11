//go:build !linux

package intent

import "context"

type Client struct {
	SocketPath string
	ServerUID  uint32
	ServerGID  uint32
}

func (Client) Call(context.Context, Request) (Response, error) {
	return Response{}, ErrUnavailable
}
