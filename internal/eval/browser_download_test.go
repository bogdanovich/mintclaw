package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

type browserDownloadWorker struct {
	*browserReadWorker
	path string
}

func (worker *browserDownloadWorker) Open(
	context.Context,
	browser.WorkerOpenRequest,
) (browser.WorkerOpenResult, error) {
	return browser.WorkerOpenResult{Owner: worker, Capabilities: browser.WorkerCapabilityActions}, nil
}

func (worker *browserDownloadWorker) Observe(context.Context) (browser.DriverObservation, error) {
	return browser.DriverObservation{
		URL: worker.page.origin + worker.page.path, Origin: worker.page.origin,
		Snapshot: `- link "Download fixture report" [ref=download]`,
		Elements: []browser.DriverElement{{Target: "download", Role: "link", Name: "Download fixture report"}},
	}, nil
}

func (worker *browserDownloadWorker) Resolve(_ context.Context, target string) (browser.DriverElement, string, error) {
	if target != "download" {
		return browser.DriverElement{}, "", browser.ErrNotFound
	}
	return browser.DriverElement{Target: target, Role: "link", Name: "Download fixture report"}, worker.page.origin, nil
}

func (worker *browserDownloadWorker) Download(
	_ context.Context, action browser.DriverAction, maximum int64,
) (browser.DriverDownload, error) {
	if action.Kind != browser.DriverDownloadAction || action.Target != "download" {
		return browser.DriverDownload{}, browser.ErrDenied
	}
	response := httptest.NewRecorder()
	worker.page.ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, worker.page.origin+worker.page.path+"report.csv", nil),
	)
	data := response.Body.Bytes()
	if response.Code != http.StatusOK || len(data) == 0 || int64(len(data)) > maximum ||
		os.WriteFile(worker.path, data, 0o600) != nil {
		return browser.DriverDownload{}, browser.ErrWorkerUnavailable
	}
	return browser.DriverDownload{
		Path:        worker.path,
		Filename:    "fixture-report.csv",
		ContentType: "text/csv",
		SHA256:      digest(data),
		Size:        int64(len(data)),
	}, nil
}

func (*browserDownloadWorker) Upload(context.Context, browser.DriverAction) error {
	return browser.ErrDenied
}

type browserDownloadControl struct {
	*browserReadControl
	action   browser.PreparedAction
	artifact browser.DownloadArtifact
	record   nodes.TransferArtifactRecord
	intent   outbox.Intent
}

// Native broker, transfer spool and outbox producers generate identities and
// hashes. The stub worker/approval is not a live browser or approval qualification.
func newBrowserDownloadControl(t *testing.T) *browserDownloadControl {
	return browserDownloadControlWithHook(t, nil)
}

