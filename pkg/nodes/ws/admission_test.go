package ws

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/protocol"
)

func TestValidateInvocationResultRejectsInvalidCompanionOutput(t *testing.T) {
	descriptor := nodes.CommandDescriptor{
		Name:        "node.info.v1",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		OutputSchema: json.RawMessage(
			`{"type":"object","required":["node_id"],"properties":{"node_id":{"type":"string"}},"additionalProperties":false}`,
		),
		Risk: nodes.RiskRead,
	}
	plan := nodes.ExecutionPlan{InvocationRequest: nodes.InvocationRequest{OutputLimitBytes: 128}}
	if _, err := validateInvocationResult(descriptor, plan, json.RawMessage(`{"unexpected":true}`)); !errors.Is(
		err,
		nodes.ErrInvalidInvocation,
	) {
		t.Fatalf("schema-invalid result error = %v", err)
	}
	oversized := json.RawMessage(`{"node_id":"` + strings.Repeat("x", 128) + `"}`)
	if _, err := validateInvocationResult(descriptor, plan, oversized); !errors.Is(
		err,
		nodes.ErrInvalidInvocation,
	) {
		t.Fatalf("oversized result error = %v", err)
	}
}

func TestAdmissionRejectsPlanForUnapprovedCatalogBeforeDispatch(t *testing.T) {
	_, handler, nodeID, plan := testInvocationAdmission(
		t,
		strings.Repeat("0", 64),
	)
	commitCalls := 0
	if _, dispatched, err := handler.Invoke(
		t.Context(),
		nodeID,
		plan,
		nil,
		func() error {
			commitCalls++
			return nil
		},
	); !errors.Is(
		err,
		nodes.ErrCommandDenied,
	) || dispatched {
		t.Fatalf("stale catalog invocation error = %v", err)
	}
	if commitCalls != 0 {
		t.Fatalf("stale catalog dispatch commit calls = %d", commitCalls)
	}
}

