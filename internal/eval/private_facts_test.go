package eval

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestMaterializePrivateFactsLeavesSourceAndTrialsIndependent(t *testing.T) {
	source := map[string]string{"name": generatePrivateFact, "browser": "{{generate}}", "note": "fixed test value"}
	first, err := materializePrivateFacts(source)
	if err != nil {
		t.Fatal(err)
	}
	second, err := materializePrivateFacts(source)
	if err != nil || first["name"] == second["name"] || first["name"] == generatePrivateFact ||
		source["name"] != generatePrivateFact || first["note"] != "fixed test value" || privateFactPlaceholder(first["browser"]) ||
		first["browser"] == second["browser"] || first["browser"] == first["name"] {
		t.Fatal("facts were reused or changed")
	}
	clear(first)
	if second["note"] != "fixed test value" || source["note"] != "fixed test value" {
		t.Fatal("trial aliases source")
	}
	for _, handle := range []string{"name", "browser"} {
		value := second[handle]
		// The public v1 family is also the value-free offline regrade guard.
		if len(value) != 24 || !strings.HasPrefix(value, "MC_EVAL_") {
			t.Fatal("generated fact no longer follows its v1 privacy contract")
		}
		if decoded, err := hex.DecodeString(value[8:]); err != nil || len(decoded) != 8 {
			t.Fatal("generated fact is not the declared random v1 format")
		}
	}
}
