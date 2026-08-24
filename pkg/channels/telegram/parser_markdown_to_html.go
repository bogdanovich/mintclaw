package telegram

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	reEscapedBlockquote = regexp.MustCompile(`(?m)^&gt;\s*(.*)$`)
	reLink              = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reBoldUnder         = regexp.MustCompile(`__(.+?)__`)
	reItalic            = regexp.MustCompile(`_([^_\n]+)_`)
	reStrike            = regexp.MustCompile(`~~(.+?)~~`)
	reListItem          = regexp.MustCompile(`^[-*]\s+`)
	reCodeBlock         = regexp.MustCompile("```[\\w]*\\n?([\\s\\S]*?)```")
	reInlineCode        = regexp.MustCompile("`([^`]+)`")
	reRawURL            = regexp.MustCompile(`https?://[^\s<]+`)
)

func markdownToTelegramHTML(text string) string {
	if text == "" {
		return ""
	}
	text = unwrapTelegramRichFooter(text)

	codeBlocks := extractCodeBlocks(text)
	text = codeBlocks.text

	inlineCodes := extractInlineCodes(text)
	text = inlineCodes.text

	links := extractLinks(text)
	text = links.text

	rawURLs := extractRawURLs(text)
	text = rawURLs.text

	text = reHeading.ReplaceAllString(text, "$1")
	text = escapeHTML(text)
	text = reBoldStar.ReplaceAllString(text, "<b>$1</b>")
	text = reBoldUnder.ReplaceAllString(text, "<b>$1</b>")
	text = replaceMarkdownItalics(text)
	text = reStrike.ReplaceAllString(text, "<s>$1</s>")
	text = reListItem.ReplaceAllString(text, "• ")

	for i, lnk := range links.links {
		label := escapeHTML(lnk[0])
		url := escapeHTMLAttr(lnk[1])
		text = strings.ReplaceAll(
			text,
			fmt.Sprintf("\x00LK%d\x00", i),
			fmt.Sprintf(`<a href="%s">%s</a>`, url, label),
		)
	}

	for i, rawURL := range rawURLs.urls {
		escaped := escapeHTML(rawURL)
		text = strings.ReplaceAll(
			text,
			fmt.Sprintf("\x00RU%d\x00", i),
			fmt.Sprintf(`<a href="%s">%s</a>`, escapeHTMLAttr(rawURL), escaped),
		)
	}

	for i, code := range inlineCodes.codes {
		escaped := escapeHTML(code)
		text = strings.ReplaceAll(
			text,
			fmt.Sprintf("\x00IC%d\x00", i),
			fmt.Sprintf("<code>%s</code>", escaped),
		)
	}

	for i, code := range codeBlocks.codes {
		escaped := escapeHTML(code)
		text = strings.ReplaceAll(
			text,
			fmt.Sprintf("\x00CB%d\x00", i),
			fmt.Sprintf("<pre><code>%s</code></pre>", escaped),
		)
	}

	text = reEscapedBlockquote.ReplaceAllString(text, "<blockquote>$1</blockquote>")

	return text
}

func replaceMarkdownItalics(text string) string {
	var builder strings.Builder
	cursor := 0
	searchFrom := 0
	for searchFrom < len(text) {
		match := reItalic.FindStringSubmatchIndex(text[searchFrom:])
		if match == nil {
			break
		}
		start := searchFrom + match[0]
		end := searchFrom + match[1]
		if !markdownUnderscoreBoundaries(text, start, end) {
			searchFrom = start + 1
			continue
		}
		builder.WriteString(text[cursor:start])
		builder.WriteString("<i>")
		builder.WriteString(text[searchFrom+match[2] : searchFrom+match[3]])
		builder.WriteString("</i>")
		cursor = end
		searchFrom = end
	}
	if cursor == 0 {
		return text
	}
	builder.WriteString(text[cursor:])
	return builder.String()
}

func markdownUnderscoreBoundaries(text string, start, end int) bool {
	if start > 0 {
		previous, _ := utf8.DecodeLastRuneInString(text[:start])
		if unicode.IsLetter(previous) || unicode.IsNumber(previous) {
			return false
		}
	}
	if end < len(text) {
		next, _ := utf8.DecodeRuneInString(text[end:])
		if unicode.IsLetter(next) || unicode.IsNumber(next) {
			return false
		}
	}
	return true
}

type linkMatch struct {
	text  string
	links [][2]string
}

func extractLinks(text string) linkMatch {
	matches := reLink.FindAllStringSubmatch(text, -1)

	extracted := make([][2]string, 0, len(matches))
	for _, match := range matches {
		extracted = append(extracted, [2]string{match[1], match[2]})
	}

	i := 0
	text = reLink.ReplaceAllStringFunc(text, func(m string) string {
		placeholder := fmt.Sprintf("\x00LK%d\x00", i)
		i++
		return placeholder
	})

	return linkMatch{text: text, links: extracted}
}

type codeBlockMatch struct {
	text  string
	codes []string
}

func extractCodeBlocks(text string) codeBlockMatch {
	matches := reCodeBlock.FindAllStringSubmatch(text, -1)

	codes := make([]string, 0, len(matches))
	for _, match := range matches {
		codes = append(codes, match[1])
	}

	i := 0
	text = reCodeBlock.ReplaceAllStringFunc(text, func(m string) string {
		placeholder := fmt.Sprintf("\x00CB%d\x00", i)
		i++
		return placeholder
	})

	return codeBlockMatch{text: text, codes: codes}
}

type rawURLMatch struct {
	text string
	urls []string
}

func extractRawURLs(text string) rawURLMatch {
	matches := reRawURL.FindAllString(text, -1)

	urls := make([]string, 0, len(matches))
	urls = append(urls, matches...)

	i := 0
	text = reRawURL.ReplaceAllStringFunc(text, func(string) string {
		placeholder := fmt.Sprintf("\x00RU%d\x00", i)
		i++
		return placeholder
	})

	return rawURLMatch{text: text, urls: urls}
}

type inlineCodeMatch struct {
	text  string
	codes []string
}

func extractInlineCodes(text string) inlineCodeMatch {
	matches := reInlineCode.FindAllStringSubmatch(text, -1)

	codes := make([]string, 0, len(matches))
	for _, match := range matches {
		codes = append(codes, match[1])
	}

	i := 0
	text = reInlineCode.ReplaceAllStringFunc(text, func(m string) string {
		placeholder := fmt.Sprintf("\x00IC%d\x00", i)
		i++
		return placeholder
	})

	return inlineCodeMatch{text: text, codes: codes}
}
