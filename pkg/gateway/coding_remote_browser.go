package gateway

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"

	codingremote "github.com/bogdanovich/mintclaw/pkg/coding/remote"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const maxCodingRemoteBrowserInvocations = 128

type codingRemoteBrowserInvocationStore struct {
	mu      sync.Mutex
	records map[string]codingRemoteBrowserInvocation
}

type codingRemoteBrowserInvocation struct {
	request codingremote.Request
	result  codingremote.CapabilityResult
}

type codingRemoteBrowserReservation uint8

const (
	codingRemoteBrowserReservationDenied codingRemoteBrowserReservation = iota
	codingRemoteBrowserReservationClaimed
	codingRemoteBrowserReservationExisting
	codingRemoteBrowserReservationFull
)

func newCodingRemoteBrowserInvocationStore() *codingRemoteBrowserInvocationStore {
	return &codingRemoteBrowserInvocationStore{
		records: make(map[string]codingRemoteBrowserInvocation),
	}
}

// reserve claims an invocation identity before the browser operation starts.
// Existing callers only observe the running or terminal receipt and can never
// dispatch the same invocation again. Capacity is fail-closed: records are not
// evicted because making an old invocation ID look new would permit replay.
func (store *codingRemoteBrowserInvocationStore) reserve(
	request codingremote.Request,
	initial codingremote.CapabilityResult,
) (codingremote.CapabilityResult, codingRemoteBrowserReservation) {
	if store == nil || request.Principal == nil || request.Operation != codingremote.OperationCapabilityInvoke ||
		initial.Validate() != nil {
		return codingremote.CapabilityResult{}, codingRemoteBrowserReservationDenied
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, found := store.records[request.InvocationID]; found {
		if !codingRemoteBrowserInvocationMatches(existing, request) {
			return codingremote.CapabilityResult{}, codingRemoteBrowserReservationDenied
		}
		return codingRemoteBrowserProjectedResult(existing, request), codingRemoteBrowserReservationExisting
	}
	if len(store.records) >= maxCodingRemoteBrowserInvocations {
		return codingremote.CapabilityResult{}, codingRemoteBrowserReservationFull
	}
	request.Arguments = nil
	record := codingRemoteBrowserInvocation{request: request, result: initial}
	store.records[request.InvocationID] = record
	return codingRemoteBrowserProjectedResult(record, request), codingRemoteBrowserReservationClaimed
}

func (store *codingRemoteBrowserInvocationStore) complete(
	request codingremote.Request,
	result codingremote.CapabilityResult,
) bool {
	if store == nil || request.Principal == nil || result.Validate() != nil {
		return false
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	existing, found := store.records[request.InvocationID]
	if !found || !codingRemoteBrowserInvocationMatches(existing, request) {
		return false
	}
	if existing.result.State != "running" {
		return reflect.DeepEqual(existing.result, result)
	}
	existing.result = result
	store.records[request.InvocationID] = existing
	return true
}

func (store *codingRemoteBrowserInvocationStore) lookup(
	request codingremote.Request,
) (codingremote.CapabilityResult, bool, bool) {
	if store == nil || request.Principal == nil {
		return codingremote.CapabilityResult{}, false, false
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	record, found := store.records[request.InvocationID]
	if !found {
		return codingremote.CapabilityResult{}, false, false
	}
	if !codingRemoteBrowserInvocationMatches(record, request) {
		return codingremote.CapabilityResult{}, true, false
	}
	result := codingRemoteBrowserProjectedResult(record, request)
	return result, true, result.Validate() == nil
}

func codingRemoteBrowserProjectedResult(
	record codingRemoteBrowserInvocation,
	request codingremote.Request,
) codingremote.CapabilityResult {
	result := record.result
	result.Grant = request.Grant
	result.GrantRevision = request.GrantRevision
	result.DiscoveryRevision = request.DiscoveryRevision
	result.Capability = request.Capability
	result.InvocationID = request.InvocationID
	if request.CapabilityOperation != codingremote.BrowserReceiptRecoveryOperation {
		result.CapabilityRevision = request.CapabilityRevision
		result.Operation = request.CapabilityOperation
	}
	return result
}

func codingRemoteBrowserInvocationMatches(
	record codingRemoteBrowserInvocation,
	request codingremote.Request,
) bool {
	if record.request.Principal == nil || request.Principal == nil {
		return false
	}
	if record.request.Grant != request.Grant ||
		record.request.GrantRevision != request.GrantRevision ||
		record.request.ThreadID != request.ThreadID || record.request.SessionKey != request.SessionKey ||
		record.request.ProjectKey != request.ProjectKey || record.request.LocalProfile != request.LocalProfile ||
		record.request.Capability != request.Capability ||
		record.request.InvocationID != request.InvocationID ||
		record.request.Principal.Runtime != request.Principal.Runtime ||
		record.request.Principal.ActorID != request.Principal.ActorID ||
		record.request.Principal.AgentID != request.Principal.AgentID ||
		record.request.Principal.SessionID != request.Principal.SessionID {
		return false
	}
	if request.Operation == codingremote.OperationInvocationStatus &&
		request.CapabilityOperation == codingremote.BrowserReceiptRecoveryOperation {
		return true
	}
	return record.request.Capability == request.Capability &&
		record.request.CapabilityRevision == request.CapabilityRevision &&
		record.request.CapabilityOperation == request.CapabilityOperation &&
		record.request.InvocationID == request.InvocationID
}

func codingRemoteBrowserRunningResult(
	request codingremote.Request,
	capability codingremote.CapabilityDescriptor,
	operation codingremote.OperationDescriptor,
) codingremote.CapabilityResult {
	return codingremote.CapabilityResult{
		Grant: request.Grant, GrantRevision: request.GrantRevision,
		DiscoveryRevision: request.DiscoveryRevision,
		Capability:        request.Capability, CapabilityRevision: request.CapabilityRevision,
		Operation: request.CapabilityOperation, InvocationID: request.InvocationID,
		Target: capability.Target, Risk: operation.Risk, State: "running",
		RecoveryAction: "Call remote_capability status with this invocation_id; do not replay the operation.",
	}
}

func codingRemoteBrowserUncertainResult(
	request codingremote.Request,
	capability codingremote.CapabilityDescriptor,
	operation codingremote.OperationDescriptor,
) codingremote.CapabilityResult {
	result := codingRemoteBrowserRunningResult(request, capability, operation)
	result.State = "unknown"
	result.ErrorCode = "INVOCATION_UNCERTAIN"
	return result
}

func codingRemoteBrowserInvokeResult(
	request codingremote.Request,
	capability codingremote.CapabilityDescriptor,
	operation codingremote.OperationDescriptor,
	toolResult *toolshared.ToolResult,
) (codingremote.CapabilityResult, error) {
	if toolResult == nil {
		return codingremote.CapabilityResult{}, tools.ErrRemoteBrowserUnavailable
	}
	raw := json.RawMessage(toolResult.ContentForLLM())
	if len(raw) == 0 || !json.Valid(raw) || len(raw) > codingremote.MaxResultBytes {
		return codingremote.CapabilityResult{}, tools.ErrRemoteBrowserUnavailable
	}
	state := "succeeded"
	errorCode := ""
	if toolResult.IsError {
		state = "failed"
		var failure struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(raw, &failure) == nil {
			errorCode = codingRemoteBrowserErrorCode(failure.Code)
		}
		if errorCode == "" {
			errorCode = "BROWSER_OPERATION_FAILED"
		}
	}
	result := codingremote.CapabilityResult{
		Grant: request.Grant, GrantRevision: request.GrantRevision,
		DiscoveryRevision: request.DiscoveryRevision,
		Capability:        request.Capability, CapabilityRevision: request.CapabilityRevision,
		Operation: request.CapabilityOperation, InvocationID: request.InvocationID,
		Target: capability.Target, Risk: operation.Risk, State: state,
		Result: append(json.RawMessage(nil), raw...), ErrorCode: errorCode,
	}
	if err := result.Validate(); err != nil {
		return codingremote.CapabilityResult{}, err
	}
	return result, nil
}

func codingRemoteBrowserErrorCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" || len(code) > 128 {
		return ""
	}
	for index, character := range code {
		if index == 0 && (character < 'A' || character > 'Z') {
			return ""
		}
		upper := character >= 'A' && character <= 'Z'
		digit := character >= '0' && character <= '9'
		if index > 0 && !upper && !digit && character != '_' {
			return ""
		}
	}
	return code
}
