package eval

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func readRecoveryDefinition(t *testing.T) LoadedCase {
	t.Helper()
	corpus, err := LoadCorpus("../../evals")
	if err != nil {
		t.Fatal(err)
	}
	items, err := Select(corpus, Selection{IDs: []string{"general.tools.recovery"}})
	if err != nil || len(items) != 1 {
		t.Fatal("read recovery definition unavailable", err)
	}
	return items[0]
}

func TestReadRecoveryDefinitionStaysPendingAndCannotLoseNativeRequirements(t *testing.T) {
	loaded := readRecoveryDefinition(t)
	if loaded.Case.Validate() != nil || loaded.Availability().Default ||
		!errors.Is(CheckRunAvailability([]LoadedCase{loaded}, false), ErrPendingCases) {
		t.Fatal("unqualified recovery entered defaults or lost validation")
	}
	for _, kind := range []string{
		"missing_spec", "missing_cap", "missing_budget", "missing_native_check", "wrong_fixture",
		"known_marker", "private_facts", "no_rubric", "extra_input", "unbounded_time",
	} {
		t.Run(kind, func(t *testing.T) {
			data, _ := json.Marshal(loaded.Case)
			var item Case
			if json.Unmarshal(data, &item) != nil {
				t.Fatal("clone")
			}
			switch kind {
			case "missing_spec":
				item.ReadRecovery = nil
			case "missing_cap":
				item.Requires = slices.DeleteFunc(
					item.Requires,
					func(value string) bool { return value == readToolCapability },
				)
			case "missing_budget":
				item.Requires = slices.DeleteFunc(
					item.Requires,
					func(value string) bool { return value == boundedCallsCapability },
				)
			case "missing_native_check":
				item.Checks = slices.DeleteFunc(
					item.Checks,
					func(check Check) bool { return check.Kind == "read_recovery" },
				)
			case "wrong_fixture":
				item.Fixtures[0].SHA256 = digest([]byte("wrong synthetic file\n"))
			case "known_marker":
				item.Task += " " + item.ReadRecovery.Marker
			case "private_facts":
				item.Driver.Facts = map[string]string{"fact": "synthetic-private-value"}
			case "no_rubric":
				item.Rubric = nil
			case "extra_input":
				item.Limits.MaxUserTurns++
			case "unbounded_time":
				item.Limits.CaseTimeout = "1h"
			}
			if !errors.Is(item.Validate(), ErrInvalidCase) {
				t.Fatal("missing evidence requirement or wider contract accepted")
			}
		})
	}
}

func TestRecoveryHistoryRequiresOneErrorThenOneSuccessAcrossActualUserTurns(t *testing.T) {
	const marker = "mc-c3-unit-marker"
	inputs := []string{"missing input", "corrected path", "recall value"}
	for _, kind := range []string{
		"positive", "missing_was_success", "corrected_was_error", "wrong_path", "no_result", "duplicate_result",
		"extra_call", "same_call_id", "early_value", "lost_continuation", "stale_final", "call_on_tool_role",
	} {
		t.Run(kind, func(t *testing.T) {
			history := []providers.Message{
				{Role: "user", Content: inputs[0]},
				{
					Role: "assistant",
					ToolCalls: []providers.ToolCall{
						{ID: "read-missing", Name: "read_file", Arguments: map[string]any{"path": readMissingPath}},
					},
				},
				{
					Role:             "tool",
					ToolCallID:       "read-missing",
					ToolResultStatus: providers.ToolResultStatusError,
					Content:          "file does not exist",
				},
				{Role: "assistant", Content: "The file could not be read. Please provide the corrected path."},
				{Role: "user", Content: inputs[1]},
				{
					Role: "assistant",
					ToolCalls: []providers.ToolCall{
						{ID: "read-corrected", Name: "read_file", Arguments: map[string]any{"path": readFixturePath}},
					},
				},
				{
					Role:             "tool",
					ToolCallID:       "read-corrected",
					ToolResultStatus: providers.ToolResultStatusSuccess,
					Content:          marker,
				},
				{Role: "assistant", Content: marker},
				{Role: "user", Content: inputs[2]},
				{Role: "assistant", Content: marker},
			}
			switch kind {
			case "missing_was_success":
				history[2].ToolResultStatus = providers.ToolResultStatusSuccess
			case "corrected_was_error":
				history[6].ToolResultStatus = providers.ToolResultStatusError
			case "wrong_path":
				history[5].ToolCalls[0].Arguments["path"] = readMissingPath
			case "no_result":
				history = slices.Delete(history, 2, 3)
			case "duplicate_result":
				history = slices.Insert(history, 3, history[2])
			case "extra_call":
				history[1].ToolCalls = append(history[1].ToolCalls, history[1].ToolCalls[0])
			case "same_call_id":
				history[5].ToolCalls[0].ID, history[6].ToolCallID = "read-missing", "read-missing"
			case "early_value":
				history[3].Content = marker
			case "lost_continuation":
				history = slices.Delete(history, 3, 4)
			case "stale_final":
				history[9].Content = "invented value"
			case "call_on_tool_role":
				history[2].ToolCalls = history[1].ToolCalls
			}
			if validRecoveryHistory(history, inputs, marker) != (kind == "positive") {
				t.Fatal("unpaired, replayed, wrongly ordered or invented recovery passed")
			}
		})
	}
}
