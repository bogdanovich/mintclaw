package eval

import (
	"errors"
	"reflect"
	"slices"
	"strings"
)

func retainedBrowserArtifact(
	writer *privateReport, trial *Trial, facts map[string]string, filter *strings.Replacer,
) []byte {
	if trial.Browser == nil || trial.Browser.Artifact == nil {
		return nil
	}
	artifact := trial.Browser.Artifact
	if artifact.Code != "artifact_collected" || artifact.File != "artifact-"+trial.ID+".csv" ||
		!artifact.RawInspected || artifact.Bytes <= 0 || artifact.Bytes > maxBrowserArtifactBytes || !validDigest(artifact.SHA256) {
		return nil
	}
	raw, err := readPrivateReportFile(writer.root, artifact.File)
	if err != nil {
		return nil
	}
	if err = inspectBrowserArtifactBody(raw, facts, filter); err != nil {
		clear(raw)
		if errors.Is(err, ErrPrivateEvidence) {
			failBrowserArtifact(trial, Fail, "private_media_evidence")
		}
		return nil
	}
	if int64(len(raw)) != artifact.Bytes || digest(raw) != artifact.SHA256 {
		clear(raw)
		failBrowserArtifact(trial, Fail, "retained_artifact_mismatch")
		return nil
	}
	return raw
}

func finalizeBrowserArtifact(
	trial *Trial,
	pending browserPendingTrial,
	target admittedTarget,
	observed browserNativeSnapshot,
	temporaryEmpty bool,
) error {
	if pending.conversation == nil || pending.conversation.wire == nil || trial.Browser == nil ||
		trial.Browser.Artifact == nil || trial.Browser.Download == nil {
		return nil
	}
	domain, artifact, download := pending.conversation, trial.Browser.Artifact, trial.Browser.Download
	defer clear(domain.wire.messages)
	if artifact.Code != "artifact_collected" || download.Code != "native_metadata_observed" {
		return nil
	}
	if download.ContentType != "text/csv" || download.Filename != "fixture-report.csv" {
		return nil
	}
	approval, err := domain.consumedApproval()
	if err != nil {
		return err
	}
	artifact.Approval = approval
	action, found := observed.state.PreparedActions[download.PreparedActionID]
	invocation, invoked := observed.state.Invocations[download.InvocationID]
	if !found || !invoked || approval.PreparedActionID != action.ID || approval.ActionHash != action.ActionHash ||
		approval.PolicyRevision != action.PolicyRevision || !reflect.DeepEqual(action, domain.approval.action) ||
		invocation.AcceptedAt/1_000_000 < approval.ConsumedAt ||
		artifact.SHA256 != download.ArtifactSHA256 || artifact.Bytes != download.ArtifactBytes ||
		artifact.MediaRef != download.MediaRef || artifact.DeliveryID != download.DeliveryID ||
		artifact.MessageID != download.PlatformMessageID {
		return nil
	}
	artifact.Wire = domain.wire.bind(*trial, download.PlatformMessageID, download.DeliveryID, download.MediaRef,
		"text/csv", trial.TransportClosed)
	for _, intent := range observed.intents {
		if intent.ID == download.DeliveryID && intent.Media != nil {
			variants := domain.wire.messages[download.PlatformMessageID]
			if len(variants) != 1 || !slices.Equal(variants[0].traces, intent.Media.TraceScopes) {
				failBrowserArtifact(trial, Fail, "wire_media_binding_mismatch")
				return nil
			}
		}
	}
	if artifact.Wire.Code != "wire_media_observed" || artifact.Wire.ObservedMessages != 1 {
		if artifact.Wire.Code == "multiple_wire_deliveries" || artifact.Wire.Code == "wire_media_conflict" ||
			artifact.Wire.Code == "wire_media_binding_mismatch" {
			failBrowserArtifact(trial, Fail, artifact.Wire.Code)
		}
		return nil
	}
	artifact.Native = inspectBrowserTask(*trial, target, pending.fixture, observed, temporaryEmpty, download, nil)
	if artifact.Native.Code != "native_post_stop_observed" || trial.Browser.Fixture == nil ||
		trial.Browser.Fixture.Effects != Pass || trial.Browser.Fixture.Artifact != Pass {
		return nil
	}
	artifact.Code = "artifact_observed"
	trial.Browser.NativeAuthority, trial.Browser.NativeCleanup = "observed", "observed"
	return nil
}
