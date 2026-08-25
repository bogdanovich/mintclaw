package browserhost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
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

	request := BrowserHostActRequest{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
		ActionInvocationID: "browser_file_chooser_1",
		Action: browserworker.Action{
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
	result, err := host.Act(t.Context(), request)
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
	if _, err = host.Act(t.Context(), request); err == nil || len(worker.actions) != 1 {
		t.Fatalf("reused artifact error = %v; actions = %#v", err, worker.actions)
	}
}

func TestBrowserTransferRevisionRoutesOutputsWithoutGrantingUpload(t *testing.T) {
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: &fakeBrowserHostWorker{
		status: browserworker.WorkerReady,
	}})
	revisions := host.TransferPolicyRevisions()
	if len(revisions) != 1 || revisions[0] != "managed-v1" {
		t.Fatalf("TransferPolicyRevisions() = %#v", revisions)
	}
	if slicesContains(host.profiles["managed"].AllowedActions, "file_chooser") {
		t.Fatal("test profile unexpectedly grants file_chooser")
	}
	if _, err := host.Open(t.Context(), browserHostOpenFixture()); err != nil {
		t.Fatal(err)
	}
	content := []byte("unauthorized browser upload")
	frame := browserArtifactFrame(t, content, browserArtifactTransferPrepare{
		SessionID: "browser_session_1", RoutedSessionID: "routed_session_1",
		ActionInvocationID: "browser_file_chooser_denied",
		ArtifactRef:        nodes.TransferArtifactRefPrefix + "artifact_denied",
		PreparedActionHash: strings.Repeat("b", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
		AgentID: "browser", ActorID: "telegram:owner", Filename: "photo.jpg",
		ContentType: "image/jpeg", ExpiresAt: host.now().Add(time.Minute).Unix(),
	})
	responses := browserTransferResponses(t, host, frame)
	if len(responses) != 1 || responses[0].Type != protocol.TransferFrameDeny {
		t.Fatalf("upload responses without file_chooser = %#v", responses)
	}
}

func TestBrowserOutputTransferIsAuthorityBoundChunkedAndRetryable(t *testing.T) {
	content := bytes.Repeat([]byte("browser-output-"), 24000)
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: &fakeBrowserHostWorker{
		status: browserworker.WorkerReady,
		observations: []browserworker.DriverObservation{
			browserUploadObservation(),
			browserUploadObservation(),
		},
	}})
	if _, err := host.Open(t.Context(), browserHostOpenFixture()); err != nil {
		t.Fatal(err)
	}
	observed, err := host.Observe(t.Context(), browserHostObserveFixture())
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := host.RegisterOutput(nodes.BrowserOutputDescriptor{
		Kind: nodes.BrowserOutputScreenshot, SessionID: "browser_session_1",
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
		WorkspaceID: "workspace_1", RouteID: "route_1",
		Target: "companion", ProfileRevision: "managed-v1",
		BrowserPolicyRevision: strings.Repeat("a", 64), InvocationID: "browser_capture_1",
		TabID: observed.TabID, SnapshotGeneration: observed.SnapshotGeneration,
		CaptureTarget: "page",
		Filename:      "capture.png", ContentType: "image/png", ExpiresAt: host.now().Add(time.Minute).Unix(),
	}, content)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.TransferID == "" || descriptor.Size != uint64(len(content)) ||
		descriptor.SHA256 == "" || descriptor.CleanupPolicy != browserOutputCleanupPolicy {
		t.Fatalf("RegisterOutput() = %#v", descriptor)
	}

	for attempt := 0; attempt < 2; attempt++ {
		received := downloadBrowserOutput(
			t,
			host,
			descriptor,
			func(value nodes.BrowserOutputDescriptor) nodes.BrowserOutputDescriptor {
				if attempt == 0 {
					value.ActorID = "telegram:attacker"
				}
				return value
			},
		)
		if attempt == 0 {
			if received != nil {
				t.Fatalf("forged transfer returned %d bytes", len(received))
			}
			received = downloadBrowserOutput(t, host, descriptor, nil)
		}
		if !bytes.Equal(received, content) {
			t.Fatalf("attempt %d returned %d bytes", attempt, len(received))
		}
	}

	host.transferMu.Lock()
	artifact := host.outputArtifacts[descriptor.TransferID]
	host.transferMu.Unlock()
	if artifact.path == "" {
		t.Fatal("committed output was not retained for retry")
	}
	if info, statErr := os.Stat(artifact.path); statErr != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("output mode = %v, %v", info, statErr)
	}
	if _, err = host.Close(t.Context(), BrowserHostCloseRequest{
		SessionID: "browser_session_1", RoutedSessionID: "routed_session_1",
		ProfileRevision: "managed-v1", AgentID: "browser", ActorID: "telegram:owner",
	}); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(artifact.path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("output survived session close: %v", statErr)
	}
}

