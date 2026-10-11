package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type dispositionRegistryTool struct {
	mockRegistryTool
	verified     bool
	verifyCalls  int
	onExecute    func()
	requirement  TurnFinalizationRequirement
	needsRelease bool
}

func (tool *dispositionRegistryTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	if tool.onExecute != nil {
		tool.onExecute()
	}
	return tool.mockRegistryTool.Execute(ctx, args)
}

func (tool *dispositionRegistryTool) VerifyResourceDisposition(
	context.Context, map[string]any, *toolshared.ToolResult,
) bool {
	tool.verifyCalls++
	return tool.verified
}

func (tool *dispositionRegistryTool) TurnFinalizationRequirement(
	context.Context,
) (TurnFinalizationRequirement, bool, error) {
	return tool.requirement, tool.needsRelease, nil
}

func dispositionTestResult(name string) *toolshared.ToolResult {
	return toolshared.SilentResult("automation released").WithDeliverable(&taskresult.Deliverable{
		LifecycleReceipts: []taskresult.Receipt{{
			ID: "release_receipt", Kind: taskresult.ReceiptKindResourceCleanup,
			Target: "fixture:desktop", Tool: name, Action: "release",
			Summary: "Automation lease released; application lifetime unchanged.",
		}},
	})
}

func TestResourceDispositionRegistryRequiresTrustedBoundedEvidence(t *testing.T) {
	tests := []struct {
		name     string
		verified bool
		mutate   func(*toolshared.ToolResult)
		want     bool
	}{
		{name: "verified release without close vocabulary", verified: true, want: true},
		{name: "tool refuses verification", verified: false},
		{name: "error", verified: true, mutate: func(result *toolshared.ToolResult) { result.IsError = true }},
		{
			name:     "asynchronous",
			verified: true,
			mutate:   func(result *toolshared.ToolResult) { result.Control.Async = true },
		},
		{name: "no receipt", verified: true, mutate: func(result *toolshared.ToolResult) { result.Deliverable = nil }},
		{name: "other tool", verified: true, mutate: func(result *toolshared.ToolResult) {
			result.Deliverable.LifecycleReceipts[0].Tool = "different_tool"
		}},
		{name: "unbounded receipt", verified: true, mutate: func(result *toolshared.ToolResult) {
			result.Deliverable.LifecycleReceipts[0].ID = strings.Repeat("x", 257)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := dispositionTestResult("fixture_session")
			result.Control.ResourceDispositionVerified = true
			if test.mutate != nil {
				test.mutate(result)
			}
			tool := &dispositionRegistryTool{
				mockRegistryTool: mockRegistryTool{name: "fixture_session", result: result},
				verified:         test.verified,
			}
			registry := NewToolRegistry()
			registry.Register(tool)
			got := registry.Execute(t.Context(), tool.Name(), nil)
			if got.Control.ResourceDispositionVerified != test.want {
				t.Fatalf("verified = %v, want %v", got.Control.ResourceDispositionVerified, test.want)
			}
			encoded, err := json.Marshal(got)
			if err != nil || strings.Contains(string(encoded), "ResourceDispositionVerified") ||
				strings.Contains(string(encoded), "resource_disposition_verified") {
				t.Fatalf("runtime proof entered serialized result: %s, %v", encoded, err)
			}
		})
	}

	registry := NewToolRegistry()
	result := dispositionTestResult("ordinary_tool")
	result.Control.ResourceDispositionVerified = true
	registry.Register(&mockRegistryTool{name: "ordinary_tool", result: result})
	if registry.Execute(t.Context(), "ordinary_tool", nil).Control.ResourceDispositionVerified {
		t.Fatal("producer flag bypassed the verifier contract")
	}
}

func TestResourceDispositionVerifiesExecutingInstanceAfterReplacement(t *testing.T) {
	registry := NewToolRegistry()
	replacement := &dispositionRegistryTool{
		mockRegistryTool: mockRegistryTool{name: "fixture_session"},
	}
	executing := &dispositionRegistryTool{
		mockRegistryTool: mockRegistryTool{name: "fixture_session", result: dispositionTestResult("fixture_session")},
		verified:         true,
		onExecute:        func() { registry.Register(replacement) },
	}
	registry.Register(executing)
	result := registry.Execute(t.Context(), executing.Name(), nil)
	if !result.Control.ResourceDispositionVerified || executing.verifyCalls != 1 || replacement.verifyCalls != 0 {
		t.Fatalf("proof = %v; executing=%d replacement=%d",
			result.Control.ResourceDispositionVerified, executing.verifyCalls, replacement.verifyCalls)
	}
}

