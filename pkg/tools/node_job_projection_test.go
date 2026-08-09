package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

func TestProjectJobDescriptorRequiresExactTargetProfile(t *testing.T) {
	descriptor := nodeJobProjectionDescriptor(t)
	if _, available := projectDescriptorForTarget(descriptor, "", "", "", ""); available {
		t.Fatal("job descriptor was visible without a target job profile")
	}
	if _, available := projectDescriptorForTarget(descriptor, "", "", "", "missing"); available {
		t.Fatal("job descriptor was visible for an unknown target job profile")
	}
	projected, available := projectDescriptorForTarget(descriptor, "", "", "", "tests")
	if !available || len(projected.JobProfiles) != 1 || projected.JobProfiles[0].Alias != "tests" ||
		projected.ModelContract == nil || projected.ModelContract.Availability != nodes.ModelAvailable {
		t.Fatalf("projected descriptor = %#v, available=%v", projected, available)
	}
	expected := nodes.JobCommandInputSchema(projected.Name, projected.JobProfiles)
	if !bytes.Equal(canonicalTestJSON(t, projected.InputSchema), canonicalTestJSON(t, expected)) {
		t.Fatalf("projected schema = %s; want %s", projected.InputSchema, expected)
	}
	contract := projectedNodeCommandContract(projected, string(nodes.ModelAvailable))
	if contract.Job == nil || contract.Job.Profile != "tests" ||
		contract.Job.ArtifactCountMax != 4 || contract.Job.Approval.Read != "none" {
		t.Fatalf("model-safe job contract = %#v", contract)
	}
	encoded, err := json.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"builds-v1", "/private", "secret"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("job contract leaked %q: %s", forbidden, encoded)
		}
	}
}

func nodeJobProjectionDescriptor(t *testing.T) nodes.CommandDescriptor {
	t.Helper()
	profiles := []nodes.JobProfileDescriptor{
		nodeJobProjectionProfile("builds", "builds-v1"),
		nodeJobProjectionProfile("tests", "tests-v1"),
	}
	descriptors, err := nodes.JobCommandDescriptors(profiles)
	if err != nil {
		t.Fatal(err)
	}
	for _, descriptor := range descriptors {
		if descriptor.Name == nodes.JobCommandStart {
			return descriptor
		}
	}
	t.Fatal("job.start descriptor is missing")
	return nodes.CommandDescriptor{}
}

func nodeJobProjectionProfile(alias, revision string) nodes.JobProfileDescriptor {
	return nodes.JobProfileDescriptor{
		Alias: alias, Revision: revision, Executor: "system_exec",
		AuthorityDigest: strings.Repeat("a", sha256.Size*2), TimeoutSecondsMax: 600,
		ConcurrentJobs: 2, StdoutBytesMax: 2048, StderrBytesMax: 2048,
		ArtifactCountMax: 4, ArtifactBytesMax: 1024,
		ArtifactsTotalBytesMax: 4096, RetentionSeconds: 3600,
		CancelGuarantee: "direct_process", ExecutableAliases: []string{"go"},
		WorkingScopes: []string{"workspace"}, EnvironmentNames: []string{"PATH"},
		Approval: nodes.JobProfileApproval{Start: "required", Read: "none", Cancel: "required"},
	}
}
