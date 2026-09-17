package document

import (
	"encoding/json"
	"io"
	"unicode"
	"unicode/utf8"
)

type formFieldsBackend interface {
	Fields(io.ReadSeeker, Limits, string) backendFormFields
}

type backendFormFields struct {
	State   State
	Facts   *FormFieldsFacts
	Failure *Failure
}

func defaultFormFieldLimits() FormFieldLimits {
	return FormFieldLimits{
		MaxFields:      DefaultMaxFormFields,
		MaxWidgets:     DefaultMaxFieldWidgets,
		MaxOptions:     DefaultMaxFieldOptions,
		MaxTextBytes:   DefaultMaxFieldTextBytes,
		MaxReportBytes: DefaultMaxFormReportBytes,
	}
}

func formFieldsReportWithinLimit(facts FormFieldsFacts) bool {
	encoded, err := json.Marshal(facts)
	return err == nil && len(encoded) <= facts.Limits.MaxReportBytes
}

func validFieldText(value string, maximum int) bool {
	if len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func failureState(code FailureCode) State {
	switch code {
	case FailurePasswordRequired, FailureFormNotPresent, FailureFormUnsupported, FailureFieldUnsupported:
		return StateUnsupported
	case FailureBackendUnavailable, FailureUnsupportedPlatform:
		return StateUnavailable
	default:
		return StateFailed
	}
}
