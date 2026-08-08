//go:build !linux && !darwin

package control

import (
	"context"
	"errors"
)

type Client struct{}

func ClientFromEnvironment() (*Client, bool, error) { return nil, false, nil }

func (client *Client) ReportHealth(Health) error {
	return errors.New("coordinator control is supported only on Linux and macOS")
}

func (client *Client) Call(context.Context, Request) (Response, error) {
	return Response{}, errors.New("coordinator control is supported only on Linux and macOS")
}

func (client *Client) ParentDone() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}

func (client *Client) Close() error { return nil }
