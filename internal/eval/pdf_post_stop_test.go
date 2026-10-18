package eval

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

func postStopPDFControl(t *testing.T) (*pdfConversation, Trial) {
	t.Helper()
	return postStopPreparedPDFControl(t, newPDFTrialFixture(t))
}

func postStopPreparedPDFControl(t *testing.T, prepared *pdfTrial) (*pdfConversation, Trial) {
	t.Helper()
	corpus, err := LoadCorpus(prepared.item.Root)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := Select(corpus, Selection{IDs: []string{"pdf.cancel.no-write"}})
	if err != nil {
		t.Fatal(err)
	}
	prepared.item = selected[0]
	prepared.item.Case.Rubric = nil
	initializeCensusRoots(t, prepared)
	if err = prepared.beginNativeCensus(t.Context()); err != nil {
		t.Fatal(err)
	}
	domain := &pdfConversation{prepared: prepared, wire: &pdfWireObservation{workspace: prepared.workspace}}
	question := &questionConversation{
		home: prepared.home, workspace: prepared.workspace, sessionID: prepared.sessionID,
		agent: prepared.agent, started: prepared.started,
	}
	transport := newFakeConversation()
	var registry *interactions.Registry
	var record interactions.Record
	transport.onSend = func(request protocol.MintClawMessage, number int) {
		if number == 1 {
			var response protocol.MintClawMessage
			registry, record, response = ordinaryQuestionFixture(t, question, request, false)
			response.Payload[protocol.PayloadKeyAgentID] = prepared.agent
			if err := domain.wire.observe(response); err != nil {
				t.Fatal(err)
			}
			transport.events <- response
			return
		}
		var cancelErr error
		record, cancelErr = registry.BeginCancellation(record.ID, record.Revision, "session_control_stop")
		if cancelErr != nil {
			t.Fatal(cancelErr)
		}
		record, cancelErr = registry.CompleteCancellation(record.ID, record.Revision)
		if cancelErr != nil {
			t.Fatal(cancelErr)
		}
		response := testResponse(request, number)
		response.Payload[protocol.PayloadKeyAgentID] = prepared.agent
		delete(response.Payload, protocol.PayloadKeyTraceScopes)
		response.Payload[protocol.PayloadKeyControls] = "remove"
		response.Payload[protocol.PayloadKeyInteraction] = "question"
		if err := domain.wire.observe(response); err != nil {
			t.Fatal(err)
		}
		transport.events <- response
	}
	trial := runDomainConversation(t.Context(), transport, prepared.item, prepared.sessionID,
		prepared.agent, prepared.workspace, conversationRoles{}, domain)
	domain.concludeWire(&trial, true)
	if trial.ExecutionVerdict != Pass || trial.Cancellation == nil || domain.censusOwner == nil {
		t.Fatal("ordinary native cancellation fixture unavailable", trial.Code)
	}
	return domain, trial
}

func cleanPDFLifecycle() *TargetLifecycle {
	code := 0
	return &TargetLifecycle{
		Mode: "owned_gateway", PID: 12345, Admitted: true, StopRequested: true, ExitObserved: true,
		ExitCode: &code, Code: "native_exit_zero",
	}
}

func postStopCheck(trial Trial, id string) Verdict {
	for _, check := range trial.Checks {
		if check.ID == id {
			return check.Verdict
		}
	}
	return Error
}

