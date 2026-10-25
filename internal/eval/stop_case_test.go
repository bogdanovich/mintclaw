package eval

import (
	"encoding/json"
	"os"
	"testing"
)

func nativeStopDefinition(t *testing.T) LoadedCase {
	t.Helper()
	data, err := os.ReadFile("../../evals/cases/general/control-stop.json")
	if err != nil {
		t.Fatal(err)
	}
	item, err := DecodeManifest[Case](data)
	if err != nil || item.Validate() != nil {
		t.Fatal("invalid native stop definition", err)
	}
	return LoadedCase{Case: item, Digest: digest(data)}
}

func TestNativeStopCaseCannotSubstituteSequentialOrLocalDriverStop(t *testing.T) {
	for _, name := range []string{
		"positive", "no_contract", "no_budget", "no_active_tool", "sequential", "driver_stop", "extra_step",
		"three_inputs", "no_judge", "no_hard_check", "same_markers", "known_marker", "private_facts", "llm_user",
	} {
		t.Run(name, func(t *testing.T) {
			item := nativeStopDefinition(t).Case
			switch name {
			case "no_contract":
				item.NativeStop = nil
			case "no_budget":
				item.Requires = []string{"conversation", activeToolCapability}
			case "no_active_tool":
				item.Requires = []string{"conversation", boundedCallsCapability}
			case "sequential":
				item.Driver.Steps[0].WaitFor.Kind = "final"
			case "driver_stop":
				item.Driver.Steps[0].Action = Action{Kind: "stop"}
			case "extra_step":
				item.Driver.Steps = append(item.Driver.Steps, item.Driver.Steps[0])
			case "three_inputs":
				item.Limits.MaxUserTurns = 3
			case "no_judge":
				item.Rubric[0].Required = false
			case "no_hard_check":
				item.Checks = item.Checks[1:]
			case "same_markers":
				item.NativeStop.QueuedMarker = item.NativeStop.ReuseMarker
			case "known_marker":
				item.Task += item.NativeStop.QueuedMarker
			case "private_facts":
				item.Driver.Facts = map[string]string{"fact": "synthetic-private-value"}
			case "llm_user":
				item.Driver = Driver{Mode: "llm_user", Persona: "A synthetic user."}
			}
			if valid := item.Validate() == nil; valid != (name == "positive") {
				t.Fatal("native stop admission changed", name, valid)
			}
		})
	}
}

func TestNativeStopRequiresExplicitSingleTurnProfile(t *testing.T) {
	item := nativeStopDefinition(t).Case
	for _, parallel := range []int{-1, 0, 1, 2} {
		preset := Preset{ActiveTool: &ActiveToolProfile{SteeringMode: "one-at-a-time", MaxParallelTurns: parallel}}
		if valid := validateNativeStopBinding(preset, item) == nil; valid != (parallel == 1) {
			t.Fatal("native capacity pin lost", parallel)
		}
	}
	if validateNativeStopBinding(Preset{}, item) == nil {
		t.Fatal("missing native fixture admitted")
	}
	preset := Preset{ActiveTool: &ActiveToolProfile{SteeringMode: "all", MaxParallelTurns: 1}}
	if validateNativeStopBinding(preset, item) == nil {
		t.Fatal("unqualified stop mode admitted")
	}
}

func stopLoadedCase(t *testing.T, item Case) LoadedCase {
	t.Helper()
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	return LoadedCase{Case: item, Digest: digest(data)}
}
