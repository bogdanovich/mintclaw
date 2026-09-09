package tui

import (
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

type diffSyntaxSpec struct {
	keywords     map[string]struct{}
	lineComments []string
	blockComment bool
}

var (
	diffCStyleKeywords = keywordSet(
		"as async await break case catch class const continue default defer do else enum export extends false finally " +
			"fn for func function go if implements import in interface let match mod new nil null package private public " +
			"range return select static struct switch this throw trait true try type typeof var while yield",
	)
	diffPythonKeywords = keywordSet(
		"and as assert async await break class continue def del elif else except false finally for from global if " +
			"import in is lambda none nonlocal not or pass raise return true try while with yield",
	)
	diffShellKeywords = keywordSet(
		"case do done elif else esac export fi for function if in local readonly return select then until while",
	)
	diffDataKeywords = keywordSet("false null true")
)

func keywordSet(value string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, word := range strings.Fields(value) {
		result[word] = struct{}{}
	}
	return result
}

func repositoryDiffSyntaxSpec(path string) (diffSyntaxSpec, bool) {
	extension := strings.ToLower(filepath.Ext(path))
	switch extension {
	case ".go", ".c", ".cc", ".cpp", ".cxx", ".h", ".hpp", ".java", ".js", ".jsx", ".rs", ".ts", ".tsx":
		return diffSyntaxSpec{
			keywords: diffCStyleKeywords, lineComments: []string{"//"}, blockComment: true,
		}, true
	case ".py", ".pyi":
		return diffSyntaxSpec{keywords: diffPythonKeywords, lineComments: []string{"#"}}, true
	case ".bash", ".sh", ".zsh":
		return diffSyntaxSpec{keywords: diffShellKeywords, lineComments: []string{"#"}}, true
	case ".json", ".jsonc":
		return diffSyntaxSpec{
			keywords: diffDataKeywords, lineComments: []string{"//"}, blockComment: extension == ".jsonc",
		}, true
	case ".yaml", ".yml", ".toml":
		return diffSyntaxSpec{keywords: diffDataKeywords, lineComments: []string{"#"}}, true
	default:
		return diffSyntaxSpec{}, false
	}
}

func highlightRepositoryDiffContent(path, value string) []cellSpan {
	spec, ok := repositoryDiffSyntaxSpec(path)
	if !ok || value == "" {
		return []cellSpan{{Text: value, Role: cellStyleDefault}}
	}
	spans := make([]cellSpan, 0, 8)
	var pendingText strings.Builder
	var pendingRole cellStyleRole
	hasPending := false
	flushPending := func() {
		if !hasPending {
			return
		}
		spans = append(spans, cellSpan{Text: pendingText.String(), Role: pendingRole})
		pendingText = strings.Builder{}
		hasPending = false
	}
	appendSpan := func(text string, role cellStyleRole) {
		if text == "" {
			return
		}
		if hasPending && pendingRole != role {
			flushPending()
		}
		pendingRole = role
		hasPending = true
		pendingText.WriteString(text)
	}

	for offset := 0; offset < len(value); {
		if comment := matchingDiffLineComment(value[offset:], spec.lineComments); comment != "" {
			appendSpan(value[offset:], cellStyleSyntaxComment)
			break
		}
		if spec.blockComment && strings.HasPrefix(value[offset:], "/*") {
			end := strings.Index(value[offset+2:], "*/")
			if end < 0 {
				appendSpan(value[offset:], cellStyleSyntaxComment)
				break
			}
			end += offset + 4
			appendSpan(value[offset:end], cellStyleSyntaxComment)
			offset = end
			continue
		}

		character, size := utf8.DecodeRuneInString(value[offset:])
		if character == utf8.RuneError && size == 0 {
			break
		}
		if character == '\'' || character == '"' || character == '`' {
			end := diffQuotedEnd(value, offset, character)
			appendSpan(value[offset:end], cellStyleSyntaxString)
			offset = end
			continue
		}
		if unicode.IsDigit(character) {
			end := offset + size
			for end < len(value) {
				next, nextSize := utf8.DecodeRuneInString(value[end:])
				if !unicode.IsDigit(next) && !unicode.IsLetter(next) && next != '.' && next != '_' {
					break
				}
				end += nextSize
			}
			appendSpan(value[offset:end], cellStyleSyntaxNumber)
			offset = end
			continue
		}
		if isDiffIdentifierStart(character) {
			end := offset + size
			for end < len(value) {
				next, nextSize := utf8.DecodeRuneInString(value[end:])
				if !isDiffIdentifierContinue(next) {
					break
				}
				end += nextSize
			}
			word := value[offset:end]
			role := cellStyleDefault
			if _, exists := spec.keywords[strings.ToLower(word)]; exists {
				role = cellStyleSyntaxKeyword
			} else if unicode.IsUpper(character) {
				role = cellStyleSyntaxType
			}
			appendSpan(word, role)
			offset = end
			continue
		}
		appendSpan(value[offset:offset+size], cellStyleDefault)
		offset += size
	}
	flushPending()
	if len(spans) == 0 {
		return []cellSpan{{Text: value, Role: cellStyleDefault}}
	}
	return spans
}

func matchingDiffLineComment(value string, prefixes []string) string {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return prefix
		}
	}
	return ""
}

func diffQuotedEnd(value string, start int, quote rune) int {
	offset := start
	_, size := utf8.DecodeRuneInString(value[offset:])
	offset += size
	escaped := false
	for offset < len(value) {
		character, characterSize := utf8.DecodeRuneInString(value[offset:])
		offset += characterSize
		if escaped {
			escaped = false
			continue
		}
		if character == '\\' && quote != '`' {
			escaped = true
			continue
		}
		if character == quote {
			break
		}
	}
	return offset
}

func isDiffIdentifierStart(character rune) bool {
	return character == '_' || character == '$' || unicode.IsLetter(character)
}

func isDiffIdentifierContinue(character rune) bool {
	return isDiffIdentifierStart(character) || unicode.IsDigit(character)
}
