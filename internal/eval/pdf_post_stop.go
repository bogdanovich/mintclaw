package eval

import (
	"context"
	"errors"
	"maps"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

const maxRetainedPDFBytes = 128 * 1024 * 1024

type pendingPDFTrial struct {
	prepared       *pdfTrial
	owner          *document.Authority
	jobID          string
	jobOwner       document.FormJobOwner
	expectedWrites map[string]string
	outputSnapshot pdfOutputSnapshot
}

type pdfCohortScope struct {
	agent, sessionKey string
	messages          map[string]bool
}

// Only the serial run owner accesses these handles. Trial checkpoints stay on
// private disk; source caches are cleared after the artifact oracle finishes.
type pdfRunFinalization struct {
	trials map[string]pendingPDFTrial
	scopes map[string]pdfCohortScope
	bytes  int
}

func retainedPDFCost(prepared *pdfTrial) int {
	// Account conservatively for the bounded case and owned-write projections.
	cost := MaxManifestBytes + maxPDFCensusEntries*400
	if prepared.census != nil && prepared.census.before != nil {
		cost += prepared.census.before.bytes
	}
	return cost
}

func (pending *pdfRunFinalization) canRetain(prepared *pdfTrial) bool {
	return len(pending.trials) < MaxCorpusCases && retainedPDFCost(prepared) <= maxRetainedPDFBytes-pending.bytes
}

func (pending *pdfRunFinalization) retain(domain *pdfConversation, trial Trial) {
	entry := pendingPDFTrial{
		prepared: domain.prepared, owner: domain.censusOwner, jobID: domain.jobID, jobOwner: domain.owner,
		outputSnapshot: domain.outputSnapshot,
	}
	if domain.approvalRecord != nil {
		if owner, err := pdfWriteOwner(entry.prepared.workspace, *domain.approvalRecord); err == nil {
			entry.owner = &owner
		}
	}
	if trial.ExecutionVerdict == Pass && trial.PDF != nil && trial.PDF.Settlement != nil &&
		len(domain.censusWrites) == 1 && validDigest(entry.outputSnapshot.operationSHA256) &&
		entry.outputSnapshot.operationID == trial.PDF.Settlement.OperationID &&
		domain.censusWrites[trial.PDF.Settlement.OperationID] == entry.outputSnapshot.operationSHA256 {
		entry.expectedWrites = maps.Clone(domain.censusWrites)
	}
	if pending.trials == nil {
		pending.trials = make(map[string]pendingPDFTrial)
	}
	pending.trials[trial.ID] = entry
	pending.bytes += retainedPDFCost(entry.prepared)
	clear(entry.prepared.source)
	entry.prepared.source = nil
}

func (pending *pdfRunFinalization) observeTrial(trial Trial) {
	if !boundedIdentity(trial.SessionKey, 512) || !boundedIdentity(trial.AgentID, 128) {
		return
	}
	scope := pdfCohortScope{agent: trial.AgentID, sessionKey: trial.SessionKey, messages: map[string]bool{}}
	for _, event := range trial.Events {
		if event.Kind != "input" && boundedIdentity(event.MessageID, 128) {
			scope.messages[strings.Clone(event.MessageID)] = true
		}
	}
	if trial.PDF != nil && trial.PDF.Wire != nil && trial.PDF.Wire.Code == "wire_media_observed" {
		scope.messages[trial.PDF.Wire.MessageID] = true
	}
	if pending.scopes == nil {
		pending.scopes = make(map[string]pdfCohortScope)
	}
	pending.scopes["mintclaw:"+trial.SessionID] = scope
}

func (pending *pdfRunFinalization) close() {
	for id, entry := range pending.trials {
		_ = entry.prepared.release(false)
		delete(pending.trials, id)
	}
	pending.scopes = nil
	pending.bytes = 0
}

func (pending *pdfRunFinalization) knownCohort(
	ctx context.Context, inventory *pdfNativeInventory, workspace string,
) map[string]string {
	known := map[string]string{}
	if inventory == nil {
		return known
	}
	writes := map[string]string{}
	for _, entry := range pending.trials {
		for id, sha := range entry.expectedWrites {
			writes[id] = sha
		}
	}
	for _, file := range inventory.files {
		if ctx.Err() != nil {
			return known
		}
		sha := digest(file.data)
		if file.kind == "write" {
			if writes[file.id] == sha {
				known["write:"+file.id] = sha
			}
			continue
		}
		intent, err := outbox.InspectIntent(file.data, file.id)
		scope, found := pending.scopes[intent.Identity.ChatID]
		if err != nil || !found || intent.OwnerWorkspace != workspace || intent.Identity.Channel != "mintclaw" ||
			intent.Identity.SessionKey != scope.sessionKey || intent.Status != outbox.StatusDelivered ||
			intent.RecoverySettlementPending() || intent.TranscriptProjectionPending() || len(intent.PlatformMessageIDs) == 0 {
			continue
		}
		matched := intent.Message != nil && intent.Message.AgentID == scope.agent &&
			intent.Message.SessionKey == scope.sessionKey || intent.Media != nil && intent.Media.AgentID == scope.agent &&
			intent.Media.SessionKey == scope.sessionKey
		for _, id := range intent.PlatformMessageIDs {
			matched = matched && scope.messages[id]
		}
		if matched {
			known["outbox:"+file.id] = sha
		}
	}
	return known
}

func (pending *pdfRunFinalization) finish(
	ctx context.Context, report *privateReport, result *RunReport, lifecycle *TargetLifecycle,
	facts map[string]string, filter *strings.Replacer, surfaces *PrivateSurfaceObservation,
	assess func(Case, *Trial),
) error {
	if len(pending.trials) == 0 {
		return nil
	}
	var inventory *pdfNativeInventory
	var observationErr error
	workspace := ""
	for _, entry := range pending.trials {
		entry.prepared.scanFacts = facts
		inventory, observationErr = entry.prepared.nativeInventory(ctx)
		workspace = entry.prepared.workspace
		break
	}
	defer inventory.close()
	known := pending.knownCohort(ctx, inventory, workspace)
	type finalizedTrial struct {
		index int
		item  Case
		trial Trial
	}
	finalized := []finalizedTrial{}
	retainedBytes := 0
	for index := range result.Trials {
		if ctx.Err() != nil {
			return ErrReport
		}
		summary := &result.Trials[index]
		entry, found := pending.trials[summary.ID]
		if !found {
			continue
		}
		data, err := report.readTrialCheckpoint(summary.File, entry.prepared.sessionID)
		if err != nil {
			return ErrReport
		}
		trial, err := decodeDocument[Trial](data, 8*1024*1024)
		retainedBytes += len(data)
		clear(data)
		if err != nil || retainedBytes > maxRetainedPDFBytes || trial.ID != summary.ID ||
			trial.CaseID != entry.prepared.item.Case.ID ||
			trial.CaseSHA256 != entry.prepared.item.Digest || trial.SessionID != entry.prepared.sessionID ||
			trial.PDF == nil || credentialJSONValue(trial, filter) || privateJSONValue(trial, facts) {
			return ErrReport
		}
		item := entry.prepared.item.Case
		entry.prepared.scanFacts = facts
		entry.conclude(ctx, &trial, inventory, observationErr, known, lifecycle, surfaces)
		finalized = append(finalized, finalizedTrial{index, item, trial})
		delete(pending.trials, summary.ID)
	}
	// Native observation/cleanup finishes under its own budget before any model
	// call. Assessment uses the suite budget and cannot starve later cleanup.
	for _, final := range finalized {
		trial := final.trial
		summary := &result.Trials[final.index]
		if assess != nil {
			assess(final.item, &trial)
		}
		if credentialJSONValue(trial, filter) || privateJSONValue(trial, facts) {
			return ErrReport
		}
		if report.write(summary.File, trial) != nil {
			return ErrReport
		}
		summary.Verdict, summary.Code, summary.DurationMS = trial.Verdict, trial.Code, trial.DurationMS
	}
	return nil
}

func (entry pendingPDFTrial) conclude(
	ctx context.Context, trial *Trial, inventory *pdfNativeInventory, observationErr error, known map[string]string,
	lifecycle *TargetLifecycle, surfaces *PrivateSurfaceObservation,
) {
	started := time.Now()
	defer func() { trial.DurationMS += time.Since(started).Milliseconds() }()
	domain := pdfConversation{prepared: entry.prepared, censusOwner: entry.owner}
	inspection := domain.inspectNativeCensus(ctx, trial, inventory, observationErr, known)
	proof := &PDFPostStopEvidence{Version: Version, Code: "post_stop_unavailable", Census: inspection.evidence}
	trial.PDF.PostStop = proof
	if inspection.violation != "" {
		proof.Code = inspection.violation
		failPDFArtifact(trial, Fail, proof.Code)
	}
	if inspection.violation == "" && (proof.Census == nil || proof.Census.SourceUnchanged == nil) {
		source, sourceErr := readPDFNativeFile(entry.prepared.scratch, "source.pdf", maxPDFBytes)
		changed := sourceErr == nil && digest(source) != entry.prepared.item.Case.Fixtures[0].SHA256
		clear(source)
		if changed {
			if proof.Census == nil {
				proof.Census = &PDFCensusEvidence{Version: Version}
			}
			unchanged := false
			proof.Census.SourceUnchanged, proof.Census.Code = &unchanged, "source_changed"
			proof.Code = "source_changed"
			failPDFArtifact(trial, Fail, proof.Code)
		}
	}
	proof.RuntimeStopped = lifecycle != nil && lifecycle.valid() && lifecycle.Code == "native_exit_zero" &&
		!lifecycle.LogTruncated
	if !proof.RuntimeStopped {
		failPDFArtifact(trial, Inconclusive, "pdf_runtime_stop_unproven")
	}
	census := proof.Census
	settled := false
	if proof.RuntimeStopped && inspection.violation == "" && census != nil && census.Code == "native_idle_unproven" &&
		census.Counts != nil && census.Counts.PendingOperations == 0 && census.Counts.ForeignChanges == 0 &&
		census.SourceUnchanged != nil && *census.SourceUnchanged {
		proof.Code = "native_post_stop_observed"
		var err error
		settled, err = entry.logicalSettlement(trial, inventory)
		proof.LogicalSettled = settled && err == nil
		if errors.Is(err, ErrPrivateEvidence) {
			proof.Code = "private_native_evidence"
			failPDFArtifact(trial, Fail, proof.Code)
		} else if !proof.LogicalSettled {
			proof.Code = "logical_settlement_unavailable"
			failPDFArtifact(trial, Inconclusive, "pdf_native_settlement_unknown")
		}
		if settled && trial.ExecutionVerdict == Pass && entry.prepared.item.Case.PDF.Output == "forbidden" &&
			census.Counts.WriteOperations == 0 && census.Counts.MediaIntents == 0 && census.Counts.DocumentDeliveries == 0 &&
			trial.PDF.Wire != nil && trial.PDF.Wire.Complete && trial.PDF.Wire.ObservedMessages == 0 {
			for index, check := range entry.prepared.item.Case.Checks {
				if check.Kind == "pdf_no_write" && index < len(trial.Checks) && trial.Checks[index].ID == check.ID {
					trial.Checks[index] = booleanCheck(check.ID, true, []string{"pdf.post_stop"})
				}
			}
		}
	} else {
		failPDFArtifact(trial, Inconclusive, "pdf_post_stop_unavailable")
	}
	remove := settled && proof.Code == "native_post_stop_observed" && surfaces != nil && surfaces.Code == "observed"
	if err := entry.prepared.release(remove); err != nil {
		proof.Code = "fixture_cleanup_unavailable"
		failPDFArtifact(trial, Inconclusive, "pdf_fixture_cleanup_failed")
	} else if remove {
		proof.FixtureRemoved = true
		trial.PDF.Cleanup = "owned_fixture_removed; native_post_stop_observed"
	} else {
		failPDFArtifact(trial, Inconclusive, "pdf_fixture_retained")
	}
	verdict, code := trial.ExecutionVerdict, trial.ExecutionCode
	for _, check := range trial.Checks {
		combined := combineVerdicts(verdict, check.Verdict)
		if combined != verdict {
			code = "required_check_unknown"
			if combined == Fail {
				code = "hard_check_failed"
			}
		}
		verdict = combined
	}
	if trial.Judge != nil && (requiredRubric(entry.prepared.item.Case.Rubric) ||
		trial.Judge.Code == "private_judge_output") {
		combined := combineVerdicts(verdict, trial.Judge.Verdict)
		if combined != verdict {
			code = trial.Judge.Code
		}
		verdict = combined
	}
	trial.Verdict, trial.Code = verdict, code
}

func (entry pendingPDFTrial) logicalSettlement(trial *Trial, inventory *pdfNativeInventory) (bool, error) {
	if entry.jobID != "" {
		job, err := entry.prepared.observeExistingJob(entry.jobID, entry.jobOwner)
		if err != nil || job.SourceDigest != trial.PDF.SourceSHA256 {
			return false, err
		}
		if job.State == document.FormJobCompleted && trial.PDF.Settlement != nil {
			return entry.outputSnapshot.matches(inventory, trial.PDF.Settlement) &&
				job.OperationID == trial.PDF.Settlement.OperationID &&
				job.ArtifactDigest == trial.PDF.Settlement.ArtifactSHA256, nil
		}
		if trial.PDF.Cancellation == nil || job.State != document.FormJobCanceled ||
			job.Revision != trial.PDF.Cancellation.JobTerminalRevision ||
			job.TerminalAt != trial.PDF.Cancellation.JobTerminalAt {
			return false, nil
		}
	}
	proof := trial.Cancellation
	if trial.PDF.Cancellation != nil {
		proof = &trial.PDF.Cancellation.Question
	}
	if proof == nil || entry.owner == nil {
		return false, nil
	}
	record, err := entry.prepared.observeInteraction(proof.InteractionID)
	if err != nil {
		return false, err
	}
	owner, err := pdfWriteOwner(entry.prepared.workspace, record)
	return err == nil && owner == *entry.owner && record.Status == interactions.StatusCancelled &&
		record.FailureCode == "session_control_stop" && record.Revision == proof.TerminalRevision &&
		record.ResolvedAt == proof.ResolvedAt && record.ShortID == proof.ShortID &&
		record.PromptDeliveryID == proof.PromptDeliveryID && record.Origin.TurnID == proof.OriginTurnID, nil
}