func browserDownloadControlWithHook(
	t *testing.T, beforeExecute func(*browserDownloadControl, browser.Preparation),
) *browserDownloadControl {
	t.Helper()
	read := newBrowserReadControl(t)
	read.item = browserCorpusCase(t, "artifact")
	read.trial.CaseID, read.trial.CaseSHA256 = read.item.Case.ID, read.item.Digest
	var err error
	read.page, err = makeBrowserFixture(read.trial.SessionID, read.page.origin)
	if err != nil {
		t.Fatal(err)
	}
	control := &browserDownloadControl{browserReadControl: read}
	store, err := browser.NewFileStore(filepath.Join(read.fixture.workspace, "state/browser/browser.json"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	worker := &browserDownloadWorker{
		browserReadWorker: &browserReadWorker{page: read.page, origin: read.page.origin},
		path:              filepath.Join(t.TempDir(), "download.csv"),
	}
	broker, err := browser.NewBroker(read.fixture.cfg, store, worker)
	if err != nil {
		t.Fatal(err)
	}
	owner := read.session.Owner
	owner.ExecutionID = browser.OpaqueExecutionID("fixture-runtime-execution")
	session, err := broker.Open(t.Context(), browser.OpenRequest{
		Owner: owner, Target: config.BrowserDefaultTarget, Profile: "eval-browser",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.CloseOwner(context.Background(), owner) })
	observation, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	_, ref, found := strings.Cut(observation.Snapshot, "[ref=")
	ref, _, closed := strings.Cut(ref, "]")
	if err != nil || !found || !closed {
		t.Fatal("native download observation unavailable", err)
	}
	prepared, err := broker.PrepareAction(t.Context(), browser.PrepareActionRequest{
		Owner: owner, RequestID: browser.OpaqueRequestID("fixture-runtime-execution", "fixture-download-call"),
		SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: browser.Action{Kind: browser.ActionDownload, Ref: ref, Deliver: true},
	})
	if err != nil || !prepared.RequiresApproval {
		t.Fatal("native download preparation unavailable", err)
	}
	control.action = prepared.Action
	if beforeExecute != nil {
		beforeExecute(control, prepared)
	}
	spool, err := nodes.NewGatewayTransferSpool(nodes.GatewayTransferSpoolPath(read.fixture.workspace), 0, 0, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = spool.Close() })
	mediaOwner, err := media.NewMediaOwner(read.fixture.workspace, read.trial.AgentID, "mintclaw-user",
		read.trial.SessionKey, read.trial.SessionKey, "mintclaw", "mintclaw:"+read.trial.SessionID, "")
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := broker.ExecuteActionWithDownloadSink(
		t.Context(),
		owner,
		prepared.Action.ID,
		&prepared.Approval,
		func(_ context.Context, action browser.PreparedAction, download browser.DriverDownload) (json.RawMessage, error) {
			data, readErr := os.ReadFile(download.Path)
			if readErr != nil {
				return nil, readErr
			}
			defer clear(data)
			transferOwner := nodes.TransferArtifactOwner{
				WorkspaceID: mediaOwner.WorkspaceID, AgentID: mediaOwner.AgentID, ActorID: mediaOwner.ActorID,
				RouteID: mediaOwner.RouteID, SessionID: session.ID, ToolCallID: action.RequestID,
			}
			spec := nodes.TransferArtifactSpec{
				TransferID: action.RequestID, Direction: nodes.TransferDirectionDownload, Target: action.Target,
				ProfileRevision: action.ProfileRevision, SourceKind: "browser_download", SourceID: action.ID,
				SourceScope: action.TabID, SourceRevision: action.SnapshotGeneration, Filename: download.Filename,
				ContentType: download.ContentType, DeclaredSize: download.Size, SHA256: download.SHA256,
				ExpiresAt: time.Now().Add(30 * time.Minute).Unix(),
			}
			writer, _, created, beginErr := spool.Begin(transferOwner, spec)
			if beginErr != nil || !created || writer == nil {
				return nil, errors.New("native transfer control unavailable")
			}
			if writeErr := writer.WriteChunk(1, data); writeErr != nil {
				return nil, writeErr
			}
			record, commitErr := writer.Commit()
			if commitErr != nil {
				return nil, commitErr
			}
			control.record = record
			control.artifact = browser.DownloadArtifact{
				Ref: record.Ref, Kind: "download", ContentType: spec.ContentType, Filename: spec.Filename,
				Size: spec.DeclaredSize, SHA256: spec.SHA256, ExpiresAt: spec.ExpiresAt, SessionID: session.ID,
				TabID: action.TabID, Generation: action.SnapshotGeneration,
			}
			return json.Marshal(map[string]any{"status": "completed", "artifact": control.artifact})
		},
	)
	if err != nil || invocation.State != browser.InvocationSucceeded || control.record.ArtifactID == "" {
		t.Fatal("native download execution unavailable", err, invocation.State, string(invocation.TerminalResult))
	}
	control.record, _, err = spool.ClaimDelivery(
		control.record.Owner,
		control.record.Ref,
		"media://fixture",
		"delivery_fixture",
	)
	if err != nil {
		t.Fatal(err)
	}
	event := read.trial.Events[1]
	intent, err := outbox.NewMediaIntent(read.fixture.workspace, outbox.Identity{
		SourceID: prepared.Action.RequestID, Channel: "mintclaw", ChatID: "mintclaw:" + read.trial.SessionID,
		SessionKey: read.trial.SessionKey,
	}, bus.OutboundMediaMessage{
		AgentID: read.trial.AgentID, SessionKey: read.trial.SessionKey, TraceScopes: event.TraceScopes,
		Parts: []bus.MediaPart{
			{
				Type:        "file",
				Ref:         control.record.MediaRef,
				Filename:    control.artifact.Filename,
				ContentType: control.artifact.ContentType,
			},
		},
		Recovery: &bus.OutboundRecovery{
			Kind:        bus.OutboundRecoveryBrowserDownload,
			ArtifactRef: control.record.Ref,
			MediaRef:    control.record.MediaRef,
			WorkspaceID: mediaOwner.WorkspaceID,
			AgentID:     mediaOwner.AgentID,
			ActorID:     mediaOwner.ActorID,
			RouteID:     mediaOwner.RouteID,
			SessionID:   session.ID,
			ToolCallID:  prepared.Action.RequestID,
		},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	intent.Status, intent.PlatformMessageIDs = outbox.StatusDelivered, []string{uuid.NewString()}
	control.intent = intent
	writeAdmissionJSON(t, filepath.Join(read.fixture.workspace, "state/outbox", intent.ID+".json"), intent)
	if _, err = broker.Close(t.Context(), owner, session.ID); err != nil {
		t.Fatal(err)
	}
	read.page.seal()
	return control
}

func (control *browserDownloadControl) transfers(t *testing.T) browserTransferSnapshot {
	t.Helper()
	observed, err := observeBrowserTransfers(t.Context(), control.fixture.workspace, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return observed
}

func TestBrowserDownloadMetadataBindsNativeActionTransferAndMediaIntent(t *testing.T) {
	for _, mode := range []string{
		"complete", "owner", "route", "source", "generation", "target", "profile", "transfer-id", "session",
		"digest", "claim", "pending", "wrong-artifact", "dry-run", "unsettled", "delivery", "recovery", "part", "duplicate",
		"claim-before-completion", "future-update", "media-query", "origin",
	} {
		t.Run(mode, func(t *testing.T) {
			control := newBrowserDownloadControl(t)
			observed, transfers := control.observe(t), control.transfers(t)
			record := transfers.Records[control.record.ArtifactID]
			for index := range observed.intents {
				intent := &observed.intents[index]
				if intent.ID != control.intent.ID {
					continue
				}
				switch mode {
				case "delivery":
					intent.Identity.SessionKey = "foreign-route"
				case "recovery":
					intent.Media.Recovery.ToolCallID = "foreign-call"
				case "part":
					intent.Media.Parts[0].Ref = "media://foreign"
				case "duplicate":
					observed.intents = append(observed.intents, *intent)
				}
			}
			action := observed.state.PreparedActions[control.action.ID]
			switch mode {
			case "owner":
				record.Owner.ActorID = "foreign_actor"
			case "route":
				record.Owner.RouteID = "foreign_route"
			case "source":
				record.Spec.SourceID = "foreign_action"
			case "generation":
				record.Spec.SourceRevision++
			case "target":
				record.Spec.Target = "foreign_target"
			case "profile":
				record.Spec.ProfileRevision = "foreign_profile"
			case "transfer-id":
				record.Spec.TransferID = "foreign_transfer"
			case "session":
				record.Owner.SessionID = "foreign_session"
			case "digest":
				record.Spec.SHA256 = strings.Repeat("f", 64)
			case "claim":
				record.MediaRef, record.DeliveryKey, record.DeliveryAt = "", "", 0
			case "claim-before-completion":
				record.DeliveryAt = record.CreatedAt
			case "future-update":
				record.UpdatedAt = time.Now().Add(time.Hour).UnixNano()
			case "media-query":
				record.MediaRef += "?unbound=1"
			case "pending":
				record.State = nodes.TransferArtifactStaging
			case "wrong-artifact":
				record.Ref = "transfer-artifact://foreign"
			case "dry-run":
				action.DryRun = true
			case "origin":
				action.CurrentOrigin = "https://foreign.invalid"
			case "unsettled":
				session := observed.state.Sessions[action.SessionID]
				session.State = browser.SessionReady
				observed.state.Sessions[session.ID] = session
			}
			observed.state.PreparedActions[action.ID] = action
			transfers.Records[record.ArtifactID] = record
			proof := inspectBrowserDownload(control.trial, control.target(), observed, transfers)
			if (proof.Code == "native_metadata_observed") != (mode == "complete") {
				t.Fatal("foreign or incomplete native download became bound metadata", mode, proof.Code)
			}
		})
	}
}

func TestBrowserTransferCaptureScansOriginalBytesBeforeNativeProjection(t *testing.T) {
	for _, mode := range []string{"encoded", "duplicate", "unknown", "alias", "mode", "budget"} {
		t.Run(mode, func(t *testing.T) {
			control := newBrowserDownloadControl(t)
			path := nodes.GatewayTransferSpoolSnapshotPath(control.fixture.workspace)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "encoded", "duplicate":
				raw = bytes.Replace(raw, []byte(`"version":1`),
					[]byte(`"version":1,"discarded":"\u0050RIVATE_DOWNLOAD_CONTROL"`), 1)
				if mode == "duplicate" {
					raw = bytes.Replace(raw, []byte(`,"records":`), []byte(`,"discarded":"public","records":`), 1)
				}
			case "unknown":
				raw = append([]byte(`{"unknown":true,`), raw[1:]...)
			case "alias":
				if os.Rename(path, path+".original") != nil || os.Symlink(path+".original", path) != nil {
					t.Fatal("alias control unavailable")
				}
			case "mode":
				if os.Chmod(path, 0o644) != nil {
					t.Fatal("mode control unavailable")
				}
			case "budget":
				raw = make([]byte, maxPrivateScanFileBytes+1)
			}
			if mode != "alias" && mode != "mode" && os.WriteFile(path, raw, 0o600) != nil {
				t.Fatal("raw negative control unavailable")
			}
			observed, err := observeBrowserTransfers(t.Context(), control.fixture.workspace,
				map[string]string{"fact": "PRIVATE_DOWNLOAD_CONTROL"}, nil)
			if err == nil || observed.sha256 != "" || len(observed.Records) != 0 ||
				(mode == "encoded" || mode == "duplicate") && !errors.Is(err, ErrPrivateEvidence) {
				t.Fatal("private, lossy or unsafe native transfer was projected", mode, err)
			}
		})
	}
}

func TestBrowserDownloadFinalizationRetainsMetadataButCannotJudgeOrHideRawLeak(t *testing.T) {
	for _, mode := range []string{"metadata", "raw-leak"} {
		t.Run(mode, func(t *testing.T) {
			control := newBrowserDownloadControl(t)
			trial := control.trial
			trial.Checks = gradeConversation(control.item.Case.Checks, trial.Events, lastOutcome(trial.Events))
			trial.Verdict, trial.Code = Inconclusive, "required_check_unknown"
			writer, err := newPrivateReport(control.fixture.output, control.fixture.home,
				control.fixture.workspace, uuid.NewString())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = writer.root.Close() }()
			name := "trial-" + trial.ID + ".json"
			if writer.write("pending-"+name, trial) != nil {
				t.Fatal("pending artifact control unavailable")
			}
			pending := browserRun{}
			pending.retain(control.item.Case, trial, control.page)
			facts := map[string]string{}
			if mode == "raw-leak" {
				facts["private"] = "PRIVATE_DOWNLOAD_CONTROL"
				if os.WriteFile(nodes.GatewayTransferSpoolSnapshotPath(control.fixture.workspace),
					[]byte(`{"version":1,"discarded":"\u0050RIVATE_DOWNLOAD_CONTROL","records":{}}`), 0o600) != nil {
					t.Fatal("raw transfer leak control unavailable")
				}
			}
			result := RunReport{
				TargetLifecycle: cleanPDFLifecycle(),
				PrivateSurfaces: &PrivateSurfaceObservation{Scope: "owned_profile_files_v1", Code: "observed"},
				BrowserFixtures: browserReadHost(control.browserReadControl), Trials: []TrialSummary{{
					ID: trial.ID, CaseID: trial.CaseID, CaseSHA256: trial.CaseSHA256, Repetition: trial.Repetition,
					Verdict: trial.Verdict, Code: trial.Code, DurationMS: trial.DurationMS, File: name,
				}},
			}
			provider := &roleTestProvider{mediated: true, response: &providers.LLMResponse{Content: "invented pass"}}
			judge := &ModelRole{provider: provider, identity: RoleIdentity{Role: "judge"}}
			if err = pending.finish(t.Context(), control.target(), writer, &result, facts, nil,
				func(item Case, current *Trial) {
					assessFinalizedBrowser(t.Context(), judge, item, current, control.fixture.workspace,
						result.TargetLifecycle, result.PrivateSurfaces, result.BrowserFixtures)
				}); err != nil {
				t.Fatal(err)
			}
			final := comparisonTrial(t, RunReport{ReportDir: writer.dir, Trials: result.Trials}, 0)
			if provider.calls != 0 || final.EvidenceEligible || final.Verdict == Pass ||
				final.Browser.NativeAuthority != "unavailable" || final.Browser.NativeCleanup != "unavailable" {
				t.Fatal("metadata became approval, bytes, wire delivery or judge authority", mode)
			}
			if mode == "metadata" && (final.Browser.Download == nil ||
				final.Browser.Download.Code != "native_metadata_observed" || final.Verdict != Inconclusive) {
				t.Fatal("native metadata receipt not retained", final.Code)
			}
			if mode == "raw-leak" && final.Verdict != Fail {
				t.Fatal("raw transfer leak hidden by decoding failure", final.Code)
			}
		})
	}
}