func TestPDFPostStopRequiresNativeFenceAndKeepsIndependentPrivacyUnknown(t *testing.T) {
	for _, mode := range []string{
		"clean", "attach", "forced", "log-truncated", "scan-unavailable", "changed-terminal", "wire-incomplete", "media",
	} {
		t.Run(mode, func(t *testing.T) {
			domain, trial := postStopPDFControl(t)
			pending := pdfRunFinalization{}
			pending.retain(domain, trial)
			entry := pending.trials[trial.ID]
			defer pending.close()
			if domain.prepared.source != nil {
				t.Fatal("artifact source cache retained unnecessarily")
			}
			inventory, err := entry.prepared.nativeInventory(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer inventory.close()
			lifecycle := cleanPDFLifecycle()
			surfaces := &PrivateSurfaceObservation{Code: "observed"}
			switch mode {
			case "attach":
				lifecycle = nil
			case "forced":
				lifecycle.Code, lifecycle.Forced = "forced_exit", true
			case "log-truncated":
				lifecycle.LogTruncated = true
			case "scan-unavailable":
				surfaces.Code = "unavailable"
			case "changed-terminal":
				trial.Cancellation.TerminalRevision++
			case "wire-incomplete":
				trial.PDF.Wire.Complete = false
			case "media":
				trial.PDF.Wire.ObservedMessages = 1
			}
			entry.conclude(t.Context(), &trial, inventory, nil, nil, lifecycle, surfaces)
			delete(pending.trials, trial.ID)
			wantNoWrite := mode == "clean" || mode == "scan-unavailable"
			wantRemoved := mode == "clean" || mode == "wire-incomplete" || mode == "media"
			if (postStopCheck(trial, "pdf-no-write") == Pass) != wantNoWrite ||
				trial.PDF.PostStop.FixtureRemoved != wantRemoved || postStopCheck(trial, "privacy") != Inconclusive ||
				trial.Verdict == Pass || trial.EvidenceEligible {
				t.Fatal("post-stop metadata exceeded its proof scope", trial.PDF.PostStop, trial.Checks)
			}
			_, sourceErr := os.Stat(
				filepath.Join(entry.prepared.workspace, ".eval-trials", trial.SessionID, "source.pdf"),
			)
			if wantRemoved != errors.Is(sourceErr, os.ErrNotExist) {
				t.Fatal("fixture deletion did not match its receipt", sourceErr)
			}
		})
	}
}

func TestPDFPostStopFindsWritesThatOccurAfterConversationConclusion(t *testing.T) {
	domain, trial := postStopPDFControl(t)
	pending := pdfRunFinalization{}
	pending.retain(domain, trial)
	entry := pending.trials[trial.ID]
	defer pending.close()
	journal, err := document.NewWriteJournal(filepath.Join(entry.prepared.home, "state", "document-writes", "journal"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = journal.Accept(t.Context(), "", *entry.owner, censusNormalizedRequest(t)); err != nil {
		t.Fatal(err)
	}
	inventory, err := entry.prepared.nativeInventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer inventory.close()
	entry.conclude(t.Context(), &trial, inventory, nil, nil, cleanPDFLifecycle(),
		&PrivateSurfaceObservation{Code: "observed"})
	delete(pending.trials, trial.ID)
	if trial.Verdict != Fail || trial.ExecutionVerdict != Fail ||
		trial.PDF.PostStop.Code != "unexpected_document_write" ||
		trial.PDF.PostStop.FixtureRemoved ||
		postStopCheck(trial, "pdf-no-write") == Pass {
		t.Fatal("late native operation was hidden by the earlier conclusion", trial.PDF.PostStop)
	}
}

func TestPDFPostStopExplainsOnlyObservedTerminalCohortMessages(t *testing.T) {
	for _, mode := range []string{"observed", "unseen", "wrong-agent", "pending"} {
		t.Run(mode, func(t *testing.T) {
			domain, trial := postStopPDFControl(t)
			pending := pdfRunFinalization{}
			pending.retain(domain, trial)
			entry := pending.trials[trial.ID]
			defer pending.close()
			chat := uuid.NewString()
			agent := entry.prepared.agent
			if mode == "wrong-agent" {
				agent = "foreign-agent"
			}
			intent, err := outbox.NewMessageIntent(entry.prepared.workspace, outbox.Identity{
				SourceID:   "later-cohort-turn",
				Channel:    "mintclaw",
				ChatID:     "mintclaw:" + chat,
				SessionKey: "cohort-route",
			}, bus.OutboundMessage{AgentID: agent, SessionKey: "cohort-route", Content: "cohort final"}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			box, err := outbox.Open(entry.prepared.workspace)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = box.Create(intent); err != nil {
				t.Fatal(err)
			}
			if mode != "pending" {
				if _, err = box.BeginAttempt(intent.ID); err != nil {
					t.Fatal(err)
				}
				if _, err = box.MarkDelivered(
					intent.ID,
					outbox.Outcome{PlatformMessageIDs: []string{"cohort-message"}},
				); err != nil {
					t.Fatal(err)
				}
			}
			observed := "cohort-message"
			if mode == "unseen" {
				observed = "different-message"
			}
			pending.observeTrial(Trial{
				SessionID: chat, AgentID: entry.prepared.agent, SessionKey: "cohort-route",
				Events: []Event{{Kind: "final", MessageID: observed}},
			})
			inventory, err := entry.prepared.nativeInventory(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer inventory.close()
			known := pending.knownCohort(t.Context(), inventory, entry.prepared.workspace)
			entry.conclude(t.Context(), &trial, inventory, nil, known, cleanPDFLifecycle(),
				&PrivateSurfaceObservation{Code: "observed"})
			delete(pending.trials, trial.ID)
			if (postStopCheck(trial, "pdf-no-write") == Pass) != (mode == "observed") ||
				(trial.PDF.PostStop.Census.Counts.ForeignChanges == 0) != (mode == "observed") {
				t.Fatal("foreign effects were ignored without actual cohort evidence", trial.PDF.PostStop)
			}
		})
	}
}

func TestPDFPostStopWritesOnlyFinalizedTrialToTheCanonicalReportName(t *testing.T) {
	domain, trial := postStopPDFControl(t)
	prepared := domain.prepared
	parent := filepath.Join(prepared.home, "reports")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	report, err := newPrivateReport(parent, prepared.home, prepared.workspace, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = report.root.Close() }()
	pending := pdfRunFinalization{}
	pending.retain(domain, trial)
	defer pending.close()
	if err = report.write("pending-trial-"+trial.ID+".json", trial); err != nil {
		t.Fatal(err)
	}
	name := "trial-" + trial.ID + ".json"
	if _, err = report.root.Stat(name); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canonical trial was published before post-stop finalization")
	}
	result := RunReport{Trials: []TrialSummary{{ID: trial.ID, File: name}}}
	if err = pending.finish(t.Context(), report, &result, cleanPDFLifecycle(), nil, nil,
		&PrivateSurfaceObservation{Code: "observed"}, nil); err != nil {
		t.Fatal(err)
	}
	data, err := readPrivateReportFile(report.root, name)
	var final Trial
	if err != nil || json.Unmarshal(data, &final) != nil || final.PDF.PostStop == nil ||
		!final.PDF.PostStop.FixtureRemoved || postStopCheck(final, "pdf-no-write") != Pass ||
		result.Trials[0].Verdict != final.Verdict || result.Trials[0].DurationMS != final.DurationMS {
		t.Fatal("final report omitted post-stop evidence or disagreed with the summary", err)
	}
	if err = report.write(name, final); !errors.Is(err, ErrReport) {
		t.Fatal("finalized trial could be overwritten")
	}
}

func TestPDFPostStopRetentionIsBoundedBeforeAnotherTask(t *testing.T) {
	prepared := newPDFTrialFixture(t)
	defer func() { _ = prepared.release(true) }()
	pending := pdfRunFinalization{bytes: maxRetainedPDFBytes - retainedPDFCost(prepared)}
	if !pending.canRetain(prepared) {
		t.Fatal("valid remaining retention budget refused")
	}
	pending.bytes++
	if pending.canRetain(prepared) {
		t.Fatal("retention could exceed its explicit budget")
	}
}

func TestPDFPostStopCohortWritesRequireThePreviouslyObservedNativeHash(t *testing.T) {
	domain, settled := settledPDFControl(t)
	domain.prepared.census = &pdfNativeCensus{before: &pdfNativeInventory{}}
	trial := Trial{
		ID: uuid.NewString(), ExecutionVerdict: Pass, SessionKey: domain.owner.RouteSessionKey,
		PDF: &PDFTrialEvidence{Settlement: &PDFSettlementEvidence{OperationID: settled.operation.OperationID}},
	}
	domain.concludeNativeCensus(t.Context(), &trial)
	pending := pdfRunFinalization{}
	pending.retain(domain, trial)
	defer pending.close()
	inventory, err := domain.prepared.nativeInventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer inventory.close()
	key := "write:" + settled.operation.OperationID
	if pending.knownCohort(t.Context(), inventory, domain.prepared.workspace)[key] == "" {
		t.Fatal("settled native write hash was not retained for the cohort")
	}
	for index, file := range inventory.files {
		if file.kind == "write" && file.id == settled.operation.OperationID {
			inventory.files[index].data = append(file.data, ' ')
		}
	}
	if pending.knownCohort(t.Context(), inventory, domain.prepared.workspace)[key] != "" {
		t.Fatal("cohort membership hid a changed native write record")
	}
}

func TestOwnedRunInspectsLateSourceChangesBeforePublishingFinalPDFTrial(t *testing.T) {
	fixture := newOwnedGatewayFixture(t, "normal")
	item := ownedPDFWireCase(t, fixture)
	writeAdmissionJSON(t, filepath.Join(fixture.home, ".security", "eval-child.json"), ownedGatewayControl{
		Mode: "normal", ParentPID: os.Getpid(), LateSourceChange: true,
	})
	result, err := Run(t.Context(), fixture.preset, []LoadedCase{item}, RunOptions{
		Home: fixture.home, OutputParent: fixture.output, StartTarget: true, SuiteTimeout: 20 * time.Second,
	})
	if !errors.Is(err, ErrNotPassed) || !result.Complete || result.Verdict != Fail ||
		result.TargetLifecycle == nil || result.TargetLifecycle.Code != "native_exit_zero" ||
		result.Coverage[Fail] != 1 || len(result.Trials) != 1 {
		t.Fatal("late source mutation was hidden or final evidence was published before stop", err, result.Trials)
	}
	data, err := os.ReadFile(filepath.Join(result.ReportDir, result.Trials[0].File))
	var trial Trial
	if err != nil || json.Unmarshal(data, &trial) != nil || trial.PDF.PostStop == nil ||
		trial.PDF.PostStop.Code != "source_changed" || trial.Verdict != Fail ||
		!trial.PDF.PostStop.RuntimeStopped || trial.PDF.PostStop.FixtureRemoved ||
		trial.PDF.PostStop.Census.SourceUnchanged == nil || *trial.PDF.PostStop.Census.SourceUnchanged {
		t.Fatal("final trial did not retain the late native source failure", err, trial.PDF)
	}
	checkpoint, err := os.ReadFile(filepath.Join(result.ReportDir, "pending-trial-"+trial.ID+".json"))
	var before Trial
	if err != nil || json.Unmarshal(checkpoint, &before) != nil || before.PDF.PostStop != nil ||
		before.Verdict == Fail {
		t.Fatal("pre-stop checkpoint was overwritten or treated as the final verdict", err)
	}
}
