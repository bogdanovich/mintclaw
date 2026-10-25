package eval

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

func TestSteeringDefinitionsRemainPendingAndCannotWidenAdmission(t *testing.T) {
	corpus, err := LoadCorpus("../../evals")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"general.steering.boundary", "general.steering.order"} {
		items, err := Select(corpus, Selection{IDs: []string{id}})
		if err != nil || len(items) != 1 || items[0].Availability().Default ||
			!errors.Is(CheckRunAvailability(items, false), ErrPendingCases) {
			t.Fatal("unqualified steering entered defaults", id, err)
		}
		if _, err := Run(t.Context(), Preset{}, items, RunOptions{}); !errors.Is(err, ErrPendingCases) {
			t.Fatal("pending gate was bypassed", err)
		}
		if _, err := Run(
			t.Context(),
			Preset{},
			items,
			RunOptions{IncludePending: true},
		); !errors.Is(
			err,
			ErrUnsafeTarget,
		) {
			t.Fatal("pending opt-in granted native target authority", err)
		}
		for _, mode := range []string{
			"missing_budget", "missing_fixture_cap", "missing_native_check", "wrong_mode", "facts",
			"overlap", "initial_knows_update", "no_rubric", "wrong_limit", "stop_not_followup",
		} {
			t.Run(id+"/"+mode, func(t *testing.T) {
				data, _ := json.Marshal(items[0].Case)
				var item Case
				if json.Unmarshal(data, &item) != nil {
					t.Fatal("clone")
				}
				switch mode {
				case "missing_budget":
					item.Requires = slices.DeleteFunc(
						item.Requires,
						func(v string) bool { return v == boundedCallsCapability },
					)
				case "missing_fixture_cap":
					item.Requires = slices.DeleteFunc(
						item.Requires,
						func(v string) bool { return v == activeToolCapability },
					)
				case "missing_native_check":
					item.Checks = slices.DeleteFunc(
						item.Checks,
						func(v Check) bool { return v.Kind == "steering_boundary" },
					)
				case "wrong_mode":
					item.Steering.Modes = []string{"anonymous"}
				case "facts":
					item.Driver.Facts = map[string]string{"fact": "synthetic"}
				case "overlap":
					item.Steering.Updates[0] = item.Steering.InitialMarker + "-suffix"
				case "initial_knows_update":
					item.Task += " " + item.Steering.Updates[0]
				case "no_rubric":
					item.Rubric = nil
				case "wrong_limit":
					item.Limits.MaxUserTurns++
				case "stop_not_followup":
					item.Driver.Steps[0].Action = Action{Kind: "stop"}
				}
				if !errors.Is(item.Validate(), ErrInvalidCase) {
					t.Fatal("invalid or widened steering definition accepted")
				}
			})
		}
	}
}