func TestBrowserHostStreamsLargeObservationAndDiscardsCommittedSnapshot(t *testing.T) {
	largeSnapshot := "- button \"Save\" [ref=driver_ref_1]\n" + strings.Repeat("x", 210*1024)
	elements := make([]browserworker.DriverElement, 0, nodes.MaxBrowserSnapshotRefs)
	for index := 0; index < nodes.MaxBrowserSnapshotRefs; index++ {
		elements = append(elements, browserworker.DriverElement{
			Target: fmt.Sprintf("driver_ref_%d", index), Role: "region",
			Name: strings.Repeat("nested semantic name ", 12),
		})
	}
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: &fakeBrowserHostWorker{
		status: browserworker.WorkerReady,
		observations: []browserworker.DriverObservation{{
			URL: "https://example.com/", Origin: "https://example.com", Snapshot: largeSnapshot,
			Elements: elements,
		}},
		navigationIdentities: []string{"navigation_1", "navigation_1"},
	}})
	if _, err := host.Open(t.Context(), browserHostOpenFixture()); err != nil {
		t.Fatal(err)
	}
	observed, err := host.Observe(t.Context(), browserHostObserveFixture())
	if err != nil || observed.DocumentID == "" {
		t.Fatalf("Observe() = %#v, %v", observed, err)
	}
	streamed, err := host.PrepareObservationOutput(nodes.BrowserHostObservationOutputRequest{
		SessionID: observed.SessionID, RoutedSessionID: "routed_session_1",
		InvocationID: "browser_observe_large_1", WorkspaceID: "workspace_1",
		BrowserTarget: "companion", AgentID: "browser", ActorID: "telegram:owner",
	}, observed)
	if err != nil || streamed.Output == nil || streamed.Snapshot != "" || len(streamed.Elements) != 0 ||
		streamed.Output.Kind != nodes.BrowserOutputSnapshot ||
		streamed.Output.Size <= uint64(nodes.MaxBrowserToolResultBytes) {
		t.Fatalf("PrepareObservationOutput() = %#v, %v", streamed, err)
	}
	payload := downloadBrowserOutput(t, host, *streamed.Output, nil)
	decoded, err := nodes.DecodeBrowserSnapshotPayload(payload, nodes.BrowserLimits{}.Effective())
	if err != nil || decoded.Snapshot != observed.Snapshot || !reflect.DeepEqual(decoded.Elements, observed.Elements) {
		t.Fatalf("streamed snapshot = %#v, %v", decoded, err)
	}
	host.transferMu.Lock()
	_, retained := host.outputArtifacts[streamed.Output.TransferID]
	_, active := host.outputTransfers[streamed.Output.TransferID]
	host.transferMu.Unlock()
	if retained || active {
		t.Fatalf("committed semantic snapshot remained retained=%v active=%v", retained, active)
	}
}

