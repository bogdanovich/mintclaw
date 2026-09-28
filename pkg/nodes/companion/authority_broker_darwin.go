//go:build darwin

package companion

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	AuthorityBrokerEnvironmentFD  = "MINTCLAW_NODE_AUTHORITY_FD"
	authorityBrokerDarwinFD       = 3
	authorityBrokerFactoryRequest = byte(1)
	authorityBrokerFactoryBusy    = byte(2)
)

var errDarwinAuthorityBrokerBusy = errors.New("macOS authority broker is busy")

type AuthorityBrokerClient struct {
	factory *net.UnixConn
	mu      sync.Mutex
	close   sync.Once
}

func (client *AuthorityBrokerClient) Close() error {
	if client == nil {
		return nil
	}
	var closeErr error
	client.close.Do(func() {
		if client.factory != nil {
			closeErr = client.factory.Close()
		}
	})
	return closeErr
}

func (*AuthorityBrokerClient) SupportsConfirmedCancellation() bool {
	return false
}

func NewAuthorityBrokerClient(endpoint string) (*AuthorityBrokerClient, error) {
	if endpoint != "" {
		return nil, errors.New("macOS authority broker rejects a caller-selected endpoint")
	}
	value, present := os.LookupEnv(AuthorityBrokerEnvironmentFD)
	if !present || value != strconv.Itoa(authorityBrokerDarwinFD) {
		return nil, errors.New("macOS authority broker capability is unavailable")
	}
	if err := os.Unsetenv(AuthorityBrokerEnvironmentFD); err != nil {
		return nil, errors.New("clear macOS authority broker environment")
	}
	file := os.NewFile(authorityBrokerDarwinFD, "mintclaw-authority-factory")
	if file == nil {
		return nil, errors.New("open macOS authority broker capability")
	}
	defer func() { _ = file.Close() }()
	var stat unix.Stat_t
	if err := unix.Fstat(authorityBrokerDarwinFD, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return nil, errors.New("macOS authority broker capability is not a socket")
	}
	connection, err := net.FileConn(file)
	if err != nil {
		return nil, errors.New("adopt macOS authority broker capability")
	}
	factory, ok := connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		return nil, errors.New("macOS authority broker capability is not Unix")
	}
	return newDarwinAuthorityBrokerClient(factory), nil
}

func newDarwinAuthorityBrokerClient(factory *net.UnixConn) *AuthorityBrokerClient {
	return &AuthorityBrokerClient{factory: factory}
}

func (client *AuthorityBrokerClient) Snapshot(ctx context.Context) (ShellBrokerSnapshot, error) {
	response, err := client.call(ctx, authorityBrokerRequestFrame{
		Version: AuthorityBrokerProtocolVersion,
		Action:  authorityBrokerActionSnapshot,
	})
	if err != nil {
		return ShellBrokerSnapshot{}, err
	}
	if !response.OK || response.Snapshot == nil || response.Result != nil {
		return ShellBrokerSnapshot{}, errors.New("authority broker returned invalid snapshot response")
	}
	return normalizeShellBrokerSnapshot(*response.Snapshot)
}

func (client *AuthorityBrokerClient) Execute(
	ctx context.Context,
	request ShellBrokerRequest,
) (ShellBrokerResult, error) {
	response, err := client.call(ctx, authorityBrokerRequestFrame{
		Version: AuthorityBrokerProtocolVersion,
		Action:  authorityBrokerActionExecute,
		Execute: &request,
	})
	if err != nil {
		return ShellBrokerResult{}, err
	}
	if response.Code == "UNKNOWN" {
		return ShellBrokerResult{}, ErrShellBrokerOutcomeUnknown
	}
	if !response.OK || response.Result == nil || response.Snapshot != nil {
		return ShellBrokerResult{}, fmt.Errorf(
			"%w: authority broker returned invalid execution response",
			ErrShellBrokerOutcomeUnknown,
		)
	}
	return *response.Result, nil
}

