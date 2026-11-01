package diagnostictrace

import "errors"

const (
	SystemSourceScopeV1  = "runtime_labeled_system_content_v1"
	MaxSystemSourceParts = 64
)

type SystemSourceGroup struct {
	Parts  int    `json:"parts"`
	SHA256 string `json:"sha256,omitempty"`
}

// SystemSourceFingerprint identifies filtered content in runtime-labeled system
// sections, not files on disk, individual skill manifests or provider wire bytes.
type SystemSourceFingerprint struct {
	Scope          string            `json:"scope"`
	Code           string            `json:"code"`
	Reason         string            `json:"reason,omitempty"`
	SystemMessages int               `json:"system_messages"`
	SystemParts    int               `json:"system_parts"`
	Workspace      SystemSourceGroup `json:"workspace"`
	SkillCatalog   SystemSourceGroup `json:"skill_catalog"`
	ActiveSkills   SystemSourceGroup `json:"active_skills"`
}

func (value SystemSourceFingerprint) Validate() error {
	invalid := errors.New("invalid system source fingerprint")
	if value.Scope != SystemSourceScopeV1 {
		return invalid
	}
	if value.Code == "unavailable" {
		switch value.Reason {
		case "unstructured_system", "protected_system", "invalid_sections", "section_limit", "system_content_mismatch":
			if value.SystemMessages == 0 && value.SystemParts == 0 &&
				value.Workspace == (SystemSourceGroup{}) && value.SkillCatalog == (SystemSourceGroup{}) &&
				value.ActiveSkills == (SystemSourceGroup{}) {
				return nil
			}
		}
		return invalid
	}
	if value.Code != "observed" || value.Reason != "" || value.SystemMessages < 0 ||
		value.SystemParts < value.SystemMessages || value.SystemParts > MaxSystemSourceParts ||
		(value.SystemMessages == 0) != (value.SystemParts == 0) {
		return invalid
	}
	parts := 0
	for _, group := range []SystemSourceGroup{value.Workspace, value.SkillCatalog, value.ActiveSkills} {
		if group.Parts < 0 || group.Parts > MaxSystemSourceParts || !validHexRange(group.SHA256, 64, 64) ||
			group.Parts == 0 && group.SHA256 != FilteredHash("[]", nil) {
			return invalid
		}
		parts += group.Parts
	}
	if parts > value.SystemParts {
		return invalid
	}
	return nil
}
