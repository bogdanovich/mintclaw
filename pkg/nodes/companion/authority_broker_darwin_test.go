//go:build darwin

package companion

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type fakeDarwinAuthorityRunner struct {
	started chan struct{}
	result  ShellBrokerResult
	err     error
	wait    bool
}

func (runner *fakeDarwinAuthorityRunner) Execute(
	ctx context.Context,
	_ preparedAuthorityBrokerExecution,
	_ ShellBrokerRequest,
) (ShellBrokerResult, error) {
	if runner.started != nil {
		close(runner.started)
	}
	if runner.wait {
		<-ctx.Done()
		return ShellBrokerResult{}, ErrShellBrokerOutcomeUnknown
	}
	return runner.result, runner.err
}

func TestDarwinAuthorityBrokerPrivateFactoryRoundTrip(t *testing.T) {
	runner := &fakeDarwinAuthorityRunner{result: ShellBrokerResult{ExitCode: 7, Stdout: "owner"}}
	client, stop := startTestDarwinAuthorityBroker(t, runner)
	defer stop()
	snapshot, err := client.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != "darwin-broker-v1" || snapshot.Profiles[0].Alias != "owner-admin" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	result, err := client.Execute(t.Context(), validDarwinAuthorityRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 7 || result.Stdout != "owner" || client.SupportsConfirmedCancellation() {
		t.Fatalf("result/client = %#v cancel=%t", result, client.SupportsConfirmedCancellation())
	}
	runtime, err := newShellExecRuntime(snapshot, client)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.handler.descriptor().SupportsTerminal || runtime.handler.descriptor().SupportsCancel {
		t.Fatalf("Darwin helper advertised unsupported controls: %#v", runtime.handler.descriptor())
	}
}

func TestDarwinAuthorityBrokerDisconnectReportsUnknown(t *testing.T) {
	runner := &fakeDarwinAuthorityRunner{started: make(chan struct{}), wait: true}
	client, stop := startTestDarwinAuthorityBroker(t, runner)
	defer stop()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := client.Execute(ctx, validDarwinAuthorityRequest())
		done <- err
	}()
	select {
	case <-runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not start")
	}
	cancel()
	if err := <-done; !errors.Is(err, ErrShellBrokerOutcomeUnknown) {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestDarwinAuthorityBrokerSaturationPreservesFactory(t *testing.T) {
	client, stop := startTestDarwinAuthorityBroker(t, &fakeDarwinAuthorityRunner{})
	defer stop()
	calls := make([]*net.UnixConn, 0, maxAuthorityBrokerConcurrentCalls)
	defer func() {
		for _, call := range calls {
			_ = call.Close()
		}
	}()
	for range maxAuthorityBrokerConcurrentCalls {
		call, err := client.acquire(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call)
	}
	if _, err := client.acquire(t.Context()); !errors.Is(err, errDarwinAuthorityBrokerBusy) {
		t.Fatalf("saturated acquire error = %v", err)
	}
	request := authorityBrokerRequestFrame{
		Version: AuthorityBrokerProtocolVersion,
		Action:  authorityBrokerActionSnapshot,
	}
	if err := writeAuthorityBrokerFrame(calls[0], request); err != nil {
		t.Fatal(err)
	}
	var response authorityBrokerResponseFrame
	if err := readAuthorityBrokerFrame(calls[0], &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.Snapshot == nil {
		t.Fatalf("in-flight response = %#v", response)
	}
	_ = calls[0].Close()
	calls = calls[1:]
	deadline := time.Now().Add(time.Second)
	for {
		call, err := client.acquire(t.Context())
		if err == nil {
			calls = append(calls, call)
			break
		}
		if !errors.Is(err, errDarwinAuthorityBrokerBusy) || time.Now().After(deadline) {
			t.Fatalf("later acquire error = %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	later := calls[len(calls)-1]
	if err := writeAuthorityBrokerFrame(later, request); err != nil {
		t.Fatal(err)
	}
	response = authorityBrokerResponseFrame{}
	if err := readAuthorityBrokerFrame(later, &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.Snapshot == nil {
		t.Fatalf("later response = %#v", response)
	}
}

func TestDarwinAuthorityBrokerRealProcessUsesConfiguredIdentityAndEnvironment(t *testing.T) {
	t.Setenv("DARWIN_BROKER_SECRET", "must-not-leak")
	client, stop := startTestDarwinAuthorityBroker(t, &darwinAuthorityBrokerProcessRunner{})
	defer stop()
	request := validDarwinAuthorityRequest()
	request.Script = `printf 'uid=%s fixed=%s supplied=%s secret=%s' "$(/usr/bin/id -u)" "$FIXED" "$SUPPLIED" "${DARWIN_BROKER_SECRET-unset}"`
	result, err := client.Execute(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	want := "uid=" + strconv.Itoa(os.Geteuid()) + " fixed=fixed supplied=supplied secret=unset"
	if result.ExitCode != 0 || result.Stdout != want || result.Stderr != "" || result.Truncated {
		t.Fatalf("result = %#v, want stdout %q", result, want)
	}
}

func TestDarwinAuthorityBrokerRealProcessBackgroundChildIsUnknown(t *testing.T) {
	client, stop := startTestDarwinAuthorityBroker(t, &darwinAuthorityBrokerProcessRunner{})
	defer stop()
	request := validDarwinAuthorityRequest()
	request.Script = "sleep 30 &"
	_, err := client.Execute(t.Context(), request)
	if !errors.Is(err, ErrShellBrokerOutcomeUnknown) {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestRunDarwinSupervisedAuthorityBrokerPassesPrivateCapability(t *testing.T) {
	root := t.TempDir()
	ready := root + "/ready"
	companionPath := root + "/companion.sh"
	script := "#!/bin/sh\n" +
		"test -S /dev/fd/3 || exit 9\n" +
		"printf ready > " + shellSingleQuote(ready) + "\n" +
		"trap 'exit 0' TERM\n" +
		"while :; do sleep 1; done\n"
	if err := os.WriteFile(companionPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := root + "/node.json"
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := testDarwinAuthorityBrokerConfig(t)
	config.Companion = &AuthorityBrokerCompanionConfig{
		ExecutablePath: companionPath,
		ConfigPath:     configPath,
		UID:            uint32(os.Geteuid()),
		GID:            uint32(os.Getegid()),
	}
	server, err := newDarwinAuthorityBrokerServer(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- runDarwinSupervisedAuthorityBroker(ctx, config, server) }()
	waitForLocalUserShellFile(t, ready)
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("supervised authority broker did not stop")
	}
}

func TestVerifyDarwinAuthorityManagedFile(t *testing.T) {
	if err := verifyDarwinAuthorityManagedFile("/usr/bin/true", true); err != nil {
		t.Fatalf("verify trusted executable: %v", err)
	}
	path := t.TempDir() + "/untrusted"
	if err := os.WriteFile(path, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := verifyDarwinAuthorityManagedFile(path, true); err == nil {
		t.Fatal("accepted a user-managed privileged executable")
	}
}

func startTestDarwinAuthorityBroker(
	t *testing.T,
	runner darwinAuthorityExecutionRunner,
) (*AuthorityBrokerClient, func()) {
	t.Helper()
	config := testDarwinAuthorityBrokerConfig(t)
	server, err := newDarwinAuthorityBrokerServer(config)
	if err != nil {
		t.Fatal(err)
	}
	if cap(server.calls) != maxAuthorityBrokerConcurrentCalls {
		t.Fatalf("authority call capacity = %d", cap(server.calls))
	}
	server.runner = runner
	descriptors, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	serverConnection := testDarwinUnixConnection(t, descriptors[0])
	clientConnection := testDarwinUnixConnection(t, descriptors[1])
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- server.ServeFactory(ctx, serverConnection) }()
	return newDarwinAuthorityBrokerClient(clientConnection), func() {
		cancel()
		_ = clientConnection.Close()
		_ = serverConnection.Close()
		if err := <-done; err != nil {
			t.Fatalf("ServeFactory() error = %v", err)
		}
	}
}

func testDarwinUnixConnection(t *testing.T, descriptor int) *net.UnixConn {
	t.Helper()
	file := os.NewFile(uintptr(descriptor), "authority-test")
	if file == nil {
		t.Fatal("open authority test descriptor")
	}
	connection, err := net.FileConn(file)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		t.Fatal("authority test connection is not Unix")
	}
	return unixConnection
}

func testDarwinAuthorityBrokerConfig(t *testing.T) AuthorityBrokerConfig {
	t.Helper()
	root := t.TempDir()
	config := validAuthorityBrokerPlatformConfig(t, root)
	config.Revision = "darwin-broker-v1"
	config.Profiles = map[string]AuthorityBrokerProfile{
		"owner-admin": {
			Revision: "darwin-profile-v1", ShellPath: "/bin/sh",
			UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
			WorkingScopes: map[string]string{"home": root}, Network: "inherit",
			FixedEnvironment:          map[string]string{"FIXED": "fixed"},
			PermittedEnvironmentNames: []string{"SUPPLIED"},
			TimeoutSecondsMax:         30, OutputBytesMax: 8192, ConcurrentCommands: 1,
		},
	}
	ready, err := NormalizeAuthorityBrokerConfig(config, root)
	if err != nil {
		t.Fatal(err)
	}
	return ready
}

func validDarwinAuthorityRequest() ShellBrokerRequest {
	return ShellBrokerRequest{
		InvocationID: "inv_darwin_owner", PlanHash: strings.Repeat("a", 64),
		Profile: "owner-admin", ProfileRevision: "darwin-profile-v1",
		Script: "id -u", WorkingScope: "home", Environment: map[string]string{"SUPPLIED": "supplied"},
		TimeoutSeconds: 5, OutputBytesMax: 4096,
	}
}
