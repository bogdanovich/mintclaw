package document

import (
	"context"
	"regexp"
	"runtime"
	"sort"

	"github.com/bogdanovich/mintclaw/pkg/media"
)

const operationFields = "fields"

var (
	opaqueFieldID  = regexp.MustCompile(`^field_[a-f0-9]{64}$`)
	opaqueWidgetID = regexp.MustCompile(`^widget_[a-f0-9]{64}$`)
)

// Fields acquires a local operator path and discovers fields only from its immutable snapshot.
func Fields(ctx context.Context, inputPath string, options AcquireOptions) (*Snapshot, Report) {
	return fieldsWithWorker(
		ctx,
		inputPath,
		options,
		runtime.GOOS,
		runtime.GOARCH,
		NewProcessFormFieldsWorker(),
	)
}

// FieldsMedia admits one exact owner-bound media reference and discovers its AcroForm fields.
func FieldsMedia(
	ctx context.Context,
	resolver OwnedMediaResolver,
	ref string,
	owner media.MediaOwner,
	options AcquireOptions,
) (*Snapshot, Report) {
	return fieldsMediaWithWorker(
		ctx,
		resolver,
		ref,
		owner,
		options,
		runtime.GOOS,
		runtime.GOARCH,
		NewProcessFormFieldsWorker(),
	)
}

func fieldsWithWorker(
	ctx context.Context,
	inputPath string,
	options AcquireOptions,
	goos string,
	goarch string,
	worker FormFieldsWorker,
) (*Snapshot, Report) {
	snapshot, report := acquireOperationForPlatform(
		ctx,
		inputPath,
		options,
		goos,
		goarch,
		operationFields,
	)
	return fieldsAcquiredSnapshot(ctx, snapshot, report, worker)
}

func fieldsMediaWithWorker(
	ctx context.Context,
	resolver OwnedMediaResolver,
	ref string,
	owner media.MediaOwner,
	options AcquireOptions,
	goos string,
	goarch string,
	worker FormFieldsWorker,
) (*Snapshot, Report) {
	snapshot, report := acquireMediaOperationForPlatform(
		ctx,
		resolver,
		ref,
		owner,
		options,
		goos,
		goarch,
		operationFields,
	)
	return fieldsAcquiredSnapshot(ctx, snapshot, report, worker)
}

func fieldsAcquiredSnapshot(
	ctx context.Context,
	snapshot *Snapshot,
	report Report,
	worker FormFieldsWorker,
) (*Snapshot, Report) {
	if snapshot == nil || report.State != StateSucceeded || report.Input == nil {
		return snapshot, report
	}
	if worker == nil {
		return cleanupFieldsFailure(
			snapshot,
			failFieldsReport(
				report,
				StateUnavailable,
				FailureBackendUnavailable,
				"document form backend is unavailable",
			),
		)
	}
	result := worker.Fields(ctx, snapshot, *report.Input, report.Limits)
	expectedInput := newWorkerOperationRequest(*report.Input, report.Limits, workerOperationFields).Input
	if result.Input != nil && *result.Input != expectedInput {
		result = workerFailure(
			report.OperationID,
			StateFailed,
			FailureWorkerProtocol,
			"document worker returned an invalid response",
		)
	}
	if result.State == StateSucceeded && result.Input != nil && result.Inspection != nil && result.Fields != nil &&
		result.Failure == nil && validInspectionFacts(*result.Inspection) && validFormFieldsFacts(*result.Fields) &&
		result.Fields.SourceSHA256 == expectedInput.SHA256 &&
		validFieldsAgainstInspection(*result.Fields, *result.Inspection) {
		eligibility := formDiscoveryInspectionEligibility(*result.Inspection)
		if eligibility.State != FormEligible {
			result = workerFailure(
				report.OperationID,
				StateFailed,
				FailureWorkerProtocol,
				"document worker returned an invalid response",
			)
		} else {
			report.State = StateSucceeded
			report.Inspection = result.Inspection
			report.FormEligibility = &eligibility
			report.Fields = result.Fields
			report.Failure = nil
			return snapshot, report
		}
	}
	if result.State == StateSucceeded {
		result = workerFailure(
			report.OperationID,
			StateFailed,
			FailureWorkerProtocol,
			"document worker returned an invalid response",
		)
	}
	state := result.State
	if state == "" {
		state = StateFailed
	}
	failure := safeWorkerFailure(result)
	report = failFieldsReport(report, state, failure.Code, failure.Message)
	if result.Inspection != nil && validInspectionFacts(*result.Inspection) {
		report.Inspection = result.Inspection
		eligibility := formDiscoveryInspectionEligibility(*result.Inspection)
		report.FormEligibility = &eligibility
	}
	return cleanupFieldsFailure(snapshot, report)
}

