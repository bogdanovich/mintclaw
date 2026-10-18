package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

// Native stores/wire are real controls; the process/profile/oracle receipts
// below are explicit stubs, not a live worker or privacy qualification claim.
func finalizedOutputFixture(t *testing.T) (Trial, Case, string, *privateReport) {
	t.Helper()
	domain, settled := settledPDFControl(t)
	prepared := domain.prepared
	prepared.census = &pdfNativeCensus{before: &pdfNativeInventory{}}
	trial := Trial{
		Version: Version, ID: uuid.NewString(), CaseID: prepared.item.Case.ID, CaseSHA256: prepared.item.Digest,
		SessionID: prepared.sessionID, AgentID: prepared.agent, SessionKey: domain.owner.RouteSessionKey,
		Verdict: Inconclusive, ExecutionVerdict: Pass, ExecutionCode: "completed", StartedAt: prepared.started,
		TransportClosed: true,
	}
	for index, event := range []Event{
		{Kind: "input", Turn: 1, RequestID: "initial", Content: "Fill the synthetic form."},
		{Kind: "final", Turn: 1, RequestID: "initial", Content: "Review the collected data."},
		{Kind: "input", Turn: 2, RequestID: domain.confirmationRequestID, Content: "I confirm the reviewed data."},
		{
			Kind: "approval", Turn: 2, RequestID: domain.confirmationRequestID, Content: "Approve this reviewed PDF?",
			InteractionID: settled.approval.ID,
		},
		{Kind: "input", Turn: 3, RequestID: domain.approvalRequestID, Content: "Allow once"},
		{Kind: "final", Turn: 3, RequestID: domain.approvalRequestID, Content: "The reviewed PDF was delivered."},
	} {
		event.ID = fmt.Sprintf("event-%06d", index+1)
		if event.Kind != "input" {
			event.AgentID, event.SessionKey = trial.AgentID, trial.SessionKey
			event.TraceScopes = []events.TraceScope{{Workspace: prepared.workspace, TurnID: event.RequestID}}
		}
		trial.Events = append(trial.Events, event)
	}
	trial.Checks = gradeConversation(prepared.item.Case.Checks, trial.Events, lastOutcome(trial.Events))
	domain.conclude(t.Context(), &trial)
	domain.wire = &mediaWireObservation{workspace: prepared.workspace}
	if err := domain.wire.observe(protocol.MintClawMessage{
		Type: protocol.TypeMessageCreate, SessionID: trial.SessionID,
		Payload: map[string]any{
			protocol.PayloadKeyMessageID: settled.delivery.PlatformMessageIDs[0],
			protocol.PayloadKeyAgentID:   trial.AgentID, protocol.PayloadKeySessionKey: trial.SessionKey,
			protocol.PayloadKeyDeliveryID: settled.delivery.ID,
			"attachments": []any{map[string]any{
				"type": "file", "content_type": "application/pdf",
				"url": "/mintclaw/media/" + strings.TrimPrefix(settled.operation.ArtifactRef, "media://"),
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	domain.concludeWire(&trial, true)
	parent := filepath.Join(prepared.home, "reports")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	report, err := newPrivateReport(parent, prepared.home, prepared.workspace, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = report.root.Close() })
	file, err := report.retainPDFArtifact(trial.ID, []byte("metadata-control-artifact"))
	if err != nil {
		t.Fatal(err)
	}
	trial.PDF.Artifact = &PDFArtifactEvidence{
		Verdict: Pass, Code: "matched", File: file, Oracle: &PDFOracleResult{
			Version: Version, Schema: pdfOracleVersion, Code: "matched", Pypdf: "6.20.0", PyMuPDF: "1.28.2",
			MuPDF: "stub", SourceSHA256: trial.PDF.SourceSHA256, ArtifactSHA256: settled.operation.Artifact.SHA256,
			FieldsMatched: true, StructureMatched: true, AppearanceMatched: true, BackgroundMatched: true,
			CheckedFields: prepared.item.Case.PDF.FieldCount, RenderedPages: prepared.item.Case.PDF.Pages,
			OracleSHA256: digest(pdfOracleScript),
		},
	}
	pending := pdfRunFinalization{}
	pending.retain(domain, trial)
	defer pending.close()
	item := prepared.item.Case
	inventory, err := prepared.nativeInventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer inventory.close()
	pending.trials[trial.ID].conclude(t.Context(), &trial, inventory, nil, nil, cleanPDFLifecycle(),
		&PrivateSurfaceObservation{Code: "observed"})
	delete(pending.trials, trial.ID)
	if !validPDFOutputEvidence(trial, item, prepared.workspace) {
		t.Fatal("finalized native output control unavailable", trial.Code)
	}
	return trial, item, prepared.workspace, report
}

func cloneOutputTrial(t *testing.T, trial Trial) Trial {
	t.Helper()
	data, err := json.Marshal(trial)
	var clone Trial
	if err != nil || json.Unmarshal(data, &clone) != nil {
		t.Fatal("cannot copy the immutable native output control")
	}
	return clone
}

func TestFinalizedPDFOutputRequiresAllIndependentWitnessesBeforeJudging(t *testing.T) {
	original, item, workspace, report := finalizedOutputFixture(t)
	for _, mode := range []string{
		"complete", "oracle", "oracle-version", "source", "artifact", "fields", "appearance", "background",
		"pages", "structure", "file", "wire", "duplicate-wire", "wire-id", "pending", "foreign", "duplicate-write",
		"no-delivery", "no-cleanup", "unsettled", "approval", "confirmation", "grant", "wrong-route", "private-value",
		"role-unknown", "execution", "profile", "stopped",
	} {
		t.Run(mode, func(t *testing.T) {
			trial := cloneOutputTrial(t, original)
			surfaces := &PrivateSurfaceObservation{Scope: "owned_profile_files_v1", Code: "observed"}
			lifecycle := cleanPDFLifecycle()
			switch mode {
			case "oracle":
				trial.PDF.Artifact.Oracle = nil
			case "oracle-version":
				trial.PDF.Artifact.Oracle.OracleSHA256 = strings.Repeat("0", 64)
			case "source":
				trial.PDF.SourceSHA256 = strings.Repeat("0", 64)
			case "artifact":
				trial.PDF.Artifact.Oracle.ArtifactSHA256 = strings.Repeat("0", 64)
			case "fields":
				trial.PDF.Artifact.Oracle.FieldsMatched = false
			case "appearance":
				trial.PDF.Artifact.Oracle.AppearanceMatched = false
			case "background":
				trial.PDF.Artifact.Oracle.BackgroundMatched = false
			case "pages":
				trial.PDF.Artifact.Oracle.RenderedPages--
			case "structure":
				trial.PDF.Artifact.Oracle.StructureMatched = false
			case "file":
				trial.PDF.Artifact.File = "foreign.pdf"
			case "wire":
				trial.PDF.Wire.Complete = false
			case "duplicate-wire":
				trial.PDF.Wire.ObservedMessages++
			case "wire-id":
				trial.PDF.Wire.MessageID = "foreign-message"
			case "pending":
				trial.PDF.PostStop.Census.Counts.PendingOperations++
			case "foreign":
				trial.PDF.PostStop.Census.Counts.ForeignChanges++
			case "duplicate-write":
				trial.PDF.PostStop.Census.Counts.WriteOperations++
			case "no-delivery":
				trial.PDF.PostStop.Census.Counts.DocumentDeliveries = 0
			case "no-cleanup":
				trial.PDF.PostStop.FixtureRemoved = false
			case "unsettled":
				trial.PDF.PostStop.LogicalSettled = false
			case "approval":
				trial.PDF.Settlement.ApprovalInteractionID = "foreign-approval"
			case "confirmation":
				trial.Events[2].RequestID = "unobserved-confirmation"
			case "grant":
				trial.Events[4].RequestID = "unobserved-grant"
			case "wrong-route":
				trial.Events[1].SessionKey = "foreign-route"
			case "private-value":
				trial.Events[1].Content = item.Driver.Facts["contact-reference"]
			case "role-unknown":
				trial.DriverCalls = []RoleCall{{Attempted: true}}
			case "execution":
				trial.ExecutionVerdict = Inconclusive
			case "profile":
				surfaces.Code = "unavailable"
			case "stopped":
				lifecycle.LogTruncated = true
			}
			provider := &roleTestProvider{mediated: true, response: &providers.LLMResponse{
				Content: adaptiveJudgePass, FinishReason: "stop",
			}}
			judge := &ModelRole{provider: provider, identity: RoleIdentity{Role: "judge"}}
			assessFinalizedPDF(t.Context(), judge, item, &trial, workspace, lifecycle, surfaces, report)
			if mode == "complete" {
				if provider.calls != 1 || trial.Verdict != Pass || trial.EvidenceEligible ||
					trial.PDF.PostStop.AssessmentScope != pdfOutputAssessmentScope || !consistentComparisonVerdict(trial) {
					t.Fatal(
						"all independent controls did not reach the judge, or granted offline eligibility",
						trial.Code,
					)
				}
				for _, check := range trial.Checks {
					if check.Verdict != Pass {
						t.Fatal("independent PDF check stayed unknown", check.ID)
					}
				}
				loaded := LoadedCase{Case: item, Digest: trial.CaseSHA256}
				summary := TrialSummary{
					ID: trial.ID, CaseSHA256: trial.CaseSHA256, Verdict: trial.Verdict, Code: trial.Code,
				}
				preset := Preset{ExpectedAgent: trial.AgentID}
				trial.EvidenceEligible = true // Even a forged legacy flag cannot admit private/output regrade.
				if gradeEligible(trial, loaded, summary, preset, workspace) {
					t.Fatal("live output scope bypassed independent offline-regrade admission")
				}
				trial.EvidenceEligible = false
			} else if provider.calls != 0 || trial.Verdict == Pass || trial.PDF.PostStop.AssessmentScope != "" {
				t.Fatal("incomplete or unsafe PDF witnesses reached the judge", mode, trial.Code)
			}
		})
	}
}

func TestFinalizedPDFOutputRetainsJudgePrivacyAndHardFailures(t *testing.T) {
	original, item, workspace, report := finalizedOutputFixture(t)
	for _, mode := range []string{
		"hard-check", "private-reasoning", "opaque-reasoning", "no-report", "changed-file", "missing-file",
	} {
		t.Run(mode, func(t *testing.T) {
			trial := cloneOutputTrial(t, original)
			current := item
			provider := &roleTestProvider{mediated: true, response: &providers.LLMResponse{
				Content: adaptiveJudgePass, FinishReason: "stop",
			}}
			retention := report
			switch mode {
			case "hard-check":
				current.Checks = append(append([]Check{}, item.Checks...),
					Check{ID: "contradiction", Kind: "response_contains", Expected: "missing"})
			case "private-reasoning":
				provider.response.Reasoning = item.Driver.Facts["contact-reference"]
			case "opaque-reasoning":
				provider.response.Reasoning = `public\opaque`
			case "no-report":
				retention = nil
			case "changed-file":
				if err := os.WriteFile(
					filepath.Join(report.dir, trial.PDF.Artifact.File),
					[]byte("changed"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			case "missing-file":
				if err := report.root.Remove(trial.PDF.Artifact.File); err != nil {
					t.Fatal(err)
				}
			}
			judge := &ModelRole{provider: provider, identity: RoleIdentity{Role: "judge"}}
			assessFinalizedPDF(t.Context(), judge, current, &trial, workspace, cleanPDFLifecycle(),
				&PrivateSurfaceObservation{Scope: "owned_profile_files_v1", Code: "observed"}, retention)
			if trial.Verdict == Pass || trial.EvidenceEligible || !consistentComparisonVerdict(trial) ||
				mode == "private-reasoning" && trial.Verdict != Fail || mode == "hard-check" && trial.Verdict != Fail ||
				mode == "changed-file" && trial.Verdict != Fail ||
				(mode == "missing-file" || mode == "changed-file" || mode == "no-report") && provider.calls != 0 {
				t.Fatal("output assessment hid a hard failure, privacy boundary or lost artifact", trial.Code)
			}
			if mode == "private-reasoning" || mode == "opaque-reasoning" {
				if trial.PDF.PostStop.AssessmentScope != "" || postStopCheck(trial, "privacy") == Pass {
					t.Fatal("unobserved or leaking judge retained a completed privacy claim")
				}
			}
		})
	}
}