func TestResourceDispositionRequirementsIncludeEveryResourceKind(t *testing.T) {
	registry := NewToolRegistry()
	for _, name := range []string{"computer_session", "browser_session"} {
		registry.Register(&dispositionRegistryTool{
			mockRegistryTool: mockRegistryTool{name: name}, needsRelease: true,
			requirement: TurnFinalizationRequirement{
				RecoveryKind: taskresult.ObjectiveKindResourceDisposition,
				Instruction:  "Dispose " + name,
			},
		})
	}
	requirement, required, err := registry.TurnFinalizationRequirement(t.Context())
	if err != nil || !required || requirement.Instruction != "Dispose browser_session\n\nDispose computer_session" {
		t.Fatalf("requirements = %#v, %v, %v", requirement, required, err)
	}
}

func TestResourceDispositionRequirementRejectsIncompatibleOrUnboundedGuidance(t *testing.T) {
	for _, test := range []struct {
		name        string
		kind        string
		instruction string
	}{
		{name: "incompatible kind", kind: taskresult.ObjectiveKindLiveHandoff, instruction: "handoff"},
		{
			name: "oversized", kind: taskresult.ObjectiveKindResourceDisposition,
			instruction: strings.Repeat("x", maxTurnFinalizationInstructionBytes),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := NewToolRegistry()
			for _, requirement := range []TurnFinalizationRequirement{
				{RecoveryKind: taskresult.ObjectiveKindResourceDisposition, Instruction: "release"},
				{RecoveryKind: test.kind, Instruction: test.instruction},
			} {
				name := requirement.Instruction[:1]
				registry.Register(&dispositionRegistryTool{
					mockRegistryTool: mockRegistryTool{name: name}, needsRelease: true, requirement: requirement,
				})
			}
			if _, _, err := registry.TurnFinalizationRequirement(t.Context()); err == nil {
				t.Fatal("invalid combined requirement was accepted")
			}
		})
	}
}

func TestBrowserResourceDispositionVerificationBindsExactTerminalReceipt(t *testing.T) {
	source := &fakeBrowserToolSource{available: true, status: browser.Session{
		ID: "browser_session_private", State: browser.SessionClosed, Target: "gateway", Profile: "managed",
	}}
	tool := NewBrowserSessionTool(browserToolTestConfig(), source)
	registry := NewToolRegistry()
	registry.Register(tool)
	args := map[string]any{"operation": "close", "browser_session_id": source.status.ID}
	result := registry.Execute(browserToolTestContext(), tool.Name(), args)
	if result.IsError || !result.Control.ResourceDispositionVerified || source.statusCalls != 0 {
		t.Fatalf("close verification = %#v; status calls=%d", result, source.statusCalls)
	}
	for _, test := range []struct {
		name   string
		mutate func(map[string]any, *toolshared.ToolResult)
	}{
		{name: "different session", mutate: func(args map[string]any, _ *toolshared.ToolResult) {
			args["browser_session_id"] = "browser_session_other"
		}},
		{name: "status is not close", mutate: func(args map[string]any, _ *toolshared.ToolResult) {
			args["operation"] = "status"
		}},
		{name: "live state", mutate: func(_ map[string]any, result *toolshared.ToolResult) {
			result.Deliverable.LifecycleReceipts[0].Metadata["state"] = "ready"
		}},
		{name: "different target", mutate: func(_ map[string]any, result *toolshared.ToolResult) {
			result.Deliverable.LifecycleReceipts[0].Target = "browser:other/profile"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := map[string]any{"operation": "close", "browser_session_id": source.status.ID}
			result := registry.Execute(browserToolTestContext(), tool.Name(), args)
			test.mutate(args, result)
			if tool.VerifyResourceDisposition(browserToolTestContext(), args, result) {
				t.Fatal("mismatched terminal evidence was accepted")
			}
		})
	}
}
