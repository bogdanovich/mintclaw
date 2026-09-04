package companion

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
	nodeupdate "github.com/bogdanovich/mintclaw/pkg/nodes/update"
	"github.com/bogdanovich/mintclaw/pkg/nodes/update/control"
)

func TestResolveUpdateProfilesAuthenticatesLocalArtifact(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := updateRuntimeManifest(t)
	manifestData, signatureData, err := nodeupdate.Sign(manifest, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, updateManifestAsset):
			_, _ = writer.Write(manifestData)
		case strings.HasSuffix(request.URL.Path, updateSignatureAsset):
			_, _ = writer.Write(signatureData)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	sources, policies, err := normalizeUpdateConfiguration(
		UpdateSources{"release": {
			BaseURL:   server.URL + "/download",
			PublicKey: base64.RawStdEncoding.EncodeToString(publicKey),
		}},
		UpdatePolicies{"stable": {
			Enabled: true, Revision: "stable-v1", Source: "release",
			Channel: nodeupdate.ChannelStable, Releases: map[string]UpdateReleaseConfig{
				"current": {Tag: manifest.Release, Version: manifest.Release},
			},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := resolveUpdateProfiles(
		t.Context(),
		sources,
		policies,
		"v1.0.0",
		runtime.GOOS,
		runtime.GOARCH,
		server.Client(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || len(profiles[0].Releases) != 1 {
		t.Fatalf("resolved profiles = %#v", profiles)
	}
	release := profiles[0].Releases[0]
	digest := sha256.Sum256(manifestData)
	wantArtifact := updateRuntimeArtifact(manifest, runtime.GOOS, runtime.GOARCH)
	if release.ManifestSHA256 != hex.EncodeToString(digest[:]) ||
		release.ArtifactSHA256 != wantArtifact.SHA256 ||
		release.ArtifactSize != wantArtifact.Size || release.AuthorityHash == "" {
		t.Fatalf("resolved release = %#v", release)
	}
}

func TestUpdateRuntimeRecoversTerminalCoordinatorResultWithoutReplay(t *testing.T) {
	coordinator := &recordingUpdateCoordinator{responses: map[control.Kind]control.Response{
		control.KindUpdate: {
			Observation: control.Observation{Phase: "activating", ActivationAttempted: true},
		},
		control.KindStatus: {
			Observation: control.Observation{
				Phase: "healthy", RequestedRelease: "v1.1.0", PreviousRelease: "v1.0.0",
				ActivationAttempted: true, SuccessorVerified: true, InstalledVersion: "v1.1.0",
			},
		},
	}}
	runtimeValue, plan := updateRuntimeFixture(t, coordinator)
	if _, err := runtimeValue.Invoke(t.Context(), plan); !errorsIs(err, ErrInvocationOutcomeUnknown) {
		t.Fatalf("Invoke() error = %v, want unknown", err)
	}
	record, found, err := runtimeValue.RecoverInvocation(t.Context(), plan.InvocationID)
	if err != nil || !found || record.State != nodes.InvocationSucceeded {
		t.Fatalf("RecoverInvocation() = %#v, %v, %v", record, found, err)
	}
	if coordinator.count(control.KindUpdate) != 1 || coordinator.count(control.KindStatus) != 1 {
		t.Fatalf("coordinator calls = %#v", coordinator.kinds())
	}
	repeated, _, err := runtimeValue.RecoverInvocation(t.Context(), plan.InvocationID)
	if err != nil || repeated.State != nodes.InvocationSucceeded || coordinator.count(control.KindStatus) != 1 {
		t.Fatalf("repeated recovery = %#v, %v, calls %#v", repeated, err, coordinator.kinds())
	}
}

func TestUpdateRuntimeRecoveryDoesNotRequireLiveCatalog(t *testing.T) {
	coordinator := &recordingUpdateCoordinator{
		errors: map[control.Kind]error{control.KindUpdate: context.DeadlineExceeded},
		responses: map[control.Kind]control.Response{
			control.KindStatus: {Observation: control.Observation{
				Phase: "healthy", RequestedRelease: "v1.1.0", PreviousRelease: "v1.0.0",
				ActivationAttempted: true, SuccessorVerified: true, InstalledVersion: "v1.1.0",
			}},
		},
	}
	runtimeValue, plan := updateRuntimeFixture(t, coordinator)
	if _, err := runtimeValue.Invoke(t.Context(), plan); !errorsIs(err, ErrInvocationOutcomeUnknown) {
		t.Fatalf("Invoke() error = %v, want unknown", err)
	}
	ledger := runtimeValue.ledger.(*InvocationLedger)
	successorPolicy := nodes.LocalCommandPolicy{
		Revision: "policy-v2", AllowedCommands: []string{"node.info.v1"},
		MaximumRisk: nodes.RiskRead, MaxTimeoutSeconds: 30, MaxOutputBytes: 4096,
	}
	successor, err := NewRuntime(
		plan.NodeID,
		"v1.1.0",
		successorPolicy,
		ledger,
		WithUpdateRecovery(coordinator),
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, descriptor := range successor.Catalog().Commands {
		if descriptor.Name == "node.update.v1" {
			t.Fatal("recovery-only successor advertised update dispatch authority")
		}
	}
	record, found, err := successor.RecoverInvocation(t.Context(), plan.InvocationID)
	if err != nil || !found || record.State != nodes.InvocationSucceeded {
		t.Fatalf("recovery-only status = %#v, %v, %v", record, found, err)
	}
	if coordinator.count(control.KindUpdate) != 1 || coordinator.count(control.KindStatus) != 1 {
		t.Fatalf("coordinator calls = %#v", coordinator.kinds())
	}
}

func TestUpdateRuntimeCancelsRecoveredPreactivationTransaction(t *testing.T) {
	coordinator := &recordingUpdateCoordinator{
		errors: map[control.Kind]error{control.KindUpdate: context.DeadlineExceeded},
		responses: map[control.Kind]control.Response{
			control.KindCancel: {Observation: control.Observation{
				Phase: "canceled", RequestedRelease: "v1.1.0",
			}},
		},
	}
	runtimeValue, plan := updateRuntimeFixture(t, coordinator)
	if _, err := runtimeValue.Invoke(t.Context(), plan); !errorsIs(err, ErrInvocationOutcomeUnknown) {
		t.Fatalf("Invoke() error = %v, want unknown", err)
	}
	successorPolicy := nodes.LocalCommandPolicy{
		Revision: "policy-v2", AllowedCommands: []string{"node.info.v1"},
		MaximumRisk: nodes.RiskRead, MaxTimeoutSeconds: 30, MaxOutputBytes: 4096,
	}
	successor, err := NewRuntime(
		plan.NodeID,
		"v1.0.0",
		successorPolicy,
		runtimeValue.ledger.(*InvocationLedger),
		WithUpdateRecovery(coordinator),
	)
	if err != nil {
		t.Fatal(err)
	}
	record, err := successor.CancelContext(
		t.Context(),
		nodes.InvocationCancelRequest{InvocationID: plan.InvocationID},
	)
	if err != nil || record.State != nodes.InvocationCanceled ||
		record.Cancellation == nil || !record.Cancellation.TerminationConfirmed {
		t.Fatalf("CancelContext() = %#v, %v", record, err)
	}
	if coordinator.count(control.KindUpdate) != 1 || coordinator.count(control.KindCancel) != 1 {
		t.Fatalf("coordinator calls = %#v", coordinator.kinds())
	}
}

func TestUpdateRuntimeDistinguishesPreacceptDenialFromAcceptedFailure(t *testing.T) {
	for _, test := range []struct {
		name      string
		response  control.Response
		wantState nodes.InvocationState
	}{
		{
			name: "preaccept denial",
			response: control.Response{
				Observation: control.Observation{Phase: "unknown"}, ErrorCode: "update_denied",
			},
			wantState: nodes.InvocationFailed,
		},
		{
			name: "accepted manifest failure",
			response: control.Response{
				Observation: control.Observation{
					Phase: "operator_action_required", RequestedRelease: "v1.1.0",
					FailureCode: "manifest_changed",
				},
			},
			wantState: nodes.InvocationSucceeded,
		},
		{
			name: "accepted activation denial",
			response: control.Response{
				Observation: control.Observation{
					Phase: "operator_action_required", RequestedRelease: "v1.1.0",
					FailureCode: "request_expired",
				},
				ErrorCode: "update_denied",
			},
			wantState: nodes.InvocationUnknown,
		},
		{
			name: "request error after acceptance",
			response: control.Response{
				Observation: control.Observation{
					Phase: "operator_action_required", RequestedRelease: "v1.1.0",
				},
				ErrorCode: "request_failed",
			},
			wantState: nodes.InvocationUnknown,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			coordinator := &recordingUpdateCoordinator{responses: map[control.Kind]control.Response{
				control.KindUpdate: test.response,
			}}
			runtimeValue, plan := updateRuntimeFixture(t, coordinator)
			_, _ = runtimeValue.Invoke(t.Context(), plan)
			record, found, err := runtimeValue.Invocation(plan.InvocationID)
			if err != nil || !found || record.State != test.wantState {
				t.Fatalf("invocation = %#v, %v, %v; want %s", record, found, err, test.wantState)
			}
		})
	}
}

func TestUpdateRuntimeBindsSignalDrivenCancellation(t *testing.T) {
	for _, test := range []struct {
		name          string
		response      control.Response
		wantConfirmed bool
	}{
		{
			name: "bound cancellation",
			response: control.Response{Observation: control.Observation{
				Phase: "canceled", RequestedRelease: "v1.1.0",
			}},
			wantConfirmed: true,
		},
		{
			name: "request error",
			response: control.Response{
				Observation: control.Observation{Phase: "canceled", RequestedRelease: "v1.1.0"},
				ErrorCode:   "identity_conflict",
			},
		},
		{
			name: "different transaction",
			response: control.Response{Observation: control.Observation{
				Phase: "canceled", RequestedRelease: "v2.0.0",
			}},
		},
		{
			name:     "empty observation",
			response: control.Response{Observation: control.Observation{Phase: "canceled"}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			coordinator := &recordingUpdateCoordinator{responses: map[control.Kind]control.Response{
				control.KindCancel: test.response,
			}}
			_, plan := updateRuntimeFixture(t, coordinator)
			handler := &updateCommandHandler{coordinator: coordinator}
			_, err := handler.cancelAfterSignal(plan, *plan.Update)
			if errorsIs(err, errCommandCancellationConfirmed) != test.wantConfirmed {
				t.Fatalf("cancelAfterSignal() error = %v, confirmed = %v", err, test.wantConfirmed)
			}
			if !test.wantConfirmed && !errorsIs(err, ErrInvocationOutcomeUnknown) {
				t.Fatalf("cancelAfterSignal() error = %v, want unknown", err)
			}
		})
	}
}

func TestUpdateRuntimeDoesNotTerminalizeUnboundRecoveryResponse(t *testing.T) {
	for _, test := range []struct {
		name     string
		response control.Response
	}{
		{
			name: "request error",
			response: control.Response{
				Observation: control.Observation{Phase: "unknown"}, ErrorCode: "identity_conflict",
			},
		},
		{
			name: "different transaction",
			response: control.Response{Observation: control.Observation{
				Phase: "healthy", RequestedRelease: "v2.0.0", SuccessorVerified: true,
			}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			coordinator := &recordingUpdateCoordinator{
				errors:    map[control.Kind]error{control.KindUpdate: context.DeadlineExceeded},
				responses: map[control.Kind]control.Response{control.KindStatus: test.response},
			}
			runtimeValue, plan := updateRuntimeFixture(t, coordinator)
			if _, err := runtimeValue.Invoke(t.Context(), plan); !errorsIs(err, ErrInvocationOutcomeUnknown) {
				t.Fatalf("Invoke() error = %v, want unknown", err)
			}
			record, found, err := runtimeValue.RecoverInvocation(t.Context(), plan.InvocationID)
			if !errorsIs(err, ErrInvocationOutcomeUnknown) || !found || record.State != nodes.InvocationUnknown {
				t.Fatalf("RecoverInvocation() = %#v, %v, %v", record, found, err)
			}
		})
	}
}

func TestUpdateRuntimePreservesDurableFailureCode(t *testing.T) {
	coordinator := &recordingUpdateCoordinator{
		errors: map[control.Kind]error{control.KindUpdate: context.DeadlineExceeded},
		responses: map[control.Kind]control.Response{
			control.KindStatus: {Observation: control.Observation{
				Phase: "operator_action_required", RequestedRelease: "v1.1.0",
				ActivationAttempted: true, FailureCode: "rollback_unproven",
			}},
		},
	}
	runtimeValue, plan := updateRuntimeFixture(t, coordinator)
	if _, err := runtimeValue.Invoke(t.Context(), plan); !errorsIs(err, ErrInvocationOutcomeUnknown) {
		t.Fatalf("Invoke() error = %v, want unknown", err)
	}
	record, found, err := runtimeValue.RecoverInvocation(t.Context(), plan.InvocationID)
	if err != nil || !found || record.State != nodes.InvocationSucceeded {
		t.Fatalf("RecoverInvocation() = %#v, %v, %v", record, found, err)
	}
	var result nodeUpdateResult
	if err = json.Unmarshal(record.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != "rollback_unproven" || result.RecoveryAction == "" {
		t.Fatalf("recovered result = %#v", result)
	}
}

type recordingUpdateCoordinator struct {
	mu        sync.Mutex
	responses map[control.Kind]control.Response
	errors    map[control.Kind]error
	calls     []control.Request
}

func (coordinator *recordingUpdateCoordinator) Call(
	_ context.Context,
	request control.Request,
) (control.Response, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.calls = append(coordinator.calls, request)
	return coordinator.responses[request.Kind], coordinator.errors[request.Kind]
}

func (coordinator *recordingUpdateCoordinator) count(kind control.Kind) int {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	count := 0
	for _, request := range coordinator.calls {
		if request.Kind == kind {
			count++
		}
	}
	return count
}

func (coordinator *recordingUpdateCoordinator) kinds() []control.Kind {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	result := make([]control.Kind, len(coordinator.calls))
	for index, request := range coordinator.calls {
		result[index] = request.Kind
	}
	return result
}

func updateRuntimeFixture(
	t *testing.T,
	coordinator UpdateCoordinator,
) (*Runtime, nodes.ExecutionPlan) {
	t.Helper()
	profile := nodes.UpdateProfileDescriptor{
		Alias: "stable", Revision: "stable-v1", Channel: "stable", Approval: "required",
		CurrentVersion: "v1.0.0", Platform: runtime.GOOS, Architecture: runtime.GOARCH,
		Releases: []nodes.UpdateReleaseDescriptor{{
			Alias: "current", Version: "v1.1.0", ManifestSHA256: strings.Repeat("a", 64),
			ArtifactSHA256: strings.Repeat("b", 64), ArtifactSize: 1024,
			AuthorityHash: strings.Repeat("c", 64),
		}},
	}
	handler := &updateCommandHandler{
		coordinator: coordinator,
		descriptorValue: nodes.CommandDescriptor{
			Name: "node.update.v1", InputSchema: nodes.NodeUpdateInputSchema([]nodes.UpdateProfileDescriptor{profile}),
			OutputSchema: nodeUpdateOutputSchema(), Risk: nodes.RiskPrivileged,
			SupportsCancel: true, UpdateProfiles: []nodes.UpdateProfileDescriptor{profile},
		},
	}
	nodeID := nodes.ID("node_" + strings.Repeat("a", 52))
	policy := nodes.LocalCommandPolicy{
		Revision: "policy-v1", AllowedCommands: []string{"node.update.v1"},
		MaximumRisk: nodes.RiskPrivileged, MaxTimeoutSeconds: 300, MaxOutputBytes: 4096,
	}
	runtimeValue, err := NewRuntime(
		nodeID,
		"v1.0.0",
		policy,
		newMemoryInvocationLedger(),
		withUpdateHandler(handler),
	)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := runtimeValue.Catalog().Commands[2]
	catalogHash, err := runtimeValue.Catalog().HashForProtocol(nodes.ProtocolV2)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := nodes.NewNodeUpdatePlanAuthority("execution-1", profile, "current")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := nodes.PrepareExecutionPlanForProtocol(nodes.ProtocolV2, nodes.InvocationRequest{
		InvocationID: "invocation-1", IdempotencyKey: "idempotency-1", NodeID: nodeID,
		CatalogHash: catalogHash, Command: "node.update.v1", Update: authority,
		Input: []byte(`{"release":"current"}`), AgentID: "agent-1", SessionID: "session-1",
		ActorID: "actor-1", TimeoutSeconds: 300, OutputLimitBytes: 4096,
	}, descriptor, LocalExecutor, policy.Revision, time.Now(), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return runtimeValue, plan
}

func updateRuntimeManifest(t *testing.T) nodeupdate.Manifest {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Minute)
	manifest := nodeupdate.Manifest{
		SchemaVersion: nodeupdate.ManifestSchemaV1, Release: "v1.1.0", Channel: nodeupdate.ChannelStable,
		PublishedAt:               now.Add(-time.Minute).Format(time.RFC3339),
		ExpiresAt:                 now.Add(24 * time.Hour).Format(time.RFC3339),
		MinimumCoordinatorVersion: "v1.0.0", CoordinatorAPI: nodeupdate.CurrentCoordinatorAPI,
		NodeProtocol: nodeupdate.CurrentNodeProtocol, NodeConfig: nodeupdate.CurrentNodeConfig,
	}
	for _, tuple := range [][2]string{
		{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"},
	} {
		manifest.Artifacts = append(manifest.Artifacts, nodeupdate.Artifact{
			Platform: tuple[0], Architecture: tuple[1], Name: updateArtifactName(tuple[0], tuple[1]),
			Size: 1024, SHA256: strings.Repeat(string(rune('a'+len(manifest.Artifacts))), 64),
		})
	}
	return manifest
}

func updateRuntimeArtifact(
	manifest nodeupdate.Manifest,
	platform string,
	architecture string,
) nodeupdate.Artifact {
	for _, artifact := range manifest.Artifacts {
		if artifact.Platform == platform && artifact.Architecture == architecture {
			return artifact
		}
	}
	return nodeupdate.Artifact{}
}

func updateArtifactName(platform string, architecture string) string {
	osName := map[string]string{"linux": "Linux", "darwin": "Darwin"}[platform]
	archName := map[string]string{"amd64": "x86_64", "arm64": "arm64"}[architecture]
	return "mintclaw-node_" + osName + "_" + archName + ".tar.gz"
}

func errorsIs(err error, target error) bool {
	return err != nil && errors.Is(err, target)
}
