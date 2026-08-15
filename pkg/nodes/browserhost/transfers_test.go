package browserhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	browserworker "github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/protocol"
)

func TestBrowserArtifactTransferIsAuthorityBoundAndConsumedOnce(t *testing.T) {
	content := []byte("companion browser upload")
	digest := sha256.Sum256(content)
	worker := &fakeBrowserHostWorker{
		status: browserworker.WorkerReady,
		observations: []browserworker.DriverObservation{
			browserUploadObservation(), browserUploadObservation(), browserUploadObservation(),
		},
	}
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: worker})
	profile := host.profiles["managed"]
	profile.AllowedActions = append(profile.AllowedActions, "file_chooser")
	host.profiles["managed"] = profile
	if _, err := host.Open(t.Context(), browserHostOpenFixture()); err != nil {
		t.Fatal(err)
	}
	observed, err := host.Observe(t.Context(), browserHostObserveFixture())
	if err != nil || len(observed.Elements) != 1 {
		t.Fatalf("Observe() = %#v, %v", observed, err)
	}
	prepare := browserArtifactFrame(t, content, browserArtifactTransferPrepare{
		SessionID: "browser_session_1", RoutedSessionID: "routed_session_1",
		ActionInvocationID: "browser_file_chooser_1",
		ArtifactRef:        nodes.TransferArtifactRefPrefix + "artifact_1",
		PreparedActionHash: strings.Repeat("b", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
		AgentID: "browser", ActorID: "telegram:owner", Filename: "photo.jpg",
		ContentType: "image/jpeg", ExpiresAt: host.now().Add(time.Minute).Unix(),
	})
	denied := prepare
	denied.TransferID = "browser_transfer_denied"
	var deniedRequest browserArtifactTransferPrepare
	if err := json.Unmarshal(denied.Payload, &deniedRequest); err != nil {
		t.Fatal(err)
	}
	deniedRequest.ActorID = "telegram:attacker"
	denied.Payload, _ = json.Marshal(deniedRequest)
	responses := browserTransferResponses(t, host, denied)
	if len(responses) != 1 || responses[0].Type != protocol.TransferFrameDeny {
		t.Fatalf("forged prepare responses = %#v", responses)
	}

	responses = browserTransferResponses(t, host, prepare)
	if len(responses) != 1 || responses[0].Type != protocol.TransferFrameAccept {
		t.Fatalf("prepare responses = %#v", responses)
	}
	chunk := prepare
	chunk.Type, chunk.Sequence, chunk.Payload = protocol.TransferFrameChunk, 1, content
	responses = browserTransferResponses(t, host, chunk)
	if len(responses) != 1 || responses[0].Type != protocol.TransferFrameAck {
		t.Fatalf("chunk responses = %#v", responses)
	}
	commit := prepare
	commit.Type, commit.Payload = protocol.TransferFrameCommit, nil
	responses = browserTransferResponses(t, host, commit)
	if len(responses) != 1 || responses[0].Type != protocol.TransferFrameCommitted {
		t.Fatalf("commit responses = %#v", responses)
	}

	request := BrowserHostNavigateRequest{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
		ActionInvocationID: "browser_file_chooser_1",
		Action: nodes.BrowserAction{
			Kind: "file_chooser", Ref: observed.Elements[0].Ref,
			ArtifactRef: nodes.TransferArtifactRefPrefix + "artifact_1",
		},
		Effect: "local_edit", CurrentOrigin: "https://example.com",
		PreparedActionHash: strings.Repeat("b", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
		ProfileRevision: "managed-v1", ExpectedRole: "button", ExpectedName: "Choose file",
		ArtifactSHA256: hex.EncodeToString(digest[:]), ArtifactBytes: int64(len(content)),
		ArtifactFilename: "photo.jpg", ArtifactContentType: "image/jpeg",
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	}
	result, err := host.FileChooser(t.Context(), request)
	if err != nil || result.SnapshotGeneration != 2 || len(worker.actions) != 1 {
		t.Fatalf("FileChooser() = %#v, %v; actions = %#v", result, err, worker.actions)
	}
	action := worker.actions[0]
	if action.Kind != browserworker.DriverUpload || action.ArtifactSHA256 != request.ArtifactSHA256 ||
		action.ArtifactBytes != request.ArtifactBytes || action.ArtifactFilename != "photo.jpg" {
		t.Fatalf("driver upload = %#v", action)
	}
	if _, statErr := os.Stat(action.Value); !os.IsNotExist(statErr) {
		t.Fatalf("consumed staged path remains: %v", statErr)
	}
	request.ActionInvocationID = "browser_file_chooser_2"
	request.PreparedActionHash = strings.Repeat("c", 64)
	request.SnapshotGeneration = 2
	if _, err = host.FileChooser(t.Context(), request); err == nil || len(worker.actions) != 1 {
		t.Fatalf("reused artifact error = %v; actions = %#v", err, worker.actions)
	}
}

func TestBrowserArtifactTransferIsCleanedWhenSessionCloses(t *testing.T) {
	content := []byte("cleanup")
	worker := &fakeBrowserHostWorker{status: browserworker.WorkerReady}
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: worker})
	profile := host.profiles["managed"]
	profile.AllowedActions = append(profile.AllowedActions, "file_chooser")
	host.profiles["managed"] = profile
	if _, err := host.Open(t.Context(), browserHostOpenFixture()); err != nil {
		t.Fatal(err)
	}
	prepare := browserArtifactFrame(t, content, browserArtifactTransferPrepare{
		SessionID: "browser_session_1", RoutedSessionID: "routed_session_1",
		ActionInvocationID: "browser_file_chooser_cleanup",
		ArtifactRef:        nodes.TransferArtifactRefPrefix + "artifact_cleanup",
		PreparedActionHash: strings.Repeat("b", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
		AgentID: "browser", ActorID: "telegram:owner", Filename: "cleanup.txt",
		ContentType: "text/plain", ExpiresAt: host.now().Add(time.Minute).Unix(),
	})
	browserTransferResponses(t, host, prepare)
	chunk := prepare
	chunk.Type, chunk.Sequence, chunk.Payload = protocol.TransferFrameChunk, 1, content
	browserTransferResponses(t, host, chunk)
	commit := prepare
	commit.Type, commit.Payload = protocol.TransferFrameCommit, nil
	browserTransferResponses(t, host, commit)
	root := host.transferRoot
	if _, err := host.Close(t.Context(), BrowserHostCloseRequest{
		SessionID: "browser_session_1", ProfileRevision: "managed-v1",
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	}); err != nil {
		t.Fatal(err)
	}
	if len(host.activeTransfers) != 0 || len(host.completedTransfers) != 0 {
		t.Fatalf("browser transfers survived close: active=%d completed=%d", len(host.activeTransfers), len(host.completedTransfers))
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("browser transfer root after close = %v, %v", entries, err)
	}
}

