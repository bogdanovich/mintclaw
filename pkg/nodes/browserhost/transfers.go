package browserhost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	lifetime  *browserArtifactLifetime
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
	lifetime  *browserArtifactLifetime
	directory string
	path      string
}

type browserArtifactLifetime struct {
	connectionDone <-chan struct{}
}

type browserOutputArtifact struct {
	descriptor nodes.BrowserOutputDescriptor
	binding    protocol.TransferFrame
	directory  string
	path       string
	expiryDone <-chan struct{}
	expiryStop context.CancelFunc
}

type browserOutputTransfer struct {
	artifact   browserOutputArtifact
	lifetime   *browserArtifactLifetime
	cancel     context.CancelFunc
	file       *os.File
	sequence   uint64
	pendingAck uint64
	lastAck    uint64
	acked      chan uint64
	streamDone chan struct{}
	finished   bool
}

const (
	browserOutputCleanupPolicy = "session_or_expiry"
	maxBrowserOutputArtifacts  = 8
)

// RegisterOutput stores one immutable browser-owned result for a later
// authenticated download-direction transfer. Trusted capture code supplies
// the complete authority descriptor; the host fills the content binding and
// never exposes the private staging path.
func (host *BrowserHost) RegisterOutput(
	descriptor nodes.BrowserOutputDescriptor,
	content []byte,
) (nodes.BrowserOutputDescriptor, error) {
	if host == nil || len(content) == 0 || descriptor.TransferID != "" ||
		descriptor.Size != 0 || descriptor.SHA256 != "" || descriptor.CapturedAt != 0 ||
		descriptor.CleanupPolicy != "" || !validBrowserOutputDescriptor(descriptor) {
		return nodes.BrowserOutputDescriptor{}, nodes.ErrBrowserHostDenied
	}
	host.mu.Lock()
	session := host.sessions[descriptor.SessionID]
	host.mu.Unlock()
	if session == nil {
		return nodes.BrowserOutputDescriptor{}, nodes.ErrBrowserHostNotFound
	}
	session.mu.Lock()
	now := host.now().UTC()
	limit := browserOutputLimit(session.limits, descriptor.Kind)
	authorized := session.state == "ready" && session.routedSessionID == descriptor.RoutedSessionID &&
		session.agentID == descriptor.AgentID && session.actorID == descriptor.ActorID &&
		session.profile.Revision == descriptor.ProfileRevision &&
		session.browserPolicyRevision == descriptor.BrowserPolicyRevision &&
		now.Before(session.expiresAt) && now.Before(session.idleExpiresAt) &&
		descriptor.ExpiresAt > now.Unix() && descriptor.ExpiresAt <= session.expiresAt.Unix() &&
		len(content) <= limit
	if descriptor.TabID != "" {
		authorized = authorized && descriptor.TabID == session.tabID &&
			descriptor.SnapshotGeneration == session.snapshotGeneration
	}
	if !authorized {
		session.mu.Unlock()
		return nodes.BrowserOutputDescriptor{}, nodes.ErrBrowserHostDenied
	}
	digest := sha256.Sum256(content)
	descriptor.Size = uint64(len(content))
	descriptor.SHA256 = hex.EncodeToString(digest[:])
	descriptor.CapturedAt = now.Unix()
	descriptor.CleanupPolicy = browserOutputCleanupPolicy
	descriptor.TransferID = browserOutputTransferID(descriptor)
	binding := protocol.TransferFrame{
		Direction: protocol.TransferDownload, TransferID: descriptor.TransferID,
		PolicyRevision: descriptor.ProfileRevision, TotalSize: descriptor.Size, SHA256: digest,
	}
	host.transferMu.Lock()
	session.mu.Unlock()
	defer host.transferMu.Unlock()
	host.expireBrowserArtifactsLocked()
	if existing, ok := host.outputArtifacts[descriptor.TransferID]; ok {
		if existing.descriptor == descriptor {
			return descriptor, nil
		}
		return nodes.BrowserOutputDescriptor{}, nodes.ErrBrowserHostDenied
	}
	if len(host.outputArtifacts) >= maxBrowserOutputArtifacts {
		return nodes.BrowserOutputDescriptor{}, nodes.ErrBrowserHostBusy
	}
	if host.transferRoot == "" {
		root, err := os.MkdirTemp("", "mintclaw-browser-artifacts-")
		if err != nil {
			return nodes.BrowserOutputDescriptor{}, nodes.ErrBrowserHostBusy
		}
		host.transferRoot = root
	}
	directory, err := os.MkdirTemp(host.transferRoot, "output-")
	if err != nil {
		return nodes.BrowserOutputDescriptor{}, nodes.ErrBrowserHostBusy
	}
	path := filepath.Join(directory, descriptor.Filename)
	if err = os.WriteFile(path, content, 0o600); err != nil {
		_ = os.RemoveAll(directory)
		return nodes.BrowserOutputDescriptor{}, nodes.ErrBrowserHostBusy
	}
	expiryContext, stopExpiry := context.WithCancel(context.Background())
	expiryDone := make(chan struct{})
	artifact := browserOutputArtifact{
		descriptor: descriptor, binding: binding, directory: directory, path: path,
		expiryDone: expiryDone, expiryStop: stopExpiry,
	}
	host.outputArtifacts[descriptor.TransferID] = artifact
	go host.watchBrowserOutputExpiry(expiryContext, descriptor.TransferID, artifact, expiryDone)
	return descriptor, nil
}

