package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/routing"
)

const (
	liveEvidenceWait      = 5 * time.Second
	liveEvidenceStartSkew = time.Second
)

var liveEvidenceAlias = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

type liveExecutionEvidence struct {
	SchemaVersion string                 `json:"schema_version"`
	Status        string                 `json:"status"`
	Parent        liveTraceEvidence      `json:"parent"`
	Delegation    liveDelegationEvidence `json:"delegation"`
	Child         liveTraceEvidence      `json:"child"`
	SafeError     *liveEvidenceError     `json:"safe_error"`
}

type liveDelegationEvidence struct {
	AgentID  string `json:"agent_id"`
	Admitted int    `json:"admitted"`
}

type liveTraceEvidence struct {
	AgentID         string                       `json:"agent_id"`
	Outcome         string                       `json:"outcome"`
	Incomplete      bool                         `json:"incomplete"`
	ToolCalls       map[string]int               `json:"tool_calls"`
	ToolFailures    map[string]int               `json:"tool_failures"`
	UnpairedCalls   map[string]int               `json:"unpaired_calls"`
	BrowserSessions []liveBrowserSessionEvidence `json:"browser_sessions"`
}

type liveBrowserSessionEvidence struct {
	Operation string `json:"operation"`
	Target    string `json:"target,omitempty"`
	Profile   string `json:"profile,omitempty"`
	State     string `json:"state,omitempty"`
}

type liveEvidenceError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func unavailableLiveEvidence(agentID, code string) liveExecutionEvidence {
	return liveExecutionEvidence{
		SchemaVersion: "mintclaw.live_execution_evidence.v1",
		Status:        "unavailable",
		Parent:        emptyLiveTraceEvidence(""),
		Delegation:    liveDelegationEvidence{AgentID: agentID},
		Child:         emptyLiveTraceEvidence(agentID),
		SafeError: &liveEvidenceError{
			Code:    code,
			Message: "Verified live execution evidence is unavailable.",
		},
	}
}

func emptyLiveTraceEvidence(agentID string) liveTraceEvidence {
	return liveTraceEvidence{
		AgentID: agentID, ToolCalls: map[string]int{}, ToolFailures: map[string]int{},
		UnpairedCalls: map[string]int{}, BrowserSessions: []liveBrowserSessionEvidence{},
	}
}

