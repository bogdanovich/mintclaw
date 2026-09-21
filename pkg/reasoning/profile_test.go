package reasoning

import (
	"slices"
	"testing"
)

func TestProfileValidationAndIntersection(t *testing.T) {
	left, err := NewProfile([]Effort{EffortLow, EffortMedium, EffortHigh}, EffortMedium, false, "left")
	if err != nil {
		t.Fatal(err)
	}
	right, err := NewProfile([]Effort{EffortMedium, EffortHigh, EffortXHigh}, EffortHigh, false, "right")
	if err != nil {
		t.Fatal(err)
	}

	intersection := Intersect(left, right)
	got := make([]Effort, 0, len(intersection.Options))
	for _, option := range intersection.Options {
		got = append(got, option.ID)
	}
	if !slices.Equal(got, []Effort{EffortMedium, EffortHigh}) || intersection.Default != EffortMedium {
		t.Fatalf("intersection = %+v", intersection)
	}
}

func TestProfileRejectsUnsupportedDefault(t *testing.T) {
	_, err := NewProfile([]Effort{EffortLow}, EffortHigh, false, "test")
	if err == nil {
		t.Fatal("NewProfile() accepted an unsupported default")
	}
}
