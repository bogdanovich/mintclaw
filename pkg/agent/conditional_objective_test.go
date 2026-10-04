package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	runtimetools "github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestConditionalHandoffOutcomePartition(t *testing.T) {
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{
		{Item: "report inspected data", Kind: taskresult.ObjectiveKindResult},
		{
			Item: "hand control over if authentication is required", Kind: taskresult.ObjectiveKindLiveHandoff,
			Requirement: taskresult.ObjectiveRequirementIfNeeded,
		},
	})
	validResult := reportedObjectiveItem{
		ObjectiveID: "objective_1", Output: &taskresult.ObjectiveOutput{Kind: "text", Text: "Verified data."},
	}
	receipt := taskresult.Receipt{
		ID: "handoff_1", Kind: taskresult.ObjectiveKindLiveHandoff, Action: "handoff",
		Metadata: map[string]string{"resource_kind": "browser_session", "resource_id": "same_session"},
	}
	for _, test := range []struct {
		name      string
		completed []reportedObjectiveItem
		missing   []string
		notNeeded []string
		receipts  []taskresult.Receipt
		want      taskresult.OutcomeStatus
	}{
		{
			name: "unnecessary", completed: []reportedObjectiveItem{validResult},
			notNeeded: []string{"objective_2"}, want: taskresult.OutcomeSucceeded,
		},
		{
			name: "performed", completed: []reportedObjectiveItem{validResult, {
				ObjectiveID: "objective_2", ReceiptIDs: []string{receipt.ID},
			}}, receipts: []taskresult.Receipt{receipt}, want: taskresult.OutcomeSucceeded,
		},
		{
			name: "missing_receipt", completed: []reportedObjectiveItem{validResult, {ObjectiveID: "objective_2"}},
			want: taskresult.OutcomePartial,
		},
		{
			name: "needed_but_failed", completed: []reportedObjectiveItem{validResult},
			missing: []string{"objective_2"}, want: taskresult.OutcomePartial,
		},
		{
			name: "required_result_skipped", notNeeded: []string{"objective_1", "objective_2"},
			want: taskresult.OutcomeBlocked,
		},
		{
			name: "performed_cannot_be_skipped", completed: []reportedObjectiveItem{validResult},
			notNeeded: []string{"objective_2"}, receipts: []taskresult.Receipt{receipt}, want: taskresult.OutcomePartial,
		},
		{
			name: "duplicate_partition", completed: []reportedObjectiveItem{validResult},
			missing: []string{"objective_2"}, notNeeded: []string{"objective_2"}, want: taskresult.OutcomePartial,
		},
		{
			name: "unknown_id", completed: []reportedObjectiveItem{validResult},
			notNeeded: []string{"objective_2", "unknown"}, want: taskresult.OutcomePartial,
		},
		{
			name: "conditional_is_not_implicit", completed: []reportedObjectiveItem{validResult},
			want: taskresult.OutcomePartial,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			reported := reportedObjectiveOutcome{
				Status: "succeeded", CompletedItems: test.completed, MissingItems: test.missing,
				NotNeededItems: test.notNeeded, Result: "Inspection finished.",
			}
			outcome := validateObjectiveOutcome(reported, nil, test.receipts, checklist)
			if outcome.Status != test.want {
				t.Fatalf("outcome = %#v, want %q", outcome, test.want)
			}
			encoded, err := json.Marshal(reported)
			if err != nil {
				t.Fatal(err)
			}
			content := objectiveOutcomeStart + string(encoded) + objectiveOutcomeEnd
			if instruction, repair := liveHandoffRecoveryInstruction(content, nil, test.receipts, checklist); repair {
				t.Fatalf("conditional handoff triggered side-effecting recovery: %q", instruction)
			}
		})
	}
}

