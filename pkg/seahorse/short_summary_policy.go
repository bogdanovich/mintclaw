package seahorse

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// SummaryPolicy selects the contract used to prepare and summarize compacted
// history. The zero value preserves the original personal-agent behavior.
type SummaryPolicy string

const (
	SummaryPolicyPersonal SummaryPolicy = ""
	SummaryPolicyCodingV1 SummaryPolicy = "coding-v1"

	codingToolResultMaxBytes  = 4096
	codingToolResultHeadBytes = 1536
	codingToolResultTailBytes = 2048
	codingArtifactMaxLines    = 4
	codingArtifactLineBytes   = 1024
)

func (p SummaryPolicy) Validate() error {
	switch p {
	case SummaryPolicyPersonal, SummaryPolicyCodingV1:
		return nil
	default:
		return fmt.Errorf("unsupported summary policy %q", p)
	}
}

// ReconciliationGeneration namespaces derived Seahorse state by summary
// policy. Changing a coding policy version rebuilds its summaries from the
// canonical session log without invalidating personal-agent summaries.
func (p SummaryPolicy) ReconciliationGeneration(base int) int {
	if p == SummaryPolicyCodingV1 {
		return base*1000 + 1
	}
	return base
}

func (p SummaryPolicy) isCoding() bool {
	return p == SummaryPolicyCodingV1
}

// projectCodingSummaryToolResults bounds historical successful tool output
// before it reaches the summarizer. It only edits cloned messages; the
// canonical JSONL and Seahorse message rows remain complete.
func projectCodingSummaryToolResults(messages []Message, policy SummaryPolicy) []Message {
	projected := cloneSeahorseMessages(messages)
	if !policy.isCoding() {
		return projected
	}

	pendingCalls := make(map[string]string)
	for i := range projected {
		if projected[i].Role == "user" {
			clear(pendingCalls)
		}
		for _, part := range projected[i].Parts {
			if part.Type == "tool_use" && part.ToolCallID != "" {
				pendingCalls[part.ToolCallID] = part.Name
			}
		}
		resultIndex, ok := singleToolResultPart(projected[i])
		if !ok {
			continue
		}
		result := &projected[i].Parts[resultIndex]
		toolName, matched := pendingCalls[result.ToolCallID]
		if matched {
			delete(pendingCalls, result.ToolCallID)
		}
		if !matched {
			continue
		}
		content := result.Text
		if content == "" {
			content = projected[i].Content
		}
		bounded := annotateCodingToolResult(toolName, result.ToolResultStatus, content)
		if result.ToolResultStatus == "success" && !isCodingMutationTool(toolName) &&
			len(content) > codingToolResultMaxBytes {
			bounded = boundCodingToolResult(toolName, result.ToolResultStatus, content)
		}
		result.Text = bounded
		projected[i].Content = bounded
		projected[i].TokenCount = EstimateMessageTokens(projected[i])
	}
	return projected
}

func annotateCodingToolResult(toolName, status, content string) string {
	if status == "" {
		status = "unknown"
	}
	return fmt.Sprintf("[coding tool result: %s; status=%s]\n%s", toolName, status, content)
}

func isCodingMutationTool(toolName string) bool {
	switch toolName {
	case "append_file", "apply_patch", "edit_file", "write_file":
		return true
	default:
		return false
	}
}

func boundCodingToolResult(toolName, status, content string) string {
	head := validSummaryUTF8Prefix(content, codingToolResultHeadBytes)
	tail := validSummaryUTF8Suffix(content, codingToolResultTailBytes)
	omitted := len(content) - len(head) - len(tail)
	if omitted < 0 {
		omitted = 0
	}
	artifacts := retainedArtifactLines(content, head, tail)
	result := fmt.Sprintf(
		"[coding tool result: %s; status=%s; original_bytes=%d]\n%s\n"+
			"... [historical tool output elided: %d bytes] ...\n%s",
		toolName,
		status,
		len(content),
		head,
		omitted,
		tail,
	)
	if len(artifacts) > 0 {
		result += "\n[retained artifact references]\n" + strings.Join(artifacts, "\n")
	}
	return result
}

func retainedArtifactLines(content, head, tail string) []string {
	seen := make(map[string]struct{})
	var result []string
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || (!strings.Contains(trimmed, "[file:") &&
			!strings.Contains(trimmed, "artifact_ref") &&
			!strings.HasPrefix(trimmed, "Structured deliverable:")) {
			continue
		}
		if strings.Contains(head, line) || strings.Contains(tail, line) {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		if len(trimmed) > codingArtifactLineBytes {
			trimmed = validSummaryUTF8Prefix(trimmed, codingArtifactLineBytes) + "... [artifact line truncated]"
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
		if len(result) == codingArtifactMaxLines {
			break
		}
	}
	return result
}

func validSummaryUTF8Prefix(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func validSummaryUTF8Suffix(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[len(value)-maxBytes:]
	for len(value) > 0 && !utf8.ValidString(value) {
		_, size := utf8.DecodeRuneInString(value)
		value = value[size:]
	}
	return value
}