func collectLiveExecutionEvidence(
	ctx context.Context,
	cfg *config.Config,
	rootScope runtimeevents.TraceScope,
	sessionKey string,
	expectedAgentID string,
	requestStarted time.Time,
) (liveExecutionEvidence, error) {
	expectedAgentID = routing.NormalizeAgentID(expectedAgentID)
	if cfg == nil || !cfg.Diagnostics.TraceCapture.Enabled {
		evidence := unavailableLiveEvidence(expectedAgentID, "trace_capture_disabled")
		return evidence, errors.New("live execution evidence requires diagnostic trace capture")
	}
	if !rootScope.Complete() || strings.TrimSpace(sessionKey) == "" || expectedAgentID == "" ||
		requestStarted.IsZero() {
		evidence := unavailableLiveEvidence(expectedAgentID, "invalid_request")
		return evidence, errors.New("live execution evidence requires a complete request identity")
	}
	childWorkspace := ""
	for _, candidate := range cfg.Agents.List {
		if routing.NormalizeAgentID(candidate.ID) == expectedAgentID {
			childWorkspace = strings.TrimSpace(candidate.Workspace)
			break
		}
	}
	if childWorkspace == "" {
		evidence := unavailableLiveEvidence(expectedAgentID, "agent_unavailable")
		return evidence, fmt.Errorf("live execution evidence agent is unavailable")
	}

	capture := cfg.Diagnostics.TraceCapture
	rootStore := diagnostictrace.Store{
		Root:      diagnostictrace.ResolveStoreRoot(capture.StateDir, rootScope.Workspace),
		MaxTraces: capture.MaxTraces,
	}
	childStore := diagnostictrace.Store{
		Root:      diagnostictrace.ResolveStoreRoot(capture.StateDir, childWorkspace),
		MaxTraces: capture.MaxTraces,
	}
	filteredSessionKey := cfg.SensitiveDataReplacer().Replace(sessionKey)
	sessionDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(filteredSessionKey)))
	waitCtx, cancel := context.WithTimeout(ctx, liveEvidenceWait)
	defer cancel()
	var rootTrace diagnostictrace.Trace
	var childTrace diagnostictrace.Trace
	admitted := 0
	for {
		var rootErr error
		rootQuery := diagnostictrace.TraceQuery{
			RootTurnID:  rootScope.TurnID,
			SessionHash: sessionDigest,
			NotBefore:   requestStarted.Add(-liveEvidenceStartSkew),
		}
		rootTrace, rootErr = rootStore.FindNewest(rootQuery)
		if errors.Is(rootErr, os.ErrNotExist) {
			// A user-only delegated final can carry the child session key while
			// retaining the parent trace scope. The parent workspace, turn, and
			// live-request time window remain the authoritative root identity.
			rootQuery.SessionHash = ""
			rootTrace, rootErr = rootStore.FindNewest(rootQuery)
		}
		if rootErr == nil {
			var childTurnID string
			childTurnID, admitted, rootErr = admittedLiveEvidenceChild(rootTrace, expectedAgentID)
			if rootErr != nil {
				evidence := unavailableLiveEvidence(expectedAgentID, "trace_invalid")
				return evidence, errors.New("live execution evidence delegation is invalid")
			}
			lastOffset := time.Duration(0)
			if count := len(rootTrace.Records); count > 0 {
				lastOffset = time.Duration(rootTrace.Records[count-1].OffsetNanos)
			}
			childTrace, rootErr = childStore.FindNewest(diagnostictrace.TraceQuery{
				ParentTurnID: rootScope.TurnID,
				ChildTurnID:  childTurnID,
				AgentID:      expectedAgentID,
				NotBefore:    rootTrace.CreatedAt.Add(-time.Second),
				NotAfter:     rootTrace.CreatedAt.Add(lastOffset + time.Second),
			})
		}
		if rootErr == nil {
			break
		}
		if !errors.Is(rootErr, os.ErrNotExist) {
			evidence := unavailableLiveEvidence(expectedAgentID, "trace_invalid")
			return evidence, errors.New("live execution evidence trace is invalid")
		}
		select {
		case <-waitCtx.Done():
			evidence := unavailableLiveEvidence(expectedAgentID, "trace_unavailable")
			return evidence, errors.New("live execution evidence did not become available")
		case <-time.After(100 * time.Millisecond):
		}
	}

	parent := summarizeLiveTrace(rootTrace)
	child := summarizeLiveTrace(childTrace)
	if traceOutcome(childTrace) == "suspended" && childTrace.Metadata.SessionHash != "" {
		continuations, continuationErr := childStore.FindAll(diagnostictrace.TraceQuery{
			AgentID:     expectedAgentID,
			SessionHash: childTrace.Metadata.SessionHash,
			NotBefore:   childTrace.CreatedAt.Add(time.Nanosecond),
		})
		if continuationErr != nil {
			evidence := unavailableLiveEvidence(expectedAgentID, "trace_invalid")
			if errors.Is(continuationErr, os.ErrNotExist) {
				evidence = unavailableLiveEvidence(expectedAgentID, "trace_unavailable")
				return evidence, errors.New("live execution evidence continuation is unavailable")
			}
			return evidence, errors.New("live execution evidence continuation is invalid")
		}
		if len(continuations) != 1 || continuations[0].TraceID == childTrace.TraceID ||
			traceOutcome(continuations[0]) != "completed" {
			evidence := unavailableLiveEvidence(expectedAgentID, "trace_invalid")
			return evidence, errors.New("live execution evidence continuation is ambiguous")
		}
		child = mergeLiveTraceEvidence(child, summarizeLiveTrace(continuations[0]))
		if completeLiveChildEvidence(child) &&
			liveEvidenceUserOnlyDelegation(rootTrace, expectedAgentID) {
			completeLiveEvidenceDelegation(&parent)
		}
	}
	return liveExecutionEvidence{
		SchemaVersion: "mintclaw.live_execution_evidence.v1",
		Status:        "verified",
		Parent:        parent,
		Delegation: liveDelegationEvidence{
			AgentID:  expectedAgentID,
			Admitted: admitted,
		},
		Child:     child,
		SafeError: nil,
	}, nil
}

