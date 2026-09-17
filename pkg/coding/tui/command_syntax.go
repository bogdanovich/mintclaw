package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// shellCommandCellLine keeps command structure visible without pretending to
// be a complete shell parser. It recognizes the stable lexical features that
// matter in a transcript: prompts, executables, control operators, quoted
// strings, numbers, comments, assignments, and path-like arguments.
func shellCommandCellLine(prefix, command string) cellLine {
	prefix = sanitizeTerminalText(prefix)
	spans := make([]cellSpan, 0, 8)
	if index := strings.LastIndex(prefix, "$ "); index >= 0 {
		appendCellSpan(&spans, prefix[:index], cellStyleMuted)
		appendCellSpan(&spans, "$", cellStyleSyntaxKeyword)
		appendCellSpan(&spans, prefix[index+1:], cellStyleMuted)
	} else {
		appendCellSpan(&spans, prefix, cellStyleMuted)
	}
	spans = append(spans, highlightShellCommand(command)...)
	return cellLine{Spans: spans}
}

func highlightShellCommand(command string) []cellSpan {
	command = sanitizeTerminalText(command)
	spans := make([]cellSpan, 0, 12)
	expectCommand := true
	for offset := 0; offset < len(command); {
		character, size := utf8.DecodeRuneInString(command[offset:])
		if unicode.IsSpace(character) {
			end := offset + size
			for end < len(command) {
				next, nextSize := utf8.DecodeRuneInString(command[end:])
				if !unicode.IsSpace(next) {
					break
				}
				end += nextSize
			}
			appendCellSpan(&spans, command[offset:end], cellStyleDefault)
			offset = end
			continue
		}
		if character == '#' && shellCommentBoundary(command, offset) {
			appendCellSpan(&spans, command[offset:], cellStyleSyntaxComment)
			break
		}
		if character == '\'' || character == '"' || character == '`' {
			end := diffQuotedEnd(command, offset, character)
			appendCellSpan(&spans, command[offset:end], cellStyleSyntaxString)
			offset = end
			expectCommand = false
			continue
		}
		if operator := shellOperator(command[offset:]); operator != "" {
			appendCellSpan(&spans, operator, cellStyleSyntaxKeyword)
			offset += len(operator)
			if operator == "&&" || operator == "||" || operator == "|" || operator == ";" || operator == "(" {
				expectCommand = true
			}
			continue
		}

		end := offset + size
		for end < len(command) {
			next, nextSize := utf8.DecodeRuneInString(command[end:])
			if unicode.IsSpace(next) || next == '\'' || next == '"' || next == '`' ||
				shellOperator(command[end:]) != "" || (next == '#' && shellCommentBoundary(command, end)) {
				break
			}
			end += nextSize
		}
		token := command[offset:end]
		role := shellTokenRole(token, expectCommand)
		appendCellSpan(&spans, token, role)
		if expectCommand && !shellAssignment(token) {
			expectCommand = false
		}
		offset = end
	}
	if len(spans) == 0 {
		return []cellSpan{{Text: command, Role: cellStyleDefault}}
	}
	return spans
}

func shellTokenRole(token string, expectCommand bool) cellStyleRole {
	lower := strings.ToLower(token)
	if _, keyword := diffShellKeywords[lower]; keyword {
		return cellStyleSyntaxKeyword
	}
	if shellAssignment(token) {
		return cellStyleSyntaxType
	}
	if expectCommand {
		return cellStyleAccent
	}
	if shellNumber(token) {
		return cellStyleSyntaxNumber
	}
	if strings.ContainsAny(token, `/\\`) || strings.HasPrefix(token, ".") || strings.HasPrefix(token, "~") {
		return cellStylePath
	}
	return cellStyleDefault
}

func shellOperator(value string) string {
	for _, operator := range []string{"&&", "||", ">>", "<<", ";", "|", "(", ")", ">", "<"} {
		if strings.HasPrefix(value, operator) {
			return operator
		}
	}
	return ""
}

func shellCommentBoundary(value string, offset int) bool {
	if offset == 0 {
		return true
	}
	previous, _ := utf8.DecodeLastRuneInString(value[:offset])
	return unicode.IsSpace(previous) || strings.ContainsRune(";|()", previous)
}

func shellAssignment(token string) bool {
	name, _, found := strings.Cut(token, "=")
	if !found || name == "" {
		return false
	}
	for index, character := range name {
		if character != '_' && !unicode.IsLetter(character) && (index == 0 || !unicode.IsDigit(character)) {
			return false
		}
	}
	return true
}

func shellNumber(token string) bool {
	if token == "" {
		return false
	}
	for _, character := range token {
		if !unicode.IsDigit(character) && character != '.' && character != '_' {
			return false
		}
	}
	return true
}

func appendCellSpan(spans *[]cellSpan, text string, role cellStyleRole) {
	if text == "" {
		return
	}
	if count := len(*spans); count > 0 && (*spans)[count-1].Role == role {
		(*spans)[count-1].Text += text
		return
	}
	*spans = append(*spans, cellSpan{Text: text, Role: role})
}