func TestBrowserHostStagesSingleChunkObservationAboveNegotiatedResultBudget(t *testing.T) {
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: &fakeBrowserHostWorker{
		status: browserworker.WorkerReady,
		observations: []browserworker.DriverObservation{{
			URL: "https://example.com/", Origin: "https://example.com",
			Snapshot: strings.Repeat("x", 160*1024),
		}},
		navigationIdentities: []string{"navigation_1", "navigation_1"},
	}})
	open := browserHostOpenFixture()
	open.Limits.ToolResultBytes = 150 * 1024
	if _, err := host.Open(t.Context(), open); err != nil {
		t.Fatal(err)
	}
	observed, err := host.Observe(t.Context(), browserHostObserveFixture())
	if err != nil {
		t.Fatal(err)
	}
	inline, err := json.Marshal(observed)
	if err != nil {
		t.Fatal(err)
	}
	streamed, err := host.PrepareObservationOutput(nodes.BrowserHostObservationOutputRequest{
		SessionID: observed.SessionID, RoutedSessionID: "routed_session_1",
		InvocationID: "browser_observe_single_chunk_1", WorkspaceID: "workspace_1",
		BrowserTarget: "companion", AgentID: "browser", ActorID: "telegram:owner",
		InlineResultBytes: len(inline),
	}, observed)
	if err != nil || streamed.Output == nil || streamed.Output.Size >= uint64(protocol.MaxTransferChunkBytes) ||
		streamed.Output.Size <= uint64(open.Limits.ToolResultBytes) {
		t.Fatalf("PrepareObservationOutput() = %#v, %v", streamed, err)
	}
	payload := downloadBrowserOutput(t, host, *streamed.Output, nil)
	decoded, err := nodes.DecodeBrowserSnapshotPayload(payload, open.Limits)
	if err != nil || decoded.Snapshot != observed.Snapshot {
		t.Fatalf("single-chunk streamed snapshot = %#v, %v", decoded, err)
	}
}

func TestBrowserHostDiscardsUnreturnedObservationOutput(t *testing.T) {
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: &fakeBrowserHostWorker{
		status: browserworker.WorkerReady,
		observations: []browserworker.DriverObservation{{
			URL: "https://example.com/", Origin: "https://example.com",
			Snapshot: strings.Repeat("x", 160*1024),
		}},
		navigationIdentities: []string{"navigation_1", "navigation_1"},
	}})
	open := browserHostOpenFixture()
	open.Limits.ToolResultBytes = 150 * 1024
	if _, err := host.Open(t.Context(), open); err != nil {
		t.Fatal(err)
	}
	observed, err := host.Observe(t.Context(), browserHostObserveFixture())
	if err != nil {
		t.Fatal(err)
	}
	inline, err := json.Marshal(observed)
	if err != nil {
		t.Fatal(err)
	}
	streamed, err := host.PrepareObservationOutput(nodes.BrowserHostObservationOutputRequest{
		SessionID: observed.SessionID, RoutedSessionID: "routed_session_1",
		InvocationID: "browser_observe_discard_1", WorkspaceID: "workspace_1",
		BrowserTarget: "companion", AgentID: "browser", ActorID: "telegram:owner",
		InlineResultBytes: len(inline),
	}, observed)
	if err != nil || streamed.Output == nil {
		t.Fatalf("PrepareObservationOutput() = %#v, %v", streamed, err)
	}
	host.transferMu.Lock()
	artifact := host.outputArtifacts[streamed.Output.TransferID]
	host.transferMu.Unlock()
	if artifact.path == "" {
		t.Fatal("staged output was not retained")
	}
	if err = host.DiscardObservationOutput(*streamed.Output); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(artifact.path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("discarded output remains: %v", statErr)
	}
	host.transferMu.Lock()
	_, retained := host.outputArtifacts[streamed.Output.TransferID]
	_, active := host.outputTransfers[streamed.Output.TransferID]
	host.transferMu.Unlock()
	if retained || active {
		t.Fatalf("discarded output retained=%v active=%v", retained, active)
	}
}