func TestBrowserArtifactTransferCleansPartialStageOnConnectionLoss(t *testing.T) {
	content := []byte("partial")
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: &fakeBrowserHostWorker{
		status: browserworker.WorkerReady,
	}})
	profile := host.profiles["managed"]
	profile.AllowedActions = append(profile.AllowedActions, "file_chooser")
	host.profiles["managed"] = profile
	if _, err := host.Open(t.Context(), browserHostOpenFixture()); err != nil {
		t.Fatal(err)
	}
	prepare := browserArtifactFrame(t, content, browserArtifactTransferPrepare{
		SessionID: "browser_session_1", RoutedSessionID: "routed_session_1",
		ActionInvocationID: "browser_file_chooser_partial",
		ArtifactRef:        nodes.TransferArtifactRefPrefix + "artifact_partial",
		PreparedActionHash: strings.Repeat("b", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
		AgentID: "browser", ActorID: "telegram:owner", Filename: "partial.txt",
		ContentType: "text/plain", ExpiresAt: host.now().Add(time.Minute).Unix(),
	})
	connection, disconnect := context.WithCancel(t.Context())
	var responses []protocol.TransferFrame
	if err := host.HandleTransferFrame(connection, prepare, func(response protocol.TransferFrame) error {
		responses = append(responses, response)
		return nil
	}); err != nil || len(responses) != 1 || responses[0].Type != protocol.TransferFrameAccept {
		t.Fatalf("prepare = %#v, %v", responses, err)
	}
	disconnect()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		host.transferMu.Lock()
		active := len(host.activeTransfers)
		host.transferMu.Unlock()
		if active == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("partial browser artifact survived connection loss")
}

func browserUploadObservation() browserworker.DriverObservation {
	return browserworker.DriverObservation{
		URL: "https://example.com/upload", Origin: "https://example.com", Title: "Upload",
		Snapshot: "- button \"Choose file\" [ref=chooser]",
		Elements: []browserworker.DriverElement{{Target: "chooser", Role: "button", Name: "Choose file"}},
	}
}

func browserArtifactFrame(
	t *testing.T,
	content []byte,
	request browserArtifactTransferPrepare,
) protocol.TransferFrame {
	t.Helper()
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.TransferFrame{
		Type: protocol.TransferFramePrepare, Direction: protocol.TransferUpload,
		TransferID: "browser_transfer_1", PolicyRevision: "managed-v1",
		TotalSize: uint64(len(content)), SHA256: sha256.Sum256(content), Payload: payload,
	}
}

func browserTransferResponses(
	t *testing.T,
	host *BrowserHost,
	frame protocol.TransferFrame,
) []protocol.TransferFrame {
	t.Helper()
	responses := make([]protocol.TransferFrame, 0, 1)
	err := host.HandleTransferFrame(context.Background(), frame, func(response protocol.TransferFrame) error {
		responses = append(responses, response)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return responses
}