func completeLiveChildEvidence(child liveTraceEvidence) bool {
	return child.Outcome == "completed" && !child.Incomplete &&
		len(child.ToolFailures) == 0 && len(child.UnpairedCalls) == 0
}

func traceOutcome(trace diagnostictrace.Trace) string {
	if trace.Outcome == nil {
		return ""
	}
	return trace.Outcome.Status
}

func liveEvidenceUserOnlyDelegation(trace diagnostictrace.Trace, expectedAgentID string) bool {
	matches := 0
	for _, record := range trace.Records {
		if record.Kind != diagnostictrace.RecordToolCall {
			continue
		}
		var payload diagnostictrace.ToolPayload
		if json.Unmarshal(record.Data, &payload) != nil || !payload.Executed || payload.Tool != "delegate" {
			continue
		}
		var arguments map[string]any
		if json.Unmarshal([]byte(payload.ArgumentsPreview), &arguments) != nil {
			continue
		}
		agentID, _ := arguments["agent_id"].(string)
		if arguments["delivery_mode"] != "user_only" ||
			routing.NormalizeAgentID(agentID) != expectedAgentID {
			continue
		}
		matches++
	}
	return matches == 1
}

func completeLiveEvidenceDelegation(parent *liveTraceEvidence) {
	if parent == nil || parent.Outcome != "suspended" || parent.Incomplete ||
		parent.ToolCalls["delegate"] != 1 || len(parent.ToolFailures) != 0 {
		return
	}
	if len(parent.UnpairedCalls) == 1 && parent.UnpairedCalls["delegate"] == 1 {
		delete(parent.UnpairedCalls, "delegate")
	}
	if len(parent.UnpairedCalls) == 0 {
		parent.Outcome = "completed"
	}
}

func mergeLiveTraceEvidence(
	initial liveTraceEvidence,
	continuation liveTraceEvidence,
) liveTraceEvidence {
	if initial.AgentID != continuation.AgentID {
		initial.Incomplete = true
		return initial
	}
	initial.Outcome = continuation.Outcome
	initial.Incomplete = initial.Incomplete || continuation.Incomplete
	for tool, count := range continuation.ToolCalls {
		initial.ToolCalls[tool] += count
	}
	for tool, count := range continuation.ToolFailures {
		initial.ToolFailures[tool] += count
	}
	for tool, count := range continuation.UnpairedCalls {
		initial.UnpairedCalls[tool] += count
	}
	if len(initial.BrowserSessions)+len(continuation.BrowserSessions) > 32 {
		initial.Incomplete = true
		return initial
	}
	initial.BrowserSessions = append(initial.BrowserSessions, continuation.BrowserSessions...)
	return initial
}

func admittedLiveEvidenceChild(trace diagnostictrace.Trace, expectedAgentID string) (string, int, error) {
	childTurnID := ""
	admitted := 0
	for _, record := range trace.Records {
		if record.Kind != diagnostictrace.RecordSubTurnAdmission {
			continue
		}
		var payload diagnostictrace.SubTurnAdmissionPayload
		if json.Unmarshal(record.Data, &payload) != nil {
			return "", 0, errors.New("invalid subturn admission record")
		}
		if payload.State != "admitted" || payload.Stage != "target_agent" ||
			routing.NormalizeAgentID(payload.AgentID) != expectedAgentID {
			continue
		}
		admitted++
		candidate := strings.TrimSpace(payload.ChildTurnID)
		if candidate == "" || childTurnID != "" && candidate != childTurnID {
			return "", admitted, errors.New("ambiguous subturn admission identity")
		}
		childTurnID = candidate
	}
	if admitted != 1 || childTurnID == "" {
		return "", admitted, errors.New("exactly one subturn admission is required")
	}
	return childTurnID, admitted, nil
}