func TestBrowserOutputTransferCancelWakesStreamAndRetainsOutput(t *testing.T) {
	content := bytes.Repeat([]byte("bounded-output"), 30000)
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: &fakeBrowserHostWorker{
		status: browserworker.WorkerReady,
		observations: []browserworker.DriverObservation{
			browserUploadObservation(), browserUploadObservation(),
		},
	}})
	if _, err := host.Open(t.Context(), browserHostOpenFixture()); err != nil {
		t.Fatal(err)
	}
	observed, err := host.Observe(t.Context(), browserHostObserveFixture())
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := host.RegisterOutput(nodes.BrowserOutputDescriptor{
		Kind: nodes.BrowserOutputDownload, SessionID: "browser_session_1",
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
		WorkspaceID: "workspace_1", RouteID: "route_1",
		Target: "companion", ProfileRevision: "managed-v1",
		BrowserPolicyRevision: strings.Repeat("a", 64), InvocationID: "browser_download_1",
		TabID: observed.TabID, SnapshotGeneration: observed.SnapshotGeneration,
		Filename: "download.bin", ContentType: "application/octet-stream",
		ExpiresAt: host.now().Add(time.Minute).Unix(),
	}, content)
	if err != nil {
		t.Fatal(err)
	}
	prepare := browserOutputPrepareFrame(t, descriptor)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	chunkSeen := make(chan struct{}, 1)
	if err = host.HandleTransferFrame(ctx, prepare, func(response protocol.TransferFrame) error {
		if response.Type == protocol.TransferFrameChunk {
			chunkSeen <- struct{}{}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-chunkSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("first output chunk was not sent")
	}
	host.transferMu.Lock()
	transfer := host.outputTransfers[descriptor.TransferID]
	host.transferMu.Unlock()
	if transfer == nil {
		t.Fatal("output transfer was not active")
	}
	cancelFrame := prepare
	cancelFrame.Type, cancelFrame.Payload = protocol.TransferFrameCancel, nil
	if err = host.HandleTransferFrame(ctx, cancelFrame, func(protocol.TransferFrame) error { return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-transfer.streamDone:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled output stream remained blocked waiting for an ACK")
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		host.transferMu.Lock()
		_, active := host.outputTransfers[descriptor.TransferID]
		_, retained := host.outputArtifacts[descriptor.TransferID]
		host.transferMu.Unlock()
		if !active {
			if !retained {
				t.Fatal("cancel removed immutable output")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancel did not clean active output transfer")
		}
		time.Sleep(time.Millisecond)
	}
	if received := downloadBrowserOutput(t, host, descriptor, nil); !bytes.Equal(received, content) {
		t.Fatalf("retry returned %d bytes", len(received))
	}
}

func TestBrowserOutputCancelIsTerminalForOutboundFrames(t *testing.T) {
	content := bytes.Repeat([]byte("ordered-output"), 30000)
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: &fakeBrowserHostWorker{
		status: browserworker.WorkerReady,
		observations: []browserworker.DriverObservation{
			browserUploadObservation(), browserUploadObservation(),
		},
	}})
	if _, err := host.Open(t.Context(), browserHostOpenFixture()); err != nil {
		t.Fatal(err)
	}
	observed, err := host.Observe(t.Context(), browserHostObserveFixture())
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := host.RegisterOutput(nodes.BrowserOutputDescriptor{
		Kind: nodes.BrowserOutputDownload, SessionID: "browser_session_1",
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
		WorkspaceID: "workspace_1", RouteID: "route_1",
		Target: "companion", ProfileRevision: "managed-v1",
		BrowserPolicyRevision: strings.Repeat("a", 64), InvocationID: "browser_ordering_1",
		TabID: observed.TabID, SnapshotGeneration: observed.SnapshotGeneration,
		Filename: "ordered.bin", ContentType: "application/octet-stream",
		ExpiresAt: host.now().Add(time.Minute).Unix(),
	}, content)
	if err != nil {
		t.Fatal(err)
	}
	chunkReady := make(chan struct{})
	releaseChunk := make(chan struct{})
	transferReady := make(chan *browserOutputTransfer, 1)
	var readyOnce sync.Once
	host.beforeOutputFrameSend = func(frame protocol.TransferFrame) {
		if frame.Type == protocol.TransferFrameChunk {
			readyOnce.Do(func() {
				transferReady <- host.outputTransfers[descriptor.TransferID]
				close(chunkReady)
			})
			<-releaseChunk
		}
	}
	prepare := browserOutputPrepareFrame(t, descriptor)
	var responseMu sync.Mutex
	responses := make([]protocol.TransferFrame, 0, 3)
	send := func(response protocol.TransferFrame) error {
		responseMu.Lock()
		responses = append(responses, response)
		responseMu.Unlock()
		return nil
	}
	if err = host.HandleTransferFrame(t.Context(), prepare, send); err != nil {
		t.Fatal(err)
	}
	select {
	case <-chunkReady:
	case <-time.After(5 * time.Second):
		t.Fatal("output stream did not reach the send boundary")
	}
	transfer := <-transferReady
	if transfer == nil {
		t.Fatal("output transfer was not active at the send boundary")
	}
	cancelDone := make(chan error, 1)
	cancelFrame := prepare
	cancelFrame.Type, cancelFrame.Payload = protocol.TransferFrameCancel, nil
	go func() { cancelDone <- host.HandleTransferFrame(t.Context(), cancelFrame, send) }()
	close(releaseChunk)
	if cancelErr := <-cancelDone; cancelErr != nil {
		t.Fatal(cancelErr)
	}
	select {
	case <-transfer.streamDone:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled output stream did not finish")
	}
	responseMu.Lock()
	defer responseMu.Unlock()
	canceledAt := -1
	for index, response := range responses {
		if response.Type == protocol.TransferFrameFailure {
			canceledAt = index
		}
	}
	if canceledAt < 0 || canceledAt != len(responses)-1 {
		t.Fatalf("cancel was not terminal: %#v", responses)
	}
}

func TestValidBrowserOutputFilenameRejectsWindowsDevices(t *testing.T) {
	for _, filename := range []string{
		"NUL", "nul.txt", "CON.json", "PRN", "AUX.log", "CLOCK$", "CONIN$", "conin$.txt",
		"CONOUT$", "conout$.log", "COM1", "com9.txt",
		"LPT1", "lpt9.log", "COM¹.txt", "LPT³", "file.", "file ", "a:b", `a\b`, "a\x00b",
	} {
		if validBrowserOutputFilename(filename) {
			t.Errorf("validBrowserOutputFilename(%q) = true", filename)
		}
	}
	for _, filename := range []string{"capture.png", "COM10.txt", "LPT20", "console.json"} {
		if !validBrowserOutputFilename(filename) {
			t.Errorf("validBrowserOutputFilename(%q) = false", filename)
		}
	}
}

func TestBrowserOutputExpiryRemovesNeverTransferredArtifact(t *testing.T) {
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: &fakeBrowserHostWorker{
		status: browserworker.WorkerReady,
		observations: []browserworker.DriverObservation{
			browserUploadObservation(), browserUploadObservation(),
		},
	}})
	if _, err := host.Open(t.Context(), browserHostOpenFixture()); err != nil {
		t.Fatal(err)
	}
	observed, err := host.Observe(t.Context(), browserHostObserveFixture())
	if err != nil {
		t.Fatal(err)
	}
	expirySignal := make(chan time.Time, 1)
	host.outputExpiryAfter = func(time.Duration) <-chan time.Time { return expirySignal }
	descriptor, err := host.RegisterOutput(nodes.BrowserOutputDescriptor{
		Kind: nodes.BrowserOutputScreenshot, SessionID: "browser_session_1",
		CaptureTarget:   "page",
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
		WorkspaceID: "workspace_1", RouteID: "route_1",
		Target: "companion", ProfileRevision: "managed-v1",
		BrowserPolicyRevision: strings.Repeat("a", 64), InvocationID: "browser_expiry_1",
		TabID: observed.TabID, SnapshotGeneration: observed.SnapshotGeneration,
		Filename: "expiring.png", ContentType: "image/png", ExpiresAt: host.now().Add(time.Minute).Unix(),
	}, []byte("expiring output"))
	if err != nil {
		t.Fatal(err)
	}
	host.transferMu.Lock()
	artifact := host.outputArtifacts[descriptor.TransferID]
	host.transferMu.Unlock()
	if artifact.path == "" {
		t.Fatal("output was not registered")
	}
	expirySignal <- host.now()
	select {
	case <-artifact.expiryDone:
	case <-time.After(5 * time.Second):
		t.Fatal("registered output did not expire")
	}
	host.transferMu.Lock()
	_, retained := host.outputArtifacts[descriptor.TransferID]
	host.transferMu.Unlock()
	if retained {
		t.Fatal("expired output remained registered")
	}
	if _, statErr := os.Stat(artifact.path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expired output remained on disk: %v", statErr)
	}
}

