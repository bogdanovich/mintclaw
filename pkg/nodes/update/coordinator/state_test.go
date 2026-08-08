package coordinator

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

func TestStateValidationBindsSlotTransitions(t *testing.T) {
	state := testState(t)
	if err := state.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	candidate := testPayload(SlotB, "v1.1.0")
	state.Transaction = testTransaction(candidate)
	state.Transaction.Phase = PhaseStaged
	if err := state.Validate(); err != nil {
		t.Fatalf("staged state error = %v", err)
	}

	state.Transaction.Phase = PhaseActivating
	state.Transaction.ActivationAttempted = true
	state.Transaction.Previous = clonePayload(state.Active)
	if err := state.Validate(); err == nil {
		t.Fatal("activating state accepted the previous active selector")
	}
	state.Active = candidate
	if err := state.Validate(); err != nil {
		t.Fatalf("activating state error = %v", err)
	}

	state.Transaction.Phase = PhaseRollingBack
	state.Transaction.RollbackAttempted = true
	if err := state.Validate(); err == nil {
		t.Fatal("rollback state accepted the candidate selector")
	}
	state.Active = *state.Transaction.Previous
	if err := state.Validate(); err != nil {
		t.Fatalf("rollback state error = %v", err)
	}
}

func TestStateValidationRejectsUnboundedOrContradictoryFacts(t *testing.T) {
	base := testState(t)
	tests := []struct {
		name   string
		mutate func(*State)
	}{
		{name: "generation", mutate: func(state *State) { state.Generation = 0 }},
		{name: "path", mutate: func(state *State) { state.Installation.ConfigPath = "relative" }},
		{name: "platform", mutate: func(state *State) { state.Installation.Platform = "windows" }},
		{
			name:   "digest case",
			mutate: func(state *State) { state.Active.SHA256 = fmt.Sprintf("%X", sha256.Sum256([]byte("x"))) },
		},
		{name: "oversized payload", mutate: func(state *State) { state.Active.Size = MaxPayloadBytes + 1 }},
		{name: "invalid node", mutate: func(state *State) { state.Installation.NodeID = nodes.ID("bad") }},
		{name: "unknown phase", mutate: func(state *State) {
			state.Transaction = testTransaction(testPayload(SlotB, "v1.1.0"))
			state.Transaction.Phase = "failed"
		}},
		{name: "verified without candidate", mutate: func(state *State) {
			state.Transaction = testTransaction(testPayload(SlotB, "v1.1.0"))
			state.Transaction.Phase = PhaseVerified
			state.Transaction.Candidate = nil
		}},
		{name: "unproven rollback", mutate: func(state *State) {
			state.Transaction = testTransaction(testPayload(SlotB, "v1.1.0"))
			state.Transaction.Phase = PhaseRolledBack
			state.Transaction.Previous = clonePayload(state.Active)
			state.Transaction.ActivationAttempted = true
			state.Transaction.RollbackAttempted = true
			state.Transaction.RollbackVerified = false
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := base
			test.mutate(&state)
			if err := state.Validate(); err == nil {
				t.Fatal("invalid state was accepted")
			}
		})
	}
}

func testState(t *testing.T) State {
	t.Helper()
	return State{
		SchemaVersion: StateSchemaVersion,
		Generation:    1,
		Installation: Installation{
			Instance: "main", Manager: "systemd", Scope: "user",
			Service: "mintclaw-node-main.service", InstallTransactionID: digestBytes(16),
			ConfigPath:        filepath.Join(t.TempDir(), "config.json"),
			CoordinatorPath:   filepath.Join(t.TempDir(), "mintclaw-node-coordinator"),
			CoordinatorSHA256: digestBytes(sha256.Size), ServiceUID: 1000, ServiceGID: 1000,
			NodeID:   nodes.ID("node_" + strings.Repeat("a", 52)),
			Platform: "linux", Architecture: "amd64",
		},
		Active: testPayload(SlotA, "v1.0.0"),
	}
}

func testPayload(slot Slot, release string) Payload {
	return Payload{Slot: slot, Release: release, Version: release, SHA256: digestBytes(sha256.Size), Size: 1024}
}

func testTransaction(candidate Payload) *Transaction {
	return &Transaction{
		Identity: ExecutionIdentity{
			InvocationID: "invocation_test", ExecutionID: "execution_test",
			PlanHash: digestBytes(sha256.Size), CatalogHash: digestBytes(sha256.Size),
			AuthorityHash: digestBytes(sha256.Size),
		},
		RequestHash: digestBytes(sha256.Size), Profile: "stable", ProfileRevision: "stable-v1",
		ReleaseAlias: "current", RequestedRelease: candidate.Release,
		ManifestSHA256: digestBytes(sha256.Size), ArtifactSHA256: candidate.SHA256,
		Phase: PhasePrepared, Candidate: &candidate, AcceptedAt: 10, ExpiresAt: 100, UpdatedAt: 10,
	}
}

func clonePayload(payload Payload) *Payload {
	copy := payload
	return &copy
}

func digestBytes(size int) string {
	return strings.Repeat("a", size*2)
}
