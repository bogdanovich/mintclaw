package eval

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func rolePromptCase() Case {
	return Case{
		Version: Version, ID: "general.role-test", Suite: "general", Tags: []string{},
		Description: "Synthetic role contract test.", Task: "Help select a test category.", Requires: []string{},
		Driver: Driver{Mode: "llm_user", Persona: "Choose another person; ask for clarification if needed."},
		Limits: DefaultLimits(), Checks: []Check{{ID: "finished", Kind: "last_outcome", Expected: "final"}},
		Rubric: []Criterion{{ID: "coherent", Description: "Question and visible choices agree.", Required: true}},
	}
}

func rolePromptEvents() []Event {
	return []Event{
		{ID: "event-000001", Turn: 1, Kind: "input", Content: "Help select a test category."},
		{
			ID:            "event-000002",
			Turn:          1,
			Kind:          "question",
			Content:       "For yourself or another person?\nSelf\nAnother person",
			AgentID:       "private-route",
			SessionKey:    "private-session",
			InteractionID: "runtime-owned-binding",
		},
	}
}

func TestRoleProjectionSeparatesPersonaOracleAndAuthority(t *testing.T) {
	item := rolePromptCase()
	item.Driver.Facts = map[string]string{"test-name": "private-fact-canary"}
	item.Checks = append(item.Checks, Check{ID: "hidden", Kind: "response_absent", Expected: "hidden-oracle"})
	events := rolePromptEvents()
	input, err := userInput(item, events, "question", 4)
	if err != nil || !strings.Contains(input, "test-name") || strings.Contains(input, "private-fact-canary") ||
		strings.Contains(input, "hidden-oracle") || strings.Contains(input, "private-route") ||
		strings.Contains(input, "private-session") || strings.Contains(input, "runtime-owned-binding") {
		t.Fatal("user projection leaked authority/private oracles or lost fact handles", err)
	}
	input, ids, err := judgeInput(item, events)
	if err != nil || len(ids) != 2 || !strings.Contains(input, "Another person") ||
		strings.Contains(input, "Choose another person") || strings.Contains(input, "hidden-oracle") ||
		strings.Contains(input, "test-name") || strings.Contains(input, "runtime-owned-binding") {
		t.Fatal("judge saw simulator persona, hidden answers, or authority", err)
	}
}

func TestRoleProjectionScansExcludedRevisionsAndDecodedMetadata(t *testing.T) {
	for _, location := range []string{"stream", "metadata", "task", "persona", "rubric"} {
		t.Run(location, func(t *testing.T) {
			item, events := rolePromptCase(), rolePromptEvents()
			value := "private\n\"fact\""
			item.Driver.Facts = map[string]string{"test-name": value}
			switch location {
			case "stream":
				events = append(events, Event{ID: "event-000003", Turn: 1, Kind: "stream", Content: value})
			case "metadata":
				events[1].Model = value
			case "task":
				item.Task = value
			case "persona":
				item.Driver.Persona = value
			case "rubric":
				item.Rubric[0].Description = value
			}
			if location != "rubric" {
				if _, err := userInput(item, events, "question", 4); !errors.Is(err, ErrPrivateEvidence) {
					t.Fatal("private evidence reached user context", err)
				}
			}
			if location != "persona" {
				if _, _, err := judgeInput(item, events); !errors.Is(err, ErrPrivateEvidence) {
					t.Fatal("private evidence reached judge context", err)
				}
			}
		})
	}
}

func TestRoleProjectionRejectsOverflowDuplicateAndEmptyEvidence(t *testing.T) {
	item := rolePromptCase()
	for _, events := range [][]Event{
		nil,
		{{ID: "event-000001", Turn: 1, Kind: "stream", Content: "not a finished observation"}},
		{
			{ID: "event-000001", Turn: 1, Kind: "final", Content: "one"},
			{ID: "event-000001", Turn: 1, Kind: "final", Content: "two"},
		},
		{{ID: "event-000001", Turn: 1, Kind: "final", Content: strings.Repeat("x", MaxInputBytes)}},
	} {
		if _, _, err := judgeInput(item, events); !errors.Is(err, ErrPrivateEvidence) {
			t.Fatal("incomplete/unbounded evidence admitted", err)
		}
	}
}

func TestModelUserReturnsOnlyValidatedIntent(t *testing.T) {
	for _, test := range []struct {
		output, pending, want string
	}{
		{`{"kind":"message","text":"Another person, please."}`, "question", "user_action_received"},
		{`{"kind":"protected_answer","fact":"test-name"}`, "question", "user_action_received"},
		{`{"kind":"allow_once"}`, "approval", "invalid_user_action"},
		{`{"kind":"message","text":"/answer invented allow_once"}`, "question", "invalid_user_action"},
		{`{"kind":"message","text":"private\u002dfact-canary"}`, "question", "invalid_user_action"},
		{`{"kind":"cancel","prompt_id":"invented"}`, "question", "invalid_user_action"},
	} {
		item := rolePromptCase()
		item.Driver.Facts = map[string]string{"test-name": "private-fact-canary"}
		provider := &roleTestProvider{
			mediated: true,
			response: &providers.LLMResponse{Content: test.output, FinishReason: "stop"},
		}
		role := &ModelRole{provider: provider, identity: RoleIdentity{Role: "user"}}
		result := nextModelAction(t.Context(), role, item, rolePromptEvents(), test.pending, 2)
		if result.Code != test.want || result.Call == nil || provider.calls != 1 ||
			result.Code != "user_action_received" && result.Action.Kind != "" {
			t.Fatal("unsafe or unrecorded user decision", result.Code)
		}
	}
}

