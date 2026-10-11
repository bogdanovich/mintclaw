package eval

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPrivateJSONContentCannotReachEitherRole(t *testing.T) {
	for _, content := range []string{
		`{"name":"private\u002dfact-canary"}`,
		`{"private\u002dfact-canary":null,"safe":"visible"}`,
		`{"name":"private\u002dfact-canary","name":"public"}`,
		`Details: {"name":"private\u002dfact-canary"} followed by ordinary text.`,
		"```json\n{\"name\":\"private\\u002dfact-canary\"}\n```",
		`private\u002dfact-canary`,
		`{"content":"{\"name\":\"private\\u002dfact-canary\"}"}`,
	} {
		for _, kind := range []string{"question", "stream"} {
			item, events := rolePromptCase(), rolePromptEvents()
			item.Driver.Facts = map[string]string{"name": "private-fact-canary"}
			events[1].Kind, events[1].Content = kind, content
			if _, err := userInput(item, events, "question", 4); !errors.Is(err, ErrPrivateEvidence) {
				t.Fatal("encoded private content reached user", kind)
			}
			if _, _, err := judgeInput(item, events); !errors.Is(err, ErrPrivateEvidence) {
				t.Fatal("encoded private content reached judge", kind)
			}
		}
	}
}

func TestPrivateContentScannerAllowsSafeJSONAndRefusesBounds(t *testing.T) {
	facts := map[string]string{"name": "private-fact-canary"}
	for _, text := range []string{
		`Choose yourself or another person.`,
		`{"name":"public\u002dname","empty":null,"options":["Self","Other"]}`,
		`Prefix: {"name":"public\u002dname"} suffix.`,
	} {
		if containsPrivate(text, facts) {
			t.Fatal("safe content was refused")
		}
	}
	text := `private\u002dfact-canary`
	for range 9 {
		data, _ := json.Marshal(text)
		text = string(data)
	}
	for _, unsafe := range []string{
		text,
		`{"name":"private\u002dfact-canary"`,
		strings.Repeat(`[`, 25) + `"public\u002dname"` + strings.Repeat(`]`, 25),
		`[` + strings.Repeat(`"\u0078",`, 4100) + `null]`,
		strings.Repeat(`{"name":"public\u002dname"} `, 65),
		`[` + strings.Repeat(`"public\u002dname",`, 5000) + `null]`,
	} {
		if !containsPrivate(unsafe, facts) {
			t.Fatal("unscannable encoded content was admitted")
		}
	}
}
