package eval

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestProtectedPDFCancellationRequiresBothNativeTerminalRecords(t *testing.T) {
	for _, mode := range []string{
		"complete", "registry-only", "job-only", "missing-controls", "foreign-reply", "private-registry",
		"changed-origin", "changed-binding", "changed-source", "private-job", "foreign-interaction", "finalized-no-write",
	} {
		t.Run(mode, func(t *testing.T) {
			prepared := newPDFTrialFixture(t)
			defer func() { _ = prepared.release(true) }()
			if mode == "finalized-no-write" {
				prepared.item = loadPDFTestCase(t, "pdf.cancel.no-write")
			}
			prepared.item.Case.Driver.Mode, prepared.item.Case.Driver.Persona = "scripted", ""
			prepared.item.Case.Driver.Steps = []Step{{
				WaitFor: Expectation{Kind: "question"}, Action: Action{Kind: "cancel"},
			}}
			prepared.item.Case.Rubric = nil
			if mode == "complete" || mode == "finalized-no-write" {
				initializeCensusRoots(t, prepared)
				if err := prepared.beginNativeCensus(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			domain := &pdfConversation{prepared: prepared, wire: &pdfWireObservation{workspace: prepared.workspace}}
			transport := newFakeConversation()
			var waiting interactions.Record
			var jobs *document.FormJobStore
			var prompt Event
			transport.onSend = func(request protocol.MintClawMessage, number int) {
				response := testResponse(request, number)
				response.Payload[protocol.PayloadKeyAgentID] = prepared.agent
				if number == 1 {
					waiting, prompt, jobs = nativePDFPromptFixtureForRequest(t, prepared, &request)
					response.Payload[protocol.PayloadKeyFinal] = false
					response.Payload[protocol.PayloadKeyMessageID] = prompt.MessageID
					response.Payload[protocol.PayloadKeyInteractionID] = waiting.ID
					response.Payload[protocol.PayloadKeyInteractionShortID] = waiting.ShortID
					response.Payload[protocol.PayloadKeyInteraction] = "question"
					response.Payload[protocol.PayloadKeyControls] = "prompt"
					response.Payload[protocol.PayloadKeyTraceScopes] = prompt.TraceScopes
					transport.events <- response
					return
				}
				if number != 2 || request.Payload[protocol.PayloadKeyInteractionChoice] != "cancel" ||
					request.Payload[protocol.PayloadKeyInteractionPrompt] != prompt.MessageID ||
					request.Payload[protocol.PayloadKeyInteractionShortID] != waiting.ShortID {
					t.Fatal("protected cancellation was replayed or not prompt-bound")
				}
				registry := interactions.NewRegistry(interactions.WorkspaceStorePath(prepared.workspace))
				if mode != "job-only" {
					var err error
					waiting, err = registry.BeginCancellation(waiting.ID, waiting.Revision, "session_control_stop")
					if err != nil {
						t.Fatal(err)
					}
				}
				if mode != "registry-only" {
					sink, err := document.NewFormProtectedAnswerSink(jobs)
					if err != nil {
						t.Fatal(err)
					}
					if err = sink.Cancel(t.Context(), interactions.ProtectedAnswerCancelRequest{
						Workspace: prepared.workspace, Route: waiting.Route,
						InteractionID: waiting.ID, IdempotencyKey: request.ID, Binding: *waiting.ProtectedAnswer,
					}); err != nil {
						t.Fatal(err)
					}
				}
				if mode != "job-only" {
					var err error
					waiting, err = registry.CompleteCancellation(waiting.ID, waiting.Revision)
					if err != nil {
						t.Fatal(err)
					}
				}
				if mode == "private-registry" || mode == "changed-origin" || mode == "changed-binding" {
					path := interactions.WorkspaceStorePath(prepared.workspace)
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					snapshot, err := interactions.InspectSnapshot(data)
					if err != nil || len(snapshot.Records) != 1 {
						t.Fatal("native cancellation control snapshot unavailable", err)
					}
					current := &snapshot.Records[0]
					switch mode {
					case "private-registry":
						current.Questions[0].Question = prepared.item.Case.Driver.Facts["contact-reference"]
					case "changed-origin":
						current.Origin.TurnID = "another-native-turn"
					case "changed-binding":
						current.ProtectedAnswer.Token += "-changed"
					}
					data, err = json.Marshal(snapshot)
					if err != nil {
						t.Fatal(err)
					}
					if err = os.WriteFile(path, data, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "changed-source" {
					if err := prepared.scratch.WriteFile("source.pdf", []byte("changed-source"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "private-job" {
					if _, err := jobs.Create(t.Context(), document.FormJobCreateRequest{
						Owner:               domain.owner,
						StartIdempotencyKey: "another-private-job",
						SourceRef:           "media://eval-source",
						SourceDigest:        prepared.item.Case.Fixtures[0].SHA256,
						FieldSchemaDigest:   domain.protectedCancel.job.FieldSchemaDigest,
						BackendRevision:     prepared.item.Case.Driver.Facts["contact-reference"],
						AuditPolicyRevision: "eval-control-v1",
					}); err != nil {
						t.Fatal(err)
					}
				}
				delete(response.Payload, protocol.PayloadKeyTraceScopes)
				response.Payload[protocol.PayloadKeyControls] = "remove"
				response.Payload[protocol.PayloadKeyInteraction] = "question"
				if mode == "missing-controls" {
					delete(response.Payload, protocol.PayloadKeyControls)
				}
				if mode == "foreign-reply" {
					response.Payload[protocol.PayloadKeySessionKey] = "foreign-session"
				}
				if mode == "foreign-interaction" {
					response.Payload[protocol.PayloadKeyInteractionID] = "interaction_foreign_control111"
				}
				transport.events <- response
			}
			trial := runDomainConversation(t.Context(), transport, prepared.item, prepared.sessionID,
				prepared.agent, prepared.workspace, conversationRoles{}, domain)
			domain.concludeWire(&trial, true)
			if len(transport.sent) != 2 || trial.Verdict == Pass || trial.EvidenceEligible ||
				trial.RemoteState != "unknown" || trial.PDF == nil || trial.Cancellation != nil {
				t.Fatal("protected witness replayed input or implied whole PDF/cleanup qualification", trial.Code)
			}
			if mode == "complete" || mode == "finalized-no-write" {
				proof := trial.PDF.Cancellation
				if trial.ExecutionVerdict != Pass || proof == nil || lastOutcome(trial.Events).Kind != "canceled" ||
					proof.Question.PromptMessageID != prompt.MessageID || proof.Question.WaitingRevision >=
					proof.Question.TerminalRevision || proof.JobWaitingRevision >= proof.JobTerminalRevision ||
					trial.PDF.State != string(document.FormJobCanceled) {
					t.Fatal("native protected cancellation lost its bound terminal records", trial.Code)
				}
			} else if trial.ExecutionVerdict == Pass || trial.PDF.Cancellation != nil {
				t.Fatal("unsettled or foreign native cancellation was admitted", trial.Code)
			}
			data, err := json.Marshal(trial)
			value := prepared.item.Case.Driver.Facts["contact-reference"]
			if err != nil || value != "" && bytes.Contains(data, []byte(value)) ||
				bytes.Contains(data, []byte(waiting.ProtectedAnswer.Token)) {
				t.Fatal("private fact or sealed binding entered retained evidence")
			}
			if (mode == "private-registry" || mode == "private-job") &&
				(trial.Verdict != Fail || trial.Code != "private_native_evidence") {
				t.Fatal("private native cancellation evidence was softened", trial.Code)
			}
			if mode == "complete" || mode == "finalized-no-write" {
				pending := pdfRunFinalization{}
				pending.retain(domain, trial)
				entry := pending.trials[trial.ID]
				inventory, err := prepared.nativeInventory(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer inventory.close()
				item := prepared.item.Case
				surfaces := observePrivateSurfaces(t.Context(), prepared.home, nil, nil,
					map[string]bool{item.Fixtures[0].SHA256: true})
				entry.conclude(t.Context(), &trial, inventory, nil, nil, cleanPDFLifecycle(), &surfaces)
				if !trial.PDF.PostStop.RuntimeStopped || !trial.PDF.PostStop.FixtureRemoved || trial.Verdict == Pass {
					t.Fatal("protected native cancellation could not finalize its owned fixture", trial.PDF.PostStop)
				}
				if mode == "finalized-no-write" {
					item.Rubric = []Criterion{
						{ID: "coherent", Description: "Cancellation is coherent.", Required: true},
					}
					provider := &roleTestProvider{mediated: true, response: &providers.LLMResponse{
						Content: adaptiveJudgePass, FinishReason: "stop",
					}}
					judge := &ModelRole{provider: provider, identity: RoleIdentity{Role: "judge"}}
					assessFinalizedPDFNoWrite(t.Context(), judge, item, &trial, cleanPDFLifecycle(), &surfaces)
					if trial.Verdict != Pass || !trial.EvidenceEligible || provider.calls != 1 ||
						!validPDFNoWriteEvidence(trial, item, prepared.workspace) {
						t.Fatal("bound native protected cancellation could not be judged", trial.Code, surfaces.Code)
					}
					manifest, _ := json.Marshal(item)
					trial.CaseSHA256 = digest(manifest)
					summary := TrialSummary{
						ID: trial.ID, CaseSHA256: trial.CaseSHA256,
						Verdict: trial.Verdict, Code: trial.Code,
					}
					loaded := LoadedCase{Case: item, Digest: trial.CaseSHA256}
					if !gradeEligible(
						trial,
						loaded,
						summary,
						Preset{ExpectedAgent: trial.AgentID},
						prepared.workspace,
					) {
						t.Fatal("protected cancellation lost offline grade admission")
					}
					baseline, _ := json.Marshal(trial)
					for _, change := range []string{"job-revision", "job-time", "job-state", "both-proofs"} {
						var corrupted Trial
						if json.Unmarshal(baseline, &corrupted) != nil {
							t.Fatal("invalid corruption fixture")
						}
						switch change {
						case "job-revision":
							corrupted.PDF.Cancellation.JobTerminalRevision = corrupted.PDF.Cancellation.JobWaitingRevision
						case "job-time":
							corrupted.PDF.Cancellation.JobTerminalAt = corrupted.PDF.Cancellation.Question.ResolvedAt + 1
						case "job-state":
							corrupted.PDF.State = string(document.FormJobCompleted)
						case "both-proofs":
							corrupted.Cancellation = &corrupted.PDF.Cancellation.Question
						}
						if gradeEligible(corrupted, loaded, summary,
							Preset{ExpectedAgent: trial.AgentID}, prepared.workspace) {
							t.Fatal("invalid protected terminal evidence reached grade admission", change)
						}
					}
				}
			}
		})
	}
}
