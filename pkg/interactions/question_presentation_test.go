package interactions

import (
	"strings"
	"testing"
)

func TestQuestionIntroductionAndQuestionHaveIndependentCharacterBudgets(t *testing.T) {
	question := Question{
		ID: "value", Header: "PDF-форма",
		Introduction: strings.Repeat("文", MaxIntroductionLength),
		Question:     strings.Repeat("Я", MaxQuestionLength-1) + "?",
	}
	if err := validateQuestions(KindQuestion, []Question{question}); err != nil {
		t.Fatalf("valid multibyte presentation rejected: %v", err)
	}
	for _, section := range []string{"introduction", "question"} {
		t.Run(section, func(t *testing.T) {
			oversized := question
			if section == "introduction" {
				oversized.Introduction += "文"
			} else {
				oversized.Question += "Я"
			}
			if err := validateQuestions(KindQuestion, []Question{oversized}); err == nil {
				t.Fatal("over-budget section accepted")
			}
		})
	}
}
