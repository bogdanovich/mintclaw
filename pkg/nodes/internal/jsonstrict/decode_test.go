package jsonstrict

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDecodeRejectsDuplicateMembersRecursively(t *testing.T) {
	for _, data := range []string{
		`{"type":"one","type":"two"}`,
		`{"schema":{"type":"one","type":"two"}}`,
		`[{"name":"one","name":"two"}]`,
	} {
		if _, err := Decode([]byte(data)); !errors.Is(err, ErrDuplicateMember) {
			t.Fatalf("Decode(%s) error = %v", data, err)
		}
	}
}

func TestDecodePreservesLargeNumbers(t *testing.T) {
	value, err := Decode([]byte(`{"maximum":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	maximum := value.(map[string]any)["maximum"]
	if maximum != json.Number("9007199254740993") {
		t.Fatalf("maximum = %#v", maximum)
	}
}

func TestCanonicalNormalizesEquivalentNumbersExactly(t *testing.T) {
	for _, data := range []string{`{"value":1}`, `{"value":1.0}`, `{"value":1e0}`} {
		canonical, err := Canonical([]byte(data))
		if err != nil {
			t.Fatal(err)
		}
		if string(canonical) != `{"value":1}` {
			t.Fatalf("Canonical(%s) = %s", data, canonical)
		}
	}
}

func TestCanonicalHandlesLargeExponentsWithoutExpansion(t *testing.T) {
	canonical, err := Canonical([]byte(`{"value":1000e999999}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != `{"value":1e1000002}` {
		t.Fatalf("Canonical() = %s", canonical)
	}
}

func TestCanonicalV2UsesPlainSyntaxForMathematicalIntegers(t *testing.T) {
	tests := map[string]string{
		`{"value":60}`:                 `{"value":60}`,
		`{"value":6e1}`:                `{"value":60}`,
		`{"value":60.0}`:               `{"value":60}`,
		`{"value":0.6e2}`:              `{"value":60}`,
		`{"value":9007199254740993.0}`: `{"value":9007199254740993}`,
		`{"value":-0e999999}`:          `{"value":0}`,
	}
	for input, want := range tests {
		got, err := CanonicalV2([]byte(input))
		if err != nil {
			t.Fatalf("CanonicalV2(%s) error = %v", input, err)
		}
		if string(got) != want {
			t.Fatalf("CanonicalV2(%s) = %s, want %s", input, got, want)
		}
	}
}

func TestCanonicalV2NormalizesFractions(t *testing.T) {
	tests := map[string]string{
		`{"value":1.25}`:     `{"value":1.25}`,
		`{"value":125e-2}`:   `{"value":1.25}`,
		`{"value":0.001}`:    `{"value":1e-3}`,
		`{"value":1e-3}`:     `{"value":1e-3}`,
		`{"value":123.4500}`: `{"value":1.2345e2}`,
	}
	for input, want := range tests {
		got, err := CanonicalV2([]byte(input))
		if err != nil {
			t.Fatalf("CanonicalV2(%s) error = %v", input, err)
		}
		if string(got) != want {
			t.Fatalf("CanonicalV2(%s) = %s, want %s", input, got, want)
		}
	}
}

func TestCanonicalV2RejectsNumbersOutsideBounds(t *testing.T) {
	inputs := []string{
		`{"value":1e4096}`,
		`{"value":1e1000001}`,
		`{"value":0.` + strings.Repeat("1", maxCanonicalSignificantDigits+1) + `}`,
	}
	for _, input := range inputs {
		if _, err := CanonicalV2([]byte(input)); err == nil {
			t.Fatalf("CanonicalV2(%s) succeeded, want bounded-number error", input)
		}
	}
}

func TestCanonicalPreservesEmptyArrays(t *testing.T) {
	canonical, err := Canonical([]byte(`{"values":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != `{"values":[]}` {
		t.Fatalf("Canonical() = %s", canonical)
	}
}
