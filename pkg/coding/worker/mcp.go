package worker

import (
	"encoding/json"
	"io"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
)

const (
	MaxMCPIdentityBytes = 1 << 10
	MaxMCPPurposeBytes  = 2 << 10
	MaxMCPResultBytes   = 16 << 10
	maxMCPJSONLookahead = 1 << 10
)

const (
	mcpLoopHaltIdenticalSuccess = "identical_call_emergency_halt"
	mcpLoopHaltRepeatedFailure  = "same_tool_failure_halt"
)

type MCPOutcome string

const (
	MCPOutcomeRunning   MCPOutcome = "running"
	MCPOutcomeSucceeded MCPOutcome = "succeeded"
	MCPOutcomeFailed    MCPOutcome = "failed"
	MCPOutcomeCanceled  MCPOutcome = "canceled"
	MCPOutcomeTimedOut  MCPOutcome = "timed_out"
	MCPOutcomeUncertain MCPOutcome = "uncertain"
)

// MCP is the protocol-v1 renderer-neutral projection of wrapper-owned MCP
// presentation evidence. It intentionally excludes argument values.
type MCP struct {
	Server            string     `json:"server"`
	Tool              string     `json:"tool"`
	Purpose           string     `json:"purpose,omitempty"`
	Outcome           MCPOutcome `json:"outcome"`
	Result            string     `json:"result,omitempty"`
	Error             string     `json:"error,omitempty"`
	Truncated         bool       `json:"truncated,omitempty"`
	LoopHaltCode      string     `json:"loop_halt_code,omitempty"`
	LoopHaltCount     int        `json:"loop_halt_count,omitempty"`
	LoopHaltThreshold int        `json:"loop_halt_threshold,omitempty"`
}

func mcpFromFrontend(source frontend.MCPState) (*MCP, bool) {
	server, serverTruncated := canonicalMCPStructural(source.Server, MaxMCPIdentityBytes)
	tool, toolTruncated := canonicalMCPStructural(source.Tool, MaxMCPIdentityBytes)
	purpose, purposeTruncated := canonicalMCPContent(source.Purpose, MaxMCPPurposeBytes)
	result, resultTruncated := canonicalMCPContent(source.Result, MaxMCPResultBytes)
	errorText, errorTruncated := canonicalMCPContent(source.Error, MaxMCPResultBytes)
	loopHaltCode, loopTruncated := canonicalMCPStructural(source.LoopHaltCode, MaxAttachmentMeta)

	observation := &MCP{
		Server:  server,
		Tool:    tool,
		Purpose: purpose,
		Outcome: MCPOutcome(source.Outcome),
		Result:  result,
		Error:   errorText,
		Truncated: source.Truncated || serverTruncated || toolTruncated || purposeTruncated ||
			resultTruncated || errorTruncated || loopTruncated,
		LoopHaltCode:      loopHaltCode,
		LoopHaltCount:     source.LoopHaltCount,
		LoopHaltThreshold: source.LoopHaltThreshold,
	}
	if !validMCP(*observation) {
		return nil, true
	}
	return observation, observation.Truncated
}

func canonicalMCPStructural(value string, maximum int) (string, bool) {
	original := value
	value = (diagnostictrace.Redactor{}).RedactText(value, maximum)
	value, normalized := boundedWireStructural(value, maximum)
	return value, normalized || value != original
}

func canonicalMCPContent(value string, maximum int) (string, bool) {
	original := value
	value = strings.ToValidUTF8(value, "�")
	if len(value) > maximum+maxMCPJSONLookahead {
		trimmed := strings.TrimSpace(value)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			return "[MCP JSON evidence omitted: oversized]", true
		}
		value = (diagnostictrace.Redactor{}).RedactText(value, maximum)
		bounded, normalized := boundedWireContent(value, maximum)
		return bounded, normalized || bounded != original
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err == nil {
		var trailing any
		if err = decoder.Decode(&trailing); err == io.EOF {
			value = (diagnostictrace.Redactor{}).RedactJSON(decoded, maximum)
			bounded, normalized := boundedWireContent(value, maximum)
			return bounded, normalized || bounded != original
		}
	}
	value = (diagnostictrace.Redactor{}).RedactText(value, maximum)
	value, normalized := boundedWireContent(value, maximum)
	return value, normalized || value != original
}

func validMCP(observation MCP) bool {
	switch observation.Outcome {
	case MCPOutcomeRunning, MCPOutcomeSucceeded, MCPOutcomeFailed, MCPOutcomeCanceled,
		MCPOutcomeTimedOut, MCPOutcomeUncertain:
	default:
		return false
	}
	if !validCanonicalMCPStructural(observation.Server, MaxMCPIdentityBytes, true) ||
		!validCanonicalMCPStructural(observation.Tool, MaxMCPIdentityBytes, true) ||
		!validCanonicalMCPContent(observation.Purpose, MaxMCPPurposeBytes, false) ||
		!validCanonicalMCPContent(observation.Result, MaxMCPResultBytes, false) ||
		!validCanonicalMCPContent(observation.Error, MaxMCPResultBytes, false) ||
		!validCanonicalMCPStructural(observation.LoopHaltCode, MaxAttachmentMeta, false) {
		return false
	}
	if observation.Outcome == MCPOutcomeRunning && (observation.Result != "" || observation.Error != "") {
		return false
	}
	if observation.Outcome == MCPOutcomeSucceeded && observation.Error != "" {
		return false
	}
	if observation.Outcome != MCPOutcomeRunning && observation.Outcome != MCPOutcomeSucceeded &&
		observation.Result != "" {
		return false
	}
	if observation.LoopHaltCode == "" {
		return observation.LoopHaltCount == 0 && observation.LoopHaltThreshold == 0
	}
	if observation.LoopHaltCode != mcpLoopHaltIdenticalSuccess &&
		observation.LoopHaltCode != mcpLoopHaltRepeatedFailure {
		return false
	}
	if observation.Outcome == MCPOutcomeRunning || observation.LoopHaltCount <= 0 ||
		observation.LoopHaltThreshold <= 0 {
		return false
	}
	if observation.LoopHaltCode == mcpLoopHaltIdenticalSuccess {
		return observation.Outcome == MCPOutcomeSucceeded
	}
	return observation.Outcome != MCPOutcomeSucceeded
}

func validCanonicalMCPStructural(value string, maximum int, required bool) bool {
	if value == "" {
		return !required
	}
	canonical, _ := canonicalMCPStructural(value, maximum)
	return canonical == value
}

func validCanonicalMCPContent(value string, maximum int, required bool) bool {
	if value == "" {
		return !required
	}
	canonical, _ := canonicalMCPContent(value, maximum)
	return canonical == value
}