func TestConditionalDeclarationCannotWeakenRequiredObjectives(t *testing.T) {
	for _, kind := range []string{taskresult.ObjectiveKindResult, taskresult.ObjectiveKindExternalAction} {
		if checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{{
			Item: "required work", Kind: kind, Requirement: taskresult.ObjectiveRequirementIfNeeded,
		}}); checklist != nil {
			t.Fatalf("conditional %s was admitted: %#v", kind, checklist)
		}
	}
	for _, requirement := range []string{"", taskresult.ObjectiveRequirementRequired} {
		checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{{
			Item: "explicitly requested handoff", Kind: taskresult.ObjectiveKindLiveHandoff, Requirement: requirement,
		}})
		outcome := validateObjectiveOutcome(reportedObjectiveOutcome{
			Status: "succeeded", NotNeededItems: []string{"objective_1"}, Result: "No handoff.",
		}, nil, nil, checklist)
		if outcome.Status != taskresult.OutcomeBlocked || len(outcome.NotNeededItems) != 0 {
			t.Fatalf("required handoff was skipped: %#v", outcome)
		}
	}
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{{
		Item: "publish the requested resource", Kind: taskresult.ObjectiveKindExternalAction,
	}})
	if outcome := validateObjectiveOutcome(reportedObjectiveOutcome{
		Status: "succeeded", NotNeededItems: []string{"objective_1"}, Result: "Published.",
	}, nil, nil, checklist); outcome.Status != taskresult.OutcomeBlocked {
		t.Fatalf("required external action was skipped: %#v", outcome)
	}
}

func TestConditionalChecklistSurvivesContinuationProjection(t *testing.T) {
	want := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{{
		Item: "authentication handoff if needed", Kind: taskresult.ObjectiveKindLiveHandoff,
		Requirement: taskresult.ObjectiveRequirementIfNeeded,
	}})
	got := runtimeObjectiveChecklist(interactionObjectiveChecklist(cloneRuntimeObjectiveChecklist(want)))
	if len(got) != 1 || got[0].Requirement != taskresult.ObjectiveRequirementIfNeeded {
		t.Fatalf("conditional requirement lost through continuation: %#v", got)
	}
	outcome := validateObjectiveOutcome(reportedObjectiveOutcome{
		Status: "succeeded", NotNeededItems: []string{"objective_1"}, Result: "Authentication already valid.",
	}, nil, nil, got)
	if outcome.Status != taskresult.OutcomeSucceeded || len(outcome.NotNeededItems) != 1 {
		t.Fatalf("conditional-only task was misclassified: %#v", outcome)
	}
}

func TestMixedConditionalAndRequiredHandoffRecoveryBindsReceipts(t *testing.T) {
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{
		{Item: "explicitly hand control to the user", Kind: taskresult.ObjectiveKindLiveHandoff},
		{Item: "sign-in handoff if needed", Kind: taskresult.ObjectiveKindLiveHandoff, Requirement: "if_needed"},
	})
	receipt := func(id string) taskresult.Receipt {
		return taskresult.Receipt{
			ID: id, Kind: taskresult.ObjectiveKindLiveHandoff, Action: "handoff",
			Metadata: map[string]string{"resource_kind": "browser_session", "resource_id": "session_" + id},
		}
	}
	for _, test := range []struct {
		name          string
		requiredIDs   []string
		conditionalID string
		receipts      []taskresult.Receipt
		wantRecovery  bool
	}{
		{"conditional receipt cannot cover required", nil, "conditional", []taskresult.Receipt{receipt("conditional")}, true},
		{"both verified", []string{"required"}, "conditional", []taskresult.Receipt{receipt("required"), receipt("conditional")}, false},
		{"unclaimed required evidence is repaired without replay", nil, "conditional", []taskresult.Receipt{receipt("conditional"), receipt("required")}, false},
		{"conditional needs its own evidence", nil, "invented", nil, false},
		{"invented required receipt needs report repair", []string{"invented"}, "conditional", []taskresult.Receipt{receipt("conditional")}, false},
		{"required evidence cannot cover conditional", []string{"required"}, "invented", []taskresult.Receipt{receipt("required")}, false},
		{"duplicate receipt cannot cover two objectives", []string{"conditional"}, "conditional", []taskresult.Receipt{receipt("conditional")}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			report := reportedObjectiveOutcome{
				Status: "succeeded", Result: "Both control steps completed.",
				CompletedItems: []reportedObjectiveItem{
					{ObjectiveID: "objective_1", ReceiptIDs: test.requiredIDs},
					{ObjectiveID: "objective_2", ReceiptIDs: []string{test.conditionalID}},
				},
			}
			for _, reversed := range []bool{false, true} {
				if reversed {
					report.CompletedItems[0], report.CompletedItems[1] = report.CompletedItems[1], report.CompletedItems[0]
				}
				encoded, err := json.Marshal(report)
				if err != nil {
					t.Fatal(err)
				}
				_, recovery := liveHandoffRecoveryInstruction(objectiveOutcomeStart+string(encoded)+objectiveOutcomeEnd,
					nil, test.receipts, checklist)
				if recovery != test.wantRecovery {
					t.Fatalf("recovery = %t, want %t (reversed=%t)", recovery, test.wantRecovery, reversed)
				}
			}
		})
	}
}

