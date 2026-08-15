package browserhost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/protocol"
)

type browserArtifactTransferPrepare struct {
	SessionID             string `json:"session_id"`
	RoutedSessionID       string `json:"routed_session_id"`
	ActionInvocationID    string `json:"action_invocation_id"`
	ArtifactRef           string `json:"artifact_ref"`
	PreparedActionHash    string `json:"prepared_action_hash"`
	BrowserPolicyRevision string `json:"browser_policy_revision"`
	AgentID               string `json:"agent_id"`
	ActorID               string `json:"actor_id"`
	Filename              string `json:"filename"`
	ContentType           string `json:"content_type"`
	ExpiresAt             int64  `json:"expires_at"`
}

type browserArtifactTransfer struct {
	binding   protocol.TransferFrame
	request   browserArtifactTransferPrepare
	directory string
	path      string
	file      *os.File
	hasher    hash.Hash
	sequence  uint64
	received  uint64
}

type browserStagedArtifact struct {
	binding   protocol.TransferFrame
	request   browserArtifactTransferPrepare
	directory string
	path      string
}

func (*BrowserHost) Descriptors() []nodes.CommandDescriptor { return nil }

func (host *BrowserHost) TransferPolicyRevisions() []string {
	if host == nil {
		return nil
	}
	revisions := make([]string, 0, len(host.profiles))
	for _, profile := range host.profiles {
		if slicesContains(profile.AllowedActions, "file_chooser") {
			revisions = append(revisions, profile.Revision)
		}
	}
	sort.Strings(revisions)
	return revisions
}

func (host *BrowserHost) HandleTransferFrame(
	ctx context.Context,
	frame protocol.TransferFrame,
	send func(protocol.TransferFrame) error,
) error {
	if host == nil || send == nil || frame.Validate() != nil || frame.Direction != protocol.TransferUpload {
		return protocol.ErrInvalidTransferFrame
	}
	switch frame.Type {
	case protocol.TransferFramePrepare:
		return host.prepareBrowserArtifact(ctx, frame, send)
	case protocol.TransferFrameChunk:
		return host.writeBrowserArtifact(frame, send)
	case protocol.TransferFrameCommit:
		return host.commitBrowserArtifact(frame, send)
	case protocol.TransferFrameCancel:
		return host.cancelBrowserArtifact(frame, send)
	case protocol.TransferFrameStatus:
		return host.statusBrowserArtifact(frame, send)
	default:
		return protocol.ErrInvalidTransferFrame
	}
}

