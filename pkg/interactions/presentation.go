package interactions

import (
	"fmt"
	"strings"

	"golang.org/x/text/language"
)

const MaxPromptLanguageLength = 35

type PromptTextKey string

const (
	PromptApprovalQuestion         PromptTextKey = "approval_question"
	PromptApprovalRequestedOutcome PromptTextKey = "approval_requested_outcome"
	PromptApprovalExactAction      PromptTextKey = "approval_exact_action"
	PromptBrowserAttachAction      PromptTextKey = "browser_attach_action"
)

var promptTextCatalog = map[string]map[PromptTextKey]string{
	"en": {
		PromptApprovalQuestion:         "Allow this action?",
		PromptApprovalRequestedOutcome: "Requested outcome:",
		PromptApprovalExactAction:      "Exact action:",
		PromptBrowserAttachAction:      "Allow MintClaw to connect to one visibly selected browser tab",
	},
	"ru": {
		PromptApprovalQuestion:         "Разрешить это действие?",
		PromptApprovalRequestedOutcome: "Запрошенный результат:",
		PromptApprovalExactAction:      "Точное действие:",
		PromptBrowserAttachAction:      "Разрешить MintClaw подключиться к одной выбранной видимой вкладке браузера",
	},
}

// CanonicalPromptLanguage validates and normalizes a BCP-47 language tag used
// only to select trusted runtime-owned interaction text. It never grants tool
// authority and cannot alter the action bound to an approval.
func CanonicalPromptLanguage(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > MaxPromptLanguageLength {
		return "", fmt.Errorf("interaction_language must be a valid BCP-47 language tag")
	}
	if _, err := language.Parse(value); err != nil {
		return "", fmt.Errorf("interaction_language must be a valid BCP-47 language tag")
	}
	return strings.ToLower(value), nil
}

// PromptText returns trusted runtime-owned copy for the requested language.
// Unsupported languages safely fall back to English without changing approval
// semantics. Adding a language does not change any tool or persistence schema.
func PromptText(language string, key PromptTextKey) string {
	base, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(language)), "-")
	if translated, ok := promptTextCatalog[base][key]; ok {
		return translated
	}
	return promptTextCatalog["en"][key]
}
