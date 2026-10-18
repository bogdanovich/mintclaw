package eval

import (
	"context"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/routing"
)

const browserDownloadMetadataScope = "owned_browser_download_metadata_v1"

// BrowserDownloadEvidence joins persisted native metadata only. It cannot
// authorize a download, prove wire delivery or file bytes, or admit a judge.
type BrowserDownloadEvidence struct {
	Scope             string `json:"scope"`
	Code              string `json:"code"`
	StateSHA256       string `json:"state_sha256,omitempty"`
	OutboxSHA256      string `json:"outbox_sha256,omitempty"`
	TransfersSHA256   string `json:"transfers_sha256,omitempty"`
	SessionID         string `json:"browser_session_id,omitempty"`
	PreparedActionID  string `json:"prepared_action_id,omitempty"`
	InvocationID      string `json:"invocation_id,omitempty"`
	ArtifactRef       string `json:"artifact_ref,omitempty"`
	ArtifactSHA256    string `json:"artifact_sha256,omitempty"`
	ArtifactBytes     int64  `json:"artifact_bytes,omitempty"`
	MediaRef          string `json:"media_ref,omitempty"`
	DeliveryID        string `json:"delivery_id,omitempty"`
	PlatformMessageID string `json:"platform_message_id,omitempty"`
}

type browserTransferSnapshot struct {
	nodes.TransferSpoolSnapshot
	sha256 string
}

func observeBrowserTransfers(
	ctx context.Context, workspace string, facts map[string]string, filter *strings.Replacer,
) (browserTransferSnapshot, error) {
	if ctx == nil || ctx.Err() != nil {
		return browserTransferSnapshot{}, errNativeEvidence
	}
	path, err := filepath.Rel(workspace, nodes.GatewayTransferSpoolSnapshotPath(workspace))
	if err != nil || !safeRelativePath(filepath.ToSlash(path)) {
		return browserTransferSnapshot{}, errNativeEvidence
	}
	root, err := openBrowserEvidenceRoot(workspace, filepath.Dir(path))
	if err != nil {
		return browserTransferSnapshot{}, errNativeEvidence
	}
	defer func() { _ = root.Close() }()
	raw, _, err := readCensusFile(root, filepath.Base(path), maxPrivateScanFileBytes)
	if err != nil {
		return browserTransferSnapshot{}, errNativeEvidence
	}
	defer clear(raw)
	if err = inspectNativeJSON(raw, facts, filter); err != nil {
		return browserTransferSnapshot{}, err
	}
	snapshot, err := nodes.InspectTransferSpoolSnapshot(raw)
	if err != nil || ctx.Err() != nil {
		return browserTransferSnapshot{}, errNativeEvidence
	}
	return browserTransferSnapshot{TransferSpoolSnapshot: snapshot, sha256: digest(raw)}, nil
}

