package agent

import (
	"testing"

	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestCodingToolObservationIsCodingOnlyAndCloned(t *testing.T) {
	exitCode := 7
	observation := &toolshared.ToolObservation{Command: &toolshared.CommandObservation{
		Output: "bounded", Status: "failed", ExitCode: &exitCode,
	}}
	if got := codingToolObservation(&turnState{}, observation); got != nil {
		t.Fatalf("personal turn observation = %#v", got)
	}
	ts := &turnState{opts: processOptions{CodingContext: CodingPromptContext{SessionKey: "thread-1"}}}
	got := codingToolObservation(ts, observation)
	if got == nil || got.Command == nil || got.Command.Output != "bounded" || got.Command.ExitCode == nil ||
		*got.Command.ExitCode != 7 {
		t.Fatalf("coding turn observation = %#v", got)
	}
	observation.Command.Output = "mutated"
	exitCode = 9
	if got.Command.Output != "bounded" || *got.Command.ExitCode != 7 {
		t.Fatalf("coding observation aliases tool result = %#v", got)
	}
}
