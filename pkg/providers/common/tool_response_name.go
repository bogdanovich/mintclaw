package common

import "strings"

// ResolveToolResponseName returns the recorded name for a tool call. Gemini
// APIs omit the name from tool-result messages, so their adapters retain an
// ID-to-name map and use the call ID only as a last resort.
func ResolveToolResponseName(toolCallID string, toolCallNames map[string]string) string {
	if toolCallID == "" {
		return ""
	}
	if name, ok := toolCallNames[toolCallID]; ok && name != "" {
		return name
	}
	return inferToolNameFromCallID(toolCallID)
}

func inferToolNameFromCallID(toolCallID string) string {
	if !strings.HasPrefix(toolCallID, "call_") {
		return toolCallID
	}
	rest := strings.TrimPrefix(toolCallID, "call_")
	if index := strings.LastIndex(rest, "_"); index > 0 {
		if name := rest[:index]; name != "" {
			return name
		}
	}
	return toolCallID
}
