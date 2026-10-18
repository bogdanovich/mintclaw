package eval

import (
	"context"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/document"
)

const (
	pdfNoWriteAssessmentScope = "owned_post_stop_no_write_v1"
	pdfOutputAssessmentScope  = "owned_post_stop_pdf_output_v1"
)

func observedRoleBoundaries(trial Trial) bool {
	for index := range trial.DriverCalls {
		call := &trial.DriverCalls[index]
		if call.Boundary == nil || !roleBoundaryEligible(call) {
			return false
		}
	}
	return trial.Judge == nil || trial.Judge.Call == nil ||
		trial.Judge.Call.Boundary != nil && roleBoundaryEligible(trial.Judge.Call)
}

func observedPDFProfile(lifecycle *TargetLifecycle, surfaces *PrivateSurfaceObservation) bool {
	return lifecycle != nil && lifecycle.valid() && lifecycle.Code == "native_exit_zero" &&
		!lifecycle.LogTruncated && surfaces != nil && surfaces.valid() && surfaces.Code == "observed"
}

// This first finalized contract admits cancellation with no private case
// facts and no output artifact. Filled PDFs require their own
// independent evidence admission; neither fixture presence nor prose suffices.
func validPDFNoWriteEvidence(trial Trial, item Case, workspace string) bool {
	pdf := trial.PDF
	if item.PDF == nil || item.PDF.Output != "forbidden" || len(item.Driver.Facts) != 0 ||
		!validID(trial.SessionID) || !validID(trial.AgentID) || !boundedIdentity(trial.SessionKey, 512) ||
		len(item.Fixtures) != 1 || pdf == nil || pdf.Version != Version ||
		pdf.SourceSHA256 != item.Fixtures[0].SHA256 || pdf.Settlement != nil || pdf.Artifact != nil ||
		pdf.WriteIntent != nil || trial.ExecutionVerdict != Pass ||
		!trial.TransportClosed || !observedRoleBoundaries(trial) ||
		lastOutcome(trial.Events).Kind != "canceled" {
		return false
	}
	conversation := trial
	if pdf.JobID != "" {
		cancellation := pdf.Cancellation
		if !validID(pdf.JobID) || pdf.State != string(document.FormJobCanceled) || trial.Cancellation != nil ||
			cancellation == nil || cancellation.JobWaitingRevision <= 0 ||
			cancellation.JobTerminalRevision <= cancellation.JobWaitingRevision ||
			cancellation.JobTerminalAt < trial.StartedAt.UnixMilli() ||
			cancellation.JobTerminalAt > cancellation.Question.ResolvedAt {
			return false
		}
		conversation.Cancellation = &cancellation.Question
	} else if pdf.Cancellation != nil || pdf.State != "unknown" {
		return false
	}
	if !validTrialConversation(conversation, trial.AgentID, workspace) {
		return false
	}
	proof, wire := pdf.PostStop, pdf.Wire
	if proof == nil || proof.Version != Version || proof.Code != "native_post_stop_observed" ||
		!proof.RuntimeStopped || !proof.LogicalSettled || !proof.FixtureRemoved ||
		wire == nil || wire.Version != Version || wire.Scope != "native_bound_client_media_v1" || !wire.Complete ||
		wire.ObservedMessages != 0 || wire.MatchingMessages != 0 || wire.MessageID != "" ||
		wire.Code != "wire_media_unavailable" {
		return false
	}
	census := proof.Census
	return census != nil && census.Version == Version && census.Code == "native_idle_unproven" &&
		validDigest(census.BaselineSHA256) && validDigest(census.ObservedSHA256) &&
		census.SourceUnchanged != nil && *census.SourceUnchanged && census.Counts != nil &&
		*census.Counts == (PDFCensusCounts{})
}

