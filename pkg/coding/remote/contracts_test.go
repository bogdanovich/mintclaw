package remote

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	codingscope "github.com/bogdanovich/mintclaw/pkg/coding/scope"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
)

func TestRequestValidateAndStrictDecode(t *testing.T) {
	threadID := uuid.NewString()
	request := Request{
		Schema: SchemaV1, RequestID: "request-one", Operation: OperationCapabilitiesList,
		Grant: "local-development", GrantRevision: "grant-v1", ThreadID: threadID,
		SessionKey: "coding:" + threadID,
		ProjectKey: "git_worktree:" + strings.Repeat("a", 64), LocalProfile: codingscope.ProfileMutate,
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRequest(raw)
	if err != nil || !reflect.DeepEqual(decoded, request) {
		t.Fatalf("DecodeRequest() = %#v, %v", decoded, err)
	}

	withUnknown := append(raw[:len(raw)-1], []byte(`,"node_id":"private"}`)...)
	if _, err = DecodeRequest(withUnknown); err == nil {
		t.Fatal("DecodeRequest() accepted an unknown field")
	}
	if _, err = DecodeRequest(append(raw, raw...)); err == nil {
		t.Fatal("DecodeRequest() accepted a trailing value")
	}
	if _, err = DecodeRequest(make([]byte, MaxFrameBytes+1)); err == nil {
		t.Fatal("DecodeRequest() accepted an oversized frame")
	}
	request.DeadlineUnixMS = 1
	if err = request.Validate(); err == nil {
		t.Fatal("Validate() accepted an execution deadline on discovery")
	}
}

func TestExecutionRequestRequiresTurnBoundPrincipalAndClosedPayload(t *testing.T) {
	request := validInvocationRequest()
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*Request)
	}{
		{name: "principal missing", mutate: func(value *Request) { value.Principal = nil }},
		{name: "principal runtime", mutate: func(value *Request) {
			value.Principal.Runtime = runtimecap.KindGateway
		}},
		{name: "principal session", mutate: func(value *Request) {
			value.Principal.SessionID = "coding:other"
		}},
		{name: "call", mutate: func(value *Request) { value.CallID = "model supplied call" }},
		{name: "discovery", mutate: func(value *Request) { value.DiscoveryRevision = "" }},
		{name: "capability", mutate: func(value *Request) { value.Capability = "*" }},
		{name: "operation", mutate: func(value *Request) { value.CapabilityOperation = "shell.exec.v1" }},
		{name: "arguments", mutate: func(value *Request) { value.Arguments = json.RawMessage(`[]`) }},
		{name: "invocation missing", mutate: func(value *Request) { value.InvocationID = "" }},
		{name: "invocation changed", mutate: func(value *Request) { value.InvocationID = "invocation_1" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := request
			principal := *request.Principal
			candidate.Principal = &principal
			candidate.Arguments = append(json.RawMessage(nil), request.Arguments...)
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatalf("Validate() accepted %#v", candidate)
			}
		})
	}

	status := request
	status.Operation = OperationInvocationStatus
	status.Arguments = nil
	status.InvocationID = "invocation_1"
	if err := status.Validate(); err != nil {
		t.Fatalf("status Validate() error = %v", err)
	}
	missingStatusOperation := status
	missingStatusOperation.CapabilityOperation = ""
	if err := missingStatusOperation.Validate(); err == nil {
		t.Fatal("Validate() accepted status without retained capability operation")
	}
	missingDeadline := request
	missingDeadline.DeadlineUnixMS = 0
	if err := missingDeadline.Validate(); err == nil {
		t.Fatal("Validate() accepted execution without a deadline")
	}
}

