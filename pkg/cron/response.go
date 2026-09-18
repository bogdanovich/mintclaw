package cron

import "strings"

// AgentTurnControl identifies a terminal control outcome emitted by a
// scheduled agent turn. Control outcomes are protocol data, not user-facing
// content, and must never be published to the destination channel.
type AgentTurnControl string

const (
	AgentTurnControlNone        AgentTurnControl = ""
	AgentTurnControlNoReply     AgentTurnControl = "NO_REPLY"
	AgentTurnControlHeartbeatOK AgentTurnControl = "HEARTBEAT_OK"
)

// AgentTurnControlMatch describes a recognized terminal control outcome.
// Canonical is false when the runtime recovered a valid terminal signal from
// an otherwise non-conforming response, such as explanatory prose followed by
// NO_REPLY. Callers should suppress delivery in both cases and may report the
// non-canonical response as a protocol violation.
type AgentTurnControlMatch struct {
	Control   AgentTurnControl
	Canonical bool
}

var agentTurnControls = [...]AgentTurnControl{
	AgentTurnControlNoReply,
	AgentTurnControlHeartbeatOK,
}

// MatchAgentTurnControl recognizes a scheduled-turn control outcome only when
// it occupies the complete response or the final standalone logical line. It
// deliberately does not search for control words inside prose, so ordinary
// user-facing content that merely mentions a marker remains deliverable.
//
// Markdown inline-code and single-value fenced-code forms are accepted because
// models sometimes format protocol tokens despite being asked for plain text.
func MatchAgentTurnControl(response string) (AgentTurnControlMatch, bool) {
	trimmed := strings.TrimSpace(response)
	if trimmed == "" {
		return AgentTurnControlMatch{}, false
	}

	if control, ok := parseAgentTurnControl(trimmed); ok {
		return AgentTurnControlMatch{Control: control, Canonical: trimmed == string(control)}, true
	}

	lastLine := trimmed
	if newline := strings.LastIndexByte(trimmed, '\n'); newline >= 0 {
		lastLine = strings.TrimSpace(trimmed[newline+1:])
	}
	if control, ok := parseAgentTurnControl(lastLine); ok {
		return AgentTurnControlMatch{Control: control, Canonical: false}, true
	}

	if control, ok := parseTerminalControlFence(trimmed); ok {
		return AgentTurnControlMatch{Control: control, Canonical: false}, true
	}

	return AgentTurnControlMatch{}, false
}

func parseAgentTurnControl(candidate string) (AgentTurnControl, bool) {
	candidate = strings.TrimSpace(candidate)
	if len(candidate) >= 2 && candidate[0] == '`' && candidate[len(candidate)-1] == '`' {
		candidate = strings.TrimSpace(candidate[1 : len(candidate)-1])
	}
	for _, control := range agentTurnControls {
		if strings.EqualFold(candidate, string(control)) {
			return control, true
		}
	}
	return AgentTurnControlNone, false
}

func parseTerminalControlFence(response string) (AgentTurnControl, bool) {
	lines := strings.Split(response, "\n")
	if len(lines) < 3 {
		return AgentTurnControlNone, false
	}
	closing := strings.TrimSpace(lines[len(lines)-1])
	if closing != "```" && closing != "~~~" {
		return AgentTurnControlNone, false
	}
	for index := len(lines) - 2; index >= 0; index-- {
		opener := strings.TrimSpace(lines[index])
		if !strings.HasPrefix(opener, closing) {
			continue
		}
		body := strings.TrimSpace(strings.Join(lines[index+1:len(lines)-1], "\n"))
		if strings.Contains(body, "\n") {
			return AgentTurnControlNone, false
		}
		return parseAgentTurnControl(body)
	}
	return AgentTurnControlNone, false
}
