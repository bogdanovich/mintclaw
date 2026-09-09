package worker

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

func TestSnapshotFromFrontendProjectsBoundedRedactedMCPState(t *testing.T) {
	binding := testBinding(t)
	resultCanary := "plain-result-canary"
	source := frontend.ThreadSnapshot{
		ThreadID: binding.ThreadID,
		Activity: frontend.ActivityIdle,
		Items: []frontend.PresentationItem{
			{
				ID: "tool:turn-1:call-1", TurnID: "turn-1", Sequence: 1, Revision: 1,
				Kind: frontend.PresentationToolCall, Lifecycle: frontend.PresentationCompleted,
				Tool: &frontend.ToolState{
					CallID: "call-1", Name: "opaque-provider-alias", Status: frontend.ToolSucceeded,
					Arguments: "fields: query",
					MCP: &frontend.MCPState{
						Server: "github", Tool: "search_repositories", Purpose: "Search sk-123456789abcdef",
						Outcome: frontend.MCPOutcomeSucceeded,
						Result:  `{"password":"` + resultCanary + `","safe":"visible"}`,
					},
				},
			},
		},
	}

	snapshot := SnapshotFromFrontend(source, nil)
	if err := validateSnapshot(binding.ControlIdentity(), snapshot); err != nil {
		t.Fatalf("validateSnapshot() error = %v", err)
	}
	if len(snapshot.Items) != 1 || snapshot.Items[0].Tool == nil || snapshot.Items[0].Tool.MCP == nil {
		t.Fatalf("projected MCP item = %#v", snapshot.Items)
	}
	tool := snapshot.Items[0].Tool
	if tool.MCP.Server != "github" || tool.MCP.Tool != "search_repositories" ||
		tool.MCP.Outcome != MCPOutcomeSucceeded || !strings.Contains(tool.MCP.Result, "visible") ||
		strings.Contains(tool.MCP.Result, resultCanary) || strings.Contains(tool.MCP.Purpose, "123456789abcdef") {
		t.Fatalf("safe MCP projection = %#v", tool.MCP)
	}
	raw, err := json.Marshal(tool)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), resultCanary) || tool.Arguments != "fields: query" {
		t.Fatalf("worker MCP projection leaked a value: %s", raw)
	}
	if strings.ContainsAny(string(raw), "\x1b\a") {
		t.Fatalf("worker MCP projection retained terminal controls: %s", raw)
	}
	wireSnapshot, err := MarshalPayload(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Snapshot
	if err = DecodePayload(wireSnapshot, &decoded); err != nil {
		t.Fatalf("DecodePayload() error = %v", err)
	}
	if err = validateSnapshot(binding.ControlIdentity(), decoded); err != nil ||
		len(decoded.Items) != 1 || decoded.Items[0].Tool == nil || decoded.Items[0].Tool.MCP == nil ||
		decoded.Items[0].Tool.MCP.Tool != "search_repositories" {
		t.Fatalf("MCP snapshot wire round trip = %#v, validation error = %v", decoded, err)
	}
}

func TestSnapshotFromFrontendPreservesSeparateMCPCallIdentitiesAndLoopHalt(t *testing.T) {
	binding := testBinding(t)
	items := make([]frontend.PresentationItem, 2)
	for index, callID := range []string{"call-1", "call-2"} {
		items[index] = frontend.PresentationItem{
			ID: "tool:turn-1:" + callID, TurnID: "turn-1", Sequence: uint64(index + 1), Revision: 1,
			Kind: frontend.PresentationToolCall, Lifecycle: frontend.PresentationCompleted,
			Tool: &frontend.ToolState{
				CallID: callID, Name: "opaque-provider-alias", Status: frontend.ToolSucceeded,
				MCP: &frontend.MCPState{
					Server: "obsidian", Tool: "get_vault_stats", Outcome: frontend.MCPOutcomeSucceeded,
					Result: "same result",
				},
			},
		}
	}
	items[1].Tool.MCP.LoopHaltCode = mcpLoopHaltIdenticalSuccess
	items[1].Tool.MCP.LoopHaltCount = 4
	items[1].Tool.MCP.LoopHaltThreshold = 4

	snapshot := SnapshotFromFrontend(frontend.ThreadSnapshot{
		ThreadID: binding.ThreadID, Activity: frontend.ActivityIdle, Items: items,
	}, nil)
	if err := validateSnapshot(binding.ControlIdentity(), snapshot); err != nil {
		t.Fatalf("validateSnapshot() error = %v", err)
	}
	if len(snapshot.Items) != 2 || snapshot.Items[0].Tool.CallID != "call-1" ||
		snapshot.Items[1].Tool.CallID != "call-2" ||
		snapshot.Items[1].Tool.MCP.LoopHaltCode != mcpLoopHaltIdenticalSuccess ||
		snapshot.Items[1].Tool.MCP.LoopHaltCount != 4 {
		t.Fatalf("separate MCP calls = %#v", snapshot.Items)
	}
}

