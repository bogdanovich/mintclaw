package eval

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestBrowserArtifactApprovalIsExactAndSingleUse(t *testing.T) {
	for _, mode := range []string{"complete", "prompt", "route", "request", "short-id", "key", "action", "replay"} {
		t.Run(mode, func(t *testing.T) {
			browserDownloadControlWithHook(t, func(control *browserDownloadControl, prepared browser.Preparation) {
				approval := browserApprovalControl(t, control, prepared)
				if err := approval.domain.observeApproval(t.Context(), approval.prompt); err != nil {
					t.Fatal(err)
				}
				prompt := approval.prompt
				switch mode {
				case "prompt":
					prompt.MessageID = "foreign-prompt"
				case "route":
					prompt.SessionKey = "foreign-route"
				case "request":
					prompt.RequestID = "foreign-request"
				case "short-id":
					prompt.InteractionShortID = "foreign"
				case "key":
					path := filepath.Join(control.fixture.workspace, "state/interaction_hmac.key")
					if os.WriteFile(path, bytes.Repeat([]byte{0}, 32), 0o600) != nil {
						t.Fatal("key control unavailable")
					}
				case "action":
					approval.domain.approval.action.ActionHash = strings.Repeat("a", 64)
				case "replay":
					if _, err := approval.domain.approveDownload(prompt); err != nil {
						t.Fatal(err)
					}
				}
				request, err := approval.domain.approveDownload(prompt)
				if mode == "complete" {
					if err != nil || request.ID == "" || request.SessionID != control.trial.SessionID {
						t.Fatal("bound allow_once refused", err)
					}
				} else if !errors.Is(err, errBrowserEvidence) || request.ID != "" {
					t.Fatal("foreign, changed, or repeated approval admitted", mode, err)
				}
			})
		})
	}
}

func TestBrowserArtifactNeverPromotesIncompleteReceipts(t *testing.T) {
	control := newBrowserArtifactControl(t)
	_, original := control.finalized(t)
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{
		"complete", "metadata-only", "raw", "approval", "prompt", "input", "revision", "future", "wire",
		"wire-incomplete", "native", "source", "bytes", "file", "cleanup", "privacy", "hard", "judge-private",
	} {
		t.Run(mode, func(t *testing.T) {
			trial, err := decodeDocument[Trial](encoded, 8*1024*1024)
			if err != nil {
				t.Fatal(err)
			}
			surfaces := &PrivateSurfaceObservation{Scope: "owned_profile_files_v1", Code: "observed"}
			item := control.item.Case
			provider := &roleTestProvider{mediated: true, response: &providers.LLMResponse{
				Content: browserArtifactJudgePass(trial), FinishReason: "stop",
			}}
			artifact := trial.Browser.Artifact
			switch mode {
			case "metadata-only":
				artifact.Code = "native_metadata_observed"
			case "raw":
				artifact.RawInspected = false
			case "approval":
				artifact.Approval = nil
			case "prompt":
				trial.Events[1].MessageID = "another-prompt"
			case "input":
				artifact.Approval.ApprovalRequestID = "another-input"
			case "revision":
				artifact.Approval.ConsumedRevision = artifact.Approval.WaitingRevision
			case "future":
				artifact.Approval.ConsumedAt = time.Now().Add(time.Hour).UnixMilli()
			case "wire":
				artifact.Wire.MessageID = "other-media"
			case "wire-incomplete":
				artifact.Wire.Complete = false
			case "native":
				artifact.Native = nil
			case "source":
				trial.Browser.Download.TransfersSHA256 = "unobserved"
			case "bytes":
				artifact.Bytes++
			case "file":
				artifact.File = "elsewhere.csv"
			case "cleanup":
				artifact.Native.TemporaryEmpty = false
			case "privacy":
				surfaces.Code = "unavailable"
			case "hard":
				item.Checks = append(
					append([]Check{}, item.Checks...),
					Check{ID: "contradiction", Kind: "response_contains", Expected: "not-present"},
				)
			case "judge-private":
				provider.response.Content += "artifact-judge-private-canary"
			}
			judge := &ModelRole{
				provider: provider, identity: RoleIdentity{Role: "judge"},
				filter:   strings.NewReplacer("artifact-judge-private-canary", "[redacted]"),
				retained: &privateFactRetention{values: map[string]string{"fact": "artifact-judge-private-canary"}},
			}
			assessFinalizedBrowser(t.Context(), judge, item, &trial, control.fixture.workspace,
				cleanPDFLifecycle(), surfaces, browserReadHost(control.browserReadControl))
			if mode == "complete" {
				if trial.Verdict != Pass || provider.calls != 1 {
					t.Fatal("complete artifact refused", trial.Code)
				}
			} else if mode == "hard" || mode == "judge-private" {
				if trial.Verdict != Fail {
					t.Fatal("hard/privacy failure lost", trial.Code)
				}
			} else if trial.Verdict == Pass || trial.EvidenceEligible || provider.calls != 0 {
				t.Fatal("incomplete artifact reached a billable judge", mode, trial.Code)
			}
		})
	}
}