func failFieldsReport(report Report, state State, code FailureCode, message string) Report {
	report.State = state
	report.Fields = nil
	report.FormEligibility = nil
	report.Failure = &Failure{Code: code, Message: message}
	return report
}

func cleanupFieldsFailure(snapshot *Snapshot, report Report) (*Snapshot, Report) {
	if err := snapshot.Close(); err != nil {
		return nil, failFieldsReport(
			report,
			StateFailed,
			FailureInternal,
			"protected scratch cleanup failed",
		)
	}
	return nil, report
}

func validFormFieldsFacts(facts FormFieldsFacts) bool {
	if !validDocumentDigest(facts.SourceSHA256) || facts.Backend.Name != PDFCPUBackendName ||
		facts.Backend.Version != PDFCPUBackendVersion ||
		facts.Backend.Role != "production" || facts.Backend.IsolationMode != "one_shot_process" ||
		facts.Limits != defaultFormFieldLimits() || len(facts.Fields) == 0 ||
		len(facts.Fields) > facts.Limits.MaxFields || !formFieldsReportWithinLimit(facts) ||
		!sort.SliceIsSorted(facts.Fields, func(i, j int) bool {
			return facts.Fields[i].ID < facts.Fields[j].ID
		}) {
		return false
	}
	fieldIDs := map[string]struct{}{}
	widgetIDs := map[string]struct{}{}
	totalWidgets := 0
	for _, field := range facts.Fields {
		if !validFormField(field, facts.Limits) {
			return false
		}
		if _, duplicate := fieldIDs[field.ID]; duplicate {
			return false
		}
		fieldIDs[field.ID] = struct{}{}
		for _, widget := range field.Widgets {
			if _, duplicate := widgetIDs[widget.ID]; duplicate {
				return false
			}
			widgetIDs[widget.ID] = struct{}{}
			totalWidgets++
		}
	}
	return totalWidgets <= facts.Limits.MaxWidgets
}

func validFormField(field FormField, limits FormFieldLimits) bool {
	if !opaqueFieldID.MatchString(field.ID) || !validFieldText(field.Name, limits.MaxTextBytes) ||
		!validFieldText(field.AlternateName, limits.MaxTextBytes) || len(field.Widgets) == 0 ||
		len(field.Options) > limits.MaxOptions || field.MaxLength < 0 {
		return false
	}
	for index, widget := range field.Widgets {
		if !opaqueWidgetID.MatchString(widget.ID) || widget.Page < 1 || widget.Page > DefaultMaxPages ||
			widget.Ordinal != index+1 {
			return false
		}
	}
	optionExports := map[string]struct{}{}
	for _, option := range field.Options {
		if option.Export == "" || option.Display == "" ||
			!validFieldText(option.Export, limits.MaxTextBytes) ||
			!validFieldText(option.Display, limits.MaxTextBytes) {
			return false
		}
		if _, duplicate := optionExports[option.Export]; duplicate {
			return false
		}
		optionExports[option.Export] = struct{}{}
	}
	switch field.Kind {
	case FormFieldText:
		return len(field.Options) == 0 && field.DateFormat == "" && !field.MultiSelect && !field.Editable
	case FormFieldDate:
		return field.DateFormat != "" && len(field.Options) == 0 && !field.Multiline &&
			!field.MultiSelect && !field.Editable
	case FormFieldCheckbox:
		return len(field.Options) == 0 && field.DateFormat == "" && field.MaxLength == 0 &&
			!field.Multiline && !field.MultiSelect && !field.Editable
	case FormFieldRadio:
		return len(field.Options) > 0 && field.DateFormat == "" && field.MaxLength == 0 &&
			!field.Multiline && !field.MultiSelect && !field.Editable
	case FormFieldCombo:
		return (field.Editable || len(field.Options) > 0) && field.DateFormat == "" &&
			field.MaxLength == 0 && !field.Multiline && !field.MultiSelect
	case FormFieldList:
		return len(field.Options) > 0 && field.DateFormat == "" && field.MaxLength == 0 &&
			!field.Multiline && !field.Editable
	default:
		return false
	}
}
