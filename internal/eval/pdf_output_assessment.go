package eval

import (
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/document"
)

// All witnesses come from the normal native transaction, independent oracle
// and joined client observation. None is replaceable by a caption or "done".
func validPDFOutputEvidence(trial Trial, item Case, workspace string) bool {
	pdf := trial.PDF
	if item.PDF == nil || item.PDF.Output != "required" || len(item.Fixtures) != 1 ||
		!validID(trial.ID) || !validID(trial.SessionID) || !validID(trial.AgentID) ||
		!boundedIdentity(trial.SessionKey, 512) || pdf == nil || pdf.Version != Version ||
		pdf.SourceSHA256 != item.Fixtures[0].SHA256 || !validID(pdf.JobID) ||
		pdf.State != string(document.FormJobCompleted) || pdf.Cancellation != nil || trial.Cancellation != nil ||
		trial.ExecutionVerdict != Pass || !trial.TransportClosed || !observedRoleBoundaries(trial) ||
		lastOutcome(trial.Events).Kind != "final" ||
		!validTrialConversation(trial, trial.AgentID, workspace) || privateJSONValue(trial, item.Driver.Facts) {
		return false
	}
	intent, settled, artifact, proof, wire := pdf.WriteIntent, pdf.Settlement, pdf.Artifact, pdf.PostStop, pdf.Wire
	if intent == nil || settled == nil || artifact == nil || proof == nil || wire == nil ||
		intent.ReviewRevision <= 0 || !validID(intent.ConfirmationRequestID) ||
		!validID(intent.ApprovalInteractionID) || !validID(intent.OperationID) ||
		settled.ApprovalInteractionID != intent.ApprovalInteractionID || settled.OperationID != intent.OperationID ||
		!validID(settled.ApprovalRequestID) || settled.ApprovalRequestID == intent.ConfirmationRequestID ||
		settled.ApprovalConsumedAt < trial.StartedAt.UnixMilli() || settled.OperationRevision == 0 ||
		!boundedIdentity(settled.BackendGeneration, 128) || !validID(settled.OutboxDeliveryID) ||
		len(settled.PlatformMessageIDs) != 1 || !boundedIdentity(settled.PlatformMessageIDs[0], 128) ||
		!strings.HasPrefix(settled.ArtifactRef, "media://") ||
		!validID(strings.TrimPrefix(settled.ArtifactRef, "media://")) || !validDigest(settled.ArtifactSHA256) ||
		settled.ArtifactSize <= 0 || settled.ArtifactSize > maxPDFBytes ||
		artifact.File != "artifact-"+trial.ID+".pdf" || artifact.Verdict != Pass || artifact.Code != "matched" {
		return false
	}
	if !validPDFOutputConversation(trial, *intent, *settled) {
		return false
	}
	oracle := artifact.Oracle
	if oracle == nil || oracle.Version != Version || oracle.Schema != pdfOracleVersion || oracle.Code != "matched" ||
		oracle.Pypdf != "6.20.0" || oracle.PyMuPDF != "1.28.2" || !boundedIdentity(oracle.MuPDF, 64) ||
		oracle.OracleSHA256 != digest(pdfOracleScript) || oracle.SourceSHA256 != pdf.SourceSHA256 ||
		oracle.ArtifactSHA256 != settled.ArtifactSHA256 || !oracle.FieldsMatched || !oracle.StructureMatched ||
		!oracle.AppearanceMatched || !oracle.BackgroundMatched || oracle.CheckedFields != item.PDF.FieldCount ||
		oracle.RenderedPages != item.PDF.Pages || proof.Version != Version ||
		proof.Code != "native_post_stop_observed" || !proof.RuntimeStopped || !proof.LogicalSettled ||
		!proof.FixtureRemoved || wire.Version != Version || wire.Scope != "native_bound_client_media_v1" ||
		wire.Code != "wire_media_observed" || !wire.Complete || wire.ObservedMessages != 1 ||
		wire.MatchingMessages != 1 || wire.MessageID != settled.PlatformMessageIDs[0] {
		return false
	}
	census := proof.Census
	return census != nil && census.Version == Version && census.Code == "native_idle_unproven" &&
		validDigest(census.BaselineSHA256) && validDigest(census.ObservedSHA256) &&
		census.SourceUnchanged != nil && *census.SourceUnchanged && census.Counts != nil &&
		*census.Counts == (PDFCensusCounts{WriteOperations: 1, MediaIntents: 1, DocumentDeliveries: 1})
}

func validPDFOutputConversation(trial Trial, intent PDFWriteIntentEvidence, settled PDFSettlementEvidence) bool {
	review, confirmation, approval, grant := -1, -1, -1, -1
	for index, event := range trial.Events {
		switch {
		case event.Kind == "final" && confirmation == -1:
			review = index
		case event.Kind == "input" && event.RequestID == intent.ConfirmationRequestID:
			if confirmation != -1 {
				return false
			}
			confirmation = index
		case event.Kind == "approval" && event.RequestID == intent.ConfirmationRequestID &&
			event.InteractionID == settled.ApprovalInteractionID:
			if approval != -1 {
				return false
			}
			approval = index
		case event.Kind == "input" && event.RequestID == settled.ApprovalRequestID:
			if grant != -1 {
				return false
			}
			grant = index
		}
	}
	return review >= 0 && confirmation > review && approval > confirmation && grant > approval
}

// Re-read only retained binary evidence; never reopen a target, worker or
// deleted source. Parser/oracle work already happened before native shutdown.
func (report *privateReport) observeFinalPDFArtifact(trial *Trial) bool {
	if report == nil || report.root == nil {
		failPDFArtifact(trial, Inconclusive, "retained_artifact_unavailable")
		return false
	}
	data, err := readPDFNativeFile(report.root, trial.PDF.Artifact.File, maxPDFBytes)
	defer clear(data)
	if err != nil {
		failPDFArtifact(trial, Inconclusive, "retained_artifact_unavailable")
		return false
	}
	if int64(len(data)) != trial.PDF.Settlement.ArtifactSize || digest(data) != trial.PDF.Settlement.ArtifactSHA256 {
		failPDFArtifact(trial, Fail, "retained_artifact_mismatch")
		return false
	}
	return true
}
