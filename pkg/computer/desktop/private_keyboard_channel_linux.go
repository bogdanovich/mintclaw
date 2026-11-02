//go:build linux && mintclaw_private_native_keyboard

package desktop

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/pkg/computer"
)

type privateKeyboardToken [32]byte

func (token *privateKeyboardToken) UnmarshalJSON(raw []byte) error {
	var values []uint16
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &values) != nil || len(values) != len(token) {
		return computer.ErrUnavailable
	}
	for index, value := range values {
		if value > 255 {
			return computer.ErrUnavailable
		}
		token[index] = byte(value)
	}
	return nil
}

type privateKeyboardOperation struct {
	Channel    privateKeyboardToken `json:"channel"`
	Generation uint64               `json:"generation"`
	Nonce      privateKeyboardToken `json:"nonce"`
}

func (operation *privateKeyboardOperation) UnmarshalJSON(raw []byte) error {
	type plain privateKeyboardOperation
	var value plain
	if privateKeyboardDecode(raw, &value) != nil || value.Generation == 0 ||
		value.Channel == (privateKeyboardToken{}) || value.Nonce == (privateKeyboardToken{}) {
		return computer.ErrUnavailable
	}
	*operation = privateKeyboardOperation(value)
	return nil
}

type privateKeyboardResources string

const (
	privateKeyboardNotCreated privateKeyboardResources = "not_created"
	privateKeyboardRetained   privateKeyboardResources = "retained"
	privateKeyboardUnverified privateKeyboardResources = "unverified_retained"
)

type privateKeyboardChannelState uint8

const (
	privateKeyboardChannelReady privateKeyboardChannelState = iota
	privateKeyboardChannelBusy
	privateKeyboardChannelEnded
	privateKeyboardChannelUnknown
	privateKeyboardChannelClosed
)

type privateKeyboardChannelConfig struct {
	Executable string
	SHA256     string
	BudgetMS   uint32
}

type privateKeyboardServer struct {
	PID  int32  `json:"pid"`
	UID  uint32 `json:"uid"`
	Root uint32 `json:"root"`
}

type privateKeyboardDesktop struct {
	Socket     string
	Screen     int
	Server     privateKeyboardServer
	AuthDigest [sha256.Size]byte
}

func privateKeyboardDesktopOf(bootstrap privateKeyboardBootstrap) privateKeyboardDesktop {
	return privateKeyboardDesktop{
		Socket: bootstrap.Socket, Screen: bootstrap.Screen, Server: bootstrap.Server,
		AuthDigest: sha256.Sum256(bootstrap.Auth[:]),
	}
}

// A channel retains the original desktop connection/resources. It holds no
// target guard, lease or action text. Input authority belongs to current only.
type privateKeyboardChannel struct {
	ctx        context.Context
	id         privateKeyboardToken
	desktop    privateKeyboardDesktop
	wire       *net.UnixConn
	process    *exec.Cmd
	exited     chan struct{}
	processErr error      // published by closing exited
	mu         sync.Mutex // current identity, state and ALL writes
	readMu     sync.Mutex // one reader; also action protocol state
	state      privateKeyboardChannelState
	current    *privateKeyboard
	generation uint64
	resources  privateKeyboardResources
	done       chan struct{}
	watchExit  chan struct{}
	doneOnce   sync.Once
	closeDone  chan struct{}
	closeErr   error // published by closing closeDone
}

func (*privateKeyboardChannel) String() string           { return "private native keyboard channel" }
func (channel *privateKeyboardChannel) GoString() string { return channel.String() }

func openPrivateKeyboardChannel(
	ctx context.Context, lease *InputLease, request WindowRequest, identity WindowIdentity,
	config privateKeyboardChannelConfig,
) (*privateKeyboardChannel, error) {
	if ctx.Err() != nil || config.BudgetMS == 0 || config.BudgetMS > 3600000 {
		return nil, computer.ErrInvalid
	}
	bootstrap, _, err := privateKeyboardAuthority(lease, request, identity)
	if err != nil {
		return nil, err
	}
	defer clear(bootstrap.Auth[:])
	executable, err := openPrivateKeyboardExecutable(config.Executable, config.SHA256)
	if err != nil {
		return nil, err
	}
	defer func() { _ = executable.Close() }()
	descriptors, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, computer.ErrUnavailable
	}
	parent, child := os.NewFile(uintptr(descriptors[0]), "private keyboard host"),
		os.NewFile(uintptr(descriptors[1]), "private keyboard child")
	defer func() { _ = parent.Close(); _ = child.Close() }()
	connection, err := net.FileConn(parent)
	if err != nil {
		return nil, computer.ErrUnavailable
	}
	command := exec.Command("/proc/self/fd/3", "--keeper")
	command.ExtraFiles, command.Stdin = []*os.File{executable}, child
	command.Env = []string{"CUA_TELEMETRY=0", "DO_NOT_TRACK=1"}
	channel := &privateKeyboardChannel{
		ctx: ctx, desktop: privateKeyboardDesktopOf(bootstrap), wire: connection.(*net.UnixConn), process: command,
		exited: make(chan struct{}), generation: 1, resources: privateKeyboardNotCreated,
		done: make(chan struct{}), watchExit: make(chan struct{}), closeDone: make(chan struct{}),
	}
	if _, err = rand.Read(channel.id[:]); err != nil || channel.id == (privateKeyboardToken{}) {
		_ = connection.Close()
		return nil, computer.ErrUnavailable
	}
	if err = command.Start(); err != nil {
		_ = connection.Close()
		return nil, computer.ErrUnavailable
	}
	go func() { channel.processErr = command.Wait(); close(channel.exited) }()
	go func() {
		defer close(channel.watchExit)
		select {
		case <-ctx.Done():
			channel.fail()
		case <-channel.done:
		}
	}()
	channel.mu.Lock()
	err = channel.sendLocked("open", struct {
		Channel   privateKeyboardToken     `json:"channel"`
		Bootstrap privateKeyboardBootstrap `json:"bootstrap"`
		BudgetMS  uint32                   `json:"budget_ms"`
	}{channel.id, bootstrap, config.BudgetMS})
	channel.mu.Unlock()
	if err == nil {
		channel.readMu.Lock()
		packet, readErr := channel.readPacket()
		if readErr != nil {
			err = readErr
		} else {
			var opened struct {
				Channel privateKeyboardToken `json:"channel"`
			}
			if packet.Kind != "opened" || privateKeyboardDecode(packet.Data, &opened) != nil ||
				opened.Channel != channel.id {
				err = computer.ErrUnavailable
			}
			clear(packet.Data)
		}
		channel.readMu.Unlock()
	}
	if err != nil {
		channel.fail()
		_ = channel.close()
		return nil, computer.ErrUnavailable
	}
	return channel, nil
}

