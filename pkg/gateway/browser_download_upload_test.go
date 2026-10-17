package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestGatewayBrowserDownloadUploadsAfterFreshObservation(t *testing.T) {
	workspace := t.TempDir()
	cfg := gatewayBrowserConfig(workspace)
	profile := cfg.Tools.Browser.Targets[config.BrowserDefaultTarget].Profiles[config.BrowserDefaultProfile]
	profile.DryRun, profile.AllowApprovedActions = false, true
	profile.ApprovalMode = config.BrowserApprovalModelRequested
	cfg.Tools.Browser.Targets[config.BrowserDefaultTarget].Profiles[config.BrowserDefaultProfile] = profile
	policyRevision, err := cfg.Tools.Browser.PolicyRevision()
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("original browser download retained for later upload")
	digest := sha256.Sum256(data)
	path := filepath.Join(t.TempDir(), "original.txt")
	if err = os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	worker := &gatewayDownloadUploadWorker{
		gatewayUploadWorker: gatewayUploadWorker{want: data},
		download: browser.DriverDownload{
			Path: path, Filename: "original.txt", ContentType: "text/plain",
			Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:]),
		},
	}
	broker, err := browser.NewBroker(cfg, browser.NewMemoryStore(), &gatewayWorkerFactory{worker: worker})
	if err != nil {
		t.Fatal(err)
	}
	nodeRuntime := &nodeAdmissionRuntime{}
	t.Cleanup(func() {
		if nodeRuntime.transferSpool != nil {
			_ = nodeRuntime.transferSpool.Close()
		}
	})
	source := &gatewayBrowserToolSource{
		services: &services{
			Browser: &browserRuntime{broker: broker, policyRevision: policyRevision}, NodeAdmission: nodeRuntime,
		},
		workspace: workspace, policyRevision: policyRevision,
		screenshotRetention: time.Hour, limits: cfg.Tools.Browser.Limits.Effective(),
	}
	ctx := gatewayBrowserArtifactContext(workspace)
	owner := browser.Owner{
		ActorID: browser.OpaqueActorID("actor_1"), AgentID: browser.OpaqueAgentID("browser"),
		SessionKey: "route_1", ExecutionID: "execution_1",
	}
	session, err := broker.Open(ctx, browser.OpenRequest{
		Owner: owner, Target: config.BrowserDefaultTarget, Profile: config.BrowserDefaultProfile,
	})
	if err != nil {
		t.Fatal(err)
	}
	observe := func() browser.Observation {
		t.Helper()
		observed, observeErr := broker.Observe(ctx, owner, session.ID, session.TabID)
		if observeErr != nil {
			t.Fatal(observeErr)
		}
		return observed
	}
	ref := func(observed browser.Observation) string {
		t.Helper()
		match := regexp.MustCompile(`\[ref=([^]]+)\]`).FindStringSubmatch(observed.Snapshot)
		if len(match) != 2 {
			t.Fatalf("snapshot has no exact ref: %q", observed.Snapshot)
		}
		return match[1]
	}
	initial := observe()
	download, err := source.PrepareAction(ctx, browser.PrepareActionRequest{
		Owner: owner, RequestID: "capture_original", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: initial.SnapshotID, SnapshotGeneration: initial.SnapshotGeneration,
		Action: browser.Action{Kind: browser.ActionDownload, Ref: ref(initial)},
	})
	if err != nil {
		t.Fatal(err)
	}
	captured, err := source.ExecuteAction(ctx, owner, download.Action.ID, &download.Approval)
	if err != nil || captured.State != browser.InvocationSucceeded || captured.Download == nil {
		t.Fatalf("download = %#v, %v", captured, err)
	}
	fresh := observe()
	if fresh.SnapshotID == initial.SnapshotID || fresh.SnapshotGeneration <= initial.SnapshotGeneration {
		t.Fatal("destination did not receive fresh authority")
	}
	request := browser.PrepareActionRequest{
		Owner: owner, RequestID: "upload_original", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: fresh.SnapshotID, SnapshotGeneration: fresh.SnapshotGeneration,
		Action: browser.Action{Kind: browser.ActionUpload, Ref: ref(fresh), ArtifactRef: captured.Download.Ref},
	}
	prepared, err := source.PrepareAction(ctx, request)
	if err != nil {
		t.Fatalf("owned download rejected after fresh observation: %v", err)
	}
	invocation, err := source.ExecuteAction(ctx, owner, prepared.Action.ID, &prepared.Approval)
	if err != nil || invocation.State != browser.InvocationSucceeded || !bytes.Equal(worker.got, data) {
		t.Fatalf("upload = %#v, %v; received %d bytes", invocation, err, len(worker.got))
	}
	for name, wrongContext := range map[string]context.Context{
		"agent": toolshared.WithToolSessionContext(ctx, "main", "history-1", nil),
		"actor": toolshared.WithToolInboundMetadata(ctx, bus.InboundContext{SenderID: "other", ActorID: "other"}),
		"route": toolshared.WithToolRouteSessionKey(ctx, "other-route"),
	} {
		t.Run("reject_"+name, func(t *testing.T) {
			if _, resolveErr := source.resolveBrowserUpload(
				wrongContext,
				request,
			); !errors.Is(
				resolveErr,
				browser.ErrDenied,
			) {
				t.Fatalf("ownership boundary = %v", resolveErr)
			}
		})
	}
	wrongSession := request
	wrongSession.SessionID = "other_session"
	if _, err = source.resolveBrowserUpload(ctx, wrongSession); !errors.Is(err, browser.ErrDenied) {
		t.Fatalf("cross-session upload = %v", err)
	}
	// Reusing bytes does not authorize a stale destination reference.
	staleDestination := request
	staleDestination.RequestID = "stale_destination"
	if _, err = source.PrepareAction(ctx, staleDestination); !errors.Is(err, browser.ErrStale) {
		t.Fatalf("stale destination authority = %v", err)
	}
	if err = os.WriteFile(worker.path, []byte("corrupted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = source.resolveBrowserUpload(ctx, request); !errors.Is(err, browser.ErrDenied) {
		t.Fatalf("corrupt artifact = %v", err)
	}
}

type gatewayDownloadUploadWorker struct {
	gatewayUploadWorker
	download browser.DriverDownload
}

func (worker *gatewayDownloadUploadWorker) Download(
	context.Context, browser.DriverAction, int64,
) (browser.DriverDownload, error) {
	return worker.download, nil
}