func downloadBrowserOutput(
	t *testing.T,
	host *BrowserHost,
	descriptor nodes.BrowserOutputDescriptor,
	mutate func(nodes.BrowserOutputDescriptor) nodes.BrowserOutputDescriptor,
) []byte {
	t.Helper()
	prepare := browserOutputPrepareFrame(t, descriptor)
	var err error
	payload := descriptor
	if mutate != nil {
		payload = mutate(payload)
	}
	prepare.Payload, _ = json.Marshal(payload)
	responses := make(chan protocol.TransferFrame, 8)
	send := func(frame protocol.TransferFrame) error {
		responses <- frame
		return nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err = host.HandleTransferFrame(ctx, prepare, send); err != nil {
		t.Fatal(err)
	}
	var received bytes.Buffer
	for {
		select {
		case response := <-responses:
			switch response.Type {
			case protocol.TransferFrameDeny:
				return nil
			case protocol.TransferFrameAccept:
			case protocol.TransferFrameChunk:
				_, _ = received.Write(response.Payload)
				ack := response
				ack.Type, ack.Payload = protocol.TransferFrameAck, nil
				if err = host.HandleTransferFrame(ctx, ack, send); err != nil {
					t.Fatal(err)
				}
			case protocol.TransferFrameStatus:
				commit := prepare
				commit.Type, commit.Payload = protocol.TransferFrameCommit, nil
				if err = host.HandleTransferFrame(ctx, commit, send); err != nil {
					t.Fatal(err)
				}
			case protocol.TransferFrameCommitted:
				return received.Bytes()
			default:
				t.Fatalf("unexpected output response %#v", response)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for browser output")
		}
	}
}

func browserOutputPrepareFrame(
	t *testing.T,
	descriptor nodes.BrowserOutputDescriptor,
) protocol.TransferFrame {
	t.Helper()
	digest, err := hex.DecodeString(descriptor.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	frame := protocol.TransferFrame{
		Type: protocol.TransferFramePrepare, Direction: protocol.TransferDownload,
		TransferID: descriptor.TransferID, PolicyRevision: descriptor.ProfileRevision,
		TotalSize: descriptor.Size,
	}
	copy(frame.SHA256[:], digest)
	frame.Payload, _ = json.Marshal(descriptor)
	return frame
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
		t.Fatalf(
			"browser transfers survived close: active=%d completed=%d",
			len(host.activeTransfers),
			len(host.completedTransfers),
		)
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

func TestBrowserArtifactTransferCleansCommittedStageOnConnectionLoss(t *testing.T) {
	content := []byte("committed")
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
		ActionInvocationID: "browser_file_chooser_committed_disconnect",
		ArtifactRef:        nodes.TransferArtifactRefPrefix + "artifact_committed_disconnect",
		PreparedActionHash: strings.Repeat("b", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
		AgentID: "browser", ActorID: "telegram:owner", Filename: "committed.txt",
		ContentType: "text/plain", ExpiresAt: host.now().Add(time.Minute).Unix(),
	})
	connection, disconnect := context.WithCancel(t.Context())
	path := stageCommittedBrowserArtifact(t, host, connection, prepare, content)
	disconnect()
	waitForBrowserArtifactRemoval(t, host, path)
}

func TestBrowserArtifactTransferReconnectAdoptsCommittedLifetime(t *testing.T) {
	content := []byte("reconnect")
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
		ActionInvocationID: "browser_file_chooser_reconnect",
		ArtifactRef:        nodes.TransferArtifactRefPrefix + "artifact_reconnect",
		PreparedActionHash: strings.Repeat("b", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
		AgentID: "browser", ActorID: "telegram:owner", Filename: "reconnect.txt",
		ContentType: "text/plain", ExpiresAt: host.now().Add(time.Minute).Unix(),
	})
	oldConnection, disconnectOld := context.WithCancel(t.Context())
	path := stageCommittedBrowserArtifact(t, host, oldConnection, prepare, content)
	cleanupEntered := make(chan struct{})
	releaseCleanup := make(chan struct{})
	cleanupExited := make(chan struct{})
	var cleanupOnce sync.Once
	host.beforeTransferCleanup = func() {
		cleanupOnce.Do(func() {
			close(cleanupEntered)
			<-releaseCleanup
			close(cleanupExited)
		})
	}
	disconnectOld()
	<-cleanupEntered
	newConnection, disconnectNew := context.WithCancel(t.Context())
	var responses []protocol.TransferFrame
	if err := host.HandleTransferFrame(newConnection, prepare, func(response protocol.TransferFrame) error {
		responses = append(responses, response)
		return nil
	}); err != nil || len(responses) != 1 || responses[0].Type != protocol.TransferFrameCommitted {
		t.Fatalf("reconnected prepare responses = %#v, %v", responses, err)
	}
	close(releaseCleanup)
	<-cleanupExited
	host.transferMu.Lock()
	artifact, found := host.completedTransfers[prepare.TransferID]
	host.transferMu.Unlock()
	if !found || artifact.lifetime == nil || artifact.lifetime.connectionDone != newConnection.Done() {
		t.Fatalf("reconnected artifact did not adopt the new lifetime: %#v", artifact)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("old watcher removed adopted artifact: %v", err)
	}
	disconnectNew()
	waitForBrowserArtifactRemoval(t, host, path)
}

func TestBrowserArtifactTransferAdmissionIsAtomicWithSessionClose(t *testing.T) {
	content := []byte("close-race")
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
		ActionInvocationID: "browser_file_chooser_close_race",
		ArtifactRef:        nodes.TransferArtifactRefPrefix + "artifact_close_race",
		PreparedActionHash: strings.Repeat("b", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
		AgentID: "browser", ActorID: "telegram:owner", Filename: "close-race.txt",
		ContentType: "text/plain", ExpiresAt: host.now().Add(time.Minute).Unix(),
	})
	admissionEntered := make(chan struct{})
	releaseAdmission := make(chan struct{})
	host.beforeTransferAdmission = func() {
		close(admissionEntered)
		<-releaseAdmission
	}
	type prepareResult struct {
		responses []protocol.TransferFrame
		err       error
	}
	prepared := make(chan prepareResult, 1)
	go func() {
		var responses []protocol.TransferFrame
		err := host.HandleTransferFrame(t.Context(), prepare, func(response protocol.TransferFrame) error {
			responses = append(responses, response)
			return nil
		})
		prepared <- prepareResult{responses: responses, err: err}
	}()
	<-admissionEntered
	type closeResult struct {
		session BrowserHostSession
		err     error
	}
	closed := make(chan closeResult, 1)
	closeStarted := make(chan struct{})
	go func() {
		close(closeStarted)
		session, err := host.Close(t.Context(), BrowserHostCloseRequest{
			SessionID: "browser_session_1", ProfileRevision: "managed-v1",
			RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
		})
		closed <- closeResult{session: session, err: err}
	}()
	<-closeStarted
	select {
	case result := <-closed:
		t.Fatalf("Close() crossed paused transfer admission: %#v, %v", result.session, result.err)
	default:
	}
	close(releaseAdmission)
	preparedResult := <-prepared
	if preparedResult.err != nil || len(preparedResult.responses) != 1 ||
		preparedResult.responses[0].Type != protocol.TransferFrameAccept {
		t.Fatalf("paused prepare responses = %#v, %v", preparedResult.responses, preparedResult.err)
	}
	closedResult := <-closed
	if closedResult.err != nil || closedResult.session.State != "closed" {
		t.Fatalf("Close() = %#v, %v", closedResult.session, closedResult.err)
	}
	host.transferMu.Lock()
	active, completed, root := len(host.activeTransfers), len(host.completedTransfers), host.transferRoot
	host.transferMu.Unlock()
	if active != 0 || completed != 0 {
		t.Fatalf("transfer survived concurrent close: active=%d completed=%d", active, completed)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("transfer root after concurrent close = %v, %v", entries, err)
	}
}

func TestBrowserArtifactTransferProactivelyCleansCommittedStageAtExpiry(t *testing.T) {
	content := []byte("expiring")
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
		ActionInvocationID: "browser_file_chooser_expiry",
		ArtifactRef:        nodes.TransferArtifactRefPrefix + "artifact_expiry",
		PreparedActionHash: strings.Repeat("b", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
		AgentID: "browser", ActorID: "telegram:owner", Filename: "expiring.txt",
		ContentType: "text/plain", ExpiresAt: host.now().Add(time.Second).Unix(),
	})
	path := stageCommittedBrowserArtifact(t, host, t.Context(), prepare, content)
	waitForBrowserArtifactRemoval(t, host, path)
}

