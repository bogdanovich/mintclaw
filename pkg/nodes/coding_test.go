package nodes

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
)

func TestCodingCommandDescriptorsAreCanonicalInternalContracts(t *testing.T) {
	descriptors, err := CodingCommandDescriptors()
	if err != nil {
		t.Fatal(err)
	}
	if len(descriptors) != 5 {
		t.Fatalf("CodingCommandDescriptors() count = %d", len(descriptors))
	}
	for _, descriptor := range descriptors {
		if !IsCodingCommand(descriptor.Name) || descriptor.ModelContract != nil ||
			descriptor.SupportsCancel || descriptor.SupportsProgress {
			t.Fatalf("coding descriptor = %#v", descriptor)
		}
		wantRisk := RiskRead
		if descriptor.Name == CodingCommandTaskStart || descriptor.Name == CodingCommandTaskSteer ||
			descriptor.Name == CodingCommandTaskCancel {
			wantRisk = RiskWrite
		}
		if descriptor.Risk != wantRisk {
			t.Fatalf("%s risk = %s, want %s", descriptor.Name, descriptor.Risk, wantRisk)
		}
		mutated := descriptor
		mutated.InputSchema = json.RawMessage(`{"type":"object"}`)
		if mutated.Validate() == nil {
			t.Fatalf("%s accepted a noncanonical input schema", descriptor.Name)
		}
	}
}

func TestCodingCommandV1NamesAreNotAccepted(t *testing.T) {
	for _, name := range []string{
		"coding.projects.v1",
		"coding.task.start.v1",
		"coding.task.status.v1",
		"coding.task.steer.v1",
		"coding.task.cancel.v1",
	} {
		if IsCodingCommand(name) {
			t.Fatalf("legacy coding command %q was accepted", name)
		}
	}
}

func TestCodingStartAndSteerInputsBindEphemeralText(t *testing.T) {
	start, startEphemeral, err := NewCodingTaskStartInputs(
		"task-one",
		"generation-one",
		"mintclaw",
		"revision-one",
		codingtask.TaskModeInvestigate,
		"Inspect the repository.",
		"Report the evidence.",
		"turn-one",
	)
	if err != nil {
		t.Fatal(err)
	}
	request, err := start.Bind(startEphemeral)
	if err != nil || request.Objective != startEphemeral.Objective ||
		request.DoneCriteria != startEphemeral.DoneCriteria {
		t.Fatalf("Bind(start) = %#v, %v", request, err)
	}
	changedStart := startEphemeral
	changedStart.Objective += " changed"
	if _, err = start.Bind(changedStart); err == nil {
		t.Fatal("Bind(start) accepted changed ephemeral text")
	}

	answer := &CodingQuestionAnswer{
		QuestionID: "question-one", QuestionRevision: 2, AnswerID: "focused",
	}
	steer, steerEphemeral, err := NewCodingTaskSteerInputs(
		"task-one",
		"generation-one",
		"worker-one",
		"turn-two",
		"Continue with the focused scope.",
		answer,
	)
	if err != nil {
		t.Fatal(err)
	}
	if text, bindErr := steer.Bind(steerEphemeral); bindErr != nil || text != steerEphemeral.Text {
		t.Fatalf("Bind(steer) = %q, %v", text, bindErr)
	}
	steerEphemeral.Text = "different"
	if _, err = steer.Bind(steerEphemeral); err == nil {
		t.Fatal("Bind(steer) accepted changed ephemeral text")
	}
}

func TestCodingInvocationDispatchAllowsOnlyBoundedCodingEphemeralInput(t *testing.T) {
	input, ephemeral, err := NewCodingTaskStartInputs(
		"task-one",
		"generation-one",
		"mintclaw",
		"revision-one",
		codingtask.TaskModeInvestigate,
		"Inspect the repository.",
		"",
		"turn-one",
	)
	if err != nil {
		t.Fatal(err)
	}
	descriptors, err := CodingCommandDescriptors()
	if err != nil {
		t.Fatal(err)
	}
	var descriptor CommandDescriptor
	for _, candidate := range descriptors {
		if candidate.Name == CodingCommandTaskStart {
			descriptor = candidate
		}
	}
	catalog := CapabilityCatalog{Commands: descriptors}
	catalogHash, err := catalog.Hash()
	if err != nil {
		t.Fatal(err)
	}
	rawInput, _ := json.Marshal(input)
	plan, err := PrepareExecutionPlan(InvocationRequest{
		InvocationID: "inv-coding-start", IdempotencyKey: "idem-coding-start",
		NodeID: ID("node-test"), CatalogHash: catalogHash, Command: descriptor.Name,
		Input: rawInput, AgentID: "agent-test", SessionID: "session-test", ActorID: "actor-test",
		TimeoutSeconds: 30, OutputLimitBytes: MinCodingTaskOutputBytes,
	}, descriptor, "local", "policy-test", time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	rawEphemeral, _ := json.Marshal(ephemeral)
	if err = (InvocationDispatch{Plan: plan, EphemeralInput: rawEphemeral}).Validate(); err != nil {
		t.Fatalf("InvocationDispatch.Validate() error = %v", err)
	}
	oversized := json.RawMessage(`{"objective":"` + strings.Repeat("x", MaxCodingEphemeralInputBytes) + `"}`)
	if err = (InvocationDispatch{Plan: plan, EphemeralInput: oversized}).Validate(); err == nil {
		t.Fatal("InvocationDispatch.Validate() accepted oversized coding content")
	}

	nonEphemeral := plan
	nonEphemeral.Command = CodingCommandTaskStatus
	if err = (InvocationDispatch{Plan: nonEphemeral, EphemeralInput: rawEphemeral}).Validate(); err == nil {
		t.Fatal("InvocationDispatch.Validate() accepted content on status")
	}
}

func TestInvocationOwnerDigestIsStableAndSeparatesOwnerTuples(t *testing.T) {
	first, err := InvocationOwnerDigest("agent-one", "session-one", "actor-one")
	if err != nil {
		t.Fatal(err)
	}
	repeated, _ := InvocationOwnerDigest("agent-one", "session-one", "actor-one")
	other, _ := InvocationOwnerDigest("agent-one", "session-one", "actor-two")
	if first != repeated || first == other || len(first) != 64 {
		t.Fatalf("owner digests = %q, %q, %q", first, repeated, other)
	}
	if _, err = InvocationOwnerDigest("", "session-one", "actor-one"); err == nil {
		t.Fatal("InvocationOwnerDigest() accepted malformed identity")
	}
}
