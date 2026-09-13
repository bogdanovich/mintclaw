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

const liveEvidenceWait = 5 * time.Second

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
) (liveExecutionEvidence, error) {
	expectedAgentID = routing.NormalizeAgentID(expectedAgentID)
	if cfg == nil || !cfg.Diagnostics.TraceCapture.Enabled {
		evidence := unavailableLiveEvidence(expectedAgentID, "trace_capture_disabled")
		return evidence, errors.New("live execution evidence requires diagnostic trace capture")
	}
	if !rootScope.Complete() || strings.TrimSpace(sessionKey) == "" || expectedAgentID == "" {
		evidence := unavailableLiveEvidence(expectedAgentID, "invalid_request")
		return evidence, errors.New("live execution evidence requires a complete trace scope and agent")
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
	for {
		var rootErr error
		rootTrace, rootErr = rootStore.FindNewest(diagnostictrace.TraceQuery{
			RootTurnID:  rootScope.TurnID,
			SessionHash: sessionDigest,
		})
		if rootErr == nil {
			lastOffset := time.Duration(0)
			if count := len(rootTrace.Records); count > 0 {
				lastOffset = time.Duration(rootTrace.Records[count-1].OffsetNanos)
			}
			childTrace, rootErr = childStore.FindNewest(diagnostictrace.TraceQuery{
				ParentTurnID: rootScope.TurnID,
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
	admitted := 0
	for _, record := range rootTrace.Records {
		if record.Kind != diagnostictrace.RecordSubTurnAdmission {
			continue
		}
		var payload diagnostictrace.SubTurnAdmissionPayload
		if json.Unmarshal(record.Data, &payload) == nil &&
			payload.State == "admitted" && payload.Stage == "target_agent" &&
			routing.NormalizeAgentID(payload.AgentID) == expectedAgentID {
			admitted++
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

func summarizeLiveTrace(trace diagnostictrace.Trace) liveTraceEvidence {
	evidence := emptyLiveTraceEvidence(routing.NormalizeAgentID(trace.Metadata.AgentID))
	if trace.Outcome != nil {
		evidence.Outcome = trace.Outcome.Status
	}
	evidence.Incomplete = trace.Truncation.Incomplete || trace.Truncation.DroppedRecords > 0
	type callRecord struct {
		tool string
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
			tool := safeLiveEvidenceTool(payload.Tool)
			evidence.ToolCalls[tool]++
			if record.Correlation.ToolCallID == "" {
				evidence.Incomplete = true
				continue
			}
			if _, duplicate := calls[record.Correlation.ToolCallID]; duplicate {
				evidence.Incomplete = true
				continue
			}
			calls[record.Correlation.ToolCallID] = callRecord{tool: tool}
			if tool == "browser_session" {
				selection, ok := browserSessionEvidence(payload.ArgumentsPreview)
				if ok && len(evidence.BrowserSessions) < 32 {
					evidence.BrowserSessions = append(evidence.BrowserSessions, selection)
				} else if !ok || len(evidence.BrowserSessions) >= 32 {
					evidence.Incomplete = true
				}
			}
		case diagnostictrace.RecordToolResult:
			var payload diagnostictrace.ToolPayload
			if json.Unmarshal(record.Data, &payload) != nil {
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
			evidence.UnpairedCalls[call.tool]++
			continue
		}
		if result.IsError || result.Status != "completed" {
			evidence.ToolFailures[call.tool]++
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

func safeLiveEvidenceTool(tool string) string {
	tool = strings.ToLower(strings.TrimSpace(tool))
	if !liveEvidenceAlias.MatchString(tool) {
		return "other"
	}
	return tool
}