func (client *AuthorityBrokerClient) call(
	ctx context.Context,
	request authorityBrokerRequestFrame,
) (authorityBrokerResponseFrame, error) {
	connection, err := client.acquire(ctx)
	if err != nil {
		if request.Action == authorityBrokerActionExecute {
			return authorityBrokerResponseFrame{}, fmt.Errorf("%w: %w", ErrShellBrokerOutcomeUnknown, err)
		}
		return authorityBrokerResponseFrame{}, err
	}
	defer func() { _ = connection.Close() }()
	if err = writeAuthorityBrokerFrame(connection, request); err != nil {
		if request.Action == authorityBrokerActionExecute {
			return authorityBrokerResponseFrame{}, fmt.Errorf(
				"%w: write authority broker request: %w",
				ErrShellBrokerOutcomeUnknown,
				err,
			)
		}
		return authorityBrokerResponseFrame{}, fmt.Errorf("write authority broker request: %w", err)
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.CloseWrite()
			_ = connection.SetReadDeadline(time.Now().Add(time.Second))
		case <-done:
		}
	}()
	var response authorityBrokerResponseFrame
	if err = readAuthorityBrokerFrame(connection, &response); err != nil {
		if request.Action == authorityBrokerActionExecute {
			return authorityBrokerResponseFrame{}, fmt.Errorf(
				"%w: read authority broker response: %w",
				ErrShellBrokerOutcomeUnknown,
				err,
			)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return authorityBrokerResponseFrame{}, ctxErr
		}
		return authorityBrokerResponseFrame{}, fmt.Errorf("read authority broker response: %w", err)
	}
	if response.Version != AuthorityBrokerProtocolVersion {
		if request.Action == authorityBrokerActionExecute {
			return authorityBrokerResponseFrame{}, fmt.Errorf(
				"%w: authority broker response version is invalid",
				ErrShellBrokerOutcomeUnknown,
			)
		}
		return authorityBrokerResponseFrame{}, errors.New("authority broker response version is invalid")
	}
	return response, nil
}

func (client *AuthorityBrokerClient) acquire(ctx context.Context) (*net.UnixConn, error) {
	if client == nil || client.factory == nil {
		return nil, errors.New("authority broker client is unavailable")
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := client.factory.SetDeadline(time.Now().Add(authorityBrokerHandshakeTimeout)); err != nil {
		return nil, err
	}
	defer func() { _ = client.factory.SetDeadline(time.Time{}) }()
	if written, writeErr := client.factory.Write(
		[]byte{authorityBrokerFactoryRequest},
	); writeErr != nil ||
		written != 1 {
		_ = client.factory.Close()
		return nil, errors.New("request macOS authority broker connection")
	}
	payload := make([]byte, 1)
	control := make([]byte, unix.CmsgSpace(4))
	n, controlN, flags, _, err := client.factory.ReadMsgUnix(payload, control)
	if err != nil || n != 1 || flags&unix.MSG_CTRUNC != 0 {
		_ = client.factory.Close()
		return nil, errors.New("receive macOS authority broker connection")
	}
	if payload[0] == authorityBrokerFactoryBusy && controlN == 0 {
		return nil, errDarwinAuthorityBrokerBusy
	}
	if payload[0] != authorityBrokerFactoryRequest {
		closeDarwinAuthorityReceivedRights(control[:controlN])
		_ = client.factory.Close()
		return nil, errors.New("receive macOS authority broker connection")
	}
	messages, err := unix.ParseSocketControlMessage(control[:controlN])
	if err != nil || len(messages) != 1 {
		closeDarwinAuthorityReceivedRights(control[:controlN])
		_ = client.factory.Close()
		return nil, errors.New("parse macOS authority broker connection")
	}
	descriptors, err := unix.ParseUnixRights(&messages[0])
	if err != nil || len(descriptors) != 1 {
		for _, descriptor := range descriptors {
			_ = unix.Close(descriptor)
		}
		_ = client.factory.Close()
		return nil, errors.New("parse macOS authority broker descriptor")
	}
	file := os.NewFile(uintptr(descriptors[0]), "mintclaw-authority-call")
	if file == nil {
		_ = unix.Close(descriptors[0])
		return nil, errors.New("open macOS authority broker connection")
	}
	defer func() { _ = file.Close() }()
	connection, err := net.FileConn(file)
	if err != nil {
		return nil, errors.New("adopt macOS authority broker connection")
	}
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		return nil, errors.New("macOS authority broker connection is not Unix")
	}
	if err := ctx.Err(); err != nil {
		_ = unixConnection.Close()
		return nil, err
	}
	return unixConnection, nil
}