func TestInvocationIDIsStableAcrossRecoveryButSeparatesAuthority(t *testing.T) {
	request := validInvocationRequest()
	baseline := request.InvocationID
	continued := request
	continued.RequestID = "request-next"
	continued.DiscoveryRevision = "discovery-v2"
	continued.Arguments = json.RawMessage(`{"path":"other.md"}`)
	if got := DeriveInvocationID(continued); got != baseline {
		t.Fatalf("recovery invocation ID = %q, want %q", got, baseline)
	}
	for name, mutate := range map[string]func(*Request){
		"call":       func(value *Request) { value.CallID = "call_2" },
		"project":    func(value *Request) { value.ProjectKey = "directory:" + strings.Repeat("e", 64) },
		"grant":      func(value *Request) { value.GrantRevision = "grant-v2" },
		"capability": func(value *Request) { value.CapabilityRevision = "capability-v2" },
		"operation":  func(value *Request) { value.CapabilityOperation = "write_file" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := request
			mutate(&candidate)
			if got := DeriveInvocationID(candidate); got == baseline {
				t.Fatalf("authority mutation retained invocation ID %q", got)
			}
		})
	}
}

func TestRequestRejectsUnboundOrPrivilegedIdentity(t *testing.T) {
	threadID := uuid.NewString()
	valid := Request{
		Schema: SchemaV1, RequestID: "request-one", Operation: OperationCapabilitiesList,
		Grant: "local-development", GrantRevision: "grant-v1", ThreadID: threadID,
		SessionKey: "coding:" + threadID,
		ProjectKey: "directory:" + strings.Repeat("b", 64), LocalProfile: codingscope.ProfileInvestigate,
	}
	tests := []struct {
		name   string
		mutate func(*Request)
	}{
		{name: "schema", mutate: func(value *Request) { value.Schema = "legacy" }},
		{name: "operation", mutate: func(value *Request) { value.Operation = "gateway.config.read" }},
		{name: "grant", mutate: func(value *Request) { value.Grant = "*" }},
		{name: "thread", mutate: func(value *Request) { value.ThreadID = "thread" }},
		{name: "session", mutate: func(value *Request) { value.SessionKey = "coding:other" }},
		{name: "project", mutate: func(value *Request) { value.ProjectKey = "/private/repository" }},
		{name: "privileged profile", mutate: func(value *Request) {
			value.LocalProfile = codingscope.ProfileMachineYoloRoot
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatalf("Validate() accepted %#v", candidate)
			}
		})
	}
}

func TestCapabilitySnapshotValidation(t *testing.T) {
	snapshot := validSnapshot()
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	response := Response{Schema: SchemaV1, RequestID: "request-one", Status: ResponseOK, Snapshot: &snapshot}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeResponse(raw); err != nil {
		t.Fatalf("DecodeResponse() error = %v", err)
	}

	unsorted := validSnapshot()
	unsorted.Capabilities = append(unsorted.Capabilities, unsorted.Capabilities[0])
	unsorted.Capabilities[1].Alias = "a-capability"
	if err = unsorted.Validate(); err == nil {
		t.Fatal("Validate() accepted unsorted capabilities")
	}
	privileged := validSnapshot()
	privileged.Capabilities[0].Operations[0].Risk = "privileged"
	if err = privileged.Validate(); err == nil {
		t.Fatal("Validate() accepted privileged risk")
	}
	badSchema := validSnapshot()
	badSchema.Capabilities[0].Operations[0].InputSchema = json.RawMessage(`[]`)
	if err = badSchema.Validate(); err == nil {
		t.Fatal("Validate() accepted non-object input schema")
	}
}

