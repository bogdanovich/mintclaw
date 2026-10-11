package document

import "context"

// These projections exist only within the protected, ephemeral interpretation
// call. Dialog intent is advisory, never commit or approval authority.
type FormIntakeDialogIntent string

const (
	FormIntakeIntentFacts   FormIntakeDialogIntent = "facts"
	FormIntakeIntentClarify FormIntakeDialogIntent = "clarify"
	FormIntakeIntentBack    FormIntakeDialogIntent = "back"
	FormIntakeIntentPause   FormIntakeDialogIntent = "pause"
	FormIntakeIntentResume  FormIntakeDialogIntent = "resume"
	FormIntakeIntentCancel  FormIntakeDialogIntent = "cancel"
	FormIntakeIntentReview  FormIntakeDialogIntent = "review"
	FormIntakeIntentFinish  FormIntakeDialogIntent = "finish"
	FormIntakeIntentDraft   FormIntakeDialogIntent = "draft"
)

type FormIntakeTextEvidence struct {
	Page int    `json:"-"`
	Text string `json:"-"`
}

type FormIntakeInterpretationView struct {
	Pass        FormIntakePass           `json:"-"`
	FieldStates []FormJobFieldState      `json:"-"`
	Evidence    []FormIntakeTextEvidence `json:"-"`
}

type FormIntakeInterpretation struct {
	Intent    FormIntakeDialogIntent    `json:"-"`
	Proposals []FormIntakeProposal      `json:"-"`
	NonData   []FormIntakeLiteral       `json:"-"`
	Findings  []FormFieldMappingBlocker `json:"-"`
}

type FormIntakeInterpreter interface {
	InterpretFormIntake(context.Context, string, FormIntakeInterpretationView) (FormIntakeInterpretation, error)
}