func TestSnapshotFromFrontendOmitsOversizedMCPJSONBeforeSensitiveValuesCanLeak(t *testing.T) {
	binding := testBinding(t)
	canary := "plain-password-canary"
	snapshot := SnapshotFromFrontend(frontend.ThreadSnapshot{
		ThreadID: binding.ThreadID, Activity: frontend.ActivityIdle,
		Items: []frontend.PresentationItem{{
			ID: "tool:turn-1:call-1", TurnID: "turn-1", Sequence: 1, Revision: 1,
			Kind: frontend.PresentationToolCall, Lifecycle: frontend.PresentationCompleted,
			Tool: &frontend.ToolState{
				CallID: "call-1", Name: "opaque-provider-alias", Status: frontend.ToolSucceeded,
				MCP: &frontend.MCPState{
					Server: "github", Tool: "inspect", Outcome: frontend.MCPOutcomeSucceeded,
					Result: `{"password":"` + canary + `","padding":"` +
						strings.Repeat("x", MaxMCPResultBytes+maxMCPJSONLookahead) + `"}`,
				},
			},
		}},
	}, nil)
	if err := validateSnapshot(binding.ControlIdentity(), snapshot); err != nil {
		t.Fatalf("validateSnapshot() error = %v", err)
	}
	mcpObservation := snapshot.Items[0].Tool.MCP
	if mcpObservation == nil || !mcpObservation.Truncated ||
		mcpObservation.Result != "[MCP JSON evidence omitted: oversized]" ||
		strings.Contains(mcpObservation.Result, canary) {
		t.Fatalf("oversized worker MCP JSON = %#v", mcpObservation)
	}
}

func TestMCPWireValidationFailsClosed(t *testing.T) {
	valid := func() Tool {
		return Tool{
			CallID: "call-1", Name: "opaque-provider-alias", Status: ToolSucceeded,
			MCP: &MCP{
				Server: "github", Tool: "search", Outcome: MCPOutcomeSucceeded, Result: "safe",
			},
		}
	}
	if !validTool(valid()) {
		t.Fatal("valid MCP tool was rejected")
	}

	for name, mutate := range map[string]func(*Tool){
		"invalid outcome": func(tool *Tool) { tool.MCP.Outcome = "done" },
		"missing server":  func(tool *Tool) { tool.MCP.Server = "" },
		"running result": func(tool *Tool) {
			tool.MCP.Outcome = MCPOutcomeRunning
		},
		"successful error":   func(tool *Tool) { tool.MCP.Error = "impossible" },
		"failed result":      func(tool *Tool) { tool.MCP.Outcome = MCPOutcomeFailed },
		"secret JSON":        func(tool *Tool) { tool.MCP.Result = `{"password":"plain-canary"}` },
		"halt without count": func(tool *Tool) { tool.MCP.LoopHaltCode = mcpLoopHaltIdenticalSuccess },
		"unknown halt": func(tool *Tool) {
			tool.MCP.LoopHaltCode = "arbitrary"
			tool.MCP.LoopHaltCount = 4
			tool.MCP.LoopHaltThreshold = 4
		},
		"successful failure halt": func(tool *Tool) {
			tool.MCP.LoopHaltCode = mcpLoopHaltRepeatedFailure
			tool.MCP.LoopHaltCount = 4
			tool.MCP.LoopHaltThreshold = 4
		},
		"failed success halt": func(tool *Tool) {
			tool.MCP.Outcome = MCPOutcomeFailed
			tool.MCP.Result = ""
			tool.MCP.Error = "failed"
			tool.MCP.LoopHaltCode = mcpLoopHaltIdenticalSuccess
			tool.MCP.LoopHaltCount = 4
			tool.MCP.LoopHaltThreshold = 4
		},
		"ambiguous command": func(tool *Tool) { tool.Command = &Command{Status: CommandSucceeded} },
		"ambiguous exploration": func(tool *Tool) {
			tool.Exploration = &Exploration{Operation: ExplorationRead, Path: "README.md"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			tool := valid()
			mutate(&tool)
			if validTool(tool) {
				t.Fatalf("invalid MCP tool admitted: %#v", tool)
			}
		})
	}
}