func (host *BrowserHost) watchBrowserOutputExpiry(
	ctx context.Context,
	transferID string,
	artifact browserOutputArtifact,
	done chan<- struct{},
) {
	defer close(done)
	delay := time.Unix(artifact.descriptor.ExpiresAt, 0).Sub(host.now().UTC())
	if delay < 0 {
		delay = 0
	}
	select {
	case <-ctx.Done():
		return
	case <-host.outputExpiryAfter(delay):
	}
	host.transferMu.Lock()
	defer host.transferMu.Unlock()
	current, ok := host.outputArtifacts[transferID]
	if ok && current.descriptor == artifact.descriptor && current.directory == artifact.directory {
		host.removeBrowserOutputLocked(transferID)
	}
}

func validBrowserOutputDescriptor(descriptor nodes.BrowserOutputDescriptor) bool {
	if descriptor.Kind != nodes.BrowserOutputScreenshot && descriptor.Kind != nodes.BrowserOutputDownload &&
		descriptor.Kind != nodes.BrowserOutputSnapshot {
		return false
	}
	if !browserHostIdentifier(descriptor.SessionID) || !browserHostIdentifier(descriptor.RoutedSessionID) ||
		!browserHostIdentifier(descriptor.AgentID) || !browserHostIdentifier(descriptor.ActorID) ||
		!browserHostIdentifier(descriptor.WorkspaceID) || !browserHostIdentifier(descriptor.Target) ||
		!browserHostIdentifier(descriptor.InvocationID) || !browserHostIdentifier(descriptor.ProfileRevision) ||
		!browserHostDigest(descriptor.BrowserPolicyRevision) || descriptor.ExpiresAt <= 0 ||
		descriptor.Filename == "" || descriptor.Filename != filepath.Base(descriptor.Filename) ||
		len(descriptor.Filename) > 255 || strings.ContainsRune(descriptor.Filename, 0) ||
		descriptor.ContentType == "" || len(descriptor.ContentType) > 255 {
		return false
	}
	for _, value := range []string{
		descriptor.TabID, descriptor.FrameID, descriptor.ContextID,
		descriptor.DocumentID, descriptor.SnapshotID,
	} {
		if value != "" && !browserHostIdentifier(value) {
			return false
		}
	}
	return descriptor.TabID != "" || descriptor.SnapshotGeneration == 0
}

func browserOutputLimit(limits nodes.BrowserLimits, kind string) int {
	switch kind {
	case nodes.BrowserOutputScreenshot:
		return limits.ScreenshotBytes
	case nodes.BrowserOutputDownload:
		return limits.DownloadBytes
	case nodes.BrowserOutputSnapshot:
		return limits.SnapshotBytes
	default:
		return 0
	}
}

