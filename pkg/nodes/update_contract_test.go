package nodes

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPrepareNodeUpdatePlanBindsAuthenticatedReleaseAuthority(t *testing.T) {
	descriptor, profile := updatePlanDescriptorFixture()
	catalog := CapabilityCatalog{Commands: []CommandDescriptor{descriptor}}
	catalogHash, err := catalog.Hash()
	if err != nil {
		t.Fatal(err)
	}
	authority, err := NewNodeUpdatePlanAuthority("execution-1", profile, "current")
	if err != nil {
		t.Fatal(err)
	}
	request := InvocationRequest{
		InvocationID: "invocation-1", IdempotencyKey: "idempotency-1",
		NodeID: ID("node_" + strings.Repeat("a", 52)), CatalogHash: catalogHash,
		Command: "node.update.v1", Update: authority, Input: json.RawMessage(`{"release":"current"}`),
		AgentID: "agent-1", SessionID: "session-1", ActorID: "actor-1",
		TimeoutSeconds: 300, OutputLimitBytes: 4096,
	}
	plan, err := PrepareExecutionPlan(
		request,
		descriptor,
		"local",
		"policy-v1",
		time.Now(),
		5*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	changed := request
	changed.Update = cloneUpdateAuthority(authority)
	changed.Update.ArtifactSHA256 = strings.Repeat("e", 64)
	if _, err = PrepareExecutionPlan(
		changed,
		descriptor,
		"local",
		"policy-v1",
		time.Now(),
		5*time.Minute,
	); err == nil {
		t.Fatal("changed artifact authority was accepted")
	}
	conflictingInput := request
	conflictingInput.Input = json.RawMessage(`{"release":"next"}`)
	if _, err = PrepareExecutionPlan(
		conflictingInput,
		descriptor,
		"local",
		"policy-v1",
		time.Now(),
		5*time.Minute,
	); err == nil {
		t.Fatal("conflicting model-visible release selector was accepted")
	}
	tampered := plan
	tampered.Update = cloneUpdateAuthority(plan.Update)
	tampered.Update.ReleaseAlias = "other"
	if err = tampered.Validate(); err == nil {
		t.Fatal("tampered retained update authority passed plan validation")
	}
	conflictingReplay := plan
	conflictingReplay.Input = json.RawMessage(`{"release":"next"}`)
	conflictingReplay.PlanHash, err = conflictingReplay.computeHash()
	if err != nil {
		t.Fatal(err)
	}
	policy := LocalCommandPolicy{
		Revision: "policy-v1", AllowedCommands: []string{"node.update.v1"}, MaximumRisk: RiskPrivileged,
		MaxTimeoutSeconds: 300, MaxOutputBytes: 4096,
	}
	if err = policy.AuthorizeReplay(conflictingReplay, catalog, request.NodeID, "local"); err == nil {
		t.Fatal("conflicting update selector passed replay authorization")
	}
}

func TestProjectUpdateDescriptorRequiresExactProfile(t *testing.T) {
	descriptor, _ := updatePlanDescriptorFixture()
	if _, ok := ProjectUpdateDescriptorForProfile(descriptor, "missing"); ok {
		t.Fatal("unknown update profile was projected")
	}
	projected, ok := ProjectUpdateDescriptorForProfile(descriptor, "stable")
	if !ok || len(projected.UpdateProfiles) != 1 || projected.UpdateProfiles[0].Alias != "stable" {
		t.Fatalf("projected descriptor = %#v, %v", projected, ok)
	}
}

func updatePlanDescriptorFixture() (CommandDescriptor, UpdateProfileDescriptor) {
	profile := UpdateProfileDescriptor{
		Alias: "stable", Revision: "stable-v1", Channel: "stable", Approval: "required",
		CurrentVersion: "v1.0.0", Platform: "linux", Architecture: "amd64",
		Releases: []UpdateReleaseDescriptor{
			{
				Alias: "current", Version: "v1.1.0", ManifestSHA256: strings.Repeat("a", 64),
				ArtifactSHA256: strings.Repeat("b", 64), ArtifactSize: 1024,
				AuthorityHash: strings.Repeat("c", 64),
			},
			{
				Alias: "next", Version: "v1.2.0", ManifestSHA256: strings.Repeat("d", 64),
				ArtifactSHA256: strings.Repeat("e", 64), ArtifactSize: 2048,
				AuthorityHash: strings.Repeat("f", 64),
			},
		},
	}
	return CommandDescriptor{
		Name: "node.update.v1", InputSchema: NodeUpdateInputSchema([]UpdateProfileDescriptor{profile}),
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		Risk:         RiskPrivileged, SupportsCancel: true, UpdateProfiles: []UpdateProfileDescriptor{profile},
	}, profile
}

func cloneUpdateAuthority(authority *NodeUpdatePlanAuthority) *NodeUpdatePlanAuthority {
	if authority == nil {
		return nil
	}
	cloned := *authority
	return &cloned
}
