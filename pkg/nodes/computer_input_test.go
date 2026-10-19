package nodes

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/computer"
)

const computerInputCanary = "computer protected canary Привет 世界 e\u0301 👩‍💻 literal \\n and \\t"

func TestComputerInputDescriptorRequiresMatchingReadOnlyProfiles(t *testing.T) {
	readOnly := computerTestDescriptors(t)
	input, err := ComputerInputCommandDescriptor([]ComputerProfileDescriptor{computerTestProfile()})
	if err != nil || input.Validate() != nil || !IsComputerCommand(input.Name) || input.Risk != RiskWrite {
		t.Fatal("invalid separately opt-in descriptor", err)
	}
	if len(readOnly) != 8 {
		t.Fatal("ordinary read-only family was widened")
	}
	if err = (CapabilityCatalog{Commands: append(readOnly, input)}).Validate(); err != nil {
		t.Fatal("matching input subset rejected", err)
	}
	if (CapabilityCatalog{Commands: []CommandDescriptor{input}}).Validate() == nil {
		t.Fatal("input-only catalog bypassed shared read-only owner")
	}
	foreign := computerTestProfile()
	foreign.Revision = strings.Repeat("b", 64)
	changed, err := ComputerInputCommandDescriptor([]ComputerProfileDescriptor{foreign})
	if err != nil || (CapabilityCatalog{Commands: append(readOnly, changed)}).Validate() == nil {
		t.Fatal("input promoted foreign profile authority", err)
	}
	for _, mutate := range []func(*CommandDescriptor){
		func(value *CommandDescriptor) { value.Risk = RiskRead },
		func(value *CommandDescriptor) { value.SupportsCancel = false },
		func(value *CommandDescriptor) { value.SupportsProgress = true },
		func(value *CommandDescriptor) { value.ModelContract = &CommandModelContract{} },
	} {
		changed := input
		mutate(&changed)
		if changed.Validate() == nil {
			t.Fatal("forged input behavior admitted")
		}
	}
}

