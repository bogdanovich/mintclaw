package eval

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

type browserReadControl struct {
	fixture    *admissionFixture
	page       *browserFixture
	item       LoadedCase
	trial      Trial
	session    browser.Session
	action     browser.PreparedAction
	invocation browser.Invocation
}

// Synthetic native records exercise the grader, not a real browser or model.
// The production decoder must accept every positive fixture before projection.
func newBrowserReadControl(t *testing.T) *browserReadControl {
	t.Helper()
	fixture := newAdmissionFixture(t)
	enableAdmissionBrowser(t, fixture)
	item := browserCorpusCase(t, "read")
	trial := newUnattemptedTrial(item, fixture.preset.ExpectedAgent)
	trial.SessionKey, trial.Repetition = "native-read-route-"+trial.SessionID, 1
	trial.StartedAt = time.Now().Add(-3 * time.Second)
	trial.ExecutionVerdict, trial.ExecutionCode = Pass, "script_completed"
	trial.TransportClosed = true
	_, profile := browserEvalProfile(fixture.cfg)
	page, err := makeBrowserFixture(trial.SessionID, profile.AllowedOrigins[0])
	if err != nil {
		t.Fatal(err)
	}
	for turn := range 2 {
		requestID := uuid.NewString()
		trial.Events = append(trial.Events,
			Event{ID: uuid.NewString(), Turn: turn + 1, Kind: "input", RequestID: requestID, Content: "Read the page."},
			Event{
				ID: uuid.NewString(), Turn: turn + 1, Kind: "final", RequestID: requestID,
				MessageID: uuid.NewString(), Content: page.code, AgentID: trial.AgentID, SessionKey: trial.SessionKey,
				TraceScopes: []events.TraceScope{{Workspace: fixture.workspace, TurnID: "native-turn-" + requestID}},
			},
		)
	}
	owner := browser.Owner{
		ActorID: browser.OpaqueActorID("mintclaw:mintclaw-user"), AgentID: browser.OpaqueAgentID(trial.AgentID),
		SessionKey: browser.OpaqueSessionKey(trial.SessionKey), ExecutionID: "execution_not_a_trace_turn_id",
	}
	control := &browserReadControl{fixture: fixture, page: page, item: item, trial: trial}
	for _, path := range []string{"state/browser", "state/outbox"} {
		if err = os.MkdirAll(filepath.Join(fixture.workspace, path), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.Mkdir(filepath.Join(fixture.home, ownedGatewayTempDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	control.produceNativeState(t, owner)
	page.seal()
	control.writeFinals(t)
	return control
}

func (control *browserReadControl) target() admittedTarget {
	return admittedTarget{config: control.fixture.cfg, preset: control.fixture.preset}
}

func (control *browserReadControl) writeFinals(t *testing.T) {
	t.Helper()
	for _, event := range control.trial.Events {
		if event.Kind != "final" {
			continue
		}
		intent, err := newBrowserFinalIntent(control.fixture.workspace, control.trial, event)
		if err != nil {
			t.Fatal(err)
		}
		writeAdmissionJSON(t, filepath.Join(control.fixture.workspace, "state/outbox", intent.ID+".json"), intent)
	}
}

func (control *browserReadControl) observe(t *testing.T) browserNativeSnapshot {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(control.fixture.workspace, "state/browser/browser.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = browser.InspectStateSnapshot(raw); err != nil {
		t.Fatal("synthetic browser store invalid", err)
	}
	observed, err := observeBrowserNative(t.Context(), control.fixture.workspace, nil, nil)
	if err != nil {
		t.Fatal("synthetic native control rejected before projection", err)
	}
	return observed
}

func newBrowserFinalIntent(workspace string, trial Trial, event Event) (outbox.Intent, error) {
	intent, err := outbox.NewMessageIntent(workspace, outbox.Identity{
		SourceID: event.RequestID, Channel: "mintclaw", ChatID: "mintclaw:" + trial.SessionID,
		SessionKey: trial.SessionKey,
	}, bus.OutboundMessage{
		AgentID: trial.AgentID, SessionKey: trial.SessionKey, Content: event.Content,
		Context: bus.InboundContext{MessageID: event.RequestID}, TraceScopes: event.TraceScopes,
		Metadata: bus.OutboundMetadata{
			MessageKind: bus.OutboundMessageKindFinalReply, OutboundKind: bus.OutboundKindFinal,
		},
	}, time.Now())
	intent.Status, intent.PlatformMessageIDs = outbox.StatusDelivered, []string{event.MessageID}
	return intent, err
}

func (control *browserReadControl) finalize(t *testing.T) Trial {
	t.Helper()
	trial := control.trial
	proof := inspectBrowserRead(trial, control.target(), control.page, control.observe(t),
		browserTemporaryRootsEmpty(control.fixture.home))
	oracle, err := inspectBrowserFixture(control.item.Case, control.page, trial.Events, nil)
	if err != nil {
		t.Fatal(err)
	}
	trial.Browser = &BrowserTrialEvidence{
		Version: Version, Scope: "owned_fixture_execution_v1",
		Fixture: &oracle, NativeAuthority: "unavailable", NativeCleanup: "unavailable", Read: proof,
	}
	if proof.Code == "native_post_stop_observed" {
		trial.Browser.NativeAuthority, trial.Browser.NativeCleanup = "observed", "observed"
	}
	trial.Checks = gradeConversation(control.item.Case.Checks, trial.Events, lastOutcome(trial.Events))
	return trial
}

func TestBrowserNativeReadBindsOwnerActionPolicyDeliveryAndCleanup(t *testing.T) {
	for _, mode := range []string{
		"complete", "actor", "agent", "route", "session-execution", "action-execution", "invocation-execution",
		"policy", "revision", "dry-run", "origin", "url", "hash", "tab", "unsettled", "unknown", "lost", "temporary",
		"delivery-id", "delivery-request", "delivery-scope", "delivery-content", "missing-final", "foreign-native", "unknown-extra",
	} {
		t.Run(mode, func(t *testing.T) {
			control := newBrowserReadControl(t)
			observed := control.observe(t)
			proof := inspectBrowserRead(control.trial, control.target(), control.page, observed, true)
			if proof.Code != "native_post_stop_observed" {
				t.Fatal("positive control unavailable", proof.Code)
			}
			session, action, invocation := control.session, control.action, control.invocation
			temporaryEmpty := true
			switch mode {
			case "actor":
				session.Owner.ActorID = browser.OpaqueActorID("mintclaw:foreign")
			case "agent":
				session.Owner.AgentID = browser.OpaqueAgentID("foreign")
			case "route":
				session.Owner.SessionKey = browser.OpaqueSessionKey("foreign")
			case "session-execution":
				session.Owner.ExecutionID = "execution_foreign"
			case "action-execution":
				action.Owner.ExecutionID = "execution_foreign"
			case "invocation-execution":
				invocation.Owner.ExecutionID = "execution_foreign"
			case "policy":
				action.PolicyRevision = strings.Repeat("f", 64)
			case "revision":
				session.ProfileRevision = "changed"
			case "dry-run":
				session.DryRun = true
			case "origin":
				action.DestinationOrigin = "https://foreign.invalid"
			case "url":
				action.Action.URL += "foreign/"
			case "hash":
				invocation.ActionHash = strings.Repeat("f", 64)
			case "tab":
				action.TabID = "foreign_tab"
			case "unsettled":
				invocation.State = browser.InvocationAccepted
			case "unknown":
				invocation.State = browser.InvocationUnknown
			case "lost":
				session.State = browser.SessionLost
			case "temporary":
				temporaryEmpty = false
			case "delivery-id":
				observed.intents[0].PlatformMessageIDs = []string{"foreign-message"}
			case "delivery-request":
				observed.intents[0].Message.Context.MessageID = "foreign-request"
			case "delivery-scope":
				observed.intents[0].Message.TraceScopes = []events.TraceScope{{Workspace: "foreign", TurnID: "turn"}}
			case "delivery-content":
				observed.intents[0].Message.Content = "Contradictory native reply."
			case "missing-final":
				observed.intents = observed.intents[:1]
			case "foreign-native":
				observed.state.Sessions = map[string]browser.Session{}
			case "unknown-extra":
				uncertain := invocation
				uncertain.ID, uncertain.State = "unknown_extra", browser.InvocationUnknown
				uncertain.TerminalResult, uncertain.SafeFailure = nil, "worker_lost"
				if uncertain.Validate() != nil {
					t.Fatal("unknown invocation control invalid")
				}
				observed.state.Invocations[uncertain.ID] = uncertain
			}
			if mode != "foreign-native" {
				observed.state.Sessions[session.ID] = session
			}
			observed.state.PreparedActions[action.ID] = action
			observed.state.Invocations[invocation.ID] = invocation
			proof = inspectBrowserRead(control.trial, control.target(), control.page, observed, temporaryEmpty)
			if (proof.Code == "native_post_stop_observed") != (mode == "complete") {
				t.Fatal("native ownership or final delivery gap became positive evidence", mode, proof.Code)
			}
			if mode == "unknown-extra" && proof.Code != "native_cleanup_unavailable" {
				t.Fatal("unknown outcome was not retained as logical settlement uncertainty", proof.Code)
			}
		})
	}
}

func TestBrowserNativeReadContentMatchesConfiguredNativeFooter(t *testing.T) {
	for _, mode := range []string{
		"complete", "altered-text", "missing-footer", "altered-footer", "disabled", "disabled-unexpected-footer",
	} {
		t.Run(mode, func(t *testing.T) {
			control := newBrowserReadControl(t)
			enabled := !strings.HasPrefix(mode, "disabled")
			control.fixture.cfg.Agents.Defaults.ResponseFooter.Enabled = enabled
			for index, event := range control.trial.Events {
				if event.Kind != "final" {
					continue
				}
				intent, err := newBrowserFinalIntent(control.fixture.workspace, control.trial, event)
				if err != nil {
					t.Fatal(err)
				}
				intent.Message.Metadata.ModelName = "fallback-model"
				intent.Message.Metadata.DefaultModelName = "primary-model"
				intent.Message.Metadata.UsageInputTokens = 10252
				intent.Message.Metadata.UsageOutputTokens = 20
				projected := channels.DecorateOutboundResponseFooter(*intent.Message, enabled)
				control.trial.Events[index].Content = projected.Content
				switch mode {
				case "altered-text":
					intent.Message.Content = "Contradictory native reply."
				case "missing-footer":
					control.trial.Events[index].Content = event.Content
				case "altered-footer", "disabled-unexpected-footer":
					control.trial.Events[index].Content += "\n\nmodel: forged"
				}
				writeAdmissionJSON(
					t,
					filepath.Join(control.fixture.workspace, "state/outbox", intent.ID+".json"),
					intent,
				)
			}
			trial := control.finalize(t)
			if (trial.Browser.Read.Code == "native_post_stop_observed") != (mode == "complete" || mode == "disabled") {
				t.Fatal(
					"native final content or configured footer mismatch was promoted",
					mode,
					trial.Browser.Read.Code,
				)
			}
		})
	}
}

func TestBrowserNativeCaptureScansRawBeforeLossyProjectionAndRejectsAliases(t *testing.T) {
	for _, mode := range []string{"encoded", "duplicate", "malformed", "unknown", "file-alias", "directory-alias", "mode"} {
		t.Run(mode, func(t *testing.T) {
			control := newBrowserReadControl(t)
			path := filepath.Join(control.fixture.workspace, "state/browser/browser.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "encoded", "duplicate":
				data = bytes.Replace(
					data,
					[]byte(`"version":2`),
					[]byte(`"version":2,"discarded":"\u0050RIVATE_BROWSER_CONTROL","discarded":"public"`),
					1,
				)
				if mode == "encoded" {
					data = bytes.Replace(data, []byte(`,"discarded":"public"`), nil, 1)
				}
			case "malformed":
				data = []byte("{\"unfinished\":")
			case "unknown":
				data = append([]byte(`{"unknown":true,`), data[1:]...)
			case "file-alias":
				if os.Rename(path, path+".original") != nil || os.Symlink(path+".original", path) != nil {
					t.Fatal("alias control unavailable")
				}
			case "directory-alias":
				parent := filepath.Dir(path)
				if os.Rename(parent, parent+"-original") != nil || os.Symlink(parent+"-original", parent) != nil {
					t.Fatal("alias control unavailable")
				}
			}
			if mode != "file-alias" && mode != "directory-alias" && os.WriteFile(path, data, 0o600) != nil {
				t.Fatal("write control")
			}
			if mode == "mode" && os.Chmod(path, 0o644) != nil {
				t.Fatal("permission control")
			}
			_, err = observeBrowserNative(t.Context(), control.fixture.workspace,
				map[string]string{"name": "PRIVATE_BROWSER_CONTROL"}, nil)
			if err == nil || (mode == "encoded" || mode == "duplicate") && !errors.Is(err, ErrPrivateEvidence) ||
				strings.Contains(err.Error(), "PRIVATE_BROWSER_CONTROL") {
				t.Fatal("raw native privacy or filesystem boundary lost", mode, err)
			}
		})
	}
}

func TestBrowserNativeOutboxCaptureRefusesLossBoundsAndAliases(t *testing.T) {
	for _, mode := range []string{"encoded", "duplicate", "unknown", "malformed", "oversized", "alias", "mode"} {
		t.Run(mode, func(t *testing.T) {
			control := newBrowserReadControl(t)
			observed := control.observe(t)
			path := filepath.Join(control.fixture.workspace, "state/outbox", observed.intents[0].ID+".json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "encoded":
				data = append([]byte(`{"discarded":"\u0050RIVATE_BROWSER_OUTBOX","discarded":"public",`), data[1:]...)
			case "duplicate":
				data = append([]byte(`{"version":1,`), data[1:]...)
			case "unknown":
				data = append([]byte(`{"unknown":true,`), data[1:]...)
			case "malformed":
				data = []byte("{\"unfinished\":")
			case "oversized":
				data = bytes.Repeat([]byte("x"), outbox.MaxInspectionBytes+1)
			case "alias":
				if os.Rename(path, path+".original") != nil || os.Symlink(path+".original", path) != nil {
					t.Fatal("alias control unavailable")
				}
			}
			if mode != "alias" && os.WriteFile(path, data, 0o600) != nil {
				t.Fatal("outbox control write")
			}
			if mode == "mode" && os.Chmod(path, 0o644) != nil {
				t.Fatal("mode control unavailable")
			}
			_, err = observeBrowserNative(t.Context(), control.fixture.workspace,
				map[string]string{"fact": "PRIVATE_BROWSER_OUTBOX"}, nil)
			if err == nil || mode == "encoded" && !errors.Is(err, ErrPrivateEvidence) ||
				strings.Contains(err.Error(), "PRIVATE_BROWSER_OUTBOX") {
				t.Fatal("raw outbox guard lost original evidence", mode, err)
			}
		})
	}
}

func TestBrowserTemporaryRootsAreObservedNeverCleanedToPromoteARead(t *testing.T) {
	control := newBrowserReadControl(t)
	if !browserTemporaryRootsEmpty(control.fixture.home) {
		t.Fatal("empty owned roots unavailable")
	}
	for _, path := range []string{browserRuntimeDirectory + "/ephemeral", ownedGatewayTempDirectory} {
		file := filepath.Join(control.fixture.home, path, "retained-control")
		if os.WriteFile(file, []byte("retained"), 0o600) != nil {
			t.Fatal("retained control unavailable")
		}
		if browserTemporaryRootsEmpty(control.fixture.home) {
			t.Fatal("retained browser scratch became a cleanup pass")
		}
		data, err := os.ReadFile(file)
		if err != nil || string(data) != "retained" {
			t.Fatal("observer deleted or changed retained evidence")
		}
		if os.Remove(file) != nil {
			t.Fatal("cannot reset our synthetic control")
		}
	}
}