func (host *BrowserHost) prepareBrowserArtifact(
	ctx context.Context,
	frame protocol.TransferFrame,
	send func(protocol.TransferFrame) error,
) error {
	var request browserArtifactTransferPrepare
	decoder := json.NewDecoder(bytes.NewReader(frame.Payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		request.SessionID == "" || request.RoutedSessionID == "" ||
		request.ActionInvocationID == "" || request.ArtifactRef == "" ||
		!browserHostDigest(request.PreparedActionHash) || !browserHostDigest(request.BrowserPolicyRevision) ||
		request.AgentID == "" ||
		request.ActorID == "" || request.Filename == "" || request.Filename != filepath.Base(request.Filename) ||
		len(request.Filename) > 255 || strings.ContainsRune(request.Filename, 0) || request.ContentType == "" ||
		len(request.ContentType) > 255 || frame.TotalSize < 1 || frame.TotalSize > uint64(nodes.MaxBrowserUploadBytes) {
		return send(browserTransferResponse(frame, protocol.TransferFrameDeny, "invalid_prepare"))
	}
	host.mu.Lock()
	session := host.sessions[request.SessionID]
	host.mu.Unlock()
	if session == nil {
		return send(browserTransferResponse(frame, protocol.TransferFrameDeny, "session_denied"))
	}
	session.mu.Lock()
	authorized := session.state == "ready" && session.profile.Revision == frame.PolicyRevision &&
		session.browserPolicyRevision == request.BrowserPolicyRevision &&
		session.routedSessionID == request.RoutedSessionID && session.agentID == request.AgentID &&
		session.actorID == request.ActorID && slicesContains(session.profile.AllowedActions, "file_chooser") &&
		frame.TotalSize <= uint64(session.limits.UploadBytes) && host.now().UTC().Unix() < request.ExpiresAt &&
		request.ExpiresAt <= host.now().UTC().Add(time.Duration(session.limits.PreparedSeconds)*time.Second).Unix()
	session.mu.Unlock()
	if !authorized {
		return send(browserTransferResponse(frame, protocol.TransferFrameDeny, "authority_denied"))
	}
	host.transferMu.Lock()
	defer host.transferMu.Unlock()
	host.expireBrowserArtifactsLocked()
	if completed, ok := host.completedTransfers[frame.TransferID]; ok {
		if browserTransferMatches(completed.binding, frame) && completed.request == request {
			return send(browserTransferResponse(frame, protocol.TransferFrameCommitted, "committed"))
		}
		return send(browserTransferResponse(frame, protocol.TransferFrameDeny, "transfer_conflict"))
	}
	if active, ok := host.activeTransfers[frame.TransferID]; ok {
		if browserTransferMatches(active.binding, frame) && active.request == request {
			return send(browserTransferResponse(frame, protocol.TransferFrameAccept, "accepted"))
		}
		return send(browserTransferResponse(frame, protocol.TransferFrameDeny, "transfer_conflict"))
	}
	if len(host.activeTransfers)+len(host.completedTransfers) >= nodes.MaxBrowserSessions {
		return send(browserTransferResponse(frame, protocol.TransferFrameDeny, "capacity_exceeded"))
	}
	if host.transferRoot == "" {
		root, err := os.MkdirTemp("", "mintclaw-browser-artifacts-")
		if err != nil {
			return send(browserTransferResponse(frame, protocol.TransferFrameFailure, "storage_unavailable"))
		}
		host.transferRoot = root
	}
	directory, err := os.MkdirTemp(host.transferRoot, "transfer-")
	if err != nil {
		return send(browserTransferResponse(frame, protocol.TransferFrameFailure, "storage_unavailable"))
	}
	path := filepath.Join(directory, request.Filename)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = os.RemoveAll(directory)
		return send(browserTransferResponse(frame, protocol.TransferFrameFailure, "storage_unavailable"))
	}
	host.activeTransfers[frame.TransferID] = &browserArtifactTransfer{
		binding: frame, request: request, directory: directory, path: path, file: file, hasher: sha256.New(),
	}
	if err := send(browserTransferResponse(frame, protocol.TransferFrameAccept, "accepted")); err != nil {
		host.removeActiveTransferLocked(frame.TransferID)
		return err
	}
	go host.watchBrowserArtifactConnection(ctx, frame.TransferID, frame)
	return nil
}

func (host *BrowserHost) watchBrowserArtifactConnection(
	ctx context.Context,
	transferID string,
	binding protocol.TransferFrame,
) {
	<-ctx.Done()
	host.transferMu.Lock()
	defer host.transferMu.Unlock()
	if active := host.activeTransfers[transferID]; active != nil && browserTransferMatches(active.binding, binding) {
		host.removeActiveTransferLocked(transferID)
	}
}

func (host *BrowserHost) writeBrowserArtifact(
	frame protocol.TransferFrame,
	send func(protocol.TransferFrame) error,
) error {
	host.transferMu.Lock()
	defer host.transferMu.Unlock()
	transfer := host.activeTransfers[frame.TransferID]
	if transfer == nil || !browserTransferMatches(transfer.binding, frame) ||
		frame.Sequence != transfer.sequence+1 || transfer.received+uint64(len(frame.Payload)) > frame.TotalSize {
		return send(browserTransferResponse(frame, protocol.TransferFrameFailure, "transfer_conflict"))
	}
	count, err := transfer.file.Write(frame.Payload)
	if err != nil || count != len(frame.Payload) {
		host.removeActiveTransferLocked(frame.TransferID)
		return send(browserTransferResponse(frame, protocol.TransferFrameFailure, "storage_unavailable"))
	}
	_, _ = transfer.hasher.Write(frame.Payload)
	transfer.sequence = frame.Sequence
	transfer.received += uint64(count)
	response := browserTransferResponse(frame, protocol.TransferFrameAck, "")
	response.Sequence = frame.Sequence
	return send(response)
}

func (host *BrowserHost) commitBrowserArtifact(
	frame protocol.TransferFrame,
	send func(protocol.TransferFrame) error,
) error {
	host.transferMu.Lock()
	defer host.transferMu.Unlock()
	transfer := host.activeTransfers[frame.TransferID]
	if transfer == nil || !browserTransferMatches(transfer.binding, frame) ||
		transfer.received != frame.TotalSize || !bytes.Equal(transfer.hasher.Sum(nil), frame.SHA256[:]) {
		if transfer != nil {
			host.removeActiveTransferLocked(frame.TransferID)
		}
		return send(browserTransferResponse(frame, protocol.TransferFrameFailure, "integrity_failed"))
	}
	if err := transfer.file.Close(); err != nil {
		transfer.file = nil
		host.removeActiveTransferLocked(frame.TransferID)
		return send(browserTransferResponse(frame, protocol.TransferFrameFailure, "storage_unavailable"))
	}
	transfer.file = nil
	host.completedTransfers[frame.TransferID] = browserStagedArtifact{
		binding: transfer.binding, request: transfer.request, directory: transfer.directory, path: transfer.path,
	}
	delete(host.activeTransfers, frame.TransferID)
	return send(browserTransferResponse(frame, protocol.TransferFrameCommitted, "committed"))
}

func (host *BrowserHost) cancelBrowserArtifact(
	frame protocol.TransferFrame,
	send func(protocol.TransferFrame) error,
) error {
	host.transferMu.Lock()
	if transfer := host.activeTransfers[frame.TransferID]; transfer != nil &&
		browserTransferMatches(transfer.binding, frame) {
		host.removeActiveTransferLocked(frame.TransferID)
	}
	if artifact, ok := host.completedTransfers[frame.TransferID]; ok &&
		browserTransferMatches(artifact.binding, frame) {
		_ = os.RemoveAll(artifact.directory)
		delete(host.completedTransfers, frame.TransferID)
	}
	host.transferMu.Unlock()
	return send(browserTransferResponse(frame, protocol.TransferFrameFailure, "canceled"))
}

func (host *BrowserHost) statusBrowserArtifact(
	frame protocol.TransferFrame,
	send func(protocol.TransferFrame) error,
) error {
	host.transferMu.Lock()
	defer host.transferMu.Unlock()
	if artifact, ok := host.completedTransfers[frame.TransferID]; ok &&
		browserTransferMatches(artifact.binding, frame) {
		return send(browserTransferResponse(frame, protocol.TransferFrameCommitted, "committed"))
	}
	if transfer, ok := host.activeTransfers[frame.TransferID]; ok && browserTransferMatches(transfer.binding, frame) {
		return send(browserTransferResponse(frame, protocol.TransferFrameAccept, "accepted"))
	}
	return send(browserTransferResponse(frame, protocol.TransferFrameFailure, "not_found"))
}

func (host *BrowserHost) takeBrowserArtifact(request nodes.BrowserHostActRequest) (browserStagedArtifact, bool) {
	host.transferMu.Lock()
	defer host.transferMu.Unlock()
	host.expireBrowserArtifactsLocked()
	for transferID, artifact := range host.completedTransfers {
		if artifact.request.SessionID == request.SessionID &&
			artifact.request.RoutedSessionID == request.RoutedSessionID &&
			artifact.request.ActionInvocationID == request.ActionInvocationID &&
			artifact.request.ArtifactRef == request.Action.ArtifactRef &&
			artifact.request.PreparedActionHash == request.PreparedActionHash &&
			artifact.request.BrowserPolicyRevision == request.BrowserPolicyRevision &&
			artifact.binding.PolicyRevision == request.ProfileRevision &&
			artifact.request.AgentID == request.AgentID && artifact.request.ActorID == request.ActorID &&
			artifact.request.Filename == request.ArtifactFilename &&
			artifact.request.ContentType == request.ArtifactContentType &&
			int64(artifact.binding.TotalSize) == request.ArtifactBytes &&
			strings.EqualFold(request.ArtifactSHA256, bytesToHex(artifact.binding.SHA256[:])) {
			delete(host.completedTransfers, transferID)
			return artifact, true
		}
	}
	return browserStagedArtifact{}, false
}

func (host *BrowserHost) expireBrowserArtifactsLocked() {
	now := host.now().UTC().Unix()
	for transferID, transfer := range host.activeTransfers {
		if now >= transfer.request.ExpiresAt {
			host.removeActiveTransferLocked(transferID)
		}
	}
	for transferID, artifact := range host.completedTransfers {
		if now >= artifact.request.ExpiresAt {
			_ = os.RemoveAll(artifact.directory)
			delete(host.completedTransfers, transferID)
		}
	}
}

func (host *BrowserHost) removeActiveTransferLocked(transferID string) {
	transfer := host.activeTransfers[transferID]
	if transfer == nil {
		return
	}
	if transfer.file != nil {
		_ = transfer.file.Close()
	}
	_ = os.RemoveAll(transfer.directory)
	delete(host.activeTransfers, transferID)
}

func (host *BrowserHost) cleanupAllBrowserArtifacts() {
	host.transferMu.Lock()
	defer host.transferMu.Unlock()
	for transferID := range host.activeTransfers {
		host.removeActiveTransferLocked(transferID)
	}
	for transferID, artifact := range host.completedTransfers {
		_ = os.RemoveAll(artifact.directory)
		delete(host.completedTransfers, transferID)
	}
	if host.transferRoot != "" {
		_ = os.RemoveAll(host.transferRoot)
		host.transferRoot = ""
	}
}

func (host *BrowserHost) cleanupBrowserArtifactsForSession(sessionID string) {
	host.transferMu.Lock()
	defer host.transferMu.Unlock()
	for transferID, transfer := range host.activeTransfers {
		if transfer.request.SessionID == sessionID {
			host.removeActiveTransferLocked(transferID)
		}
	}
	for transferID, artifact := range host.completedTransfers {
		if artifact.request.SessionID == sessionID {
			_ = os.RemoveAll(artifact.directory)
			delete(host.completedTransfers, transferID)
		}
	}
}

func browserTransferMatches(left, right protocol.TransferFrame) bool {
	return left.Direction == right.Direction && left.TransferID == right.TransferID &&
		left.PolicyRevision == right.PolicyRevision && left.TotalSize == right.TotalSize &&
		bytes.Equal(left.SHA256[:], right.SHA256[:])
}

func browserTransferResponse(
	request protocol.TransferFrame,
	kind protocol.TransferFrameType,
	status string,
) protocol.TransferFrame {
	request.Type, request.Sequence = kind, 0
	request.Payload = nil
	if status != "" {
		request.Payload, _ = json.Marshal(map[string]string{"status": status})
	}
	return request
}

func bytesToHex(data []byte) string {
	const alphabet = "0123456789abcdef"
	encoded := make([]byte, len(data)*2)
	for index, value := range data {
		encoded[index*2] = alphabet[value>>4]
		encoded[index*2+1] = alphabet[value&0x0f]
	}
	return string(encoded)
}

func slicesContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