func TestConditionalHandoffDoesNotUpgradeProducerPartialOrBlocked(t *testing.T) {
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{
		{Item: "report data", Kind: taskresult.ObjectiveKindResult},
		{Item: "authentication handoff", Kind: taskresult.ObjectiveKindLiveHandoff, Requirement: "if_needed"},
	})
	for _, status := range []taskresult.OutcomeStatus{taskresult.OutcomePartial, taskresult.OutcomeBlocked} {
		outcome := validateObjectiveOutcome(reportedObjectiveOutcome{
			Status: string(status), Explanation: "The requested postcondition remains unverified.",
			CompletedItems: []reportedObjectiveItem{{
				ObjectiveID: "objective_1", Output: &taskresult.ObjectiveOutput{Kind: "text", Text: "Known finding."},
			}}, NotNeededItems: []string{"objective_2"},
		}, nil, nil, checklist)
		if outcome.Status != status {
			t.Fatalf("producer %s was upgraded: %#v", status, outcome)
		}
	}
}

type conditionalClosedBrowserSource struct {
	runtimetools.BrowserToolSource
	target       string
	closed       bool
	handoffCalls int
}

func (*conditionalClosedBrowserSource) HandoffAvailable() bool { return true }

func (source *conditionalClosedBrowserSource) Close(
	_ context.Context, owner browser.Owner, sessionID string,
) (browser.Session, error) {
	source.closed = true
	return browser.Session{
		ID: sessionID, State: browser.SessionClosed, Owner: owner, Target: source.target, Profile: "managed",
	}, nil
}

func (source *conditionalClosedBrowserSource) Handoff(
	context.Context, browser.Owner, string,
) (browser.Session, error) {
	source.handoffCalls++
	return browser.Session{}, errors.New("the existing browser session is closed")
}

func (*conditionalClosedBrowserSource) CloseOwner(context.Context, browser.Owner) ([]browser.Session, error) {
	return nil, nil
}

