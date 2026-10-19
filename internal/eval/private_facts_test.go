package eval

import "testing"

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
}
