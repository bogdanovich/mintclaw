package remote

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	codingscope "github.com/bogdanovich/mintclaw/pkg/coding/scope"
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
	if err != nil || decoded != request {
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