func gradeFinalizedPDFChecks(item Case, trial Trial) []CheckResult {
	checks := gradeConversation(item.Checks, trial.Events, lastOutcome(trial.Events))
	if trial.PDF == nil || trial.PDF.PostStop == nil ||
		trial.PDF.PostStop.AssessmentScope != pdfNoWriteAssessmentScope &&
			trial.PDF.PostStop.AssessmentScope != pdfOutputAssessmentScope {
		return checks
	}
	for index, check := range item.Checks {
		switch check.Kind {
		case "pdf_no_write":
			if trial.PDF.PostStop.AssessmentScope == pdfNoWriteAssessmentScope {
				checks[index] = booleanCheck(check.ID, true, []string{"pdf.post_stop"})
			}
		case "pdf_fields", "pdf_structure":
			if trial.PDF.PostStop.AssessmentScope == pdfOutputAssessmentScope {
				checks[index] = booleanCheck(check.ID, true, []string{trial.PDF.Artifact.File})
			}
		case "pdf_delivery":
			if trial.PDF.PostStop.AssessmentScope == pdfOutputAssessmentScope {
				checks[index] = booleanCheck(check.ID, true,
					[]string{"pdf.settlement", "pdf.post_stop", "pdf.wire"})
			}
		case "private_surfaces":
			checks[index] = booleanCheck(check.ID, true,
				[]string{"run.private_surfaces", "pdf.post_stop", "pdf.wire"})
		}
	}
	return checks
}

func assessFinalizedPDF(
	ctx context.Context, judge *ModelRole, item Case, trial *Trial, workspace string,
	lifecycle *TargetLifecycle, surfaces *PrivateSurfaceObservation,
	report *privateReport,
) {
	if !observedPDFProfile(lifecycle, surfaces) {
		return
	}
	var scope string
	switch {
	case validPDFNoWriteEvidence(*trial, item, workspace):
		scope = pdfNoWriteAssessmentScope
	case validPDFOutputEvidence(*trial, item, workspace):
		if !report.observeFinalPDFArtifact(trial) {
			return
		}
		scope = pdfOutputAssessmentScope
	default:
		return
	}
	started := time.Now()
	defer func() { trial.DurationMS += time.Since(started).Milliseconds() }()
	trial.PDF.PostStop.AssessmentScope = scope
	trial.Checks = gradeFinalizedPDFChecks(item, *trial)
	// Output judging uses the still-retained private fact guard. Offline regrade
	// has no such guard yet and must not inherit eligibility from a live pass.
	trial.EvidenceEligible = scope == pdfNoWriteAssessmentScope
	trial.Verdict, trial.Code = trial.ExecutionVerdict, trial.ExecutionCode
	for _, check := range trial.Checks {
		if combined := combineVerdicts(trial.Verdict, check.Verdict); combined != trial.Verdict {
			trial.Verdict, trial.Code = combined, "required_check_unknown"
			if combined == Fail {
				trial.Code = "hard_check_failed"
			}
		}
	}
	if len(item.Rubric) == 0 {
		return
	}
	judgement := judgeConversation(ctx, judge, item, trial.Events)
	trial.Judge = &judgement
	retainPDFJudgePrivacy(item, trial)
	if !roleBoundaryEligible(judgement.Call) {
		trial.EvidenceEligible = false
	}
	if judgement.Code == "private_judge_output" {
		failPDFArtifact(trial, Fail, judgement.Code)
	}
	if requiredRubric(item.Rubric) || judgement.Code == "private_judge_output" ||
		!roleBoundaryEligible(judgement.Call) {
		if combined := combineVerdicts(trial.Verdict, judgement.Verdict); combined != trial.Verdict {
			trial.Verdict, trial.Code = combined, judgement.Code
		}
	}
}

func retainPDFJudgePrivacy(item Case, trial *Trial) {
	if trial.PDF == nil || trial.PDF.PostStop == nil ||
		trial.PDF.PostStop.AssessmentScope != pdfNoWriteAssessmentScope &&
			trial.PDF.PostStop.AssessmentScope != pdfOutputAssessmentScope || trial.Judge == nil ||
		roleBoundaryEligible(trial.Judge.Call) && trial.Judge.Code != "private_judge_output" {
		return
	}
	trial.EvidenceEligible = false
	trial.PDF.PostStop.AssessmentScope = ""
	for index, check := range item.Checks {
		if check.Kind != "private_surfaces" || index >= len(trial.Checks) {
			continue
		}
		trial.Checks[index] = CheckResult{
			ID: check.ID, Verdict: Inconclusive,
			Code: "role_privacy_unavailable", EvidenceIDs: []string{"judge.call.boundary"},
		}
		if trial.Judge.Code == "private_judge_output" {
			trial.Checks[index].Verdict, trial.Checks[index].Code = Fail, "private_judge_output"
		}
	}
}
