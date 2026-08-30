package cliui

import (
	"fmt"
	"io"
	"strings"
)

// MCPShowServer holds the server metadata for PrintMCPShow.
type MCPShowServer struct {
	Name              string
	Type              string
	Target            string
	Enabled           bool
	EffectiveDeferred bool // resolved value (per-server override or global default)
	DeferredExplicit  bool // true = per-server override set, false = inherited from global
	ExclusiveLock     bool
	EnvKeys           []string // sorted env var names (values intentionally omitted)
	EnvFile           string
	Headers           []string // sorted header names
}

// MCPShowTool holds one tool's info for PrintMCPShow.
type MCPShowTool struct {
	Name        string
	Description string
	Parameters  []MCPShowParam
}

// MCPShowParam is one parameter entry.
type MCPShowParam struct {
	Name        string
	Type        string
	Description string
	Required    bool
}

// MCPShowDiscoveryState describes the bounded result of live tool discovery.
type MCPShowDiscoveryState uint8

const (
	MCPShowDiscoveryAvailable MCPShowDiscoveryState = iota
	MCPShowDiscoveryDisabled
	MCPShowDiscoveryUnavailable
	MCPShowDiscoveryBusy
)

// PrintMCPShow renders the mcp show output.
// w is where the output is written; pass cmd.OutOrStdout() from cobra commands.
func PrintMCPShow(w io.Writer, server MCPShowServer, tools []MCPShowTool, discoveryState MCPShowDiscoveryState) {
	printMCPShowPlain(w, server, tools, discoveryState)
}

// ── plain (narrow / non-TTY) ────────────────────────────────────────────────

func printMCPShowPlain(
	w io.Writer,
	server MCPShowServer,
	tools []MCPShowTool,
	discoveryState MCPShowDiscoveryState,
) {
	fmt.Fprintf(w, "Server: %s\n", server.Name)
	fmt.Fprintf(w, "Type:   %s\n", server.Type)
	fmt.Fprintf(w, "Target: %s\n", server.Target)
	fmt.Fprintf(w, "Enabled: %s\n", boolWord(server.Enabled))
	deferredLabel := boolWord(server.EffectiveDeferred)
	if !server.DeferredExplicit {
		deferredLabel += " (default)"
	}
	fmt.Fprintf(w, "Deferred: %s\n", deferredLabel)
	fmt.Fprintf(w, "Exclusive lock: %s\n", boolWord(server.ExclusiveLock))
	if len(server.EnvKeys) > 0 {
		fmt.Fprintf(w, "Env vars: %s\n", strings.Join(server.EnvKeys, ", "))
	}
	if server.EnvFile != "" {
		fmt.Fprintf(w, "Env file: %s\n", server.EnvFile)
	}
	if len(server.Headers) > 0 {
		fmt.Fprintf(w, "Headers: %s\n", strings.Join(server.Headers, ", "))
	}
	fmt.Fprintln(w)

	if note := mcpShowDiscoveryNote(discoveryState); note != "" {
		fmt.Fprintln(w, note)
		return
	}
	if len(tools) == 0 {
		fmt.Fprintln(w, "No tools exposed by this server.")
		return
	}

	fmt.Fprintf(w, "Tools (%d):\n", len(tools))
	for _, tool := range tools {
		fmt.Fprintf(w, "  %s\n", tool.Name)
		if tool.Description != "" {
			fmt.Fprintf(w, "    %s\n", truncateDescription(tool.Description, 120))
		}
		if len(tool.Parameters) == 0 {
			fmt.Fprintln(w, "    Parameters: none")
			continue
		}
		for _, p := range tool.Parameters {
			line := fmt.Sprintf("    - %s", p.Name)
			if p.Type != "" {
				line += fmt.Sprintf(" (%s", p.Type)
				if p.Required {
					line += ", required"
				}
				line += ")"
			} else if p.Required {
				line += " (required)"
			}
			if p.Description != "" {
				line += ": " + truncateDescription(p.Description, 80)
			}
			fmt.Fprintln(w, line)
		}
	}
}

func mcpShowDiscoveryNote(state MCPShowDiscoveryState) string {
	switch state {
	case MCPShowDiscoveryDisabled:
		return "Server is disabled; skipping tool discovery."
	case MCPShowDiscoveryBusy:
		return "Tool discovery unavailable: configured exclusive lease is busy."
	case MCPShowDiscoveryUnavailable:
		return "Tool discovery unavailable: MCP connection failed."
	default:
		return ""
	}
}

// ── mcp list ────────────────────────────────────────────────────────────────

// MCPListRow is one row in the mcp list output.
type MCPListRow struct {
	Name              string
	Type              string
	Target            string
	Status            string // "enabled", "disabled", "ok (N tools)", "error"
	EffectiveDeferred bool   // resolved value (per-server override or global default)
	DeferredExplicit  bool   // true = per-server override set, false = inherited from global
}

// PrintMCPList renders the mcp list output.
func PrintMCPList(w io.Writer, rows []MCPListRow) {
	printMCPListPlain(w, rows)
}

func printMCPListPlain(w io.Writer, rows []MCPListRow) {
	headers := []string{"Name", "Type", "Command", "Status", "Deferred"}
	tableRows := make([][]string, len(rows))
	for i, r := range rows {
		deferred := boolWord(r.EffectiveDeferred)
		if !r.DeferredExplicit {
			deferred += " (default)"
		}
		tableRows[i] = []string{r.Name, r.Type, r.Target, r.Status, deferred}
	}
	// reuse the ASCII table renderer already in helpers.go via the caller
	// (list.go still uses renderTable for the plain path)
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range tableRows {
		for i, cell := range row {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	border := func() {
		fmt.Fprint(w, "+")
		for _, width := range widths {
			fmt.Fprint(w, strings.Repeat("-", width+2)+"+")
		}
		fmt.Fprintln(w)
	}
	writeRow := func(row []string) {
		fmt.Fprint(w, "|")
		for i, cell := range row {
			fmt.Fprintf(w, " %s%s |", cell, strings.Repeat(" ", widths[i]-len(cell)))
		}
		fmt.Fprintln(w)
	}
	border()
	writeRow(headers)
	border()
	for _, row := range tableRows {
		writeRow(row)
	}
	border()
}

// ── helpers ─────────────────────────────────────────────────────────────────

func boolWord(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// truncateDescription strips newlines, collapses whitespace, and caps length.
func truncateDescription(s string, maxLen int) string {
	// collapse newlines and repeated spaces into a single space
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxLen {
		return s
	}
	// cut at last space before maxLen
	cut := s[:maxLen]
	if idx := strings.LastIndex(cut, " "); idx > maxLen/2 {
		cut = cut[:idx]
	}
	return cut + "…"
}