func summarizeLiveTrace(trace diagnostictrace.Trace) liveTraceEvidence {
	evidence := emptyLiveTraceEvidence(routing.NormalizeAgentID(trace.Metadata.AgentID))
	if trace.Outcome != nil {
		evidence.Outcome = trace.Outcome.Status
	}
	evidence.Incomplete = trace.Truncation.Incomplete || trace.Truncation.DroppedRecords > 0
	type callRecord struct {
		tool                string
		evidenceTool        string
		browserSessionIndex int
	}
	calls := make(map[string]callRecord)
	results := make(map[string]diagnostictrace.ToolPayload)
	for _, record := range trace.Records {
		switch record.Kind {
		case diagnostictrace.RecordToolCall:
			var payload diagnostictrace.ToolPayload
			if json.Unmarshal(record.Data, &payload) != nil {
				evidence.Incomplete = true
				continue
			}
			if !payload.Executed {
				evidence.Incomplete = true
				continue
			}
			tool := safeLiveEvidenceTool(payload.Tool)
			evidenceTool := tool
			if tool == "browser_contexts" && payload.Action != "list" {
				evidenceTool = "other"
			}
			evidence.ToolCalls[evidenceTool]++
			if record.Correlation.ToolCallID == "" {
				evidence.Incomplete = true
				continue
			}
			if _, duplicate := calls[record.Correlation.ToolCallID]; duplicate {
				evidence.Incomplete = true
				continue
			}
			call := callRecord{
				tool: tool, evidenceTool: evidenceTool, browserSessionIndex: -1,
			}
			if tool == "browser_session" {
				selection, ok := browserSessionEvidence(payload.ArgumentsPreview)
				if ok && len(evidence.BrowserSessions) < 32 {
					call.browserSessionIndex = len(evidence.BrowserSessions)
					evidence.BrowserSessions = append(evidence.BrowserSessions, selection)
				} else if !ok || len(evidence.BrowserSessions) >= 32 {
					evidence.Incomplete = true
				}
			}
			calls[record.Correlation.ToolCallID] = call
		case diagnostictrace.RecordToolResult:
			var payload diagnostictrace.ToolPayload
			if json.Unmarshal(record.Data, &payload) != nil {
				evidence.Incomplete = true
				continue
			}
			if !payload.Executed {
				evidence.Incomplete = true
				continue
			}
			if record.Correlation.ToolCallID == "" {
				evidence.Incomplete = true
				continue
			}
			if _, duplicate := results[record.Correlation.ToolCallID]; duplicate {
				evidence.Incomplete = true
				continue
			}
			results[record.Correlation.ToolCallID] = payload
		}
	}
	for callID, call := range calls {
		result, ok := results[callID]
		if !ok || safeLiveEvidenceTool(result.Tool) != call.tool {
			evidence.UnpairedCalls[call.evidenceTool]++
			continue
		}
		if result.IsError || result.Status != "completed" {
			evidence.ToolFailures[call.evidenceTool]++
			continue
		}
		if call.browserSessionIndex >= 0 &&
			evidence.BrowserSessions[call.browserSessionIndex].Operation == "status" {
			state, stateOK := browserSessionStateEvidence(result.ResultPreview)
			if !stateOK {
				evidence.Incomplete = true
				continue
			}
			evidence.BrowserSessions[call.browserSessionIndex].State = state
		}
	}
	return evidence
}

func browserSessionEvidence(preview string) (liveBrowserSessionEvidence, bool) {
	var arguments map[string]any
	if strings.TrimSpace(preview) == "" || json.Unmarshal([]byte(preview), &arguments) != nil {
		return liveBrowserSessionEvidence{}, false
	}
	operation, _ := arguments["operation"].(string)
	if !liveEvidenceAlias.MatchString(operation) {
		return liveBrowserSessionEvidence{}, false
	}
	selection := liveBrowserSessionEvidence{Operation: operation}
	if target, _ := arguments["target"].(string); liveEvidenceAlias.MatchString(target) {
		selection.Target = target
	}
	if profile, _ := arguments["profile"].(string); liveEvidenceAlias.MatchString(profile) {
		selection.Profile = profile
	}
	if operation == "open" && (selection.Target == "" || selection.Profile == "") {
		return liveBrowserSessionEvidence{}, false
	}
	return selection, true
}

func browserSessionStateEvidence(preview string) (string, bool) {
	var result map[string]any
	if strings.TrimSpace(preview) == "" || json.Unmarshal([]byte(preview), &result) != nil {
		return "", false
	}
	state, _ := result["state"].(string)
	if !liveEvidenceAlias.MatchString(state) {
		return "", false
	}
	return state, true
}

func safeLiveEvidenceTool(tool string) string {
	tool = strings.ToLower(strings.TrimSpace(tool))
	if !liveEvidenceAlias.MatchString(tool) {
		return "other"
	}
	return tool
}
