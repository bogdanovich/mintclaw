package nodes

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestJobDescriptorsProjectExactlyOneProfile(t *testing.T) {
	profiles := []JobProfileDescriptor{
		testJobProfileDescriptor("builds", "builds-v1"),
		testJobProfileDescriptor("tests", "tests-v1"),
	}
	descriptors, err := JobCommandDescriptors(profiles)
	if err != nil || len(descriptors) != 5 {
		t.Fatalf("JobCommandDescriptors() count = %d, error %v", len(descriptors), err)
	}
	for _, descriptor := range descriptors {
		projected, ok := ProjectJobDescriptorForProfile(descriptor, "tests")
		if !ok || len(projected.JobProfiles) != 1 || projected.JobProfiles[0].Alias != "tests" ||
			projected.ModelContract.Availability != ModelAvailable ||
			len(projected.ModelContract.Constraints.ProfileAliases) != 0 {
			t.Fatalf("projected %s = %#v, ok %v", descriptor.Name, projected, ok)
		}
		if err := projected.Validate(); err != nil {
			t.Fatalf("projected %s invalid: %v", descriptor.Name, err)
		}
	}
	if _, ok := ProjectJobDescriptorForProfile(descriptors[0], "missing"); ok {
		t.Fatal("missing job profile projected")
	}
}

func TestPrepareJobPlanBindsProfileAndCanonicalSchema(t *testing.T) {
	descriptors, err := JobCommandDescriptors([]JobProfileDescriptor{
		testJobProfileDescriptor("builds", "builds-v1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, ok := ProjectJobDescriptorForProfile(descriptors[0], "builds")
	if !ok {
		t.Fatal("project job descriptor")
	}
	request := InvocationRequest{
		InvocationID: "inv_job", IdempotencyKey: "idem_job", NodeID: "node_test",
		CatalogHash: strings.Repeat("a", 64), Command: JobCommandStart, JobProfile: "builds",
		Input: json.RawMessage(
			`{"argv":["go","test","./..."],"cwd":"repo","timeout_seconds":60,"env":{}}`,
		),
		AgentID: "agent_test", SessionID: "session_test", ActorID: "actor_test",
		TimeoutSeconds: 60, OutputLimitBytes: 4096,
	}
	if _, err := PrepareExecutionPlan(
		request,
		descriptor,
		"local",
		"policy-v1",
		time.Now(),
		time.Minute,
	); err != nil {
		t.Fatalf("PrepareExecutionPlan() error = %v", err)
	}
	request.JobProfile = "tests"
	if _, err := PrepareExecutionPlan(
		request,
		descriptor,
		"local",
		"policy-v1",
		time.Now(),
		time.Minute,
	); err == nil {
		t.Fatal("changed job profile accepted")
	}
	request.JobProfile = "builds"
	request.Input = json.RawMessage(
		`{"argv":["/usr/bin/go"],"cwd":"repo","timeout_seconds":60,"env":{}}`,
	)
	if _, err := PrepareExecutionPlan(
		request,
		descriptor,
		"local",
		"policy-v1",
		time.Now(),
		time.Minute,
	); err == nil {
		t.Fatal("host executable path accepted outside safe projection")
	}
}

func TestJobDescriptorCloneIsDeep(t *testing.T) {
	descriptors, err := JobCommandDescriptors([]JobProfileDescriptor{
		testJobProfileDescriptor("builds", "builds-v1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	cloned := CloneJobProfileDescriptors(descriptors[0].JobProfiles)
	cloned[0].ExecutableAliases[0] = "mutated"
	if descriptors[0].JobProfiles[0].ExecutableAliases[0] != "go" {
		t.Fatal("job profile clone aliases shared backing storage")
	}
}

func testJobProfileDescriptor(alias, revision string) JobProfileDescriptor {
	return JobProfileDescriptor{
		Alias: alias, Revision: revision, Executor: "system_exec",
		AuthorityDigest: strings.Repeat("b", 64), TimeoutSecondsMax: 3600,
		ConcurrentJobs: 2, StdoutBytesMax: 1024, StderrBytesMax: 1024,
		ArtifactCountMax: 2, ArtifactBytesMax: 1024, ArtifactsTotalBytesMax: 2048,
		RetentionSeconds: 3600, CancelGuarantee: "process_group",
		ExecutableAliases: []string{"go"}, WorkingScopes: []string{"repo"},
		EnvironmentNames: []string{"GOFLAGS"},
		Approval:         JobProfileApproval{Start: "required", Read: "none", Cancel: "required"},
	}
}
