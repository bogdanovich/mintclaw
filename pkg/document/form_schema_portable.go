//go:build (linux && amd64) || (darwin && (amd64 || arm64)) || (windows && amd64)

package document

import (
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/form"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/primitives"
)

// pdfcpu also exports plain text as dates when V/DV happens to look like one.
// Only admitted structural format actions give a field date semantics here.
// Action-bearing fields still pass the existing strict date/action validation.
func structuralPDFCPUForm(context *model.Context, exported form.Form) (form.Form, *Failure) {
	if len(exported.DateFields) == 0 {
		return exported, nil
	}
	locations, failure := collectFormWidgetLocations(context, exported)
	if failure != nil {
		return form.Form{}, failure
	}
	dates := make([]*form.DateField, 0, len(exported.DateFields))
	for _, item := range exported.DateFields {
		if item == nil {
			return form.Form{}, malformedFormField()
		}
		if validateFieldActions(context, item.ID, locations[item.ID], FormFieldText, "") != nil {
			dates = append(dates, item)
			continue
		}
		flags, flagsFailure := fieldFlags(context, item.ID)
		maximum, maximumFailure := fieldInteger(context, item.ID, "MaxLen")
		if flagsFailure != nil || maximumFailure != nil || maximum < 0 {
			return form.Form{}, malformedFormField()
		}
		exported.TextFields = append(exported.TextFields, &form.TextField{
			Pages: item.Pages, ID: item.ID, Name: item.Name, AltName: item.AltName,
			Default: item.Default, Value: item.Value, Locked: item.Locked, MaxLen: maximum,
			Multiline: primitives.FieldFlags(flags)&primitives.FieldMultiline != 0,
		})
	}
	exported.DateFields = dates
	return exported, nil
}
