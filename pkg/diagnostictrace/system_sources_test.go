package diagnostictrace

import (
	"encoding/json"
	"strings"
	"testing"
)

func observedSystemSourcesForTest() SystemSourceFingerprint {
	empty := SystemSourceGroup{SHA256: FilteredHash("[]", nil)}
	return SystemSourceFingerprint{
		Scope: SystemSourceScopeV1, Code: "observed", SystemMessages: 1, SystemParts: 1,
		Workspace:    SystemSourceGroup{Parts: 1, SHA256: FilteredHash(`["workspace"]`, nil)},
		SkillCatalog: empty, ActiveSkills: empty,
	}
}

func TestSystemSourceFingerprintValidatesObservedAbsenceAndUnknowns(t *testing.T) {
	observed := observedSystemSourcesForTest()
	if err := observed.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, reason := range []string{
		"unstructured_system", "protected_system", "invalid_sections", "section_limit", "system_content_mismatch",
	} {
		value := SystemSourceFingerprint{Scope: SystemSourceScopeV1, Code: "unavailable", Reason: reason}
		if err := value.Validate(); err != nil {
			t.Fatal(reason, err)
		}
	}
	for _, change := range []string{
		"scope", "code", "reason", "negative", "limit", "sum", "hash", "empty-hash", "zero-messages", "unknown-data",
	} {
		t.Run(change, func(t *testing.T) {
			value := observed
			switch change {
			case "scope":
				value.Scope = "unrecognized"
			case "code":
				value.Code = "partial"
			case "reason":
				value.Reason = "unexpected"
			case "negative":
				value.Workspace.Parts = -1
			case "limit":
				value.SystemParts = MaxSystemSourceParts + 1
			case "sum":
				value.Workspace.Parts = 2
			case "hash":
				value.Workspace.SHA256 = strings.Repeat("A", 64)
			case "empty-hash":
				value.SkillCatalog.SHA256 = strings.Repeat("a", 64)
			case "zero-messages":
				value.SystemMessages = 0
			case "unknown-data":
				value.Code, value.Reason = "unavailable", "protected_system"
			}
			if value.Validate() == nil {
				t.Fatal("contradictory fingerprint qualified", change)
			}
		})
	}
}

func TestModelPayloadKeepsLegacySystemSourceAbsence(t *testing.T) {
	var old ModelPayload
	if json.Unmarshal([]byte(`{"model":"legacy"}`), &old) != nil || old.SystemSources != nil {
		t.Fatal("legacy absence became observed sources")
	}
	data, err := json.Marshal(old)
	if err != nil || strings.Contains(string(data), "system_sources") {
		t.Fatal("legacy metadata-only payload changed", err)
	}
	value := observedSystemSourcesForTest()
	old.SystemSources = &value
	data, err = json.Marshal(old)
	var current ModelPayload
	if err != nil || json.Unmarshal(data, &current) != nil || current.SystemSources == nil ||
		current.SystemSources.Validate() != nil || *current.SystemSources != value {
		t.Fatal("source witness did not round-trip", err)
	}
}
