package document

import (
	"strings"
	"testing"
)

func TestFormIntakeBooleanPolicyIsTheNativeNormalizerAndReturnsAnOwnedCopy(t *testing.T) {
	policy := FormIntakeBooleanLiterals()
	if len(policy) != 10 {
		t.Fatal("boolean text policy changed without qualification")
	}
	for index, text := range policy {
		for _, variant := range []string{text, " " + strings.ToUpper(text) + " "} {
			checked, valid := deterministicBoolean(FormProtectedValue{Kind: ProtectedValueText, Text: variant})
			if !valid || checked != (index < 5) {
				t.Fatal("private literal policy diverged from native boolean validation")
			}
		}
	}
	policy[0] = "changed-by-caller"
	if FormIntakeBooleanLiterals()[0] != "yes" {
		t.Fatal("caller mutated the native policy")
	}
	for _, invalid := range []string{"yes,true", "same address", "yes no", "affirmative"} {
		if _, valid := deterministicBoolean(FormProtectedValue{Kind: ProtectedValueText, Text: invalid}); valid {
			t.Fatal("native policy began accepting invented or composite boolean values")
		}
	}
}