// Caller holds mu. Guarding current and writing under the SAME lock prevents a
// late old callback from queuing a frame that a later action reader could see.
func (channel *privateKeyboardChannel) sendLocked(kind string, data any) error {
	encoded, err := json.Marshal(struct {
		Kind string `json:"kind"`
		Data any    `json:"data"`
	}{kind, data})
	defer clear(encoded)
	if err != nil || len(encoded) == 0 || len(encoded) > privateKeyboardFrameLimit {
		return computer.ErrInvalid
	}
	if channel.wire.SetWriteDeadline(time.Now().Add(250*time.Millisecond)) != nil {
		return computer.ErrUnavailable
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(encoded)))
	for _, part := range [][]byte{header[:], encoded} {
		for len(part) > 0 {
			n, writeErr := channel.wire.Write(part)
			if writeErr != nil || n == 0 {
				return computer.ErrUnavailable
			}
			part = part[n:]
		}
	}
	return nil
}

// Caller holds readMu. Payload is returned only to the sole current reader.
func (channel *privateKeyboardChannel) readPacket() (privateKeyboardReply, error) {
	var packet privateKeyboardReply
	if channel.wire.SetReadDeadline(time.Now().Add(3*time.Second)) != nil {
		return packet, computer.ErrUnavailable
	}
	var header [4]byte
	if _, err := io.ReadFull(channel.wire, header[:]); err != nil {
		return packet, computer.ErrUnavailable
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 || length > privateKeyboardFrameLimit {
		return packet, computer.ErrUnavailable
	}
	encoded := make([]byte, length)
	defer clear(encoded)
	if _, err := io.ReadFull(channel.wire, encoded); err != nil {
		return packet, computer.ErrUnavailable
	}
	if privateKeyboardDecode(encoded, &packet) != nil {
		return packet, computer.ErrUnavailable
	}
	return packet, nil
}

func (channel *privateKeyboardChannel) fail() {
	channel.mu.Lock()
	if channel.state == privateKeyboardChannelClosed {
		channel.mu.Unlock()
		return
	}
	channel.state, channel.resources = privateKeyboardChannelUnknown, privateKeyboardUnverified
	channel.mu.Unlock()
	channel.doneOnce.Do(func() { close(channel.done) })
	_ = channel.wire.Close()
}

// Only an idle channel can close normally. Closing a channel is NOT disposal:
// the native server may retain devices, explicitly reported as unverified.
func (channel *privateKeyboardChannel) close() error {
	channel.readMu.Lock()
	channel.mu.Lock()
	if channel.state == privateKeyboardChannelClosed {
		channel.mu.Unlock()
		channel.readMu.Unlock()
		<-channel.closeDone
		return channel.closeErr
	}
	if channel.current != nil && channel.state != privateKeyboardChannelUnknown {
		channel.mu.Unlock()
		channel.readMu.Unlock()
		return computer.ErrBusy
	}
	previous, resources := channel.state, channel.resources
	channel.state = privateKeyboardChannelClosed
	channel.mu.Unlock()
	channel.doneOnce.Do(func() { close(channel.done) })
	<-channel.watchExit
	var result error
	if previous == privateKeyboardChannelReady {
		channel.mu.Lock()
		err := channel.sendLocked("close", struct {
			Channel privateKeyboardToken `json:"channel"`
		}{channel.id})
		channel.mu.Unlock()
		packet, readErr := channel.readPacket()
		var closed struct {
			Channel   privateKeyboardToken     `json:"channel"`
			Resources privateKeyboardResources `json:"resource_disposition"`
		}
		expected := privateKeyboardUnverified
		if resources == privateKeyboardNotCreated {
			expected = privateKeyboardNotCreated
		}
		if err != nil || readErr != nil || packet.Kind != "closed" ||
			privateKeyboardDecode(packet.Data, &closed) != nil || closed.Channel != channel.id ||
			closed.Resources != expected {
			result = computer.ErrUnavailable
			resources = privateKeyboardUnverified
		} else {
			resources = closed.Resources
		}
		clear(packet.Data)
	} else if previous != privateKeyboardChannelEnded {
		result = computer.ErrUnavailable
		resources = privateKeyboardUnverified
	}
	_ = channel.wire.Close()
	channel.readMu.Unlock()
	select {
	case <-channel.exited:
		if channel.processErr != nil {
			result = computer.ErrUnavailable
		}
	case <-time.After(time.Second):
		// This retained os.Process only. A kill/wait never supplies a receipt.
		_ = channel.process.Process.Kill()
		result = computer.ErrUnavailable
	}
	channel.mu.Lock()
	channel.resources, channel.closeErr = resources, result
	channel.mu.Unlock()
	close(channel.closeDone)
	return result
}
