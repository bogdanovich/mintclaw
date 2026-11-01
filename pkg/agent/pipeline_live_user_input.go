package agent

import (
	"errors"
	"sort"
	"strings"
	"unicode"
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

func (p *Pipeline) liveUserInputSources(
	input *toolshared.ToolContextInput, filteredText string,
) ([]toolshared.ToolContextInputSource, error) {
	if input == nil || len(input.Sources) == 0 {
		return nil, nil
	}
	if len(input.Sources) > 2 {
		return nil, errors.New("native input has too many source utterances")
	}
	sources := append([]toolshared.ToolContextInputSource(nil), input.Sources...)
	texts := make([]string, 0, len(sources))
	seen := make(map[string]bool)
	for index := range sources {
		source := &sources[index]
		if len(source.ConversationInputID) > 256 || !utf8.ValidString(source.ConversationInputID) ||
			!utf8.ValidString(source.Text) {
			return nil, errors.New("native input source is invalid")
		}
		if source.ConversationInputID != "" {
			if seen[source.ConversationInputID] {
				return nil, errors.New("native input source identity was repeated")
			}
			seen[source.ConversationInputID] = true
		}
		source.Text = p.filterToolContentForLLM(source.Text)
		texts = append(texts, source.Text)
	}
	joined := strings.Join(texts, "\n\n")
	start, end := 0, len(joined)
	if joined != filteredText {
		start = len(joined) - len(strings.TrimLeftFunc(joined, unicode.IsSpace))
		end = len(strings.TrimRightFunc(joined, unicode.IsSpace))
	}
	if end < start || joined[start:end] != filteredText {
		return nil, errors.New("native input sources changed the whole admitted request")
	}
	// Preserve internal whitespace. Only the same outer trimming already made
	// by the native grant may be removed, keeping projected text within the
	// existing whole-request byte allowance.
	offset := 0
	for index := range sources {
		length := len(sources[index].Text)
		left, right := max(offset, start), min(offset+length, end)
		sources[index].Text = ""
		if left < right {
			sources[index].Text = joined[left:right]
		}
		offset += length + 2
	}
	return sources, nil
}

// Source identities, not repeated text or provider call IDs, bind a grant to
// ordinary owner history. The full body may have a frozen steering wrapper or
// explicit answer-command prefix; it must still be present in its owned frame.
func missingLiveUserInputTexts(
	messages []providers.Message, projection liveToolContextProjection,
) ([]string, error) {
	if len(projection.userInputSources) == 0 {
		return []string{projection.userInputText}, nil
	}
	var missing []string
	for _, source := range projection.userInputSources {
		if strings.TrimSpace(source.Text) == "" {
			continue
		}
		found := false
		if source.ConversationInputID != "" {
			for _, message := range messages {
				if message.Role != "user" || documentConversationID(message) != source.ConversationInputID {
					continue
				}
				if found || !strings.Contains(message.Content, source.Text) {
					return nil, errors.New("native input source has ambiguous or changed ordinary history")
				}
				found = true
			}
		}
		if !found {
			missing = append(missing, source.Text)
		}
	}
	return missing, nil
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
	return liveUserInputTokenReserveForHistory(projections, nil)
}

func liveUserInputTokenReserveForHistory(
	projections []liveToolContextProjection, protectedHistory []providers.Message,
) int {
	tokens := 0
	for _, projection := range projections {
		if projection.turnScopedInput && projection.userInputText != "" {
			texts, err := missingLiveUserInputTexts(protectedHistory, projection)
			if err != nil {
				// An ambiguous protected-tail snapshot cannot reduce reservation.
				texts, _ = missingLiveUserInputTexts(nil, projection)
			}
			for _, text := range texts {
				tokens += EstimateMessageTokens(providers.Message{Role: "user", Content: text})
			}
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
	seenAnchors := make(map[int]bool)
	for _, projection := range projections {
		if !projection.turnScopedInput || projection.userInputText == "" {
			continue
		}
		anchor, err := liveUserInputAnchor(messages, projection)
		if err != nil {
			return nil, err
		}
		if seenAnchors[anchor] {
			return nil, errors.New("authorized live user input owner was repeated")
		}
		seenAnchors[anchor] = true
		after := anchor + 1
		for after < len(messages) && messages[after].Role == "tool" {
			after++
		}
		texts, sourceErr := missingLiveUserInputTexts(messages, projection)
		if sourceErr != nil {
			return nil, sourceErr
		}
		for _, text := range texts {
			insertions = append(insertions, insertion{after: after, anchor: anchor, text: text})
		}
	}
	if len(insertions) == 0 {
		return messages, nil
	}
	sort.SliceStable(insertions, func(i, j int) bool { return insertions[i].anchor < insertions[j].anchor })
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
