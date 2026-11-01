package agent

import (
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/providers"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// A typed native grant, unlike generic extracted document text, is a current
// user input. Spend its existing whole-text allowance once, then separate its
// instruction from its body before constructing any model/journal message.
func (p *Pipeline) splitLiveUserInput(input *toolshared.ToolContextInput, admitted string) (string, string, error) {
	if input == nil || input.Text == "" {
		return admitted, "", nil
	}
	if !strings.HasSuffix(admitted, input.Text) || !utf8.ValidString(input.Text) {
		return "", "", errors.New("authorized live user input was not admitted in full")
	}
	text := p.filterToolContentForLLM(input.Text)
	if len(text) > maxLiveToolContextTextBytes || !utf8.ValidString(text) {
		return "", "", errors.New("authorized live user input exceeds its model bound")
	}
	return strings.TrimSuffix(admitted, input.Text), text, nil
}

func hasLiveUserInput(projections []liveToolContextProjection) bool {
	for _, projection := range projections {
		if projection.turnScopedInput && projection.userInputText != "" {
			return true
		}
	}
	return false
}

// Provider-only user messages are absent from canonical history. Reserve their
// actual message cost during rebuilds without changing the output-token limit.
func liveUserInputTokenReserve(projections []liveToolContextProjection) int {
	tokens := 0
	for _, projection := range projections {
		if projection.turnScopedInput && projection.userInputText != "" {
			tokens += EstimateMessageTokens(providers.Message{Role: "user", Content: projection.userInputText})
		}
	}
	return tokens
}

func liveUserInputAnchor(messages []providers.Message, projection liveToolContextProjection) (int, error) {
	anchor := -1
	for index, message := range messages {
		if projection.matches(message) {
			if anchor >= 0 {
				return -1, errors.New("authorized live user input has ambiguous ownership")
			}
			anchor = index
		}
	}
	if anchor < 0 {
		return -1, errors.New("authorized live user input owner is unavailable")
	}
	return anchor, nil
}

// Hooks see detached provider-shaped copies. Restore only the identity of an
// unchanged, already-owned native grant; never borrow an older reused call ID.
func restoreLiveUserInputIdentities(before, after []providers.Message, projections []liveToolContextProjection) error {
	for _, projection := range projections {
		if !projection.turnScopedInput || projection.userInputText == "" {
			continue
		}
		anchor, err := liveUserInputAnchor(before, projection)
		if err != nil {
			return err
		}
		if anchor >= len(after) || !llmHookMessagePayloadUnchanged(before[anchor], after[anchor]) {
			return errors.New("before_llm changed an authorized live input owner")
		}
		after[anchor].LiveToolContextID = before[anchor].LiveToolContextID
	}
	return nil
}

// Only a detached provider request gets the extra user message. Insert after
// the complete tool-result block so pairing and later steering order survive.
func projectLiveUserInputs(
	messages []providers.Message,
	projections []liveToolContextProjection,
) ([]providers.Message, error) {
	type insertion struct {
		after  int
		anchor int
		text   string
	}
	var insertions []insertion
	for _, projection := range projections {
		if !projection.turnScopedInput || projection.userInputText == "" {
			continue
		}
		anchor, err := liveUserInputAnchor(messages, projection)
		if err != nil {
			return nil, err
		}
		for _, existing := range insertions {
			if existing.anchor == anchor {
				return nil, errors.New("authorized live user input owner was repeated")
			}
		}
		after := anchor + 1
		for after < len(messages) && messages[after].Role == "tool" {
			after++
		}
		insertions = append(insertions, insertion{after: after, anchor: anchor, text: projection.userInputText})
	}
	if len(insertions) == 0 {
		return messages, nil
	}
	sort.Slice(insertions, func(i, j int) bool { return insertions[i].anchor < insertions[j].anchor })
	projected := make([]providers.Message, 0, len(messages)+len(insertions))
	for index, message := range messages {
		projected = append(projected, message)
		for _, input := range insertions {
			if input.after == index+1 {
				projected = append(projected, providers.Message{Role: "user", Content: input.text})
			}
		}
	}
	return projected, nil
}
