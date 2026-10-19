package agent

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func unavailableSystemSources(reason string) *diagnostictrace.SystemSourceFingerprint {
	return &diagnostictrace.SystemSourceFingerprint{
		Scope: diagnostictrace.SystemSourceScopeV1, Code: "unavailable", Reason: reason,
	}
}

func fingerprintSystemSources(
	settings traceCaptureSettings, messages []providers.Message,
) *diagnostictrace.SystemSourceFingerprint {
	if !settings.enabled {
		return nil
	}
	classifications := diagnosticMessageClassifications(messages)
	result := &diagnostictrace.SystemSourceFingerprint{Scope: diagnostictrace.SystemSourceScopeV1, Code: "observed"}
	workspace, catalog, active := []string{}, []string{}, []string{}
	for index, message := range messages {
		if message.Role != "system" {
			continue
		}
		if classifications[index].sensitive {
			return unavailableSystemSources("protected_system")
		}
		result.SystemMessages++
		if len(message.SystemParts) == 0 {
			return unavailableSystemSources("unstructured_system")
		}
		if len(message.SystemParts) > diagnostictrace.MaxSystemSourceParts {
			return unavailableSystemSources("section_limit")
		}
		texts := make([]string, 0, len(message.SystemParts))
		for _, block := range message.SystemParts {
			texts = append(texts, block.Text)
			if block.Type != "text" || len(block.PromptSections) == 0 {
				return unavailableSystemSources("unstructured_system")
			}
			if len(block.PromptSections) > diagnostictrace.MaxSystemSourceParts-result.SystemParts {
				return unavailableSystemSources("section_limit")
			}
			end := 0
			for sectionIndex, section := range block.PromptSections {
				start := end
				if sectionIndex > 0 {
					start += len(promptPartSeparator)
				}
				if section.Start != start || section.End <= start || section.End > len(block.Text) ||
					sectionIndex > 0 && block.Text[end:start] != promptPartSeparator ||
					!validSystemSourceLabel(section.Layer) || !validSystemSourceLabel(section.Slot) ||
					!validSystemSourceLabel(section.Source) {
					return unavailableSystemSources("invalid_sections")
				}
				text := block.Text[start:section.End]
				if !utf8.ValidString(text) {
					return unavailableSystemSources("invalid_sections")
				}
				// Filter decoded section text before JSON encoding, including
				// literals containing quotes/backslashes escaped by Marshal.
				if settings.filter != nil {
					text = settings.filter(text)
				}
				switch PromptSourceID(section.Source) {
				case PromptSourceWorkspace:
					if section.Layer != string(PromptLayerInstruction) || section.Slot != string(PromptSlotWorkspace) {
						return unavailableSystemSources("invalid_sections")
					}
					workspace = append(workspace, text)
				case PromptSourceSkillCatalog:
					if section.Layer != string(PromptLayerCapability) ||
						section.Slot != string(PromptSlotSkillCatalog) {
						return unavailableSystemSources("invalid_sections")
					}
					catalog = append(catalog, text)
				case PromptSourceActiveSkills:
					if section.Layer != string(PromptLayerCapability) || section.Slot != string(PromptSlotActiveSkill) {
						return unavailableSystemSources("invalid_sections")
					}
					active = append(active, text)
				}
				end = section.End
				result.SystemParts++
			}
			if end != len(block.Text) {
				return unavailableSystemSources("invalid_sections")
			}
		}
		if strings.Join(texts, promptPartSeparator) != message.Content {
			return unavailableSystemSources("system_content_mismatch")
		}
	}
	group := func(texts []string) diagnostictrace.SystemSourceGroup {
		return diagnostictrace.SystemSourceGroup{Parts: len(texts), SHA256: safeJSONHash(traceCaptureSettings{}, texts)}
	}
	result.Workspace, result.SkillCatalog, result.ActiveSkills = group(workspace), group(catalog), group(active)
	if result.Validate() != nil {
		return unavailableSystemSources("invalid_sections")
	}
	return result
}

func validSystemSourceLabel(value string) bool {
	return len(value) > 0 && len(value) <= 128 && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func diagnosticSystemSources(
	settings traceCaptureSettings, value *diagnostictrace.SystemSourceFingerprint,
) *diagnostictrace.SystemSourceFingerprint {
	if value == nil {
		return nil
	}
	if value.Validate() != nil {
		return unavailableSystemSources("invalid_sections")
	}
	if settings.filter != nil {
		for _, group := range []diagnostictrace.SystemSourceGroup{value.Workspace, value.SkillCatalog, value.ActiveSkills} {
			if settings.filter(group.SHA256) != group.SHA256 {
				return unavailableSystemSources("invalid_sections")
			}
		}
	}
	copy := *value
	return &copy
}
