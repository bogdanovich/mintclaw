package interactions

import "testing"

func TestPromptTextUsesCanonicalLanguageAndSafeFallback(t *testing.T) {
	language, err := CanonicalPromptLanguage("RU-ru")
	if err != nil || language != "ru-ru" {
		t.Fatalf("CanonicalPromptLanguage() = %q, %v", language, err)
	}
	if got := PromptText(language, PromptApprovalQuestion); got != "Разрешить это действие?" {
		t.Fatalf("Russian approval question = %q", got)
	}
	if got := PromptText(language, PromptResponseRecorded); got != "Ответ принят." {
		t.Fatalf("Russian response acknowledgement = %q", got)
	}
	if got := PromptText("ja-JP", PromptApprovalQuestion); got != "Allow this action?" {
		t.Fatalf("unsupported language fallback = %q", got)
	}
	if got := PromptText("ja-JP", PromptResponseRecorded); got != "Response recorded." {
		t.Fatalf("unsupported response acknowledgement fallback = %q", got)
	}
}

func TestCanonicalPromptLanguageRejectsPresentationInjection(t *testing.T) {
	for _, invalid := range []string{
		"", "ru Russian", "ru\nExact action", "x", "русский", "en-u", "en-US-u",
	} {
		if language, err := CanonicalPromptLanguage(invalid); err == nil {
			t.Fatalf("CanonicalPromptLanguage(%q) = %q, nil", invalid, language)
		}
	}
}
