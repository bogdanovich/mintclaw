//go:build linux

package approval

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/internal/credentialipc"
)

const consoleFixturePath = "/run/mintclaw-credential-owner/setup.sock"

func requireConsoleMarker(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/.dockerenv"); err != nil {
		t.Fatal("console qualification requires Docker")
	}
	marker, err := os.ReadFile("/qual-phone/synthetic-only")
	if err != nil || string(marker) != "mintclaw-synthetic-phone-v1\n" {
		t.Fatal("console qualification marker missing")
	}
}

func TestPrivateConsoleFixture(t *testing.T) {
	if os.Getenv("MINTCLAW_SYNTHETIC_PHONE_CONSOLE_FIXTURE") != "1" {
		t.Skip("disposable console fixture only")
	}
	requireConsoleMarker(t)
	if os.Getuid() != 0 || os.Getgid() != 0 {
		t.Fatal("console fixture requires its disposable root supervisor")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	service := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConsoleChild$", "-test.timeout=25s")
	service.Env = []string{"MINTCLAW_SYNTHETIC_PHONE_CONSOLE_CHILD=service"}
	service.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65532, Gid: 65532}}
	ready, err := service.StdoutPipe()
	if err != nil {
		t.Fatal("console readiness pipe unavailable")
	}
	service.Stderr = io.Discard
	if service.Start() != nil {
		t.Fatal("console fixture could not launch")
	}
	t.Cleanup(func() { _ = service.Process.Kill(); _ = service.Wait() })
	line := make(chan string, 1)
	go func() { text, _ := bufio.NewReader(ready).ReadString('\n'); line <- text }()
	select {
	case text := <-line:
		if text != "ready\n" {
			t.Fatal("console did not become ready")
		}
	case <-ctx.Done():
		t.Fatal("console readiness timed out")
	}
	directory, err := openConsoleDirectory(65532, 65532)
	if err != nil {
		t.Fatal("qualified console directory refused")
	}
	defer func() { _ = unix.Close(directory) }()
	invite, err := requestConsoleInvite(ctx, directory, 65532, 65532)
	if err != nil || !validInvite(invite) {
		t.Fatal("live root console failed")
	}
	clear(invite)
	if _, err = requestConsoleInvite(ctx, directory, 65531, 65531); err == nil {
		t.Fatal("console accepted a different service identity")
	}
	for _, mode := range []string{"nonroot", "handoff"} {
		child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConsoleChild$", "-test.timeout=5s")
		child.Env = []string{"MINTCLAW_SYNTHETIC_PHONE_CONSOLE_CHILD=" + mode}
		child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65532, Gid: 65532}}
		if mode == "handoff" {
			connection, connectErr := net.DialUnix(
				"unixpacket",
				nil,
				&net.UnixAddr{Name: consoleFixturePath, Net: "unixpacket"},
			)
			if connectErr != nil {
				t.Fatal("handoff fixture connection failed")
			}
			file, fileErr := connection.File()
			_ = connection.Close()
			if fileErr != nil {
				t.Fatal("handoff fixture descriptor failed")
			}
			child.ExtraFiles = []*os.File{file}
			if child.Run() != nil {
				_ = file.Close()
				t.Fatal("root-created socket handoff was admitted")
			}
			_ = file.Close()
		} else if child.Run() != nil {
			t.Fatal("nonroot console caller was admitted")
		}
	}
	connection, err := net.DialUnix("unixpacket", nil, &net.UnixAddr{Name: consoleFixturePath, Net: "unixpacket"})
	if err != nil {
		t.Fatal("rights fixture connection failed")
	}
	null, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal("rights fixture descriptor failed")
	}
	_, _, _ = connection.WriteMsgUnix([]byte(consoleRequest), unix.UnixRights(int(null.Fd())), nil)
	_ = null.Close()
	if connection.SetReadDeadline(time.Now().Add(time.Second)) != nil {
		t.Fatal("rights fixture deadline failed")
	}
	var data [1024]byte
	count, _, _, _, _ := connection.ReadMsgUnix(data[:], nil)
	_ = connection.Close()
	if count > 0 {
		t.Fatal("console accepted ancillary rights")
	}

	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal("operator terminal fixture unavailable")
	}
	defer func() { _ = master.Close(); _ = slave.Close() }()
	operator := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConsoleChild$", "-test.timeout=5s")
	operator.Env = []string{"MINTCLAW_SYNTHETIC_PHONE_CONSOLE_CHILD=operator"}
	operator.Stdin = slave
	operator.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	var standard bytes.Buffer
	operator.Stdout, operator.Stderr = &standard, &standard
	if operator.Run() != nil {
		t.Fatal("protected operator enrollment failed")
	}
	fd := int(master.Fd())
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	if _, err = unix.Poll(fds, 1000); err != nil || fds[0].Revents&unix.POLLIN == 0 {
		t.Fatal("setup link was not written to the private terminal")
	}
	count, err = unix.Read(fd, data[:])
	if err != nil || count == 0 {
		t.Fatal("private setup link unavailable")
	}
	private := strings.ReplaceAll(string(data[:count]), "\r", "")
	link := ""
	for _, line := range strings.Split(private, "\n") {
		if strings.HasPrefix(line, testOrigin+"/#enroll=") {
			link = line
		}
	}
	if !validInvite([]byte(link)) || strings.Contains(standard.String(), "#enroll=") {
		t.Fatal("setup link was lost or leaked to stdout/stderr")
	}
	clear(data[:])
	if os.Chmod("/run/mintclaw-credential-owner", 0o755) != nil {
		t.Fatal("directory fixture mutation failed")
	}
	if bad, badErr := openConsoleDirectory(65532, 65532); badErr == nil {
		_ = unix.Close(bad)
		t.Fatal("public console directory accepted")
	}
	if os.Chmod("/run/mintclaw-credential-owner", 0o700) != nil {
		t.Fatal("directory fixture restore failed")
	}
}