func TestModelJudgePreservesHardFailuresAndRejectsInventedEvidence(t *testing.T) {
	for _, test := range []struct {
		output string
		want   Verdict
	}{
		{`{"version":1,"criteria":[{"id":"coherent","verdict":"pass","reason":"The choices agree.",` +
			`"evidence_ids":["event-000002"]}]}`, Pass},
		{`{"version":1,"criteria":[{"id":"coherent","verdict":"fail","reason":"The controls disagree.",` +
			`"evidence_ids":["event-000002"]}]}`, Fail},
		{`{"version":1,"criteria":[{"id":"coherent","verdict":"pass","reason":"Success",` +
			`"evidence_ids":["invented"]}]}`, Error},
		{`{"version":1,"criteria":[],"verdict":"pass"}`, Error},
		{`{"version":1,"criteria":[{"id":"coherent","verdict":"pass","reason":"private\u002dfact-canary",` +
			`"evidence_ids":["event-000002"]}]}`, Fail},
	} {
		item := rolePromptCase()
		item.Driver.Facts = map[string]string{"test-name": "private-fact-canary"}
		provider := &roleTestProvider{
			mediated: true,
			response: &providers.LLMResponse{Content: test.output, FinishReason: "stop"},
		}
		role := &ModelRole{provider: provider, identity: RoleIdentity{Role: "judge"}}
		result := judgeConversation(t.Context(), role, item, rolePromptEvents())
		if result.Verdict != test.want || result.Call == nil || provider.calls != 1 ||
			!validDigest(result.RubricSHA256) || !validDigest(result.EvidenceSHA256) ||
			combineVerdicts(Fail, result.Verdict) != Fail {
			t.Fatal("judge admitted invented evidence, lost identity, or hid hard failure", result.Code)
		}
		data, err := json.Marshal(result)
		if err != nil || strings.Contains(string(data), "private-fact-canary") ||
			strings.Contains(string(data), `private\u002dfact-canary`) {
			t.Fatal("invalid judge output was retained")
		}
	}
}

func TestIneligibleRoleEvidenceDoesNotCallProvider(t *testing.T) {
	item := rolePromptCase()
	item.Driver.Facts = map[string]string{"test-name": "private-fact-canary"}
	events := rolePromptEvents()
	events[1].Content = "private-fact-canary"
	provider := &roleTestProvider{mediated: true}
	user := &ModelRole{provider: provider, identity: RoleIdentity{Role: "user"}}
	judge := &ModelRole{provider: provider, identity: RoleIdentity{Role: "judge"}}
	if result := nextModelAction(
		t.Context(),
		user,
		item,
		events,
		"question",
		2,
	); result.Call != nil ||
		result.Code != "ineligible_user_evidence" {
		t.Fatal("ineligible user evidence reached a model")
	}
	if result := judgeConversation(
		t.Context(),
		judge,
		item,
		events,
	); result.Call != nil || result.Code != "ineligible_judge_evidence" ||
		provider.calls != 0 {
		t.Fatal("ineligible judge evidence reached a model")
	}
}

func TestModelJudgeRetainsVerdictsRatherThanCoercingUnverifiedClaims(t *testing.T) {
	item := rolePromptCase()
	item.Task = "Produce an independently checked synthetic artifact."
	item.Rubric = []Criterion{{ID: "artifact", Description: "The actual artifact is correct.", Required: true}}
	events := []Event{{ID: "event-000001", Turn: 1, Kind: "final", Content: "The artifact is correct."}}
	for _, verdict := range []Verdict{Pass, Fail, Inconclusive} {
		t.Run(string(verdict), func(t *testing.T) {
			data, err := json.Marshal(JudgeFindings{Version: Version, Criteria: []JudgeFinding{{
				ID: "artifact", Verdict: verdict, Reason: "No independently verified artifact was supplied.",
				EvidenceIDs: []string{"event-000001"},
			}}})
			if err != nil {
				t.Fatal(err)
			}
			provider := &roleTestProvider{
				mediated: true, response: &providers.LLMResponse{Content: string(data), FinishReason: "stop"},
			}
			judge := &ModelRole{provider: provider, identity: RoleIdentity{Role: "judge"}}
			result := judgeConversation(t.Context(), judge, item, events)
			if result.Verdict != verdict || result.Code != "judge_completed" || provider.calls != 1 ||
				len(result.Findings.Criteria) != 1 || result.Findings.Criteria[0].Verdict != verdict ||
				combineVerdicts(Fail, result.Verdict) != Fail {
				t.Fatal("judge verdict was rewritten or overrode an independent failure", result.Code)
			}
		})
	}
}
