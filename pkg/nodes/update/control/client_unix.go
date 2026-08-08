//go:build linux || darwin

package control

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const EnvironmentFD = "MINTCLAW_NODE_COORDINATOR_FD"

type Client struct {
	file    *os.File
	codec   *Codec
	mu      sync.Mutex
	pending map[string]chan Response
	done    chan struct{}
	once    sync.Once
	err     error
}

func ClientFromEnvironment() (*Client, bool, error) {
	value, present := os.LookupEnv(EnvironmentFD)
	if !present {
		return nil, false, nil
	}
	if value != "3" {
		return nil, true, errors.New("coordinator control descriptor must be fd 3")
	}
	fd, err := strconv.Atoi(value)
	if err != nil {
		return nil, true, errors.New("invalid coordinator control descriptor")
	}
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return nil, true, errors.New("coordinator control descriptor is not a socket")
	}
	unix.CloseOnExec(fd)
	if err = os.Unsetenv(EnvironmentFD); err != nil {
		_ = unix.Close(fd)
		return nil, true, errors.New("clear coordinator control environment")
	}
	file := os.NewFile(uintptr(fd), "mintclaw-node-coordinator")
	if file == nil {
		return nil, true, errors.New("open coordinator control descriptor")
	}
	codec, err := NewCodec(file, file)
	if err != nil {
		_ = file.Close()
		return nil, true, err
	}
	client := &Client{
		file: file, codec: codec, pending: make(map[string]chan Response), done: make(chan struct{}),
	}
	go client.readResponses()
	return client, true, nil
}

func (client *Client) Call(ctx context.Context, request Request) (Response, error) {
	if client == nil {
		return Response{}, errors.New("coordinator control client is unavailable")
	}
	if request.RequestID == "" {
		requestID, err := NewRequestID()
		if err != nil {
			return Response{}, err
		}
		request.RequestID = requestID
	}
	request.SchemaVersion = SchemaVersion
	response := make(chan Response, 1)
	client.mu.Lock()
	if client.err != nil {
		err := client.err
		client.mu.Unlock()
		return Response{}, err
	}
	if _, duplicate := client.pending[request.RequestID]; duplicate {
		client.mu.Unlock()
		return Response{}, errors.New("duplicate coordinator request identity")
	}
	client.pending[request.RequestID] = response
	client.mu.Unlock()
	if err := client.codec.WriteRequest(request, time.Now().UTC()); err != nil {
		client.removePending(request.RequestID)
		return Response{}, err
	}
	select {
	case result := <-response:
		return result, nil
	case <-ctx.Done():
		client.removePending(request.RequestID)
		return Response{}, ctx.Err()
	case <-client.done:
		client.mu.Lock()
		err := client.err
		client.mu.Unlock()
		if err == nil {
			err = errors.New("coordinator control connection closed")
		}
		return Response{}, err
	}
}

func (client *Client) ReportHealth(health Health) error {
	if client == nil {
		return errors.New("coordinator control client is unavailable")
	}
	health.SchemaVersion = SchemaVersion
	health.Kind = KindHealth
	return client.codec.WriteHealth(health)
}

func (client *Client) ParentDone() <-chan struct{} {
	if client == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return client.done
}

func (client *Client) Close() error {
	if client == nil {
		return nil
	}
	err := client.file.Close()
	client.finish(errors.New("coordinator control connection closed"))
	return err
}

func (client *Client) readResponses() {
	for {
		response, err := client.codec.ReadResponse()
		if err != nil {
			client.finish(err)
			return
		}
		client.mu.Lock()
		pending := client.pending[response.RequestID]
		delete(client.pending, response.RequestID)
		client.mu.Unlock()
		if pending != nil {
			pending <- response
		}
	}
}

func (client *Client) removePending(requestID string) {
	client.mu.Lock()
	delete(client.pending, requestID)
	client.mu.Unlock()
}

func (client *Client) finish(err error) {
	client.once.Do(func() {
		client.mu.Lock()
		client.err = err
		client.mu.Unlock()
		close(client.done)
	})
}
