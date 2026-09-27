//go:build darwin || linux

package remote

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	codingscope "github.com/bogdanovich/mintclaw/pkg/coding/scope"
)

func TestSameUserIPCRoundTrip(t *testing.T) {
	socketPath := testSocketPath(t)
	requestSeen := make(chan Request, 1)
	server, err := StartServer(t.Context(), socketPath, HandlerFunc(func(_ context.Context, request Request) Response {
		requestSeen <- request
		snapshot := validSnapshot()
		snapshot.Grant = request.Grant
		snapshot.GrantRevision = request.GrantRevision
		return Response{Schema: SchemaV1, RequestID: request.RequestID, Status: ResponseOK, Snapshot: &snapshot}
	}))
	if err != nil {
		t.Fatalf("StartServer() error = %v", err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if closeErr := server.Close(closeCtx); closeErr != nil {
			t.Errorf("Close() error = %v", closeErr)
		}
	})
	info, err := os.Lstat(socketPath)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket info = %#v, %v", info, err)
	}
	client, err := NewClient(socketPath)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	request := validDiscoveryRequest()
	snapshot, err := client.Discover(t.Context(), request)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if snapshot.Grant != request.Grant || snapshot.GrantRevision != request.GrantRevision {
		t.Fatalf("Discover() snapshot = %#v", snapshot)
	}
	select {
	case seen := <-requestSeen:
		if seen != request {
			t.Fatalf("handler request = %#v, want %#v", seen, request)
		}
	case <-time.After(time.Second):
		t.Fatal("handler did not receive request")
	}
}

func TestIPCRejectsPeerUIDMismatch(t *testing.T) {
	socketPath := testSocketPath(t)
	server, err := startServer(t.Context(), socketPath, HandlerFunc(func(_ context.Context, request Request) Response {
		snapshot := validSnapshot()
		return Response{Schema: SchemaV1, RequestID: request.RequestID, Status: ResponseOK, Snapshot: &snapshot}
	}), func(*net.UnixConn) (uint32, error) { return currentUID() + 1, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	}()
	client, err := NewClient(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Discover(t.Context(), validDiscoveryRequest()); err == nil {
		t.Fatal("Discover() succeeded for a mismatched server-side peer UID")
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	if err = server.Close(closeCtx); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()

	server, err = StartServer(t.Context(), socketPath, HandlerFunc(func(_ context.Context, request Request) Response {
		snapshot := validSnapshot()
		return Response{Schema: SchemaV1, RequestID: request.RequestID, Status: ResponseOK, Snapshot: &snapshot}
	}))
	if err != nil {
		t.Fatal(err)
	}
	client.peerUID = func(*net.UnixConn) (uint32, error) { return client.uid + 1, nil }
	if _, err = client.Discover(t.Context(), validDiscoveryRequest()); err == nil ||
		!strings.Contains(err.Error(), "peer authentication") {
		t.Fatalf("Discover() peer error = %v", err)
	}
}

func TestIPCRejectsUnsafeSocketPaths(t *testing.T) {
	root := canonicalTempDir(t)
	handler := HandlerFunc(func(_ context.Context, request Request) Response {
		return Response{Schema: SchemaV1, RequestID: request.RequestID, Status: ResponseDenied, Code: "DENIED"}
	})

	publicDirectory := filepath.Join(root, "public")
	if err := os.Mkdir(publicDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := StartServer(t.Context(), filepath.Join(publicDirectory, "broker.sock"), handler); err == nil {
		t.Fatal("StartServer() accepted a group/world-accessible directory")
	}

	privateDirectory := filepath.Join(root, "private")
	if err := os.Mkdir(privateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	regularPath := filepath.Join(privateDirectory, "broker.sock")
	if err := os.WriteFile(regularPath, []byte("do not replace"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := StartServer(t.Context(), regularPath, handler); err == nil {
		t.Fatal("StartServer() replaced a regular file")
	}
	if content, err := os.ReadFile(regularPath); err != nil || string(content) != "do not replace" {
		t.Fatalf("regular socket target changed: %q, %v", content, err)
	}

	linkedDirectory := filepath.Join(root, "linked")
	if err := os.Symlink(privateDirectory, linkedDirectory); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := StartServer(t.Context(), filepath.Join(linkedDirectory, "linked.sock"), handler); err == nil {
		t.Fatal("StartServer() accepted a symlinked directory")
	}
}

func TestClientRejectsBroadenedSocketMode(t *testing.T) {
	socketPath := testSocketPath(t)
	server, err := StartServer(t.Context(), socketPath, HandlerFunc(func(_ context.Context, request Request) Response {
		snapshot := validSnapshot()
		return Response{Schema: SchemaV1, RequestID: request.RequestID, Status: ResponseOK, Snapshot: &snapshot}
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Chmod(socketPath, 0o600)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	}()
	if err := os.Chmod(socketPath, 0o660); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Discover(t.Context(), validDiscoveryRequest()); err == nil {
		t.Fatal("Discover() accepted a group-accessible socket")
	}
}

func TestServerRecoversOnlyOwnedStaleSocket(t *testing.T) {
	socketPath := testSocketPath(t)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err = os.Chmod(socketPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	server, err := StartServer(t.Context(), socketPath, HandlerFunc(func(_ context.Context, request Request) Response {
		return Response{Schema: SchemaV1, RequestID: request.RequestID, Status: ResponseDenied, Code: "DENIED"}
	}))
	if err != nil {
		t.Fatalf("StartServer() stale socket error = %v", err)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = server.Close(closeCtx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err = os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket remained after Close(): %v", err)
	}
}

func validDiscoveryRequest() Request {
	threadID := uuid.NewString()
	return Request{
		Schema: SchemaV1, RequestID: "request-one", Operation: OperationCapabilitiesList,
		Grant: "local-development", GrantRevision: "grant-v1", ThreadID: threadID,
		SessionKey: "coding:" + threadID,
		ProjectKey: "git_worktree:" + strings.Repeat("c", 64), LocalProfile: codingscope.ProfileMutate,
	}
}

func testSocketPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(canonicalTempDir(t), "broker.sock")
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "mintclaw-remote-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cleanupErr := os.RemoveAll(directory); cleanupErr != nil {
			t.Errorf("remove temp directory: %v", cleanupErr)
		}
	})
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(resolved, 0o700); err != nil {
		t.Fatal(err)
	}
	return resolved
}