func browserOutputTransferID(descriptor nodes.BrowserOutputDescriptor) string {
	copy := descriptor
	copy.TransferID = ""
	encoded, _ := json.Marshal(copy)
	digest := sha256.Sum256(append([]byte("mintclaw.browser.output.v1\x00"), encoded...))
	return "browser_output_" + hex.EncodeToString(digest[:16])
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
	if host == nil || send == nil || frame.Validate() != nil {
		return protocol.ErrInvalidTransferFrame
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if frame.Direction == protocol.TransferDownload {
		return host.handleBrowserOutputFrame(ctx, frame, send)
	}
	if frame.Direction != protocol.TransferUpload {
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

func (host *BrowserHost) handleBrowserOutputFrame(
	ctx context.Context,
	frame protocol.TransferFrame,
	send func(protocol.TransferFrame) error,
) error {
	switch frame.Type {
	case protocol.TransferFramePrepare:
		return host.prepareBrowserOutput(ctx, frame, send)
	case protocol.TransferFrameAck:
		return host.ackBrowserOutput(frame)
	case protocol.TransferFrameCommit:
		return host.commitBrowserOutput(frame, send)
	case protocol.TransferFrameCancel:
		return host.cancelBrowserOutput(frame, send)
	case protocol.TransferFrameStatus:
		return host.statusBrowserOutput(frame, send)
	default:
		return protocol.ErrInvalidTransferFrame
	}
}

func (host *BrowserHost) prepareBrowserOutput(
	ctx context.Context,
	frame protocol.TransferFrame,
	send func(protocol.TransferFrame) error,
) error {
	var descriptor nodes.BrowserOutputDescriptor
	decoder := json.NewDecoder(bytes.NewReader(frame.Payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&descriptor) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		descriptor.TransferID != frame.TransferID || descriptor.ProfileRevision != frame.PolicyRevision ||
		descriptor.Size != frame.TotalSize || !strings.EqualFold(descriptor.SHA256, bytesToHex(frame.SHA256[:])) {
		return send(browserTransferResponse(frame, protocol.TransferFrameDeny, "invalid_prepare"))
	}
	host.mu.Lock()
	session := host.sessions[descriptor.SessionID]
	host.mu.Unlock()
	if session == nil {
		return send(browserTransferResponse(frame, protocol.TransferFrameDeny, "session_denied"))
	}
	session.mu.Lock()
	now := host.now().UTC()
	authorized := session.state == "ready" && session.routedSessionID == descriptor.RoutedSessionID &&
		session.agentID == descriptor.AgentID && session.actorID == descriptor.ActorID &&
		session.profile.Revision == descriptor.ProfileRevision &&
		session.browserPolicyRevision == descriptor.BrowserPolicyRevision &&
		now.Before(session.expiresAt) && now.Before(session.idleExpiresAt) && now.Unix() < descriptor.ExpiresAt
	if !authorized {
		session.mu.Unlock()
		return send(browserTransferResponse(frame, protocol.TransferFrameDeny, "authority_denied"))
	}
	if host.beforeTransferAdmission != nil {
		host.beforeTransferAdmission()
	}
	host.transferMu.Lock()
	session.mu.Unlock()
	host.expireBrowserArtifactsLocked()
	artifact, ok := host.outputArtifacts[frame.TransferID]
	if !ok || artifact.descriptor != descriptor || !browserTransferMatches(artifact.binding, frame) {
		host.transferMu.Unlock()
		return send(browserTransferResponse(frame, protocol.TransferFrameDeny, "output_denied"))
	}
	if _, busy := host.outputTransfers[frame.TransferID]; busy {
		host.transferMu.Unlock()
		return send(browserTransferResponse(frame, protocol.TransferFrameDeny, "transfer_busy"))
	}
	file, err := os.Open(artifact.path)
	if err != nil {
		host.removeBrowserOutputLocked(frame.TransferID)
		host.transferMu.Unlock()
		return send(browserTransferResponse(frame, protocol.TransferFrameFailure, "storage_unavailable"))
	}
	transferContext, cancel := context.WithCancel(ctx)
	lifetime := newBrowserArtifactLifetime(transferContext)
	transfer := &browserOutputTransfer{
		artifact: artifact, lifetime: lifetime, cancel: cancel, file: file,
		acked: make(chan uint64, 1), streamDone: make(chan struct{}),
	}
	host.outputTransfers[frame.TransferID] = transfer
	host.transferMu.Unlock()
	if err = send(browserTransferResponse(frame, protocol.TransferFrameAccept, "accepted")); err != nil {
		host.removeBrowserOutputTransfer(frame.TransferID, transfer)
		return err
	}
	go host.streamBrowserOutput(transfer, frame, send)
	go host.watchBrowserOutputTransfer(frame.TransferID, transfer)
	return nil
}

func (host *BrowserHost) ackBrowserOutput(frame protocol.TransferFrame) error {
	host.transferMu.Lock()
	transfer := host.outputTransfers[frame.TransferID]
	if transfer == nil || !browserTransferMatches(transfer.artifact.binding, frame) || transfer.finished ||
		frame.Sequence == 0 || frame.Sequence != transfer.pendingAck || frame.Sequence == transfer.lastAck {
		host.transferMu.Unlock()
		return protocol.ErrInvalidTransferFrame
	}
	transfer.lastAck = frame.Sequence
	select {
	case transfer.acked <- frame.Sequence:
		host.transferMu.Unlock()
		return nil
	default:
		host.transferMu.Unlock()
		return protocol.ErrInvalidTransferFrame
	}
}

func (host *BrowserHost) streamBrowserOutput(
	transfer *browserOutputTransfer,
	binding protocol.TransferFrame,
	send func(protocol.TransferFrame) error,
) {
	defer close(transfer.streamDone)
	buffer := make([]byte, protocol.MaxTransferChunkBytes)
	hasher := sha256.New()
	var observed uint64
	for {
		count, readErr := transfer.file.Read(buffer)
		if count > 0 {
			chunk := append([]byte(nil), buffer[:count]...)
			_, _ = hasher.Write(chunk)
			host.transferMu.Lock()
			if host.outputTransfers[binding.TransferID] != transfer {
				host.transferMu.Unlock()
				return
			}
			transfer.sequence++
			transfer.pendingAck = transfer.sequence
			sequence := transfer.sequence
			host.transferMu.Unlock()
			frame := browserTransferResponse(binding, protocol.TransferFrameChunk, "")
			frame.Sequence, frame.Payload = sequence, chunk
			if send(frame) != nil {
				host.removeBrowserOutputTransfer(binding.TransferID, transfer)
				return
			}
			select {
			case <-transfer.lifetime.connectionDone:
				host.removeBrowserOutputTransfer(binding.TransferID, transfer)
				return
			case acknowledged := <-transfer.acked:
				if acknowledged != sequence {
					host.failBrowserOutputTransfer(binding, transfer, send, "sequence_mismatch")
					return
				}
			}
			observed += uint64(count)
			host.transferMu.Lock()
			transfer.pendingAck = 0
			host.transferMu.Unlock()
		}
		if readErr != nil && readErr != io.EOF {
			host.failBrowserOutputTransfer(binding, transfer, send, "source_changed")
			return
		}
		if readErr == io.EOF {
			break
		}
	}
	info, err := transfer.file.Stat()
	status := ""
	if err != nil {
		status = "source_stat_failed"
	} else if uint64(info.Size()) != binding.TotalSize {
		status = "source_size_changed"
	} else if observed != binding.TotalSize {
		status = "source_short_read"
	} else if !bytes.Equal(hasher.Sum(nil), binding.SHA256[:]) {
		status = "source_digest_changed"
	}
	if status != "" {
		host.failBrowserOutputTransfer(binding, transfer, send, status)
		return
	}
	host.transferMu.Lock()
	if host.outputTransfers[binding.TransferID] != transfer {
		host.transferMu.Unlock()
		return
	}
	transfer.finished = true
	_ = transfer.file.Close()
	transfer.file = nil
	host.transferMu.Unlock()
	_ = send(browserTransferResponse(binding, protocol.TransferFrameStatus, "received"))
}

func (host *BrowserHost) commitBrowserOutput(
	frame protocol.TransferFrame,
	send func(protocol.TransferFrame) error,
) error {
	host.transferMu.Lock()
	transfer := host.outputTransfers[frame.TransferID]
	if transfer == nil || !browserTransferMatches(transfer.artifact.binding, frame) || !transfer.finished {
		host.transferMu.Unlock()
		return send(browserTransferResponse(frame, protocol.TransferFrameFailure, "transfer_conflict"))
	}
	host.removeBrowserOutputTransferLocked(frame.TransferID, transfer)
	host.transferMu.Unlock()
	return send(browserTransferResponse(frame, protocol.TransferFrameCommitted, "committed"))
}

func (host *BrowserHost) cancelBrowserOutput(
	frame protocol.TransferFrame,
	send func(protocol.TransferFrame) error,
) error {
	host.transferMu.Lock()
	if transfer := host.outputTransfers[frame.TransferID]; transfer != nil &&
		browserTransferMatches(transfer.artifact.binding, frame) {
		host.removeBrowserOutputTransferLocked(frame.TransferID, transfer)
	}
	host.transferMu.Unlock()
	return send(browserTransferResponse(frame, protocol.TransferFrameFailure, "canceled"))
}

func (host *BrowserHost) statusBrowserOutput(
	frame protocol.TransferFrame,
	send func(protocol.TransferFrame) error,
) error {
	host.transferMu.Lock()
	defer host.transferMu.Unlock()
	if transfer := host.outputTransfers[frame.TransferID]; transfer != nil &&
		browserTransferMatches(transfer.artifact.binding, frame) {
		state := "streaming"
		if transfer.finished {
			state = "received"
		}
		return send(browserTransferResponse(frame, protocol.TransferFrameStatus, state))
	}
	if artifact, ok := host.outputArtifacts[frame.TransferID]; ok && browserTransferMatches(artifact.binding, frame) {
		return send(browserTransferResponse(frame, protocol.TransferFrameCommitted, "available"))
	}
	return send(browserTransferResponse(frame, protocol.TransferFrameFailure, "not_found"))
}

func (host *BrowserHost) failBrowserOutputTransfer(
	binding protocol.TransferFrame,
	transfer *browserOutputTransfer,
	send func(protocol.TransferFrame) error,
	status string,
) {
	host.removeBrowserOutputTransfer(binding.TransferID, transfer)
	_ = send(browserTransferResponse(binding, protocol.TransferFrameFailure, status))
}

func (host *BrowserHost) watchBrowserOutputTransfer(transferID string, transfer *browserOutputTransfer) {
	delay := time.Unix(transfer.artifact.descriptor.ExpiresAt, 0).Sub(host.now().UTC())
	if delay < 0 {
		delay = 0
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-transfer.lifetime.connectionDone:
	case <-timer.C:
	}
	host.removeBrowserOutputTransfer(transferID, transfer)
}

func (host *BrowserHost) removeBrowserOutputTransfer(transferID string, transfer *browserOutputTransfer) {
	host.transferMu.Lock()
	defer host.transferMu.Unlock()
	host.removeBrowserOutputTransferLocked(transferID, transfer)
}

func (host *BrowserHost) removeBrowserOutputTransferLocked(transferID string, transfer *browserOutputTransfer) {
	if host.outputTransfers[transferID] != transfer {
		return
	}
	transfer.cancel()
	if transfer.file != nil {
		_ = transfer.file.Close()
	}
	delete(host.outputTransfers, transferID)
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
	now := host.now().UTC()
	authorized := session.state == "ready" && session.profile.Revision == frame.PolicyRevision &&
		session.browserPolicyRevision == request.BrowserPolicyRevision &&
		session.routedSessionID == request.RoutedSessionID && session.agentID == request.AgentID &&
		session.actorID == request.ActorID && slicesContains(session.profile.AllowedActions, "file_chooser") &&
		frame.TotalSize <= uint64(session.limits.UploadBytes) && now.Before(session.expiresAt) &&
		now.Before(session.idleExpiresAt) && now.Unix() < request.ExpiresAt &&
		request.ExpiresAt <= now.Add(time.Duration(session.limits.PreparedSeconds)*time.Second).Unix()
	if !authorized {
		session.mu.Unlock()
		return send(browserTransferResponse(frame, protocol.TransferFrameDeny, "authority_denied"))
	}
	if host.beforeTransferAdmission != nil {
		host.beforeTransferAdmission()
	}
	host.transferMu.Lock()
	session.mu.Unlock()
	defer host.transferMu.Unlock()
	host.expireBrowserArtifactsLocked()
	if completed, ok := host.completedTransfers[frame.TransferID]; ok {
		if browserTransferMatches(completed.binding, frame) && completed.request == request {
			if completed.lifetime == nil || completed.lifetime.connectionDone != ctx.Done() {
				lifetime := newBrowserArtifactLifetime(ctx)
				completed.lifetime = lifetime
				host.completedTransfers[frame.TransferID] = completed
				go host.watchBrowserArtifactLifetime(
					lifetime.connectionDone,
					frame.TransferID,
					lifetime,
					request.ExpiresAt,
				)
			}
			return send(browserTransferResponse(frame, protocol.TransferFrameCommitted, "committed"))
		}
		if browserArtifactLifetimeEnded(completed.lifetime) {
			host.removeCompletedTransferLocked(frame.TransferID)
		} else {
			return send(browserTransferResponse(frame, protocol.TransferFrameDeny, "transfer_conflict"))
		}
	}
	if active, ok := host.activeTransfers[frame.TransferID]; ok {
		if browserTransferMatches(active.binding, frame) && active.request == request && active.received == 0 &&
			active.lifetime != nil && active.lifetime.connectionDone == ctx.Done() &&
			!browserArtifactLifetimeEnded(active.lifetime) {
			return send(browserTransferResponse(frame, protocol.TransferFrameAccept, "accepted"))
		}
		if browserTransferMatches(active.binding, frame) && active.request == request ||
			browserArtifactLifetimeEnded(active.lifetime) {
			host.removeActiveTransferLocked(frame.TransferID)
		} else {
			return send(browserTransferResponse(frame, protocol.TransferFrameDeny, "transfer_conflict"))
		}
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
	lifetime := newBrowserArtifactLifetime(ctx)
	host.activeTransfers[frame.TransferID] = &browserArtifactTransfer{
		binding: frame, request: request, lifetime: lifetime,
		directory: directory, path: path, file: file, hasher: sha256.New(),
	}
	if err := send(browserTransferResponse(frame, protocol.TransferFrameAccept, "accepted")); err != nil {
		host.removeActiveTransferLocked(frame.TransferID)
		return err
	}
	go host.watchBrowserArtifactLifetime(
		lifetime.connectionDone,
		frame.TransferID,
		lifetime,
		request.ExpiresAt,
	)
	return nil
}

func (host *BrowserHost) watchBrowserArtifactLifetime(
	connectionDone <-chan struct{},
	transferID string,
	lifetime *browserArtifactLifetime,
	expiresAt int64,
) {
	expiryDelay := time.Unix(expiresAt, 0).Sub(host.now().UTC())
	if expiryDelay < 0 {
		expiryDelay = 0
	}
	timer := time.NewTimer(expiryDelay)
	defer timer.Stop()
	select {
	case <-connectionDone:
	case <-timer.C:
	}
	if host.beforeTransferCleanup != nil {
		host.beforeTransferCleanup()
	}
	host.transferMu.Lock()
	defer host.transferMu.Unlock()
	if active := host.activeTransfers[transferID]; active != nil && active.lifetime == lifetime {
		host.removeActiveTransferLocked(transferID)
	}
	if completed, ok := host.completedTransfers[transferID]; ok && completed.lifetime == lifetime {
		host.removeCompletedTransferLocked(transferID)
	}
}

func newBrowserArtifactLifetime(ctx context.Context) *browserArtifactLifetime {
	done := ctx.Done()
	if done == nil {
		done = make(chan struct{})
	}
	return &browserArtifactLifetime{connectionDone: done}
}

func browserArtifactLifetimeEnded(lifetime *browserArtifactLifetime) bool {
	if lifetime == nil || lifetime.connectionDone == nil {
		return true
	}
	select {
	case <-lifetime.connectionDone:
		return true
	default:
		return false
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
		binding: transfer.binding, request: transfer.request, lifetime: transfer.lifetime,
		directory: transfer.directory, path: transfer.path,
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
		host.removeCompletedTransferLocked(frame.TransferID)
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
			host.removeCompletedTransferLocked(transferID)
		}
	}
	for transferID, artifact := range host.outputArtifacts {
		if now >= artifact.descriptor.ExpiresAt {
			host.removeBrowserOutputLocked(transferID)
		}
	}
}

func (host *BrowserHost) removeBrowserOutputLocked(transferID string) {
	if transfer := host.outputTransfers[transferID]; transfer != nil {
		host.removeBrowserOutputTransferLocked(transferID, transfer)
	}
	artifact, ok := host.outputArtifacts[transferID]
	if !ok {
		return
	}
	artifact.expiryStop()
	_ = os.RemoveAll(artifact.directory)
	delete(host.outputArtifacts, transferID)
}

func (host *BrowserHost) removeCompletedTransferLocked(transferID string) {
	artifact, ok := host.completedTransfers[transferID]
	if !ok {
		return
	}
	_ = os.RemoveAll(artifact.directory)
	delete(host.completedTransfers, transferID)
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
	for transferID := range host.completedTransfers {
		host.removeCompletedTransferLocked(transferID)
	}
	for transferID := range host.outputArtifacts {
		host.removeBrowserOutputLocked(transferID)
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
			host.removeCompletedTransferLocked(transferID)
		}
	}
	for transferID, artifact := range host.outputArtifacts {
		if artifact.descriptor.SessionID == sessionID {
			host.removeBrowserOutputLocked(transferID)
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
