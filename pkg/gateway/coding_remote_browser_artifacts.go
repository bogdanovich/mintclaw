package gateway

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	codingremote "github.com/bogdanovich/mintclaw/pkg/coding/remote"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

type codingRemoteBrowserArtifactReceipt struct {
	Ref                string
	Kind               string
	ContentType        string
	Filename           string
	Size               int64
	SHA256             string
	SessionID          string
	TabID              string
	SnapshotID         string
	SnapshotGeneration uint64
}

type codingRemoteBrowserArtifactSource interface {
	codingRemoteBrowserArtifact(
		context.Context,
		*codingRemoteBrowserArtifactReceipt,
		string,
		string,
		string,
		int64,
		int,
		bool,
	) (nodes.TransferArtifactRecord, []byte, error)
}

func (store *codingRemoteBrowserInvocationStore) artifactReceipt(
	request codingremote.Request,
) (codingRemoteBrowserArtifactReceipt, bool, bool) {
	if store == nil || request.Principal == nil {
		return codingRemoteBrowserArtifactReceipt{}, false, false
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	record, found := store.records[request.InvocationID]
	if !found {
		return codingRemoteBrowserArtifactReceipt{}, false, false
	}
	if !codingRemoteBrowserInvocationMatches(record, request) ||
		record.result.State != "succeeded" || record.result.Operation != request.CapabilityOperation {
		return codingRemoteBrowserArtifactReceipt{}, true, false
	}
	receipt, err := codingRemoteBrowserArtifactFromResult(record.result)
	if err != nil || receipt.Ref != request.ArtifactRef {
		return codingRemoteBrowserArtifactReceipt{}, true, false
	}
	return receipt, true, true
}

func codingRemoteBrowserArtifactFromResult(
	result codingremote.CapabilityResult,
) (codingRemoteBrowserArtifactReceipt, error) {
	switch result.Operation {
	case "browser_capture":
		var payload struct {
			Artifact browser.ScreenshotArtifact `json:"artifact"`
		}
		if json.Unmarshal(result.Result, &payload) != nil {
			return codingRemoteBrowserArtifactReceipt{}, errors.New("browser screenshot receipt is unavailable")
		}
		artifact := payload.Artifact
		receipt := codingRemoteBrowserArtifactReceipt{
			Ref: artifact.Ref, Kind: artifact.Kind, ContentType: artifact.ContentType,
			Filename: artifact.Filename, Size: artifact.Size, SHA256: artifact.SHA256,
			SessionID: artifact.SessionID, TabID: artifact.TabID, SnapshotID: artifact.SnapshotID,
			SnapshotGeneration: artifact.SnapshotGeneration,
		}
		if receipt.Kind != "screenshot" || !validCodingRemoteBrowserArtifactReceipt(receipt) {
			return codingRemoteBrowserArtifactReceipt{}, errors.New("browser screenshot receipt is invalid")
		}
		return receipt, nil
	case "browser_act":
		var payload struct {
			Artifact      *browser.DownloadArtifact `json:"artifact"`
			ArtifactState string                    `json:"artifact_state"`
		}
		if json.Unmarshal(result.Result, &payload) != nil || payload.Artifact == nil ||
			payload.ArtifactState != "committed" {
			return codingRemoteBrowserArtifactReceipt{}, errors.New("browser download receipt is unavailable")
		}
		artifact := payload.Artifact
		receipt := codingRemoteBrowserArtifactReceipt{
			Ref: artifact.Ref, Kind: artifact.Kind, ContentType: artifact.ContentType,
			Filename: artifact.Filename, Size: artifact.Size, SHA256: artifact.SHA256,
			SessionID: artifact.SessionID, TabID: artifact.TabID,
			SnapshotGeneration: artifact.Generation,
		}
		if receipt.Kind != "download" || !validCodingRemoteBrowserArtifactReceipt(receipt) {
			return codingRemoteBrowserArtifactReceipt{}, errors.New("browser download receipt is invalid")
		}
		return receipt, nil
	default:
		return codingRemoteBrowserArtifactReceipt{}, errors.New("browser artifact operation is unavailable")
	}
}

func validCodingRemoteBrowserArtifactReceipt(receipt codingRemoteBrowserArtifactReceipt) bool {
	if !strings.HasPrefix(receipt.Ref, nodes.TransferArtifactRefPrefix) || receipt.Filename == "" ||
		receipt.ContentType == "" || receipt.Size < 1 || receipt.Size > codingremote.MaxFetchedArtifactBytes ||
		len(receipt.SHA256) != 64 || receipt.SessionID == "" || receipt.TabID == "" ||
		receipt.SnapshotGeneration == 0 {
		return false
	}
	_, err := hex.DecodeString(receipt.SHA256)
	return err == nil
}

func (source *gatewayBrowserToolSource) codingRemoteBrowserArtifact(
	ctx context.Context,
	receipt *codingRemoteBrowserArtifactReceipt,
	artifactRef string,
	expectedKind string,
	expectedTarget string,
	offset int64,
	limit int,
	fetch bool,
) (nodes.TransferArtifactRecord, []byte, error) {
	if source == nil || source.services == nil || source.services.NodeAdmission == nil ||
		source.workspace == "" ||
		(receipt != nil && !validCodingRemoteBrowserArtifactReceipt(*receipt)) {
		return nodes.TransferArtifactRecord{}, nil, nodes.ErrTransferArtifactNotFound
	}
	requestID, err := tools.RemoteBrowserArtifactRequestID(ctx)
	if err != nil {
		return nodes.TransferArtifactRecord{}, nil, nodes.ErrTransferArtifactNotFound
	}
	spool, err := source.services.NodeAdmission.gatewayTransferSpool(
		nodes.GatewayTransferSpoolPath(source.workspace),
	)
	if err != nil {
		return nodes.TransferArtifactRecord{}, nil, err
	}
	var record nodes.TransferArtifactRecord
	var data []byte
	if receipt != nil {
		var owner nodes.TransferArtifactOwner
		owner, _, err = browserScreenshotOwners(ctx, source.workspace, receipt.SessionID, requestID)
		if err != nil {
			return nodes.TransferArtifactRecord{}, nil, nodes.ErrTransferArtifactNotFound
		}
		if fetch {
			data, record, err = spool.ReadOwnedRange(ctx, owner, artifactRef, offset, limit)
		} else {
			var file *os.File
			file, record, err = spool.ResolveOwned(owner, artifactRef)
			if err == nil {
				_ = file.Close()
			}
		}
	} else {
		var owner nodes.TransferArtifactCallOwner
		owner, err = codingRemoteBrowserArtifactCallOwner(ctx, source.workspace, requestID)
		if err != nil {
			return nodes.TransferArtifactRecord{}, nil, nodes.ErrTransferArtifactNotFound
		}
		if fetch {
			data, record, err = spool.ReadOwnedCallRange(ctx, owner, artifactRef, offset, limit)
		} else {
			var file *os.File
			file, record, err = spool.ResolveOwnedCall(owner, artifactRef)
			if err == nil {
				_ = file.Close()
			}
		}
	}
	if err != nil ||
		!codingRemoteBrowserArtifactMatches(record, receipt, artifactRef, expectedKind, expectedTarget, requestID) {
		return nodes.TransferArtifactRecord{}, nil, nodes.ErrTransferArtifactNotFound
	}
	return record, data, nil
}

func codingRemoteBrowserArtifactCallOwner(
	ctx context.Context,
	workspace string,
	requestID string,
) (nodes.TransferArtifactCallOwner, error) {
	mediaOwner, err := browserScreenshotMediaOwner(ctx, workspace)
	if err != nil {
		return nodes.TransferArtifactCallOwner{}, err
	}
	owner := nodes.TransferArtifactCallOwner{
		WorkspaceID: mediaOwner.WorkspaceID,
		AgentID:     mediaOwner.AgentID,
		ActorID:     mediaOwner.ActorID,
		RouteID:     mediaOwner.RouteID,
		ToolCallID:  requestID,
	}
	return owner, owner.Validate()
}

func codingRemoteBrowserArtifactMatches(
	record nodes.TransferArtifactRecord,
	receipt *codingRemoteBrowserArtifactReceipt,
	artifactRef string,
	expectedKind string,
	expectedTarget string,
	requestID string,
) bool {
	validKind := expectedKind == "screenshot" && validBrowserScreenshotRecord(record) ||
		expectedKind == "download" && validBrowserDownloadRecord(record)
	if !validKind || record.Ref != artifactRef || record.Spec.TransferID != requestID ||
		record.Spec.Target != expectedTarget {
		return false
	}
	if receipt == nil {
		return true
	}
	return receipt.Kind == expectedKind && receipt.Ref == artifactRef &&
		record.Spec.SourceScope == receipt.TabID &&
		record.Spec.SourceRevision == receipt.SnapshotGeneration &&
		(receipt.SnapshotID == "" || record.Spec.SourceID == receipt.SnapshotID) &&
		record.Spec.Filename == receipt.Filename && record.Spec.ContentType == receipt.ContentType &&
		record.Spec.DeclaredSize == receipt.Size && record.Spec.SHA256 == receipt.SHA256
}