func TestComputerInputStrictSchemasAndProtectedReceipt(t *testing.T) {
	descriptor, err := ComputerInputCommandDescriptor([]ComputerProfileDescriptor{computerTestProfile()})
	if err != nil {
		t.Fatal(err)
	}
	input, _, err := NewComputerActInputs(computerInputFixture(), "workspace")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(input)
	var object map[string]any
	_ = json.Unmarshal(encoded, &object)
	if err = validateDescriptorInvocationInput(descriptor, object); err != nil {
		t.Fatal("canonical metadata rejected", err)
	}
	for _, field := range []string{"text", "Text", "ephemeral_input", "argv", "pid"} {
		object[field] = "forbidden"
		if validateDescriptorInvocationInput(descriptor, object) == nil {
			t.Fatal("unknown or protected field admitted", field)
		}
		delete(object, field)
		request := object["request"].(map[string]any)
		request[field] = "forbidden"
		if validateDescriptorInvocationInput(descriptor, object) == nil {
			t.Fatal("nested unknown or protected field admitted", field)
		}
		delete(request, field)
	}
	for _, raw := range []json.RawMessage{
		mustJSON(ComputerActResult{ActionID: input.Request.ID, Observation: computerTestObservation()}),
		json.RawMessage(`{"protected_result":true}`),
	} {
		if _, err = ValidateInvocationOutput(descriptor, raw, MaxInvocationOutput); err != nil {
			t.Fatal("live evidence/protected receipt rejected", err)
		}
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"protected_result":false}`),
		json.RawMessage(`{"protected_result":true,"text":"private"}`),
		mustJSON(ComputerActResult{ActionID: "bad\nidentifier", Observation: computerTestObservation()}),
	} {
		if _, err = ValidateInvocationOutput(descriptor, raw, MaxInvocationOutput); err == nil {
			t.Fatal("forged post-action output admitted")
		}
	}
}

func TestComputerInputDispatchUsesProtectedEnvelope(t *testing.T) {
	descriptor, err := ComputerInputCommandDescriptor([]ComputerProfileDescriptor{computerTestProfile()})
	if err != nil {
		t.Fatal(err)
	}
	input, ephemeral, err := NewComputerActInputs(computerInputFixture(), "workspace")
	if err != nil {
		t.Fatal(err)
	}
	request := computerInputPlan(t, input).InvocationRequest
	request.CatalogHash = strings.Repeat("c", 64)
	request.IdempotencyKey, request.TimeoutSeconds, request.OutputLimitBytes = "input-idem", 5, 4096
	plan, err := PrepareExecutionPlan(request, descriptor, "companion", "policy", time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(ephemeral)
	if (InvocationDispatch{Plan: plan, EphemeralInput: raw}).Validate() != nil {
		t.Fatal("protected input transport rejected")
	}
	if (InvocationDispatch{Plan: plan, EphemeralInput: make([]byte, MaxComputerEphemeralInputBytes+1)}).Validate() == nil {
		t.Fatal("unbounded protected envelope admitted")
	}
}

func computerInputFixture() computer.InputRequest {
	observation := computerTestObservation()
	return computer.InputRequest{
		ID: "act_1", Binding: observation.Session.Binding, ObservationID: observation.Observation.ID,
		Action: computer.InputAction{
			Kind: computer.InputInsertText, Text: &computer.InputTextSpec{ElementRef: "field_1"},
		},
		Text: []byte(computerInputCanary),
	}
}

func computerInputPlan(t *testing.T, input ComputerActInput) ExecutionPlan {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	principal := input.Request.Binding.Principal
	return ExecutionPlan{InvocationRequest: InvocationRequest{
		InvocationID: input.Request.ID, Command: ComputerCommandAct, NodeID: ID(input.Request.Binding.Target.Node),
		ActorID: principal.ActorID, AgentID: principal.AgentID, SessionID: principal.SessionID, Input: raw,
	}}
}

func TestComputerProtectedInputRoundTripAndMetadataPrivacy(t *testing.T) {
	request := computerInputFixture()
	input, ephemeral, err := NewComputerActInputs(request, "workspace")
	if err != nil || ephemeral == nil {
		t.Fatal("typed protected input preparation", err)
	}
	encoded, err := json.Marshal(input)
	if err != nil || strings.Contains(string(encoded), computerInputCanary) ||
		strings.Contains(string(encoded), base64.StdEncoding.EncodeToString(request.Text)) {
		t.Fatal("plaintext or base64 text escaped into durable input metadata", err)
	}
	for _, formatted := range []string{fmt.Sprint(*ephemeral), fmt.Sprintf("%#v", *ephemeral)} {
		if strings.Contains(formatted, computerInputCanary) {
			t.Fatal("ordinary ephemeral diagnostic exposed text")
		}
	}
	raw, _ := json.Marshal(ephemeral)
	bound, err := BindComputerAct(computerInputPlan(t, input), raw)
	if err != nil || string(bound.Text) != computerInputCanary {
		t.Fatal("literal protected Unicode round trip changed text", err)
	}
	request.Text[0] = '!'
	request.Action.Text.ElementRef = "changed_by_caller"
	if input.Request.Action.Text.ElementRef != "field_1" || ephemeral.Text != computerInputCanary {
		t.Fatal("prepared input retained caller-owned mutable metadata/text")
	}
	if _, err = input.Bind(nil); err == nil {
		t.Fatal("durable metadata alone became an executable text request")
	}
}

func TestComputerInputCommitmentBindsFullMetadataAndLiteralText(t *testing.T) {
	input, ephemeral, err := NewComputerActInputs(computerInputFixture(), "workspace")
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ComputerActInput){
		"action id":             func(value *ComputerActInput) { value.Request.ID = "other_action" },
		"observation":           func(value *ComputerActInput) { value.Request.ObservationID = "other_observation" },
		"actor":                 func(value *ComputerActInput) { value.Request.Binding.Principal.ActorID = "other_actor" },
		"agent":                 func(value *ComputerActInput) { value.Request.Binding.Principal.AgentID = "other_agent" },
		"thread":                func(value *ComputerActInput) { value.Request.Binding.Principal.SessionID = "other_thread" },
		"execution":             func(value *ComputerActInput) { value.Request.Binding.Principal.ExecutionID = "other_turn" },
		"session":               func(value *ComputerActInput) { value.Request.Binding.SessionID = "other_session" },
		"worker":                func(value *ComputerActInput) { value.Request.Binding.WorkerID = "other_worker" },
		"policy":                func(value *ComputerActInput) { value.Request.Binding.PolicyRevision = strings.Repeat("b", 64) },
		"control generation":    func(value *ComputerActInput) { value.Request.Binding.Generation++ },
		"node":                  func(value *ComputerActInput) { value.Request.Binding.Target.Node = "node_other" },
		"display":               func(value *ComputerActInput) { value.Request.Binding.Target.Scope.DisplayID = "other_display" },
		"window":                func(value *ComputerActInput) { value.Request.Binding.Target.Scope.WindowID = "other_window" },
		"window generation":     func(value *ComputerActInput) { value.Request.Binding.Target.Scope.WindowGeneration++ },
		"element":               func(value *ComputerActInput) { value.Request.Action.Text.ElementRef = "other_field" },
		"insert to replacement": func(value *ComputerActInput) { value.Request.Action.Kind = computer.InputReplaceText },
		"workspace":             func(value *ComputerActInput) { value.WorkspaceID = "other_workspace" },
		"byte length":           func(value *ComputerActInput) { value.TextBytes++ },
	} {
		t.Run(name, func(t *testing.T) {
			encoded, _ := json.Marshal(input)
			var changed ComputerActInput
			_ = json.Unmarshal(encoded, &changed)
			mutate(&changed)
			if _, err := changed.Bind(ephemeral); err == nil {
				t.Fatal("protected payload could be transplanted to changed authority")
			}
		})
	}
	changed := *ephemeral
	changed.Text = strings.Replace(changed.Text, "literal", "altered", 1)
	if _, err = input.Bind(&changed); err == nil {
		t.Fatal("same-length changed plaintext matched the commitment")
	}
}

func TestComputerInputWireRejectsUnboundOrMalformedEphemeralBytes(t *testing.T) {
	input, ephemeral, err := NewComputerActInputs(computerInputFixture(), "workspace")
	if err != nil {
		t.Fatal(err)
	}
	plan := computerInputPlan(t, input)
	raw, _ := json.Marshal(ephemeral)
	for _, malformed := range []json.RawMessage{
		nil, []byte(`null`), []byte(`{}`), []byte(`[]`), []byte(`{"text":null}`),
		[]byte(`{"Text":"value"}`), []byte(`{"text":"first","text":"second"}`),
		[]byte(`{"text":"value","unknown":true}`), []byte(`{"text":12}`),
		[]byte("{\"text\":\"\xff\"}"), []byte(`{"text":"\u0000"}`),
		[]byte(`{"text":"` + strings.Repeat("x", MaxComputerEphemeralInputBytes) + `"}`),
	} {
		if _, err = BindComputerAct(plan, malformed); err == nil || strings.Contains(err.Error(), computerInputCanary) {
			t.Fatal("invalid payload was accepted or leaked protected text")
		}
	}
	for _, mutate := range []func(*ExecutionPlan){
		func(value *ExecutionPlan) { value.Command = ComputerCommandObserve },
		func(value *ExecutionPlan) { value.ActorID = "other_actor" },
		func(value *ExecutionPlan) { value.AgentID = "other_agent" },
		func(value *ExecutionPlan) { value.SessionID = "other_thread" },
		func(value *ExecutionPlan) { value.InvocationID = "other_invocation" },
		func(value *ExecutionPlan) { value.NodeID = "node_other" },
		func(value *ExecutionPlan) {
			value.Input = append([]byte(`{"request":null,`), value.Input[1:]...)
		},
	} {
		changed := plan
		mutate(&changed)
		if _, err = BindComputerAct(changed, raw); err == nil {
			t.Fatal("payload admitted outside its authenticated invocation owner")
		}
	}
}

func TestComputerInputEmptyReplacementAndNontextAreDistinct(t *testing.T) {
	request := computerInputFixture()
	request.Action.Kind = computer.InputReplaceText
	request.Text = []byte{}
	input, ephemeral, err := NewComputerActInputs(request, "workspace")
	if err != nil || ephemeral == nil || input.TextBytes != 0 || input.TextDigest == "" {
		t.Fatal("explicit empty replacement was lost", err)
	}
	bound, err := input.Bind(ephemeral)
	if err != nil || bound.Text == nil || len(bound.Text) != 0 {
		t.Fatal("empty replacement became missing payload", err)
	}
	if _, err = input.Bind(nil); err == nil {
		t.Fatal("missing payload was accepted as an empty replacement")
	}
	request.Action = computer.InputAction{Kind: computer.InputKey, Key: &computer.InputKeySpec{Name: "escape"}}
	request.Text = nil
	input, ephemeral, err = NewComputerActInputs(request, "workspace")
	if err != nil || ephemeral != nil || input.TextDigest != "" || input.TextBytes != 0 {
		t.Fatal("nontext action fabricated a protected text envelope", err)
	}
	if _, err = input.Bind(&ComputerActEphemeralInput{}); err == nil {
		t.Fatal("nontext action admitted an otherwise empty text payload")
	}
}

func TestComputerInputEscapedTransportLimitDoesNotWeakenDecodedLimit(t *testing.T) {
	request := computerInputFixture()
	request.Text = []byte(strings.Repeat("\x01", computer.MaxInputTextBytes))
	input, ephemeral, err := NewComputerActInputs(request, "workspace")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(ephemeral)
	if len(raw) <= computer.MaxInputTextBytes || len(raw) > MaxComputerEphemeralInputBytes {
		t.Fatal("escaped wire bound is not distinct from the UTF-8 bound")
	}
	if _, err = BindComputerAct(computerInputPlan(t, input), raw); err != nil {
		t.Fatal("fully escaped maximum bounded payload rejected", err)
	}
	request.Text = append(request.Text, 'x')
	if _, _, err = NewComputerActInputs(request, "workspace"); err == nil {
		t.Fatal("decoded input limit was relaxed to the larger JSON wire limit")
	}
}