func inspectBrowserDownload(
	trial Trial, target admittedTarget, observed browserNativeSnapshot, transfers browserTransferSnapshot,
) *BrowserDownloadEvidence {
	proof := &BrowserDownloadEvidence{Scope: browserDownloadMetadataScope, Code: "native_metadata_unavailable"}
	if target.config == nil || trial.AgentID != target.preset.ExpectedAgent || !validID(trial.SessionID) ||
		!boundedIdentity(trial.SessionKey, 512) || !validDigest(observed.stateSHA256) ||
		!validDigest(observed.outboxSHA256) || !validDigest(transfers.sha256) {
		return proof
	}
	proof.StateSHA256, proof.OutboxSHA256, proof.TransfersSHA256 = observed.stateSHA256,
		observed.outboxSHA256, transfers.sha256
	owner, err := media.NewMediaOwner(target.preset.ExpectedWorkspace, trial.AgentID, "mintclaw-user",
		trial.SessionKey, trial.SessionKey, "mintclaw", "mintclaw:"+trial.SessionID, "")
	if err != nil {
		return proof
	}
	name, profile := browserEvalProfile(target.config)
	policy, err := target.config.Tools.Browser.PolicyRevision()
	if err != nil || len(profile.AllowedOrigins) != 1 {
		return proof
	}
	var selected *BrowserDownloadEvidence
	for _, invocation := range observed.state.Invocations {
		if invocation.Owner.SessionKey != browser.OpaqueSessionKey(trial.SessionKey) ||
			invocation.State != browser.InvocationSucceeded || invocation.Execution != nil {
			continue
		}
		action, found := observed.state.PreparedActions[invocation.PreparedActionID]
		session, sessionFound := observed.state.Sessions[invocation.SessionID]
		if !found || !sessionFound || action.Action.Kind != browser.ActionDownload || !action.Action.Deliver ||
			action.Owner != invocation.Owner || session.Owner != invocation.Owner || action.SessionID != session.ID ||
			invocation.ActionHash != action.ActionHash || invocation.Effect != action.Effect ||
			session.Target != config.BrowserDefaultTarget || session.Profile != name ||
			session.ProfileRevision != profile.Revision || session.PolicyRevision != policy ||
			action.Target != session.Target || action.Profile != session.Profile ||
			action.ProfileRevision != session.ProfileRevision || action.PolicyRevision != session.PolicyRevision ||
			action.CurrentOrigin != profile.AllowedOrigins[0] ||
			action.TabID != session.TabID || session.DryRun || action.DryRun ||
			session.State != browser.SessionClosed || session.SafeFailure != "" ||
			session.Owner.ActorID != browser.OpaqueActorID("mintclaw:mintclaw-user") ||
			session.Owner.AgentID != browser.OpaqueAgentID(routing.NormalizeAgentID(trial.AgentID)) ||
			session.CreatedAt < trial.StartedAt.UnixNano() || action.CreatedAt < session.CreatedAt ||
			invocation.CreatedAt < action.CreatedAt || invocation.UpdatedAt > session.UpdatedAt ||
			session.UpdatedAt > time.Now().UnixNano() {
			continue
		}
		terminal, decodeErr := decodeDocument[struct {
			Status   string                   `json:"status"`
			Artifact browser.DownloadArtifact `json:"artifact"`
		}](invocation.TerminalResult, browser.MaxTerminalBytes)
		if decodeErr != nil || terminal.Status != "completed" {
			continue
		}
		artifact := terminal.Artifact
		id, hasPrefix := strings.CutPrefix(artifact.Ref, nodes.TransferArtifactRefPrefix)
		record, retained := transfers.Records[id]
		if !hasPrefix || !retained ||
			!browserTransferMatches(record, artifact, action, invocation, owner, trial.StartedAt) {
			continue
		}
		for _, intent := range observed.intents {
			if !browserDownloadIntentMatches(intent, record, action, trial, target.preset.ExpectedWorkspace) {
				continue
			}
			if selected != nil {
				return proof
			}
			candidate := *proof
			candidate.Code, candidate.SessionID = "native_metadata_observed", session.ID
			candidate.PreparedActionID, candidate.InvocationID = action.ID, invocation.ID
			candidate.ArtifactRef, candidate.ArtifactSHA256, candidate.ArtifactBytes = record.Ref,
				record.Spec.SHA256, record.Spec.DeclaredSize
			candidate.MediaRef, candidate.DeliveryID = record.MediaRef, intent.ID
			candidate.PlatformMessageID = intent.PlatformMessageIDs[0]
			selected = &candidate
		}
	}
	if selected != nil {
		return selected
	}
	return proof
}

