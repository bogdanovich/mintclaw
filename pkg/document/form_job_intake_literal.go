package document

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// FormIntakeLiteral is private proposal evidence, not a public tool argument.
// Occurrence is one-based among exact, token-aligned, non-overlapping matches.
type FormIntakeLiteral struct {
	Text       string `json:"-"`
	Occurrence int    `json:"-"`
}

type formIntakeSpan struct {
	Start   int    `json:"start"`
	End     int    `json:"end"`
	FieldID string `json:"field_id,omitempty"`
}

const MaxFormIntakeAccountedSpans = 64

func resolveFormIntakeLiteral(text string, literal FormIntakeLiteral) (formIntakeSpan, error) {
	if !utf8.ValidString(text) || len(text) > MaxFormIntakeInputBytes || !utf8.ValidString(literal.Text) ||
		literal.Text == "" || strings.TrimSpace(literal.Text) != literal.Text ||
		len(literal.Text) > MaxFormIntakeInputBytes || literal.Occurrence <= 0 ||
		literal.Occurrence > MaxFormIntakeAccountedSpans {
		return formIntakeSpan{}, ErrFormJobAnswerConflict
	}
	offset := 0
	for occurrence := 1; occurrence <= literal.Occurrence; occurrence++ {
		span, found := nextFormIntakeLiteral(text, literal.Text, offset)
		if !found {
			return formIntakeSpan{}, ErrFormJobAnswerConflict
		}
		if occurrence == literal.Occurrence {
			return span, nil
		}
		offset = span.End
	}
	return formIntakeSpan{}, ErrFormJobAnswerConflict
}

func nextFormIntakeLiteral(text, literal string, offset int) (formIntakeSpan, bool) {
	for offset < len(text) {
		index := strings.Index(text[offset:], literal)
		if index < 0 {
			break
		}
		start, end := offset+index, offset+index+len(literal)
		if formIntakeLiteralTokenBoundaries(text, start, end) {
			return formIntakeSpan{Start: start, End: end}, true
		}
		offset = end
	}
	return formIntakeSpan{}, false
}

func formIntakeLiteralOccurrence(text string, span formIntakeSpan) int {
	literal := text[span.Start:span.End]
	offset := 0
	for occurrence := 1; occurrence <= MaxFormIntakeAccountedSpans; occurrence++ {
		match, found := nextFormIntakeLiteral(text, literal, offset)
		if !found || match.Start > span.Start {
			return 0
		}
		if match.Start == span.Start && match.End == span.End {
			return occurrence
		}
		offset = match.End
	}
	return 0
}

func formIntakeLiteralTokenBoundaries(text string, start, end int) bool {
	first, _ := utf8.DecodeRuneInString(text[start:end])
	last, _ := utf8.DecodeLastRuneInString(text[start:end])
	if start > 0 && formIntakeWordRune(first) {
		previous, _ := utf8.DecodeLastRuneInString(text[:start])
		if formIntakeWordRune(previous) {
			return false
		}
	}
	if end < len(text) && formIntakeWordRune(last) {
		next, _ := utf8.DecodeRuneInString(text[end:])
		if formIntakeWordRune(next) {
			return false
		}
	}
	return true
}

func formIntakeWordRune(character rune) bool {
	return unicode.IsLetter(character) || unicode.IsNumber(character) || unicode.IsMark(character) || character == '_'
}

func formIntakeSpansOverlap(left, right formIntakeSpan) bool {
	return left.Start < right.End && right.Start < left.End
}

// Coverage is structural bookkeeping, not proof that a model's classification
// of non-data prose or a person's role is semantically correct.
func formIntakeTextAccounted(text string, spans []formIntakeSpan) bool {
	if len(text) > MaxFormIntakeInputBytes || len(spans) > MaxFormIntakeAccountedSpans || !utf8.ValidString(text) {
		return false
	}
	covered := make([]bool, len(text))
	for _, span := range spans {
		if span.Start < 0 || span.End <= span.Start || span.End > len(text) ||
			!utf8.ValidString(text[span.Start:span.End]) {
			return false
		}
		for index := span.Start; index < span.End; index++ {
			covered[index] = true
		}
	}
	for index, character := range text {
		if (formIntakeWordRune(character) || unicode.IsSymbol(character)) && !covered[index] {
			return false
		}
	}
	return true
}
