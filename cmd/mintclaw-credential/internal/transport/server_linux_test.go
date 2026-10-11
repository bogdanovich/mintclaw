//go:build linux

package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker/intent"
)

const testReference = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

var testPrincipal = credentialbroker.Principal{OwnerID: "owner", ActorID: "actor", AgentID: "agent", HostID: "host"}

type transportFixture struct {
	caller *Caller
	client intent.Client
	cancel context.CancelFunc
}

func newTransportFixture(t *testing.T, handler Handler) transportFixture {
	t.Helper()
	if os.Geteuid() == 0 || os.Getegid() == 0 {
		t.Skip("private transport fixtures require an unprivileged test identity")
	}
	dir, err := os.MkdirTemp("", "credential-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "socket")
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	// Establish sender-credential reception before exposing a ready listener.
	if err := prepareListener(listener); err != nil {
		t.Fatal(err)
	}
	caller, err := PinCaller(CallerConfig{
		PID: int32(os.Getpid()), UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
		Principal: testPrincipal, Accounts: []string{"facebook", "ebay"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(caller.Close)
	server, err := NewServer(caller, handler)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, ErrUnavailable) {
				t.Error("private server did not shut down safely")
			}
		case <-time.After(3 * time.Second):
			t.Error("private server workers did not terminate")
		}
	})
	return transportFixture{
		caller,
		intent.Client{SocketPath: path, ServerUID: uint32(os.Geteuid()), ServerGID: uint32(os.Getegid())},
		cancel,
	}
}