func TestFailedResponseContainsNoSnapshot(t *testing.T) {
	valid := Response{
		Schema: SchemaV1, RequestID: "request-one", Status: ResponseDenied,
		Code: "GRANT_UNAVAILABLE", Message: "coding remote grant is unavailable",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	withSnapshot := valid
	snapshot := validSnapshot()
	withSnapshot.Snapshot = &snapshot
	if err := withSnapshot.Validate(); err == nil {
		t.Fatal("Validate() accepted failure snapshot")
	}
	withResult := valid
	result := validCapabilityResult()
	withResult.Result = &result
	if err := withResult.Validate(); err == nil {
		t.Fatal("Validate() accepted failure result")
	}
	missingCode := valid
	missingCode.Code = ""
	if err := missingCode.Validate(); err == nil {
		t.Fatal("Validate() accepted failure without code")
	}
	unsafeCode := valid
	unsafeCode.Code = "grant changed"
	if err := unsafeCode.Validate(); err == nil {
		t.Fatal("Validate() accepted an unstructured failure code")
	}
	unsafeMessage := valid
	unsafeMessage.Message = "denied\nprivate detail"
	if err := unsafeMessage.Validate(); err == nil {
		t.Fatal("Validate() accepted a control character in safe failure detail")
	}
}

func TestCapabilityResultValidation(t *testing.T) {
	result := validCapabilityResult()
	response := Response{Schema: SchemaV1, RequestID: "request-one", Status: ResponseOK, Result: &result}
	if err := response.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	unsorted := result
	unsorted.Changes = []ChangeReceipt{{Path: "z.txt", Action: "write"}, {Path: "a.txt", Action: "write"}}
	if err := unsorted.Validate(); err == nil {
		t.Fatal("Validate() accepted unsorted changes")
	}
	unsafe := result
	unsafe.RecoveryAction = "retry\nsecret"
	if err := unsafe.Validate(); err == nil {
		t.Fatal("Validate() accepted unsafe recovery action")
	}
	absolute := result
	absolute.Changes = []ChangeReceipt{{Path: "/private/result.md", Action: "write"}}
	if err := absolute.Validate(); err == nil {
		t.Fatal("Validate() accepted an absolute remote change path")
	}
}

func validSnapshot() CapabilitySnapshot {
	return CapabilitySnapshot{
		Schema: SchemaV1, Grant: "local-development", GrantRevision: "grant-v1",
		DiscoveryRevision: "discovery-v1", GeneratedAtUnixMS: 1,
		Capabilities: []CapabilityDescriptor{{
			Alias: "build-workspace", Revision: "capability-v1", Target: "laptop",
			Kind: CapabilityRemoteWorkspace, Availability: AvailabilityAvailable,
			Operations: []OperationDescriptor{{
				Alias: "read_file", Risk: RiskRead, InputSchema: json.RawMessage(`{"type":"object"}`),
				ResultKind: "json", SupportsCancel: true,
			}},
		}},
		TaskScopes: []TaskScopeDescriptor{{
			Alias: "mintclaw-dev", Revision: "scope-v1", Target: "laptop",
			Profiles:     []codingscope.Profile{codingscope.ProfileInvestigate, codingscope.ProfileMutate},
			Availability: AvailabilityAvailable,
		}},
	}
}

func validInvocationRequest() Request {
	threadID := uuid.NewString()
	sessionKey := "coding:" + threadID
	request := Request{
		Schema: SchemaV1, RequestID: "request-execute", Operation: OperationCapabilityInvoke,
		Grant: "local-development", GrantRevision: "grant-v1", ThreadID: threadID,
		SessionKey: sessionKey, ProjectKey: "git_worktree:" + strings.Repeat("d", 64),
		LocalProfile: codingscope.ProfileMutate,
		Principal: &runtimecap.Principal{
			Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
			SessionID: sessionKey, ExecutionID: "turn-execution-1",
		},
		CallID: "call_1", DiscoveryRevision: "discovery-v1",
		Capability: "build-workspace", CapabilityRevision: "capability-v1",
		CapabilityOperation: "read_file", Arguments: json.RawMessage(`{"path":"README.md"}`),
		DeadlineUnixMS: time.Now().Add(time.Minute).UnixMilli(),
	}
	request.InvocationID = DeriveInvocationID(request)
	return request
}

func validCapabilityResult() CapabilityResult {
	return CapabilityResult{
		Grant: "local-development", GrantRevision: "grant-v1", DiscoveryRevision: "discovery-v1",
		Capability: "build-workspace", CapabilityRevision: "capability-v1", Operation: "write_file",
		InvocationID: "invocation_1", Target: "laptop", Risk: RiskWrite, State: "succeeded",
		Result:  json.RawMessage(`{"path":"README.md"}`),
		Changes: []ChangeReceipt{{Path: "README.md", Action: "write"}},
	}
}
