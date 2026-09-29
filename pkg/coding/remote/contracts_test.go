package remote

import (
	"encoding/base64"
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

func TestArtifactRequestAndResponseAreStrictAndRangeBound(t *testing.T) {
	request := validInvocationRequest()
	request.Operation = OperationArtifactDescribe
	request.CapabilityOperation = "workspace_exec"
	request.Arguments = nil
	request.InvocationID = "remote_capability_job_start"
	request.ArtifactRef = "jobart_0123456789abcdef0123456789abcdef"
	if err := request.Validate(); err != nil {
		t.Fatalf("describe Validate() error = %v", err)
	}
	browser := request
	browser.CapabilityOperation = "browser_capture"
	browser.ArtifactRef = "transfer-artifact://capture_0123456789abcdef"
	if err := browser.Validate(); err != nil {
		t.Fatalf("browser describe Validate() error = %v", err)
	}
	wrongWorkspaceRef := request
	wrongWorkspaceRef.ArtifactRef = browser.ArtifactRef
	if err := wrongWorkspaceRef.Validate(); err == nil {
		t.Fatal("Validate() accepted a browser artifact reference for workspace execution")
	}
	wrongBrowserRef := browser
	wrongBrowserRef.ArtifactRef = request.ArtifactRef
	if err := wrongBrowserRef.Validate(); err == nil {
		t.Fatal("Validate() accepted a workspace artifact reference for browser capture")
	}
	badBrowser := browser
	badBrowser.ArtifactRef = "transfer-artifact:///private/path"
	if err := badBrowser.Validate(); err == nil {
		t.Fatal("Validate() accepted a malformed browser artifact reference")
	}
	fetch := request
	fetch.Operation = OperationArtifactFetch
	fetch.Offset = 4
	fetch.LimitBytes = 3
	if err := fetch.Validate(); err != nil {
		t.Fatalf("fetch Validate() error = %v", err)
	}
	oversized := fetch
	oversized.LimitBytes = MaxArtifactChunkBytes + 1
	if err := oversized.Validate(); err == nil {
		t.Fatal("Validate() accepted an oversized artifact range")
	}
	described := ArtifactResult{
		Grant: request.Grant, GrantRevision: request.GrantRevision,
		DiscoveryRevision: request.DiscoveryRevision,
		Capability:        request.Capability, CapabilityRevision: request.CapabilityRevision,
		InvocationID: request.InvocationID, Target: "laptop", ArtifactRef: request.ArtifactRef,
		Name: "result.txt", State: "available", Size: 7, SHA256: strings.Repeat("a", 64),
		ContentType: "text/plain",
	}
	if err := described.Validate(); err != nil {
		t.Fatalf("description Validate() error = %v", err)
	}
	browserDescription := described
	browserDescription.ArtifactRef = browser.ArtifactRef
	if err := browserDescription.Validate(); err != nil {
		t.Fatalf("browser description Validate() error = %v", err)
	}
	chunk := described
	chunk.Offset = 4
	chunk.NextOffset = 7
	chunk.EOF = true
	chunk.DataBase64 = base64.StdEncoding.EncodeToString([]byte("end"))
	if err := chunk.Validate(); err != nil {
		t.Fatalf("chunk Validate() error = %v", err)
	}
	response := Response{
		Schema: SchemaV1, RequestID: request.RequestID, Status: ResponseOK, Artifact: &chunk,
	}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeResponse(raw); err != nil {
		t.Fatalf("DecodeResponse() error = %v", err)
	}
	malformed := chunk
	malformed.NextOffset--
	if err = malformed.Validate(); err == nil {
		t.Fatal("Validate() accepted an artifact chunk with a mismatched range")
	}
	response.Result = &CapabilityResult{}
	if err = response.Validate(); err == nil {
		t.Fatal("Validate() accepted multiple successful payloads")
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

func TestCodingTaskRequestsBindStartAndQuestionAuthority(t *testing.T) {
	start := validTaskStartRequest()
	if err := start.Validate(); err != nil {
		t.Fatalf("start Validate() error = %v", err)
	}
	changedObjective := start
	changedObjective.TaskObjective = "Inspect a different failure."
	if got := DeriveTaskID(changedObjective); got != start.TaskID {
		t.Fatalf("changed objective task ID = %q, want retained %q", got, start.TaskID)
	}
	for name, mutate := range map[string]func(*Request){
		"scope":    func(value *Request) { value.TaskScope = "other-project" },
		"revision": func(value *Request) { value.TaskScopeRevision = "scope-v2" },
		"profile":  func(value *Request) { value.TaskProfile = codingscope.ProfileProjectYolo },
		"call":     func(value *Request) { value.CallID = "call_2" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := start
			mutate(&candidate)
			if got := DeriveTaskID(candidate); got == start.TaskID {
				t.Fatalf("authority mutation retained task ID %q", got)
			}
			if err := candidate.Validate(); err == nil {
				t.Fatal("Validate() accepted a task ID derived from different authority")
			}
		})
	}

	answer := validTaskControlRequest(OperationTaskAnswer, start)
	answer.TaskText = "Inspect AGENTS.md"
	answer.TaskQuestionID = "question_1"
	answer.TaskQuestionRevision = 2
	answer.TaskAnswerID = DeriveTaskAnswerID(answer)
	if err := answer.Validate(); err != nil {
		t.Fatalf("answer Validate() error = %v", err)
	}
	changedQuestion := answer
	changedQuestion.TaskQuestionRevision++
	if err := changedQuestion.Validate(); err == nil {
		t.Fatal("Validate() accepted an answer ID bound to an old question revision")
	}
}

func TestCodingTaskRequestOperationsAreStrict(t *testing.T) {
	start := validTaskStartRequest()
	status := validTaskControlRequest(OperationTaskStatus, start)
	if err := status.Validate(); err != nil {
		t.Fatalf("status Validate() error = %v", err)
	}
	cancel := validTaskControlRequest(OperationTaskCancel, start)
	if err := cancel.Validate(); err != nil {
		t.Fatalf("cancel Validate() error = %v", err)
	}
	steer := validTaskControlRequest(OperationTaskSteer, start)
	steer.TaskText = "Continue with the focused validation."
	if err := steer.Validate(); err != nil {
		t.Fatalf("steer Validate() error = %v", err)
	}

	withCapability := status
	withCapability.Capability = "build-workspace"
	if err := withCapability.Validate(); err == nil {
		t.Fatal("Validate() accepted capability fields on a coding task request")
	}
	withObjective := status
	withObjective.TaskObjective = "start another task"
	if err := withObjective.Validate(); err == nil {
		t.Fatal("Validate() accepted start fields on status")
	}
	emptySteer := steer
	emptySteer.TaskText = ""
	if err := emptySteer.Validate(); err == nil {
		t.Fatal("Validate() accepted empty steer text")
	}
}

func TestCodingTaskResultIsBoundedAndContainsNoPrivatePlacement(t *testing.T) {
	result := validTaskResult()
	response := Response{Schema: SchemaV1, RequestID: "request-task", Status: ResponseOK, Task: &result}
	if err := response.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"node_scope", "workspace_path", "channel", "credential", "transcript"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("task response exposed %q: %s", forbidden, raw)
		}
	}
	oversized := result
	oversized.TerminalSummary = strings.Repeat("x", MaxTaskTerminalBytes+1)
	if err := oversized.Validate(); err == nil {
		t.Fatal("Validate() accepted oversized terminal summary")
	}
	duplicate := result
	duplicate.Question.Options = append(duplicate.Question.Options, duplicate.Question.Options[0])
	if err := duplicate.Validate(); err == nil {
		t.Fatal("Validate() accepted duplicate question option IDs")
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
	jobStart := result
	jobStart.Operation = "workspace_exec"
	jobStart.JobInvocationID = jobStart.InvocationID
	if err := jobStart.Validate(); err != nil {
		t.Fatalf("Validate() rejected a bound job invocation reference: %v", err)
	}
	jobStart.JobInvocationID = "another_invocation"
	if err := jobStart.Validate(); err == nil {
		t.Fatal("Validate() accepted a mismatched job start invocation reference")
	}
	nonJob := result
	nonJob.JobInvocationID = nonJob.InvocationID
	if err := nonJob.Validate(); err == nil {
		t.Fatal("Validate() accepted a job invocation reference on a non-job operation")
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

func validTaskStartRequest() Request {
	request := validInvocationRequest()
	request.RequestID = "request-task-start"
	request.Operation = OperationTaskStart
	request.Capability = ""
	request.CapabilityRevision = ""
	request.CapabilityOperation = ""
	request.Arguments = nil
	request.InvocationID = ""
	request.TaskScope = "mintclaw-dev"
	request.TaskScopeRevision = "scope-v1"
	request.TaskProfile = codingscope.ProfileInvestigate
	request.TaskObjective = "Inspect the repository failure."
	request.TaskDoneCriteria = "Return the root cause without changing files."
	request.TaskID = DeriveTaskID(request)
	return request
}

func validTaskControlRequest(operation Operation, start Request) Request {
	return Request{
		Schema: SchemaV1, RequestID: "request-task-control", Operation: operation,
		Grant: start.Grant, GrantRevision: start.GrantRevision,
		ThreadID: start.ThreadID, SessionKey: start.SessionKey,
		ProjectKey: start.ProjectKey, LocalProfile: start.LocalProfile,
		Principal: start.Principal, CallID: "control_call",
		DiscoveryRevision: start.DiscoveryRevision,
		DeadlineUnixMS:    time.Now().Add(time.Minute).UnixMilli(),
		TaskID:            start.TaskID, TaskScope: start.TaskScope,
		TaskScopeRevision: start.TaskScopeRevision, TaskProfile: start.TaskProfile,
	}
}

func validTaskResult() TaskResult {
	return TaskResult{
		Grant: "local-development", GrantRevision: "grant-v1", DiscoveryRevision: "discovery-v1",
		TaskID: "coding-0123456789abcdef", GenerationID: uuid.NewString(),
		Scope: "mintclaw-dev", Target: "laptop", Profile: codingscope.ProfileInvestigate,
		Status: "running", NodeState: "waiting_for_input", ThreadID: uuid.NewString(),
		WorkerGenerationID: uuid.NewString(), Activity: "waiting_for_input",
		Progress: "coding task is waiting for correlated user input",
		Question: &TaskQuestion{
			ID: "question_1", Revision: 2, Prompt: "Which file should I inspect?",
			Options: []TaskQuestionOption{
				{ID: "agents", Label: "AGENTS.md", Description: "Inspect agent instructions."},
				{ID: "readme", Label: "README.md", Description: "Inspect the project overview."},
			},
		},
	}
}
