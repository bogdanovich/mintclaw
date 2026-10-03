package protocoltypes

import (
	"reflect"
	"testing"
)

func TestPromptCachePlanOptionsDetachAndValidate(t *testing.T) {
	indexes := []int{4, 1, 4}
	options := map[string]any{}
	SetPromptCachePlan(options, PromptCachePlan{
		Version:                  PromptCachePlanVersion1,
		LineageKey:               "opaque-lineage",
		WritePolicy:              PromptCacheWriteReuse,
		BreakpointMessageIndexes: indexes,
	})
	indexes[0] = 99

	plan, ok := PromptCachePlanFromOptions(options)
	if !ok {
		t.Fatal("PromptCachePlanFromOptions() rejected a valid plan")
	}
	if !reflect.DeepEqual(plan.BreakpointMessageIndexes, []int{4, 1}) {
		t.Fatalf("breakpoint indexes = %v, want detached stable order", plan.BreakpointMessageIndexes)
	}
	plan.BreakpointMessageIndexes[0] = 88
	second, ok := PromptCachePlanFromOptions(options)
	if !ok || second.BreakpointMessageIndexes[0] != 4 {
		t.Fatalf("stored plan was mutated through read: %#v", second)
	}

	ClearPromptCachePlan(options)
	if _, ok := PromptCachePlanFromOptions(options); ok {
		t.Fatal("cleared prompt cache plan remained visible")
	}
}

func TestPromptCachePlanOptionsFailClosed(t *testing.T) {
	tests := []PromptCachePlan{
		{Version: 2, LineageKey: "key", WritePolicy: PromptCacheWriteReuse},
		{Version: PromptCachePlanVersion1, WritePolicy: PromptCacheWriteReuse},
		{Version: PromptCachePlanVersion1, LineageKey: "key", WritePolicy: "future"},
		{
			Version: PromptCachePlanVersion1, LineageKey: "key", WritePolicy: PromptCacheWriteReuse,
			BreakpointMessageIndexes: []int{-1},
		},
	}
	for _, plan := range tests {
		options := map[string]any{}
		SetPromptCachePlan(options, plan)
		if !PromptCachePlanOptionPresent(options) {
			t.Fatal("PromptCachePlanOptionPresent() = false for supplied invalid plan")
		}
		if got, ok := PromptCachePlanFromOptions(options); ok {
			t.Fatalf("malformed plan accepted: %#v", got)
		}
	}
}

func TestPromptCachePlanOptionPresence(t *testing.T) {
	if PromptCachePlanOptionPresent(nil) {
		t.Fatal("PromptCachePlanOptionPresent(nil) = true")
	}
	options := make(map[string]any)
	if PromptCachePlanOptionPresent(options) {
		t.Fatal("PromptCachePlanOptionPresent(empty) = true")
	}
	SetPromptCachePlan(options, PromptCachePlan{Version: PromptCachePlanVersion1 + 1})
	if !PromptCachePlanOptionPresent(options) {
		t.Fatal("PromptCachePlanOptionPresent(future plan) = false")
	}
	ClearPromptCachePlan(options)
	if PromptCachePlanOptionPresent(options) {
		t.Fatal("PromptCachePlanOptionPresent(cleared) = true")
	}
	DisablePromptCache(options)
	if !PromptCachePlanOptionPresent(options) {
		t.Fatal("PromptCachePlanOptionPresent(disabled) = false")
	}
	if plan, ok := PromptCachePlanFromOptions(options); ok {
		t.Fatalf("disabled cache marker exposed a provider plan: %#v", plan)
	}
}

func TestPromptCacheNoWriteDropsBreakpoints(t *testing.T) {
	options := map[string]any{}
	SetPromptCachePlan(options, PromptCachePlan{
		Version:                  PromptCachePlanVersion1,
		LineageKey:               "opaque-lineage",
		WritePolicy:              PromptCacheWriteNoWrite,
		BreakpointMessageIndexes: []int{1, 2},
	})
	plan, ok := PromptCachePlanFromOptions(options)
	if !ok || len(plan.BreakpointMessageIndexes) != 0 {
		t.Fatalf("no-write plan retained breakpoints: %#v", plan)
	}
}