func browserTransferMatches(
	record nodes.TransferArtifactRecord, artifact browser.DownloadArtifact, action browser.PreparedAction,
	invocation browser.Invocation,
	owner media.MediaOwner, started time.Time,
) bool {
	ref, err := url.Parse(record.MediaRef)
	if err != nil || ref.Scheme != "media" || ref.User != nil || !validID(ref.Host) ||
		ref.Host == "." || ref.Host == ".." || ref.Path != "" || ref.RawQuery != "" || ref.ForceQuery ||
		ref.Fragment != "" || ref.Opaque != "" {
		return false
	}
	return record.State == nodes.TransferArtifactCommitted && record.Spec.SourceKind == "browser_download" &&
		record.Spec.Direction == nodes.TransferDirectionDownload && record.Spec.TransferID == action.RequestID &&
		record.Spec.SourceID == action.ID && record.Spec.SourceScope == action.TabID &&
		record.Spec.SourceRevision == action.SnapshotGeneration && record.Spec.Target == action.Target &&
		record.Spec.ProfileRevision == action.ProfileRevision && record.CreatedAt >= started.UnixNano() &&
		record.CreatedAt >= invocation.AcceptedAt && record.CommittedAt <= invocation.CompletedAt &&
		record.DeliveryAt >= invocation.CompletedAt && record.DeliveryAt <= record.UpdatedAt &&
		record.UpdatedAt <= time.Now().UnixNano() && record.DeliveryAt != 0 &&
		record.Owner == (nodes.TransferArtifactOwner{
			WorkspaceID: owner.WorkspaceID, AgentID: owner.AgentID, ActorID: owner.ActorID, RouteID: owner.RouteID,
			SessionID: action.SessionID, ToolCallID: action.RequestID,
		}) && artifact.Ref == record.Ref && artifact.Kind == "download" && !artifact.Truncated &&
		artifact.SessionID == action.SessionID && artifact.TabID == action.TabID &&
		artifact.Generation == action.SnapshotGeneration && artifact.Filename == record.Spec.Filename &&
		artifact.ContentType == record.Spec.ContentType && artifact.SHA256 == record.Spec.SHA256 &&
		artifact.Size > 0 && artifact.Size == record.Spec.DeclaredSize && artifact.ExpiresAt == record.Spec.ExpiresAt
}

func browserDownloadIntentMatches(
	intent outbox.Intent,
	record nodes.TransferArtifactRecord,
	action browser.PreparedAction,
	trial Trial,
	workspace string,
) bool {
	if intent.Status != outbox.StatusDelivered || intent.RecoverySettlementPending() ||
		intent.TranscriptProjectionPending() || intent.OwnerWorkspace != workspace || intent.Media == nil ||
		intent.Identity.Channel != "mintclaw" || intent.Identity.ChatID != "mintclaw:"+trial.SessionID ||
		intent.Identity.SessionKey != trial.SessionKey || len(intent.PlatformMessageIDs) != 1 ||
		!boundedIdentity(intent.PlatformMessageIDs[0], 128) || intent.CreatedAt.Before(trial.StartedAt) {
		return false
	}
	message := intent.Media
	if message.AgentID != trial.AgentID || message.SessionKey != trial.SessionKey ||
		message.Channel != intent.Identity.Channel || message.ChatID != intent.Identity.ChatID ||
		len(message.Parts) != 1 || message.Recovery == nil ||
		len(message.TraceScopes) == 0 || len(message.TraceScopes) > 16 {
		return false
	}
	for _, scope := range message.TraceScopes {
		if !scope.Complete() || scope.Workspace != workspace {
			return false
		}
	}
	part, recovery := message.Parts[0], message.Recovery
	return part.Type == "file" && part.Ref == record.MediaRef && part.Filename == record.Spec.Filename &&
		part.ContentType == record.Spec.ContentType && recovery.Kind == "browser_download_claim" &&
		recovery.ArtifactRef == record.Ref && recovery.MediaRef == record.MediaRef &&
		recovery.WorkspaceID == record.Owner.WorkspaceID && recovery.AgentID == record.Owner.AgentID &&
		recovery.ActorID == record.Owner.ActorID && recovery.RouteID == record.Owner.RouteID &&
		recovery.SessionID == action.SessionID && recovery.ToolCallID == action.RequestID
}
