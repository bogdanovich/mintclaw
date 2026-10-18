package tasks

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func TestEventByteBudgetRetainsLongestSuffixAtExactEncodedBoundaries(t *testing.T) {
	for name, text := range map[string]string{
		"plain":         "synthetic progress",
		"escaping":      "quote\" newline\n slash\\ <>&",
		"unicode":       "日本語 Δοκιμή",
		"invalid UTF-8": string([]byte{0xff, 0xfe, 'x'}),
	} {
		t.Run(name, func(t *testing.T) {
			registry := NewRegistry("")
			if err := registry.Create(
				Record{TaskID: "kept-task", Task: "protected", Status: StatusRunning},
			); err != nil {
				t.Fatal(err)
			}
			for index := range 12 {
				if err := registry.AppendEvent("kept-task", EventTaskProgress, map[string]string{
					"progress": fmt.Sprintf("%d:%s", index, text),
				}); err != nil {
					t.Fatal(err)
				}
			}
			original := registry.snapshotLocked()
			encodedSize := func(drop int) int {
				t.Helper()
				candidate := Snapshot{Tasks: original.Tasks, Events: original.Events[drop:]}
				data, err := json.MarshalIndent(candidate, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				return len(data)
			}
			budgets := map[int]bool{1: true}
			for drop := 0; drop <= len(original.Events); drop++ {
				for _, delta := range []int{-1, 0, 1} {
					budgets[encodedSize(drop)+delta] = true
				}
			}
			for budget := range budgets {
				// The oracle serializes each possible persisted suffix, independently
				// of the production algorithm's per-event byte accounting.
				wantDrop := 0
				for wantDrop < len(original.Events) && encodedSize(wantDrop) > budget {
					wantDrop++
				}
				registry.options.MaxSnapshotBytes = budget
				registry.events = append([]TaskEvent(nil), original.Events...)
				changed := registry.trimEventsForSnapshotBudgetLocked()
				if changed != (wantDrop > 0) || !reflect.DeepEqual(registry.events, original.Events[wantDrop:]) {
					t.Fatalf("budget=%d retained wrong event suffix; want drop=%d", budget, wantDrop)
				}
				if len(registry.records) != 1 || registry.records["kept-task"].Task != "protected" {
					t.Fatalf("budget=%d changed protected task state", budget)
				}
			}
		})
	}
}