func TestAuthenticatedIntentUsesTrustedPrincipalAndBoundedCall(t *testing.T) {
	retained := make(chan Call, 1)
	fixture := newTransportFixture(t, func(ctx context.Context, call Call) (intent.Response, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second || call.Principal() != testPrincipal || call.Authorize() != nil {
			return intent.Response{}, errors.New("trusted call authority was missing")
		}
		request := call.Request()
		if request.Operation != intent.RequestApproval || len(request.Accounts) != 1 ||
			request.Accounts[0] != "facebook" {
			return intent.Response{}, errors.New("typed intent scope was changed")
		}
		request.Accounts[0] = "modified"
		if call.Request().Accounts[0] != "facebook" {
			return intent.Response{}, errors.New("handler mutated immutable call scope")
		}
		retained <- call
		return intent.Response{Code: intent.ApprovalRequired, ApprovalID: testReference, ValidForSeconds: 300}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	response, err := fixture.client.Call(ctx, intent.Request{
		Operation: intent.RequestApproval, Accounts: []string{"facebook"}, DurationSeconds: 72000,
	})
	if err != nil || response.Code != intent.ApprovalRequired || response.ApprovalID != testReference {
		t.Fatal("authenticated typed intent did not reach its trusted handler")
	}
	call := <-retained
	deadline := time.Now().Add(time.Second)
	for call.Authorize() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !errors.Is(call.Authorize(), ErrUnavailable) {
		t.Fatal("completed call retained authority")
	}
}

func TestUnknownAccountAndMalformedIdentityNeverReachHandler(t *testing.T) {
	var calls atomic.Int32
	fixture := newTransportFixture(t, func(context.Context, Call) (intent.Response, error) {
		calls.Add(1)
		return intent.Response{Code: intent.Ready}, nil
	})
	for _, data := range [][]byte{
		[]byte(`{"version":1,"operation":"status","owner_id":"SYNTHETIC_PRIVATE_CANARY"}`),
		[]byte(`{"version":1,"operation":"status","operation":"login"}`),
		[]byte(`{"version":1,"operation":"request_approval","accounts":["unknown"],"duration_seconds":1}`),
		[]byte(`{"version":1,"operation":"login","account":"unknown","grant_id":"` + testReference + `"}`),
	} {
		packet, err := net.DialUnix(
			"unixpacket",
			nil,
			&net.UnixAddr{Name: fixture.client.SocketPath, Net: "unixpacket"},
		)
		if err != nil {
			t.Fatal(err)
		}
		if packet.SetDeadline(time.Now().Add(2*time.Second)) != nil {
			t.Fatal("fixture deadline failed")
		}
		_, _, err = packet.WriteMsgUnix(data, nil, nil)
		if err != nil {
			t.Fatal("fixture packet failed")
		}
		buffer := make([]byte, 512)
		count, _, _, _, readErr := packet.ReadMsgUnix(buffer, nil)
		_ = packet.Close()
		response, decodeErr := intent.DecodeResponse(intent.Status, buffer[:count])
		if readErr != nil || decodeErr != nil || response.Code != intent.Denied ||
			bytes.Contains(buffer, []byte("CANARY")) {
			t.Fatal("invalid authority was accepted or echoed")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unadmitted input reached the private consumer")
	}
}

func TestPrivateHandlerFailuresNeverEscapeResponseBoundary(t *testing.T) {
	const canary = "SYNTHETIC_PRIVATE_HANDLER_CANARY"
	for _, scenario := range []string{"error", "panic", "invalid_response"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newTransportFixture(t, func(context.Context, Call) (intent.Response, error) {
				switch scenario {
				case "error":
					return intent.Response{Code: intent.Ready}, errors.New(canary)
				case "panic":
					panic(canary)
				default:
					return intent.Response{Code: intent.Ready, SessionID: canary}, nil
				}
			})
			response, err := fixture.client.Call(context.Background(), intent.Request{Operation: intent.Status})
			if err != nil || response != (intent.Response{Code: intent.Failed}) {
				t.Fatal("private handler diagnostic or invalid authority escaped")
			}
		})
	}
}

func TestClientCancellationStopsPrivateHandlerAndDoesNotReplay(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	fixture := newTransportFixture(t, func(ctx context.Context, call Call) (intent.Response, error) {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		defer close(stopped)
		if call.Authorize() == nil {
			return intent.Response{}, errors.New("canceled call kept authority")
		}
		return intent.Response{Code: intent.OutcomeUnknown}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := fixture.client.Call(ctx, intent.Request{Operation: intent.Status}); done <- err }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("fixture handler never started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, intent.ErrUnavailable) {
			t.Fatal("canceled client did not fail closed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client cancellation did not close its connection")
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("private consumer cancellation was not delivered")
	}
	if calls.Load() != 1 {
		t.Fatal("canceled operation was replayed")
	}
}

func TestPacketTruncationAndDescriptorTransferAreDenied(t *testing.T) {
	var calls atomic.Int32
	fixture := newTransportFixture(t, func(context.Context, Call) (intent.Response, error) {
		calls.Add(1)
		return intent.Response{Code: intent.Ready}, nil
	})
	for _, scenario := range []string{"oversized", "rights"} {
		packet, err := net.DialUnix(
			"unixpacket",
			nil,
			&net.UnixAddr{Name: fixture.client.SocketPath, Net: "unixpacket"},
		)
		if err != nil {
			t.Fatal(err)
		}
		_ = packet.SetDeadline(time.Now().Add(2 * time.Second))
		data, _ := intent.EncodeRequest(intent.Request{Operation: intent.Status})
		var control []byte
		var file *os.File
		if scenario == "oversized" {
			data = bytes.Repeat([]byte{'x'}, intent.MaxPacketBytes+2)
		} else {
			file, err = os.Open("/dev/null")
			if err != nil {
				t.Fatal(err)
			}
			control = unix.UnixRights(int(file.Fd()))
		}
		_, _, sendErr := packet.WriteMsgUnix(data, control, nil)
		if file != nil {
			_ = file.Close()
		}
		buffer := make([]byte, 128)
		count, _, _, _, readErr := packet.ReadMsgUnix(buffer, nil)
		_ = packet.Close()
		if sendErr != nil || count > 0 ||
			!errors.Is(readErr, io.EOF) && !errors.Is(readErr, unix.ECONNRESET) {
			t.Fatal("unsafe packet reached a credential outcome")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unsafe packet invoked a consumer")
	}
}

func TestSocketHandoffCannotImpersonatePinnedCreator(t *testing.T) {
	var calls atomic.Int32
	fixture := newTransportFixture(t, func(context.Context, Call) (intent.Response, error) {
		calls.Add(1)
		return intent.Response{Code: intent.Ready}, nil
	})
	packet, err := net.DialUnix("unixpacket", nil, &net.UnixAddr{Name: fixture.client.SocketPath, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = packet.Close() }()
	file, err := packet.File()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestTransportSubprocessFixture$")
	command.Env = []string{"MINTCLAW_CREDENTIAL_TRANSPORT_FIXTURE=handoff"}
	command.ExtraFiles = []*os.File{file}
	if err := command.Run(); err != nil {
		t.Fatal("handed-off descriptor fixture did not observe rejection")
	}
	if calls.Load() != 0 {
		t.Fatal("descriptor handoff impersonated the trusted caller")
	}
}

func TestTransportSubprocessFixture(t *testing.T) {
	mode := os.Getenv("MINTCLAW_CREDENTIAL_TRANSPORT_FIXTURE")
	if mode != "handoff" && mode != "connect" {
		return
	}
	var connection net.Conn
	var err error
	if mode == "handoff" {
		file := os.NewFile(3, "synthetic handoff socket")
		connection, err = net.FileConn(file)
		_ = file.Close()
	} else {
		connection, err = net.Dial("unixpacket", os.Getenv("MINTCLAW_CREDENTIAL_TRANSPORT_SOCKET"))
	}
	if err != nil {
		os.Exit(1)
	}
	packet, ok := connection.(*net.UnixConn)
	if !ok || packet.SetDeadline(time.Now().Add(2*time.Second)) != nil {
		os.Exit(1)
	}
	data, _ := intent.EncodeRequest(intent.Request{Operation: intent.Status})
	_, _, err = packet.WriteMsgUnix(data, nil, nil)
	if err != nil {
		// An unpinned connection is closed before reading a request. Depending
		// on scheduling, that denial is observed while writing or reading.
		if mode == "connect" && (errors.Is(err, unix.EPIPE) || errors.Is(err, unix.ECONNRESET)) {
			_ = packet.Close()
			os.Exit(0)
		}
		os.Exit(1)
	}
	buffer := make([]byte, 128)
	count, _, _, _, readErr := packet.ReadMsgUnix(buffer, nil)
	_ = packet.Close()
	// recvmsg can return a negative byte count with ECONNRESET when the
	// server closes before consuming the unpinned process's queued packet.
	if count > 0 || !errors.Is(readErr, io.EOF) && !errors.Is(readErr, unix.ECONNRESET) {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestUnpinnedProcessCannotAcquireCallerAuthority(t *testing.T) {
	var calls atomic.Int32
	fixture := newTransportFixture(t, func(context.Context, Call) (intent.Response, error) {
		calls.Add(1)
		return intent.Response{Code: intent.Ready}, nil
	})
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestTransportSubprocessFixture$")
	command.Env = []string{
		"MINTCLAW_CREDENTIAL_TRANSPORT_FIXTURE=connect",
		"MINTCLAW_CREDENTIAL_TRANSPORT_SOCKET=" + fixture.client.SocketPath,
	}
	if err := command.Run(); err != nil {
		t.Fatalf("unpinned process fixture did not observe rejection: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("an arbitrary same-UID process impersonated the trusted caller")
	}
}

func TestClientRejectsWrongServerIdentity(t *testing.T) {
	var calls atomic.Int32
	fixture := newTransportFixture(t, func(context.Context, Call) (intent.Response, error) {
		calls.Add(1)
		return intent.Response{Code: intent.Ready}, nil
	})
	fixture.client.ServerUID++
	response, err := fixture.client.Call(context.Background(), intent.Request{Operation: intent.Status})
	if !errors.Is(err, intent.ErrUnavailable) || response.Code != "" || calls.Load() != 0 {
		t.Fatal("wrong service identity was accepted")
	}
}

func TestClosedCallerInvalidatesInFlightAuthority(t *testing.T) {
	started := make(chan struct{})
	fixture := newTransportFixture(t, func(ctx context.Context, call Call) (intent.Response, error) {
		close(started)
		<-ctx.Done()
		if call.Authorize() == nil {
			return intent.Response{}, errors.New("closed caller retained authority")
		}
		return intent.Response{Code: intent.Failed}, nil
	})
	done := make(chan error, 1)
	go func() {
		_, err := fixture.client.Call(context.Background(), intent.Request{Operation: intent.Status})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("fixture handler did not start")
	}
	fixture.caller.Close()
	select {
	case err := <-done:
		if !errors.Is(err, intent.ErrUnavailable) {
			t.Fatal("closed caller did not invalidate its request")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closed caller did not stop its in-flight consumer")
	}
}

func TestZeroAndUntrustedCallerInputsFailClosed(t *testing.T) {
	if (Call{}).Authorize() == nil || (Call{}).Principal() != (credentialbroker.Principal{}) {
		t.Fatal("zero call acquired authority")
	}
	if (&Server{}).Serve(context.Background(), nil) == nil {
		t.Fatal("uninitialized server started")
	}
	for _, config := range []CallerConfig{
		{},
		{PID: int32(os.Getpid()), UID: 0, GID: 1, Principal: testPrincipal},
		{PID: int32(os.Getpid()), UID: 1, GID: 1, Principal: testPrincipal, Accounts: []string{"a", "a"}},
		{PID: int32(os.Getpid()), UID: 1, GID: 1, Principal: credentialbroker.Principal{OwnerID: "SYNTHETIC_PRIVATE_INPUT"}},
	} {
		caller, err := PinCaller(config)
		if !errors.Is(err, ErrUnavailable) || caller != nil || strings.Contains(err.Error(), "SYNTHETIC") {
			t.Fatal("untrusted startup authority was accepted or echoed")
		}
	}
}

func TestPrivateConsumerConcurrencyIsBounded(t *testing.T) {
	entered := make(chan struct{}, maxConcurrentCalls)
	var active, peak atomic.Int32
	fixture := newTransportFixture(t, func(ctx context.Context, _ Call) (intent.Response, error) {
		count := active.Add(1)
		defer active.Add(-1)
		for previous := peak.Load(); count > previous; previous = peak.Load() {
			if peak.CompareAndSwap(previous, count) {
				break
			}
		}
		entered <- struct{}{}
		<-ctx.Done()
		return intent.Response{Code: intent.Failed}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{}, maxConcurrentCalls)
	for range maxConcurrentCalls {
		go func() {
			_, _ = fixture.client.Call(ctx, intent.Request{Operation: intent.Status})
			finished <- struct{}{}
		}()
	}
	for range maxConcurrentCalls {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("bounded consumer fixtures did not start")
		}
	}
	probeCtx, stopProbe := context.WithTimeout(context.Background(), time.Second)
	defer stopProbe()
	_, err := fixture.client.Call(probeCtx, intent.Request{Operation: intent.Status})
	if !errors.Is(err, intent.ErrUnavailable) || peak.Load() != maxConcurrentCalls {
		t.Fatal("private consumer capacity was exceeded")
	}
	cancel()
	for range maxConcurrentCalls {
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Fatal("bounded caller fixtures did not terminate")
		}
	}
}
