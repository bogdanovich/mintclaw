package document

import (
	"context"
	"errors"
)

// Closed, value-free interpretation failures. They describe neither private
// response content nor permission to retry or change the native ledger.
var (
	ErrFormIntakeInvalidContext  = errors.New("form intake interpretation context invalid")
	ErrFormIntakeInvalidResponse = errors.New("form intake interpretation response invalid")
	ErrFormIntakeModelMissing    = errors.New("form intake interpretation model unavailable")
	ErrFormIntakeProviderAuth    = errors.New("form intake provider authentication failed")
	ErrFormIntakeProviderBilling = errors.New("form intake provider billing failed")
	ErrFormIntakeProviderRequest = errors.New("form intake provider request rejected")
	ErrFormIntakeProviderLimited = errors.New("form intake provider rate limited")
	ErrFormIntakeProviderTemp    = errors.New("form intake provider temporarily unavailable")
)

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

func (intent FormIntakeDialogIntent) Valid() bool {
	switch intent {
	case FormIntakeIntentFacts, FormIntakeIntentClarify, FormIntakeIntentBack,
		FormIntakeIntentPause, FormIntakeIntentResume, FormIntakeIntentCancel,
		FormIntakeIntentReview, FormIntakeIntentFinish, FormIntakeIntentDraft:
		return true
	default:
		return false
	}
}

type FormIntakeTextEvidence struct {
	Page int    `json:"-"`
	Text string `json:"-"`
}

type FormIntakeInterpretationView struct {
	Pass                 FormIntakePass           `json:"-"`
	FieldStates          []FormJobFieldState      `json:"-"`
	Evidence             []FormIntakeTextEvidence `json:"-"`
	RelatedInputs        []FormIntakeInput        `json:"-"`
	RelatedInputsPartial bool                     `json:"-"`
	OutputMode           FormOutputMode           `json:"-"`
	OutputScope          FormOutputScope          `json:"-"`
	DeferredFieldIDs     []string                 `json:"-"`
}

type FormIntakeInterpretation struct {
	Intent    FormIntakeDialogIntent       `json:"-"`
	Proposals []FormIntakeProposal         `json:"-"`
	NonData   []FormIntakeLiteral          `json:"-"`
	Findings  []FormFieldMappingBlocker    `json:"-"`
	Output    *FormIntakeOutputInstruction `json:"-"`
}

type FormIntakeInterpreter interface {
	InterpretFormIntake(context.Context, string, FormIntakeInterpretationView) (FormIntakeInterpretation, error)
}
