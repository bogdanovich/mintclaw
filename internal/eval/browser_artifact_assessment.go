package eval

import (
	"bytes"
	"encoding/csv"
	"os"
	"strings"
	"time"
)

func validBrowserArtifactEvidence(trial Trial, item Case, workspace string) bool {
	if item.Browser == nil || item.Browser.Scenario != "artifact" ||
		!validBrowserConversation(trial, item, workspace) ||
		trial.Browser.Read != nil || trial.Browser.Denial != nil ||
		trial.Browser.Artifact == nil ||
		trial.Browser.Download == nil {
		return false
	}
	artifact, download, fixture := trial.Browser.Artifact, trial.Browser.Download, trial.Browser.Fixture
	if artifact.Scope != browserArtifactAssessmentScope || artifact.Code != "artifact_observed" ||
		!artifact.RawInspected || artifact.File != "artifact-"+trial.ID+".csv" || artifact.Bytes <= 0 ||
		artifact.Bytes > maxBrowserArtifactBytes || !validDigest(artifact.SHA256) ||
		!validBrowserTaskProof(trial, artifact.Native, browserArtifactAssessmentScope) ||
		fixture == nil || fixture.Version != Version || fixture.Scope != "local_fixture_only" ||
		!fixture.FixtureClosed || fixture.Visits < 1 || fixture.Downloads < 1 || fixture.Submissions != 0 || fixture.Removals != 0 ||
		fixture.Effects != Pass || fixture.EffectsCode != "matched" || fixture.Artifact != Pass || fixture.ArtifactCode != "matched" ||
		!validDigest(
			fixture.PageSHA256,
		) || fixture.CSVSourceSHA256 != artifact.SHA256 || fixture.ArtifactSHA256 != artifact.SHA256 ||
		download.Scope != browserDownloadMetadataScope || download.Code != "native_metadata_observed" ||
		download.StateSHA256 != artifact.Native.StateSHA256 || download.OutboxSHA256 != artifact.Native.OutboxSHA256 ||
		!validDigest(download.TransfersSHA256) || download.ArtifactSHA256 != artifact.SHA256 ||
		download.ArtifactBytes != artifact.Bytes || download.ContentType != "text/csv" || download.Filename != "fixture-report.csv" ||
		download.MediaRef != artifact.MediaRef || download.DeliveryID != artifact.DeliveryID ||
		download.PlatformMessageID != artifact.MessageID || !validID(artifact.DeliveryID) ||
		!boundedIdentity(
			artifact.MessageID,
			128,
		) || !validID(download.PreparedActionID) || !validID(download.InvocationID) ||
		!strings.HasPrefix(download.ArtifactRef, "transfer-artifact://") ||
		!validID(strings.TrimPrefix(download.ArtifactRef, "transfer-artifact://")) ||
		!strings.HasPrefix(
			artifact.MediaRef,
			"media://",
		) || !validID(strings.TrimPrefix(artifact.MediaRef, "media://")) ||
		artifact.MediaRef == "media://." || artifact.MediaRef == "media://.." {
		return false
	}
	approval, wire := artifact.Approval, artifact.Wire
	if approval == nil || approval.Scope != browserApprovalScope || approval.Code != "native_allow_once_consumed" ||
		!validID(
			approval.InteractionID,
		) || !validID(approval.PromptDeliveryID) || !validID(approval.ApprovalRequestID) ||
		!boundedIdentity(approval.PromptMessageID, 128) || !validID(approval.PromptRequestID) ||
		approval.PreparedActionID != download.PreparedActionID || !validDigest(approval.ActionHash) ||
		approval.PolicyRevision != artifact.Native.PolicyRevision || approval.WaitingRevision < 1 ||
		approval.ConsumedRevision <= approval.WaitingRevision || approval.ConsumedAt < trial.StartedAt.UnixMilli() ||
		approval.ConsumedAt > time.Now().UnixMilli() || !browserApprovalEventsMatch(trial, approval) ||
		wire == nil || wire.Version != Version || wire.Scope != "native_bound_client_media_v1" ||
		wire.Code != "wire_media_observed" || !wire.Complete || wire.ObservedMessages != 1 || wire.MatchingMessages != 1 ||
		wire.MessageID != artifact.MessageID {
		return false
	}
	return true
}

func browserApprovalEventsMatch(trial Trial, approval *BrowserApprovalEvidence) bool {
	prompts, answers := 0, 0
	for index, event := range trial.Events {
		if event.Kind == "approval" {
			prompts++
			if event.InteractionID != approval.InteractionID || event.MessageID != approval.PromptMessageID ||
				event.RequestID != approval.PromptRequestID || event.InteractionKind != "approval" ||
				event.Controls != "prompt" || index+1 >= len(trial.Events) ||
				trial.Events[index+1].Kind != "input" || trial.Events[index+1].RequestID != approval.ApprovalRequestID ||
				trial.Events[index+1].Turn != event.Turn+1 {
				return false
			}
		}
		if event.Kind == "input" && event.RequestID == approval.ApprovalRequestID {
			answers++
		}
	}
	return prompts == 1 && answers == 1
}

func browserArtifactForGrade(root *os.Root, trial Trial, filter *strings.Replacer) ([]byte, error) {
	if trial.Browser == nil || trial.Browser.Artifact == nil {
		return nil, ErrGrade
	}
	artifact := trial.Browser.Artifact
	raw, err := readPrivateReportFile(root, artifact.File)
	if err != nil {
		return nil, ErrGrade
	}
	if int64(len(raw)) != artifact.Bytes || len(raw) > maxBrowserArtifactBytes || digest(raw) != artifact.SHA256 ||
		inspectBrowserArtifactBody(raw, nil, filter) != nil {
		clear(raw)
		return nil, ErrGrade
	}
	reader := csv.NewReader(bytes.NewReader(raw))
	reader.FieldsPerRecord = 2
	rows, err := reader.ReadAll()
	if err != nil || len(rows) != 2 || rows[0][0] != "session" || rows[0][1] != "reference" ||
		rows[1][0] != trial.SessionID || !boundedIdentity(rows[1][1], 128) {
		clear(raw)
		return nil, ErrGrade
	}
	return raw, nil
}