func TestDelegateConditionalHandoffDoesNotRepairAfterBrowserClose(t *testing.T) {
	for _, target := range []string{"gateway", "companion"} {
		for _, channel := range []string{"telegram", "discord"} {
			t.Run(target+"/"+channel, func(t *testing.T) {
				provider := &sequenceProvider{responses: []*providers.LLMResponse{
					{ToolCalls: []providers.ToolCall{{
						ID: "delegate-authenticated", Name: "delegate", Arguments: map[string]any{
							"agent_id":      "beta",
							"task":          "Inspect both results, hand off only if sign-in is required, then close.",
							"delivery_mode": string(toolshared.AsyncDeliveryUserOnly),
							"objective_items": []any{
								map[string]any{"item": "inspect first result", "kind": "result"},
								map[string]any{"item": "inspect second result and close", "kind": "result"},
								map[string]any{
									"item":        "sign-in handoff if needed",
									"kind":        "live_handoff",
									"requirement": "if_needed",
								},
							},
						},
					}}},
					{ToolCalls: []providers.ToolCall{{
						ID: "close-browser", Name: "browser_session", Arguments: map[string]any{
							"operation": "close", "browser_session_id": "same-session",
						},
					}}},
					{Content: objectiveOutcomeStart + `{"status":"succeeded","completed_items":[` +
						`{"objective_id":"objective_1","output":{"kind":"text","text":"First verified finding."}},` +
						`{"objective_id":"objective_2","output":{"kind":"text","text":"Second verified finding; browser closed."}}],` +
						`"missing_items":[],"not_needed_items":["objective_3"],"result":"First verified finding. Second verified finding; browser closed."}` +
						objectiveOutcomeEnd, FinishReason: "stop"},
				}}
				fixture := newAgentLoopTestFixture(t, provider, func(cfg *config.Config) {
					cfg.Agents.Defaults.ToolFeedback = config.ToolFeedbackConfig{Enabled: true, Subagents: true}
					cfg.Agents.List = []config.AgentConfig{
						{
							ID: "alpha", Default: true, Workspace: filepath.Join(cfg.WorkspacePath(), "alpha"),
							Subagents: &config.SubagentsConfig{AllowAgents: []string{"beta"}},
						},
						{ID: "beta", Workspace: filepath.Join(cfg.WorkspacePath(), "beta")},
					}
				})
				beta, _ := fixture.Loop.registry.GetAgent("beta")
				source := &conditionalClosedBrowserSource{target: target}
				beta.Tools.Register(runtimetools.NewBrowserSessionTool(
					runtimetools.NewBrowserToolOptions(
						config.BrowserToolsConfig{Enabled: true, Agents: []string{"beta"}},
					),
					source,
				))
				adapter := &presentationFeedbackChannel{}
				fixture.Loop.SetChannelManager(newStartedTestChannelManagerWithConfig(
					t, fixture.Config, fixture.Bus, media.NewFileMediaStore(), channel, adapter,
				))
				response, err := fixture.Loop.runAgentLoop(t.Context(), fixture.Agent, turnSpec{
					Dispatch: DispatchRequest{
						SessionKey: "conditional-auth", UserMessage: "Inspect both accounts.",
						InboundContext: &bus.InboundContext{Channel: channel, ChatID: "chat-1", SenderID: "test-user"},
					}, SendResponse: true, DefaultResponse: defaultResponse,
				})
				if err != nil || response != "" {
					t.Fatalf("delegated result = (%q, %v)", response, err)
				}
				if !source.closed || source.handoffCalls != 0 || provider.callCount != 3 {
					t.Fatalf("unexpected recovery: closed=%t handoffs=%d provider_calls=%d",
						source.closed, source.handoffCalls, provider.callCount)
				}
				var messages []bus.OutboundMessage
				feedbackCount := 0
				for _, msg := range adapter.messagesSnapshot() {
					if msg.Metadata.IsToolFeedback() {
						feedbackCount++
					} else {
						messages = append(messages, msg)
					}
				}
				if len(messages) != 1 ||
					messages[0].Content != "First verified finding. Second verified finding; browser closed." {
					t.Fatalf("channel result = %#v", messages)
				}
				adapter.feedbackMu.Lock()
				activeFeedback := len(adapter.active)
				adapter.feedbackMu.Unlock()
				if feedbackCount == 0 || activeFeedback != 0 {
					t.Fatalf("feedback cleanup: created=%d active=%d", feedbackCount, activeFeedback)
				}
				history := fixture.Agent.Sessions.GetHistory("conditional-auth")
				outcome := history[len(history)-1].Deliverable.ObjectiveOutcome
				if outcome.Status != taskresult.OutcomeSucceeded || len(outcome.NotNeededItems) != 1 {
					t.Fatalf("completed task downgraded: %#v", outcome)
				}
			})
		}
	}
}
