// MintClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package cliprovider

import (
	"encoding/json"
	"fmt"
	"strings"
)

// buildCLIToolsPrompt creates the tool definitions section for a CLI provider system prompt.
func buildCLIToolsPrompt(tools []ToolDefinition) string {
	var sb strings.Builder

	sb.WriteString("## Available Tools\n\n")
	sb.WriteString("When you need to use a tool, respond with ONLY a JSON object:\n\n")
	sb.WriteString("```json\n")
	sb.WriteString(
		`{"tool_calls":[{"id":"call_xxx","type":"function","function":{"name":"tool_name","arguments":"{...}"}}]}`,
	)
	sb.WriteString("\n```\n\n")
	sb.WriteString("CRITICAL: The 'arguments' field MUST be a JSON-encoded STRING.\n\n")
	sb.WriteString("Escaping rules (what to type in `function.arguments`):\n")
	sb.WriteString("- Use `\\n` to represent a real newline character.\n")
	sb.WriteString("- Use `\\\\n` to represent a literal backslash+n sequence (`\\n`).\n")
	sb.WriteString(
		"- `function.arguments` is a JSON-encoded string, so quotes/backslashes must be escaped in the outer payload.\n\n",
	)
	sb.WriteString("### Tool Definitions:\n\n")

	for _, tool := range tools {
		if tool.Type != "function" {
			continue
		}
		fmt.Fprintf(&sb, "#### %s\n", tool.Function.Name)
		if tool.Function.Description != "" {
			fmt.Fprintf(&sb, "Description: %s\n", tool.Function.Description)
		}
		if len(tool.Function.Parameters) > 0 {
			paramsJSON, _ := json.Marshal(tool.Function.Parameters)
			fmt.Fprintf(&sb, "Parameters:\n```json\n%s\n```\n", string(paramsJSON))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