func TestAdmissionRejectsPlanFromPreviousSessionProtocol(t *testing.T) {
	registry, handler, nodeID, plan := testInvocationAdmission(t, "")
	registration, found, err := registry.Registration(nodeID)
	if err != nil || !found {
		t.Fatalf("Registration() = %#v, %v, found %v", registration, err, found)
	}
	v2CatalogHash, err := registration.Snapshot.Catalog.HashForProtocol(nodes.ProtocolV2)
	if err != nil {
		t.Fatal(err)
	}
	if v2CatalogHash != registration.Snapshot.CatalogHash {
		t.Fatal("test catalog does not exercise identical v1/v2 hashes")
	}
	registration.Snapshot.ProtocolVersion = nodes.ProtocolV2
	if err = registry.Upsert(registration.Snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err = handler.validateInvocationPreflight(nodeID, plan); !errors.Is(err, nodes.ErrCommandDenied) {
		t.Fatalf("previous-protocol plan error = %v", err)
	}
}

func TestValidateInvocationApprovalProjectsServiceProfile(t *testing.T) {
	_, _, nodeID, _ := testInvocationAdmission(t, "")
	profiles := []nodes.ServiceProfileDescriptor{
		{
			Alias: "database-services", Revision: "database-services-v1", Manager: "systemd",
			Services: []nodes.ServiceDescriptor{{Alias: "database", Status: true}},
			LogLimits: nodes.ServiceLogLimits{
				EntriesMax: 100, BytesMax: 4096, AgeSecondsMax: 3600,
			},
			ActionApproval: "required",
		},
		{
			Alias: "server-services", Revision: "server-services-v1", Manager: "systemd",
			Services: []nodes.ServiceDescriptor{{Alias: "vpn", Status: true}},
			LogLimits: nodes.ServiceLogLimits{
				EntriesMax: 100, BytesMax: 4096, AgeSecondsMax: 3600,
			},
			ActionApproval: "required",
		},
	}
	descriptor := nodes.CommandDescriptor{
		Name:         "service.status.v1",
		InputSchema:  nodes.ServiceCommandInputSchema("service.status.v1", profiles),
		OutputSchema: nodes.ServiceCommandOutputSchema("service.status.v1"),
		Risk:         nodes.RiskRead,
		ModelContract: &nodes.CommandModelContract{
			Availability: nodes.ModelUnavailable, TimeoutSecondsMax: 30,
			OutputBytesMax: 4096, ResultKind: "json",
			AuthorityDigest: strings.Repeat("a", 64),
			Guidance:        []string{}, Examples: []json.RawMessage{},
		},
		ServiceProfiles: profiles,
	}
	projected, ok := nodes.ProjectServiceDescriptorForProfile(descriptor, "server-services")
	if !ok {
		t.Fatal("project service descriptor")
	}
	catalogHash := strings.Repeat("b", 64)
	plan, err := nodes.PrepareExecutionPlan(
		nodes.InvocationRequest{
			InvocationID: "inv_service", IdempotencyKey: "idem_service", NodeID: nodeID,
			CatalogHash: catalogHash, Command: descriptor.Name,
			ServiceProfile: "server-services", Input: json.RawMessage(`{"service":"vpn"}`),
			AgentID: "main", SessionID: "session_service", ActorID: "user_service",
			TimeoutSeconds: 30, OutputLimitBytes: 4096,
		},
		projected,
		"local",
		"policy-service",
		time.Unix(10, 0),
		time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	approval := nodes.CommandApproval{Descriptor: descriptor, CatalogHash: catalogHash}
	if err := validateInvocationApproval(approval, nodeID, plan); err != nil {
		t.Fatalf("profile-projected service approval rejected: %v", err)
	}
	plan.ServiceProfile = "database-services"
	if err := validateInvocationApproval(approval, nodeID, plan); !errors.Is(err, nodes.ErrCommandDenied) {
		t.Fatalf("changed service profile error = %v", err)
	}
}

func TestValidateInvocationApprovalProjectsJobProfile(t *testing.T) {
	_, _, nodeID, _ := testInvocationAdmission(t, "")
	profiles := []nodes.JobProfileDescriptor{
		{
			Alias: "builds", Revision: "builds-v1", Executor: "system_exec",
			AuthorityDigest: strings.Repeat("a", 64), TimeoutSecondsMax: 7200,
			ConcurrentJobs: 2, StdoutBytesMax: 1024, StderrBytesMax: 1024,
			ArtifactCountMax: 2, ArtifactBytesMax: 1024, ArtifactsTotalBytesMax: 2048,
			RetentionSeconds: 3600, CancelGuarantee: "process_group",
			ExecutableAliases: []string{"go"}, WorkingScopes: []string{"repo"},
			Approval: nodes.JobProfileApproval{Start: "required", Read: "none", Cancel: "required"},
		},
		{
			Alias: "tests", Revision: "tests-v1", Executor: "system_exec",
			AuthorityDigest: strings.Repeat("b", 64), TimeoutSecondsMax: 3600,
			ConcurrentJobs: 2, StdoutBytesMax: 1024, StderrBytesMax: 1024,
			ArtifactCountMax: 2, ArtifactBytesMax: 1024, ArtifactsTotalBytesMax: 2048,
			RetentionSeconds: 3600, CancelGuarantee: "process_group",
			ExecutableAliases: []string{"go"}, WorkingScopes: []string{"repo"},
			Approval: nodes.JobProfileApproval{Start: "required", Read: "none", Cancel: "required"},
		},
	}
	descriptors, err := nodes.JobCommandDescriptors(profiles)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := descriptors[0]
	projected, ok := nodes.ProjectJobDescriptorForProfile(descriptor, "tests")
	if !ok {
		t.Fatal("project job descriptor")
	}
	catalogHash := strings.Repeat("c", 64)
	plan, err := nodes.PrepareExecutionPlan(
		nodes.InvocationRequest{
			InvocationID: "inv_job", IdempotencyKey: "idem_job", NodeID: nodeID,
			CatalogHash: catalogHash, Command: descriptor.Name, JobProfile: "tests",
			Input: json.RawMessage(
				`{"argv":["go","test","./..."],"cwd":"repo","timeout_seconds":120,"env":{}}`,
			),
			AgentID: "main", SessionID: "session_job", ActorID: "user_job",
			TimeoutSeconds: 30, OutputLimitBytes: 4096,
		},
		projected,
		"local",
		"policy-job",
		time.Unix(10, 0),
		time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	approval := nodes.CommandApproval{Descriptor: descriptor, CatalogHash: catalogHash}
	if err := validateInvocationApproval(approval, nodeID, plan); err != nil {
		t.Fatalf("profile-projected job approval rejected: %v", err)
	}
	plan.JobProfile = "builds"
	if err := validateInvocationApproval(approval, nodeID, plan); !errors.Is(err, nodes.ErrCommandDenied) {
		t.Fatalf("changed job profile error = %v", err)
	}
}

func TestValidateInvocationApprovalProjectsFileProfileFromPreparedInput(t *testing.T) {
	_, _, nodeID, _ := testInvocationAdmission(t, "")
	profiles := []nodes.FileProfileDescriptor{
		{
			Alias: "legacy", Revision: "legacy-v1",
			ReadableRoots: []string{"/srv/legacy"}, WritableRoots: []string{"/srv/legacy"},
			AllowCreate: true, AllowOverwrite: true, MaxFileBytes: 1024,
			Approval: nodes.FileProfileApproval{Metadata: "none", Read: "required", Write: "required"},
		},
		{
			Alias: "workspace", Revision: "workspace-v1",
			ReadableRoots: []string{"/srv/workspace"}, WritableRoots: []string{"/srv/workspace"},
			AllowCreate: true, AllowOverwrite: true, MaxFileBytes: 1024,
			Approval: nodes.FileProfileApproval{Metadata: "none", Read: "none", Write: "none"},
		},
	}
	descriptors, err := nodes.WorkspaceDescriptors(profiles, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	var descriptor nodes.CommandDescriptor
	for _, candidate := range descriptors {
		if candidate.Name == nodes.WorkspaceCommandWrite {
			descriptor = candidate
			break
		}
	}
	projected, ok := nodes.ProjectFileDescriptorForProfile(descriptor, "workspace")
	if !ok {
		t.Fatal("project workspace file descriptor")
	}
	catalogHash := strings.Repeat("d", 64)
	plan, err := nodes.PrepareExecutionPlan(
		nodes.InvocationRequest{
			InvocationID: "inv_workspace", IdempotencyKey: "idem_workspace", NodeID: nodeID,
			CatalogHash: catalogHash, Command: descriptor.Name,
			Input: json.RawMessage(
				`{"profile_revision":"workspace-v1","workspace_revision":"route-v1",` +
					`"working_scope":"project","path":"README.md","content":"ok","overwrite":false}`,
			),
			AgentID: "main", SessionID: "session_workspace", ActorID: "user_workspace",
			TimeoutSeconds: 30, OutputLimitBytes: 4096,
		},
		projected,
		"local",
		"policy-workspace",
		time.Unix(10, 0),
		time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	approval := nodes.CommandApproval{Descriptor: descriptor, CatalogHash: catalogHash}
	if err := validateInvocationApproval(approval, nodeID, plan); err != nil {
		t.Fatalf("profile-projected workspace approval rejected: %v", err)
	}
	plan.Input = json.RawMessage(
		`{"profile_revision":"legacy-v1","workspace_revision":"route-v1",` +
			`"working_scope":"project","path":"README.md","content":"ok","overwrite":false}`,
	)
	if err := validateInvocationApproval(approval, nodeID, plan); !errors.Is(err, nodes.ErrCommandDenied) {
		t.Fatalf("changed workspace profile error = %v", err)
	}
}

func TestAdmissionRevocationWaitsForDispatchWrite(t *testing.T) {
	registry, handler, nodeID, plan := testInvocationAdmission(t, "")
	connection := newStubPeerConnection()
	session := newPeer(connection)
	session.markReady()
	releaseSession, err := handler.sessions.Claim(nodeID, session, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = releaseSession() }()

	commitStarted := make(chan struct{})
	allowCommit := make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type invokeResult struct {
		dispatched bool
		err        error
	}
	invoked := make(chan invokeResult, 1)
	go func() {
		_, dispatched, invokeErr := handler.Invoke(ctx, nodeID, plan, nil, func() error {
			close(commitStarted)
			<-allowCommit
			return nil
		})
		invoked <- invokeResult{dispatched: dispatched, err: invokeErr}
	}()
	<-commitStarted

	revoked := make(chan error, 1)
	go func() {
		_, revokeErr := registry.Revoke(nodeID, nodes.Revocation{
			Reason: "test revocation",
			At:     time.Now().Unix(),
		})
		revoked <- revokeErr
	}()
	select {
	case revokeErr := <-revoked:
		t.Fatalf("revocation completed before dispatch write: %v", revokeErr)
	case <-time.After(25 * time.Millisecond):
	}

	close(allowCommit)
	<-connection.writeStarted
	select {
	case revokeErr := <-revoked:
		if revokeErr != nil {
			t.Fatal(revokeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("revocation remained blocked after dispatch write")
	}
	cancel()
	result := <-invoked
	if !result.dispatched || !errors.Is(result.err, context.Canceled) {
		t.Fatalf(
			"Invoke() = (dispatched %v, error %v)",
			result.dispatched,
			result.err,
		)
	}
}

func TestAdmissionWritesAfterCommittedDispatchError(t *testing.T) {
	_, handler, nodeID, plan := testInvocationAdmission(t, "")
	connection := newStubPeerConnection()
	session := newPeer(connection)
	session.markReady()
	releaseSession, err := handler.sessions.Claim(nodeID, session, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = releaseSession() }()
	commitErr := &fileutil.CommittedWriteError{Err: errors.New("sync invocation directory")}

	_, dispatched, err := handler.Invoke(
		t.Context(),
		nodeID,
		plan,
		nil,
		func() error { return commitErr },
	)
	if !dispatched || !errors.Is(err, commitErr) {
		t.Fatalf("Invoke() = (dispatched %v, error %v)", dispatched, err)
	}
	select {
	case <-connection.writeStarted:
	default:
		t.Fatal("committed dispatch error prevented frame write")
	}
	session.pendingMu.Lock()
	_, abandoned := session.abandoned["req_1"]
	session.pendingMu.Unlock()
	if !abandoned {
		t.Fatal("post-write commit warning did not retain late-response correlation")
	}
}

func TestAdmissionPreservesBoundedCompanionRejectionCode(t *testing.T) {
	_, handler, nodeID, plan := testInvocationAdmission(t, "")
	connection := newStubPeerConnection()
	session := newPeer(connection)
	session.markReady()
	releaseSession, err := handler.sessions.Claim(nodeID, session, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = releaseSession() }()

	type invokeResult struct {
		dispatched bool
		err        error
	}
	invoked := make(chan invokeResult, 1)
	go func() {
		_, dispatched, invokeErr := handler.Invoke(
			t.Context(),
			nodeID,
			plan,
			nil,
			func() error { return nil },
		)
		invoked <- invokeResult{dispatched: dispatched, err: invokeErr}
	}()
	<-connection.writeStarted
	waitForPeerPending(t, session, 1)
	ok := false
	if err := session.handleResponse(protocol.Envelope{
		Type: protocol.FrameResponse,
		ID:   "req_1",
		OK:   &ok,
		Error: &protocol.Error{
			Code:    nodes.InvocationDispatchCommandDenied,
			Message: "secret local policy detail",
		},
	}); err != nil {
		t.Fatal(err)
	}
	result := <-invoked
	code, typed := nodes.InvocationDispatchErrorCode(result.err)
	if !result.dispatched || !typed || code != nodes.InvocationDispatchCommandDenied {
		t.Fatalf("Invoke() = (dispatched %v, code %q, typed %v, error %v)", result.dispatched, code, typed, result.err)
	}
	if strings.Contains(result.err.Error(), "secret local policy detail") {
		t.Fatalf("companion rejection leaked detail: %v", result.err)
	}
}

func testInvocationAdmission(
	t *testing.T,
	requestCatalogHash string,
) (*nodes.FileRegistry, *AdmissionHandler, nodes.ID, nodes.ExecutionPlan) {
	t.Helper()
	descriptor := nodes.CommandDescriptor{
		Name:         "node.info.v1",
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false}`),
		OutputSchema: json.RawMessage(`{"type":"object"}`),
		Risk:         nodes.RiskRead,
	}
	catalog := nodes.CapabilityCatalog{Commands: []nodes.CommandDescriptor{descriptor}}
	catalogHash, err := catalog.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if requestCatalogHash == "" {
		requestCatalogHash = catalogHash
	}
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	nodeID, err := nodes.DeriveID(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := nodes.NewFileRegistry(filepath.Join(t.TempDir(), "registry.json"), 4)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := nodes.Snapshot{
		ID:              nodeID,
		State:           nodes.StatePendingPairing,
		ProtocolVersion: nodes.ProtocolV1,
		Platform:        "linux",
		Architecture:    "amd64",
		SoftwareVersion: "v0.1.0",
		CatalogHash:     catalogHash,
		Catalog:         catalog,
		Executor:        "local",
		PolicyRevision:  "policy-1",
		LastSeenAt:      1,
	}
	if upsertErr := registry.UpsertPending(nodes.PendingPairing{
		Node:          snapshot,
		PublicKey:     publicKey,
		KeyAlgorithm:  nodes.KeyAlgorithmEd25519,
		RequestedRole: "companion",
		RequestedAt:   1,
	}); upsertErr != nil {
		t.Fatal(upsertErr)
	}
	if _, approveErr := registry.Approve(nodeID, nodes.PairingApproval{
		AllowedCommands: []string{descriptor.Name},
		At:              2,
	}); approveErr != nil {
		t.Fatal(approveErr)
	}
	snapshot.State = nodes.StateConnected
	snapshot.LastSeenAt = 2
	if upsertErr := registry.Upsert(snapshot); upsertErr != nil {
		t.Fatal(upsertErr)
	}
	authenticator, err := nodes.NewAuthenticator(registry, nodes.AdmissionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewAdmissionHandler(authenticator, AdmissionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	request := nodes.InvocationRequest{
		InvocationID:     "inv_test",
		IdempotencyKey:   "idem_test",
		NodeID:           nodeID,
		CatalogHash:      requestCatalogHash,
		Command:          descriptor.Name,
		Input:            json.RawMessage(`{}`),
		AgentID:          "main",
		SessionID:        "session_test",
		ActorID:          "user_test",
		TimeoutSeconds:   30,
		OutputLimitBytes: 1024,
	}
	plan, err := nodes.PrepareExecutionPlan(
		request,
		descriptor,
		"local",
		"policy-1",
		time.Unix(10, 0),
		time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	return registry, handler, nodeID, plan
}

func TestAdmissionPersistsSignedIdentityOverWSS(t *testing.T) {
	registry, handler := testAdmissionHandler(t, false)
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	transport, ok := server.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("test server transport = %T", server.Client().Transport)
	}
	dialer := websocket.Dialer{TLSClientConfig: transport.TLSClientConfig.Clone()}
	connection, handshakeResponse, err := dialer.Dial(
		"wss"+strings.TrimPrefix(server.URL, "https"),
		nil,
	)
	if handshakeResponse != nil && handshakeResponse.Body != nil {
		defer func() { _ = handshakeResponse.Body.Close() }()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close() }()

	challenge := readChallenge(t, connection)
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := nodes.NewIdentityProof(
		privateKey, challenge.Nonce, nodes.ProtocolV1, nodes.ProtocolV1,
		"v0.1.0", "linux", "amd64", nodes.CapabilityCatalog{},
		nodes.ExecutionProfile{Executor: "local", PolicyRevision: "policy-1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	params, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	requestData, err := protocol.Encode(protocol.Envelope{
		Type:   protocol.FrameRequest,
		ID:     "req_auth",
		Method: "node.authenticate",
		Params: params,
	})
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := connection.WriteMessage(websocket.TextMessage, requestData); writeErr != nil {
		t.Fatal(writeErr)
	}
	_, responseData, err := connection.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	response, err := protocol.Decode(responseData)
	if err != nil {
		t.Fatal(err)
	}
	if response.OK == nil || !*response.OK {
		t.Fatalf("authentication response = %#v", response)
	}
	var result nodes.AdmissionResult
	if unmarshalErr := json.Unmarshal(response.Result, &result); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	if result.NodeID != proof.NodeID || result.State != nodes.StatePendingPairing {
		t.Fatalf("authentication result = %#v", result)
	}
	pending, exists, err := registry.Pending(proof.NodeID)
	if err != nil || !exists || pending.Node.State != nodes.StatePendingPairing {
		t.Fatalf("Pending() = %#v, exists %v, error %v", pending, exists, err)
	}
}

func TestAdmissionConsumesAndroidEnrollmentOfferOverWSS(t *testing.T) {
	registry, err := nodes.NewFileRegistry(filepath.Join(t.TempDir(), "registry.json"), 4)
	if err != nil {
		t.Fatal(err)
	}
	offers := nodes.NewEnrollmentOfferManager(nodes.EnrollmentOfferConfig{})
	authenticator, err := nodes.NewAuthenticator(registry, nodes.AdmissionConfig{EnrollmentOffers: offers})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewAdmissionHandler(authenticator, AdmissionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	connection := dialTestAdmission(t, server)
	defer func() { _ = connection.Close() }()

	challenge := readChallenge(t, connection)
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	offer, err := offers.Issue("wss://gateway.example/nodes/v1/ws", "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	proof := androidEnrollmentProof(t, privateKey, challenge.Nonce, offer)
	response := sendAuthenticationProof(t, connection, proof)
	if response.OK == nil || !*response.OK {
		t.Fatalf("authentication response = %#v", response)
	}
	var result nodes.AdmissionResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.NodeID != proof.NodeID || result.State != nodes.StatePendingPairing {
		t.Fatalf("authentication result = %#v", result)
	}
	if _, exists, err := registry.Pending(proof.NodeID); err != nil || !exists {
		t.Fatalf("pending pairing exists = %v, error = %v", exists, err)
	}
}

func TestAdmissionRejectsPlaintextByDefault(t *testing.T) {
	_, handler := testAdmissionHandler(t, false)
	server := httptest.NewServer(handler)
	defer server.Close()
	connection, response, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(server.URL, "http"), nil,
	)
	if response != nil && response.Body != nil {
		defer func() { _ = response.Body.Close() }()
	}
	if connection != nil {
		_ = connection.Close()
	}
	if err == nil {
		t.Fatal("plaintext WebSocket admission succeeded")
	}
	if response == nil || response.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("response = %#v", response)
	}
}

func TestAdmissionAllowsExplicitLoopbackDevelopment(t *testing.T) {
	_, handler := testAdmissionHandler(t, true)
	server := httptest.NewServer(handler)
	defer server.Close()
	connection, response, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(server.URL, "http"), nil,
	)
	if response != nil && response.Body != nil {
		defer func() { _ = response.Body.Close() }()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	challenge := readChallenge(t, connection)
	if challenge.Nonce == "" {
		t.Fatal("development connection received empty challenge")
	}
}

func TestAdmissionCloseDrainsInFlightHandshake(t *testing.T) {
	_, handler := testAdmissionHandler(t, false)
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	connection := dialTestAdmission(t, server)
	defer func() { _ = connection.Close() }()
	_ = readChallenge(t, connection)

	if err := handler.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := connection.ReadMessage(); err == nil {
		t.Fatal("in-flight handshake survived admission shutdown")
	}
}

func TestAdmissionHeartbeatDisconnectsRevokedLiveSession(t *testing.T) {
	registry, handler := testAdmissionHandlerWithConfig(t, AdmissionConfig{
		HeartbeatPeriod: 10 * time.Millisecond,
		LivenessTimeout: 100 * time.Millisecond,
	})
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	pendingConnection := dialTestAdmission(t, server)
	pending := authenticateTestConnection(t, pendingConnection, privateKey)
	_ = pendingConnection.Close()
	if _, approveErr := registry.Approve(
		pending.NodeID,
		nodes.PairingApproval{At: time.Now().Unix()},
	); approveErr != nil {
		t.Fatal(approveErr)
	}

	activeConnection := dialTestAdmission(t, server)
	connected := authenticateTestConnection(t, activeConnection, privateKey)
	if connected.State != nodes.StateConnected {
		t.Fatalf("approved admission state = %q", connected.State)
	}
	if _, revokeErr := registry.Revoke(connected.NodeID, nodes.Revocation{
		Reason: "test revocation",
		At:     time.Now().Unix(),
	}); revokeErr != nil {
		t.Fatal(revokeErr)
	}
	_ = activeConnection.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, readErr := activeConnection.ReadMessage(); readErr == nil {
		t.Fatal("revoked live session remained connected after heartbeat")
	}
	registration, exists, err := registry.Registration(connected.NodeID)
	if err != nil || !exists {
		t.Fatalf("Registration() = exists %v, error %v", exists, err)
	}
	if registration.Snapshot.State != nodes.StateRevoked {
		t.Fatalf("revoked node state = %q", registration.Snapshot.State)
	}
}

func TestAdmissionDoesNotTrustForwardedProtoFromRemotePeer(t *testing.T) {
	_, handler := testAdmissionHandler(t, false)
	request := httptest.NewRequest(http.MethodGet, "http://gateway.example/nodes/v1/ws", nil)
	request.RemoteAddr = "192.0.2.20:12345"
	request.Header.Set("X-Forwarded-Proto", "https")
	if handler.secureRequest(request) {
		t.Fatal("remote peer spoofed secure transport with X-Forwarded-Proto")
	}
}

func TestAdmissionDoesNotTrustForwardedProtoFromLoopbackPeer(t *testing.T) {
	_, handler := testAdmissionHandler(t, false)
	request := httptest.NewRequest(http.MethodGet, "http://gateway.example/nodes/v1/ws", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("X-Forwarded-Proto", "https")
	if handler.secureRequest(request) {
		t.Fatal("loopback peer spoofed secure transport with X-Forwarded-Proto")
	}
}

func testAdmissionHandler(
	t *testing.T,
	allowPlaintext bool,
) (*nodes.FileRegistry, *AdmissionHandler) {
	t.Helper()
	return testAdmissionHandlerWithConfig(t, AdmissionConfig{
		AllowLoopbackPlaintext: allowPlaintext,
	})
}

func testAdmissionHandlerWithConfig(
	t *testing.T,
	cfg AdmissionConfig,
) (*nodes.FileRegistry, *AdmissionHandler) {
	t.Helper()
	registry, err := nodes.NewFileRegistry(filepath.Join(t.TempDir(), "registry.json"), 4)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := nodes.NewAuthenticator(registry, nodes.AdmissionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewAdmissionHandler(authenticator, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return registry, handler
}

func dialTestAdmission(t *testing.T, server *httptest.Server) *websocket.Conn {
	t.Helper()
	transport, ok := server.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("test server transport = %T", server.Client().Transport)
	}
	dialer := websocket.Dialer{TLSClientConfig: transport.TLSClientConfig.Clone()}
	connection, response, err := dialer.Dial(
		"wss"+strings.TrimPrefix(server.URL, "https"),
		nil,
	)
	if response != nil && response.Body != nil {
		defer func() { _ = response.Body.Close() }()
	}
	if err != nil {
		t.Fatal(err)
	}
	return connection
}

func authenticateTestConnection(
	t *testing.T,
	connection *websocket.Conn,
	privateKey ed25519.PrivateKey,
) nodes.AdmissionResult {
	t.Helper()
	challenge := readChallenge(t, connection)
	proof, err := nodes.NewIdentityProof(
		privateKey, challenge.Nonce, nodes.ProtocolV1, nodes.ProtocolV1,
		"v0.1.0", "linux", "amd64", nodes.CapabilityCatalog{},
		nodes.ExecutionProfile{Executor: "local", PolicyRevision: "policy-1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	params, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	requestData, err := protocol.Encode(protocol.Envelope{
		Type:   protocol.FrameRequest,
		ID:     "req_auth",
		Method: "node.authenticate",
		Params: params,
	})
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := connection.WriteMessage(websocket.TextMessage, requestData); writeErr != nil {
		t.Fatal(writeErr)
	}
	_, responseData, err := connection.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	response, err := protocol.Decode(responseData)
	if err != nil {
		t.Fatal(err)
	}
	if response.OK == nil || !*response.OK {
		t.Fatalf("authentication response = %#v", response)
	}
	var result nodes.AdmissionResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func androidEnrollmentProof(
	t *testing.T,
	privateKey *ecdsa.PrivateKey,
	nonce string,
	offer nodes.EnrollmentOffer,
) nodes.IdentityProof {
	t.Helper()
	publicKey, err := privateKey.PublicKey.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	publicKeyBytes := publicKey.Bytes()
	nodeID, err := nodes.DeriveIDForAlgorithm(nodes.KeyAlgorithmECDSAP256SHA256, publicKeyBytes)
	if err != nil {
		t.Fatal(err)
	}
	catalog := nodes.CapabilityCatalog{Commands: []nodes.CommandDescriptor{}}
	catalogHash, err := catalog.Hash()
	if err != nil {
		t.Fatal(err)
	}
	proof := nodes.IdentityProof{
		Nonce: nonce, NodeID: nodeID, PublicKey: base64.RawURLEncoding.EncodeToString(publicKeyBytes),
		KeyAlgorithm: nodes.KeyAlgorithmECDSAP256SHA256, EnrollmentOfferID: offer.OfferID,
		MinProtocol: nodes.ProtocolV1, MaxProtocol: nodes.ProtocolV1,
		ClientVersion: "android-wss-test", Platform: "android", Architecture: "arm64-v8a",
		RequestedRole: "companion", CatalogHash: catalogHash, Catalog: catalog,
		Executor: "local", PolicyRevision: "policy-1",
	}
	transcript := androidIdentityTranscript(t, proof)
	digest := sha256.Sum256(transcript)
	r, s, err := ecdsa.Sign(rand.Reader, privateKey, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	halfOrder := new(big.Int).Rsh(new(big.Int).Set(elliptic.P256().Params().N), 1)
	if s.Cmp(halfOrder) > 0 {
		s.Sub(elliptic.P256().Params().N, s)
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	proof.Signature = base64.RawURLEncoding.EncodeToString(signature)
	secret, err := base64.RawURLEncoding.Strict().DecodeString(offer.Secret)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("mintclaw-android-enrollment-v1\x00"))
	_, _ = mac.Write([]byte(offer.OfferID))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(transcript)
	proof.EnrollmentProof = base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return proof
}

func androidIdentityTranscript(t *testing.T, proof nodes.IdentityProof) []byte {
	t.Helper()
	data, err := json.Marshal(struct {
		Nonce             string             `json:"nonce"`
		NodeID            nodes.ID           `json:"node_id"`
		PublicKey         string             `json:"public_key"`
		KeyAlgorithm      nodes.KeyAlgorithm `json:"key_algorithm"`
		EnrollmentOfferID string             `json:"enrollment_offer_id,omitempty"`
		MinProtocol       int                `json:"min_protocol"`
		MaxProtocol       int                `json:"max_protocol"`
		ClientVersion     string             `json:"client_version"`
		Platform          string             `json:"platform"`
		Architecture      string             `json:"architecture"`
		RequestedRole     string             `json:"requested_role"`
		CatalogHash       string             `json:"catalog_hash"`
		Executor          string             `json:"executor"`
		PolicyRevision    string             `json:"policy_revision"`
	}{
		Nonce: proof.Nonce, NodeID: proof.NodeID, PublicKey: proof.PublicKey,
		KeyAlgorithm: proof.KeyAlgorithm, EnrollmentOfferID: proof.EnrollmentOfferID,
		MinProtocol: proof.MinProtocol, MaxProtocol: proof.MaxProtocol,
		ClientVersion: proof.ClientVersion, Platform: proof.Platform, Architecture: proof.Architecture,
		RequestedRole: proof.RequestedRole, CatalogHash: proof.CatalogHash,
		Executor: proof.Executor, PolicyRevision: proof.PolicyRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	return append([]byte("mintclaw-node-auth-v1:ecdsa-p256-sha256\x00"), data...)
}

func sendAuthenticationProof(
	t *testing.T,
	connection *websocket.Conn,
	proof nodes.IdentityProof,
) protocol.Envelope {
	t.Helper()
	params, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	requestData, err := protocol.Encode(protocol.Envelope{
		Type: protocol.FrameRequest, ID: "req_android_auth", Method: "node.authenticate", Params: params,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.WriteMessage(websocket.TextMessage, requestData); err != nil {
		t.Fatal(err)
	}
	_, responseData, err := connection.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	response, err := protocol.Decode(responseData)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func readChallenge(t *testing.T, connection *websocket.Conn) nodes.Challenge {
	t.Helper()
	messageType, data, err := connection.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.TextMessage {
		t.Fatalf("challenge message type = %d", messageType)
	}
	envelope, err := protocol.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Type != protocol.FrameEvent || envelope.Event != "node.challenge" {
		t.Fatalf("challenge envelope = %#v", envelope)
	}
	var challenge nodes.Challenge
	if err := json.Unmarshal(envelope.Payload, &challenge); err != nil {
		t.Fatal(err)
	}
	return challenge
}