func TestBrowserArtifactFileChooserRejectsNavigationBeforeUpload(t *testing.T) {
	content := []byte("navigation-bound")
	digest := sha256.Sum256(content)
	worker := &fakeBrowserHostWorker{
		status: browserworker.WorkerReady,
		observations: []browserworker.DriverObservation{
			browserUploadObservation(), browserUploadObservation(),
		},
		dispatchNavigationID: "navigation_1",
	}
	worker.beforeBoundDispatch = func() { worker.dispatchNavigationID = "navigation_2" }
	host := newTestBrowserHost(t, &fakeBrowserHostFactory{worker: worker})
	profile := host.profiles["managed"]
	profile.AllowedActions = append(profile.AllowedActions, "file_chooser")
	host.profiles["managed"] = profile
	if _, err := host.Open(t.Context(), browserHostOpenFixture()); err != nil {
		t.Fatal(err)
	}
	observed, err := host.Observe(t.Context(), browserHostObserveFixture())
	if err != nil {
		t.Fatal(err)
	}
	prepare := browserArtifactFrame(t, content, browserArtifactTransferPrepare{
		SessionID: "browser_session_1", RoutedSessionID: "routed_session_1",
		ActionInvocationID: "browser_file_chooser_navigation",
		ArtifactRef:        nodes.TransferArtifactRefPrefix + "artifact_navigation",
		PreparedActionHash: strings.Repeat("b", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
		AgentID: "browser", ActorID: "telegram:owner", Filename: "navigation.txt",
		ContentType: "text/plain", ExpiresAt: host.now().Add(time.Minute).Unix(),
	})
	path := stageCommittedBrowserArtifact(t, host, t.Context(), prepare, content)
	request := BrowserHostActRequest{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
		ActionInvocationID: "browser_file_chooser_navigation",
		Action: browserworker.Action{
			Kind: "file_chooser", Ref: observed.Elements[0].Ref,
			ArtifactRef: nodes.TransferArtifactRefPrefix + "artifact_navigation",
		},
		Effect: "local_edit", CurrentOrigin: "https://example.com",
		PreparedActionHash: strings.Repeat("b", 64), BrowserPolicyRevision: strings.Repeat("a", 64),
		ProfileRevision: "managed-v1", ExpectedRole: "button", ExpectedName: "Choose file",
		ArtifactSHA256: hex.EncodeToString(digest[:]), ArtifactBytes: int64(len(content)),
		ArtifactFilename: "navigation.txt", ArtifactContentType: "text/plain",
		RoutedSessionID: "routed_session_1", AgentID: "browser", ActorID: "telegram:owner",
	}
	if _, err = host.Act(t.Context(), request); !errors.Is(err, ErrBrowserHostStale) ||
		len(worker.actions) != 0 {
		t.Fatalf("FileChooser(navigation race) error = %v; actions = %#v", err, worker.actions)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("navigation-rejected staged path remains: %v", err)
	}
}

func stageCommittedBrowserArtifact(
	t *testing.T,
	host *BrowserHost,
	ctx context.Context,
	prepare protocol.TransferFrame,
	content []byte,
) string {
	t.Helper()
	var responses []protocol.TransferFrame
	if err := host.HandleTransferFrame(ctx, prepare, func(response protocol.TransferFrame) error {
		responses = append(responses, response)
		return nil
	}); err != nil || len(responses) != 1 || responses[0].Type != protocol.TransferFrameAccept {
		t.Fatalf("prepare responses = %#v, %v", responses, err)
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
	host.transferMu.Lock()
	artifact := host.completedTransfers[prepare.TransferID]
	host.transferMu.Unlock()
	if artifact.path == "" {
		t.Fatal("committed browser artifact is unavailable")
	}
	return artifact.path
}

func waitForBrowserArtifactRemoval(t *testing.T, host *BrowserHost, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		host.transferMu.Lock()
		active, completed := len(host.activeTransfers), len(host.completedTransfers)
		host.transferMu.Unlock()
		_, statErr := os.Stat(path)
		if active == 0 && completed == 0 && os.IsNotExist(statErr) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("browser artifact remained after lifecycle boundary: %s", path)
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