func TestBrowserArtifactOriginalBodyPrivacyBeforeLossyProjection(t *testing.T) {
	private := "artifact-source-private-canary"
	for _, value := range []string{private, `artifact-source-private\u002dcanary`} {
		data := []byte("session,reference\ncontrol," + value + "\n")
		if !errors.Is(inspectBrowserArtifactBody(data, map[string]string{"secret": private}, nil), ErrPrivateEvidence) {
			t.Fatal("original or encoded private bytes were not rejected")
		}
	}
	for _, data := range [][]byte{{0xff}, {'a', 0, 'b'}} {
		if !errors.Is(inspectBrowserArtifactBody(data, nil, nil), errNativeEvidence) {
			t.Fatal("opaque body promoted")
		}
	}
}

func TestBrowserArtifactNativeApprovalProfilePrivacy(t *testing.T) {
	control := newBrowserArtifactControl(t)
	pending := browserRun{trials: map[string]browserPendingTrial{control.trial.ID: {conversation: control.domain}}}
	witness := pending.approvalKeyWitness(control.fixture.home)
	result := observePrivateSurfaces(t.Context(), control.fixture.home, nil, nil, nil, witness)
	if result.Code != "observed" || result.AuthorizedInteractionKeys != 1 {
		t.Fatalf(
			"native approval profile privacy incomplete: code=%s opaque=%d files=%d",
			result.Code,
			result.OpaqueFiles,
			result.Files,
		)
	}
}

func TestBrowserArtifactNativeFinalizationRejectsChangedAuthority(t *testing.T) {
	for _, mode := range []string{"action", "accepted-before-consumption", "trace", "unsettled", "wire-duplicate"} {
		t.Run(mode, func(t *testing.T) {
			control := newBrowserArtifactControl(t)
			_, trial := control.collect(t)
			observed := control.observe(t)
			transfers, err := observeBrowserTransfers(t.Context(), control.fixture.workspace, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			trial.Browser.Download = inspectBrowserDownload(trial, control.target(), observed, transfers)
			oracle, err := inspectBrowserFixture(control.item.Case, control.page, trial.Events, control.page.file)
			if err != nil {
				t.Fatal(err)
			}
			trial.Browser.Fixture = &oracle
			download := trial.Browser.Download
			action := observed.state.PreparedActions[download.PreparedActionID]
			invocation := observed.state.Invocations[download.InvocationID]
			switch mode {
			case "action":
				action.ActionHash = strings.Repeat("f", 64)
				observed.state.PreparedActions[action.ID] = action
			case "accepted-before-consumption":
				invocation.AcceptedAt = control.domain.approval.requestedAt.Add(-time.Second).UnixNano()
				observed.state.Invocations[invocation.ID] = invocation
			case "trace":
				variants := control.domain.wire.messages[download.PlatformMessageID]
				variants[0].traces[0].TurnID = "foreign-turn"
			case "unsettled":
				invocation.State = browser.InvocationUnknown
				observed.state.Invocations[invocation.ID] = invocation
			case "wire-duplicate":
				control.domain.wire.messages["duplicate-message"] = control.domain.wire.messages[download.PlatformMessageID]
			}
			pending := browserPendingTrial{item: control.item.Case, fixture: control.page, conversation: control.domain}
			if err := finalizeBrowserArtifact(&trial, pending, control.target(), observed, true); err != nil {
				t.Fatal(err)
			}
			if trial.Browser.Artifact.Code == "artifact_observed" || trial.Browser.NativeAuthority == "observed" {
				t.Fatal("changed native authority promoted", mode)
			}
		})
	}
}
