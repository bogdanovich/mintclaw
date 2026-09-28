package gateway

import (
	"bytes"
	"encoding/json"
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
	order   []string
}

type codingRemoteBrowserInvocation struct {
	request codingremote.Request
	result  codingremote.CapabilityResult
}

func newCodingRemoteBrowserInvocationStore() *codingRemoteBrowserInvocationStore {
	return &codingRemoteBrowserInvocationStore{
		records: make(map[string]codingRemoteBrowserInvocation),
	}
}

func (store *codingRemoteBrowserInvocationStore) retain(
	request codingremote.Request,
	result codingremote.CapabilityResult,
) bool {
	if store == nil || request.Principal == nil || result.Validate() != nil {
		return false
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, found := store.records[request.InvocationID]; found {
		return codingRemoteBrowserInvocationMatches(existing, request) &&
			bytes.Equal(existing.result.Result, result.Result) &&
			existing.result.State == result.State && existing.result.ErrorCode == result.ErrorCode
	}
	for len(store.order) >= maxCodingRemoteBrowserInvocations {
		oldest := store.order[0]
		store.order = store.order[1:]
		delete(store.records, oldest)
	}
	request.Arguments = nil
	store.records[request.InvocationID] = codingRemoteBrowserInvocation{request: request, result: result}
	store.order = append(store.order, request.InvocationID)
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
	result := record.result
	result.Grant = request.Grant
	result.GrantRevision = request.GrantRevision
	result.DiscoveryRevision = request.DiscoveryRevision
	result.Capability = request.Capability
	result.CapabilityRevision = request.CapabilityRevision
	result.Operation = request.CapabilityOperation
	result.InvocationID = request.InvocationID
	return result, true, result.Validate() == nil
}

func codingRemoteBrowserInvocationMatches(
	record codingRemoteBrowserInvocation,
	request codingremote.Request,
) bool {
	if record.request.Principal == nil || request.Principal == nil {
		return false
	}
	return record.request.Grant == request.Grant &&
		record.request.GrantRevision == request.GrantRevision &&
		record.request.ThreadID == request.ThreadID && record.request.SessionKey == request.SessionKey &&
		record.request.ProjectKey == request.ProjectKey && record.request.LocalProfile == request.LocalProfile &&
		record.request.Capability == request.Capability &&
		record.request.CapabilityRevision == request.CapabilityRevision &&
		record.request.CapabilityOperation == request.CapabilityOperation &&
		record.request.InvocationID == request.InvocationID &&
		record.request.Principal.Runtime == request.Principal.Runtime &&
		record.request.Principal.ActorID == request.Principal.ActorID &&
		record.request.Principal.AgentID == request.Principal.AgentID &&
		record.request.Principal.SessionID == request.Principal.SessionID
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