func TestConsoleChild(t *testing.T) {
	mode := os.Getenv("MINTCLAW_SYNTHETIC_PHONE_CONSOLE_CHILD")
	if mode == "" {
		t.Skip("disposable console child only")
	}
	requireConsoleMarker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if mode == "operator" {
		if Enroll(ctx) != nil {
			t.Fatal("protected terminal setup failed")
		}
		return
	}
	if os.Getuid() != 65532 || os.Getgid() != 65532 {
		t.Fatal("service fixture identity invalid")
	}
	if mode == "service" {
		fixture := newFixture(t)
		enrollment, err := NewEnrollment(fixture.setup)
		if err != nil {
			t.Fatal("service enrollment unavailable")
		}
		listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: consoleFixturePath, Net: "unixpacket"})
		if err != nil {
			t.Fatal("service console listener unavailable")
		}
		defer func() { _ = listener.Close() }()
		if os.Chmod(consoleFixturePath, 0o600) != nil || credentialipc.PrepareListener(listener) != nil {
			t.Fatal("service console preparation failed")
		}
		if _, err = io.WriteString(os.Stdout, "ready\n"); err != nil {
			t.Fatal("fixture readiness failed")
		}
		if enrollment.ServeConsole(ctx, listener) != nil {
			t.Fatal("service console failed")
		}
		return
	}
	var connection *net.UnixConn
	var err error
	switch mode {
	case "handoff":
		file := os.NewFile(3, "synthetic-handoff")
		borrowed, borrowErr := net.FileConn(file)
		_ = file.Close()
		if borrowErr != nil {
			t.Fatal("handoff descriptor unavailable")
		}
		var ok bool
		connection, ok = borrowed.(*net.UnixConn)
		if !ok {
			_ = borrowed.Close()
			t.Fatal("handoff socket type invalid")
		}
	case "nonroot":
		connection, err = net.DialUnix("unixpacket", nil, &net.UnixAddr{Name: consoleFixturePath, Net: "unixpacket"})
	default:
		t.Fatal("unsupported console fixture mode")
	}
	if err != nil {
		t.Fatal("nonroot fixture connection unavailable")
	}
	defer func() { _ = connection.Close() }()
	if connection.SetDeadline(time.Now().Add(time.Second)) != nil {
		t.Fatal("nonroot deadline failed")
	}
	_, _, _ = connection.WriteMsgUnix([]byte(consoleRequest), nil, nil)
	var data [1024]byte
	count, _, _, _, _ := connection.ReadMsgUnix(data[:], nil)
	if count > 0 {
		t.Fatal("untrusted sender received an enrollment link")
	}
}
