//go:build linux && amd64

package document

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"slices"
	"strings"
	"unicode"

	pdfcpuapi "github.com/pdfcpu/pdfcpu/pkg/api"
	pdffont "github.com/pdfcpu/pdfcpu/pkg/font"
	pdfcpucore "github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/create"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/form"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/primitives"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

type pdfCPUFormWriteBackend struct{}

const pdfCPUUTF8FormFontName = "Roboto-Regular"

type pdfCPUFormValue struct {
	kind    FormFieldKind
	text    string
	checked bool
	choices []string
}

type pdfCPUFormBinding struct {
	backendID  string
	field      FormField
	expected   pdfCPUFormValue
	normalized FormValue
}

type boundedFormWriteBuffer struct {
	bytes.Buffer
	maximum int64
}

func newFormWriteBackend() formWriteBackend { return pdfCPUFormWriteBackend{} }

func (buffer *boundedFormWriteBuffer) Write(value []byte) (int, error) {
	remaining := buffer.maximum - int64(buffer.Len())
	if remaining <= 0 {
		return 0, errors.New("document form candidate exceeds its byte limit")
	}
	if int64(len(value)) > remaining {
		written, _ := buffer.Buffer.Write(value[:remaining])
		return written, errors.New("document form candidate exceeds its byte limit")
	}
	return buffer.Buffer.Write(value)
}

func (backend pdfCPUFormWriteBackend) Fill(data []byte, request WorkerRequest) (result backendFormWrite) {
	defer func() {
		if recover() != nil {
			result = failedFormWrite(StateFailed, FailureWriteFailed, "document form writer failed unexpectedly")
		}
	}()
	return backend.fill(data, request)
}

func (pdfCPUFormWriteBackend) fill(data []byte, request WorkerRequest) backendFormWrite {
	if request.Fill == nil || !validNormalizedFillRequest(*request.Fill) ||
		request.Fill.SourceSHA256 != request.Input.SHA256 {
		return failedFormWrite(StateFailed, FailureWorkerProtocol, "document form write request is invalid")
	}
	inspection := newInspectionBackend().Inspect(bytes.NewReader(data), request.Limits)
	if inspection.State != StateSucceeded || inspection.Facts == nil {
		return formWriteInspectionFailure(inspection)
	}
	if failure := formWriteAdmissionFailure(*inspection.Facts); failure != nil {
		return failedFormWrite(failureState(failure.Code), failure.Code, failure.Message)
	}
	hybrid := inspection.Facts.XFA.State == FactPresent
	sourceFields := newFormFieldsBackend().Fields(bytes.NewReader(data), request.Limits, request.Input.SHA256)
	if sourceFields.State != StateSucceeded || sourceFields.Facts == nil {
		if sourceFields.Failure == nil {
			return failedFormWrite(
				StateFailed,
				FailureVerificationStructural,
				"document form structure is unavailable",
			)
		}
		return failedFormWrite(
			sourceFields.State,
			sourceFields.Failure.Code,
			sourceFields.Failure.Message,
		)
	}
	context, failure := readFormContext(bytes.NewReader(data), request.Limits)
	if failure != nil {
		return failedFormWrite(failureState(failure.Code), failure.Code, failure.Message)
	}
	context.Cmd = model.FILLFORMFIELDS
	if err := pdfcpuapi.OptimizeContext(context); err != nil {
		return failedFormWrite(StateFailed, FailureWriteFailed, "document form candidate could not be written")
	}
	if err := pdfcpucore.CacheFormFonts(context); err != nil {
		return failedFormWrite(
			StateUnsupported,
			FailureAppearanceUnavailable,
			"document form appearance resources are unavailable",
		)
	}
	sourceGroup, present, err := form.ExportForm(context.XRefTable, "")
	if err != nil || !present || sourceGroup == nil || len(sourceGroup.Forms) != 1 {
		return failedFormWrite(StateFailed, FailureVerificationStructural, "document form structure changed")
	}
	target, baseline, bindings, failure := preparePDFCPUFormWrite(
		sourceGroup.Forms[0],
		*sourceFields.Facts,
		*request.Fill,
	)
	if failure != nil {
		return failedFormWrite(failureState(failure.Code), failure.Code, failure.Message)
	}
	if !pdfCPUFormGlyphsAvailable(bindings) {
		return failedFormWrite(
			StateUnsupported,
			FailureAppearanceUnavailable,
			"document form appearance resources are unavailable",
		)
	}
	if hybrid {
		if err = normalizePDFCPUHybridContext(context); err != nil {
			return failedFormWrite(
				StateFailed,
				FailureVerificationStructural,
				"hybrid form normalization failed",
			)
		}
	}
	_, pages, err := form.FillForm(
		context,
		form.FillDetails(&target, nil),
		target.Pages,
		form.JSON,
	)
	if err != nil {
		return classifyPDFCPUFormWriteError(err)
	}
	if err = ensurePDFCPUSelectedAppearances(context, bindings); err != nil {
		return classifyPDFCPUFormWriteError(err)
	}
	if err = disablePDFCPUAppearanceRegeneration(context); err != nil {
		return failedFormWrite(
			StateFailed,
			FailureVerificationStructural,
			"document form appearance state could not be finalized",
		)
	}
	if err = applyPDFCPUCanonicalChoiceValues(context, bindings); err != nil {
		return failedFormWrite(
			StateFailed,
			FailureVerificationStructural,
			"document form choice value could not be encoded exactly",
		)
	}
	if _, _, err = create.UpdatePageTree(context, pages, nil); err != nil {
		return failedFormWrite(StateFailed, FailureWriteFailed, "document form candidate could not be written")
	}
	if err = pdfcpuapi.ValidateContext(context); err != nil {
		return failedFormWrite(StateFailed, FailureWriteFailed, "document form candidate could not be validated")
	}
	output := &boundedFormWriteBuffer{maximum: DefaultMaxArtifactBytes}
	if err = pdfcpuapi.WriteContext(context, output); err != nil {
		return failedFormWrite(StateFailed, FailureWriteFailed, "document form candidate could not be written")
	}
	candidate := append([]byte(nil), output.Bytes()...)
	return verifyPDFCPUFormCandidate(
		candidate,
		request,
		*inspection.Facts,
		*sourceFields.Facts,
		baseline,
		bindings,
		hybrid,
	)
}

func pdfCPUFormGlyphsAvailable(bindings map[string]pdfCPUFormBinding) bool {
	needsUTF8Font := false
	for _, binding := range bindings {
		for _, value := range pdfCPUFormVisualStrings(binding.expected) {
			for _, character := range value {
				if character > unicode.MaxASCII {
					needsUTF8Font = true
					break
				}
			}
		}
	}
	if !needsUTF8Font {
		return true
	}
	metrics, found, err := pdffont.UserFont(pdfCPUUTF8FormFontName)
	if err != nil || !found {
		return false
	}
	for _, binding := range bindings {
		for _, value := range pdfCPUFormVisualStrings(binding.expected) {
			for _, character := range value {
				if unicode.IsSpace(character) {
					continue
				}
				if _, found = metrics.Chars[uint32(character)]; !found {
					return false
				}
			}
		}
	}
	return true
}

func pdfCPUFormVisualStrings(value pdfCPUFormValue) []string {
	if value.kind == FormFieldText || value.kind == FormFieldDate {
		return []string{value.text}
	}
	if value.kind == FormFieldCombo || value.kind == FormFieldList {
		return value.choices
	}
	return nil
}

func disablePDFCPUAppearanceRegeneration(context *model.Context) error {
	catalog, err := context.Catalog()
	if err != nil || catalog == nil {
		return errors.New("form catalog is invalid")
	}
	formObject, present := catalog.Find("AcroForm")
	if !present {
		return errors.New("form catalog is invalid")
	}
	formDictionary, err := context.DereferenceDict(formObject)
	if err != nil || formDictionary == nil {
		return errors.New("form catalog is invalid")
	}
	formDictionary["NeedAppearances"] = types.Boolean(false)
	return nil
}

func ensurePDFCPUSelectedAppearances(
	context *model.Context,
	bindings map[string]pdfCPUFormBinding,
) error {
	fonts := map[string]types.IndirectRef{}
	for _, binding := range bindings {
		if binding.field.Kind == FormFieldCheckbox || binding.field.Kind == FormFieldRadio {
			continue
		}
		numbers, failure := fieldObjectNumbers(binding.backendID)
		if failure != nil {
			return errors.New("form field identity is invalid")
		}
		object, err := context.FindObject(numbers[len(numbers)-1])
		if err != nil {
			return err
		}
		field, err := context.DereferenceDict(object)
		if err != nil || field == nil {
			return errors.New("form field structure is invalid")
		}
		widgets := field.ArrayEntry("Kids")
		if len(widgets) == 0 {
			widgets = []types.Object{object}
		}
		defaultAppearance := field.StringEntry("DA")
		for _, widgetObject := range widgets {
			widget, dereferenceErr := context.DereferenceDict(widgetObject)
			if dereferenceErr != nil || widget == nil {
				return errors.New("form widget structure is invalid")
			}
			switch binding.field.Kind {
			case FormFieldText:
				err = primitives.EnsureTextFieldAP(
					context,
					widget,
					binding.expected.text,
					binding.field.Multiline,
					false,
					binding.field.MaxLength,
					defaultAppearance,
					fonts,
				)
			case FormFieldDate:
				err = primitives.EnsureDateFieldAP(
					context,
					widget,
					binding.expected.text,
					defaultAppearance,
					fonts,
				)
			case FormFieldCombo:
				selected := ""
				if len(binding.expected.choices) == 1 {
					selected = binding.expected.choices[0]
				}
				err = primitives.EnsureComboBoxAP(
					context,
					widget,
					selected,
					defaultAppearance,
					fonts,
				)
			case FormFieldList:
				err = primitives.EnsureListBoxAP(
					context,
					widget,
					pdfCPUChoiceDisplays(binding.field),
					pdfCPUChoiceIndices(binding.field, binding.normalized.Choices),
					defaultAppearance,
					fonts,
				)
			default:
				err = errors.New("form field appearance type is invalid")
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func pdfCPUChoiceDisplays(field FormField) []string {
	displays := make([]string, 0, len(field.Options))
	for _, option := range field.Options {
		displays = append(displays, strings.TrimSpace(option.Display))
	}
	return displays
}

func pdfCPUChoiceIndices(field FormField, choices []string) types.Array {
	indices := make(types.Array, 0, len(choices))
	for _, choice := range choices {
		if index := pdfCPUChoiceIndex(field, choice); index >= 0 {
			indices = append(indices, types.Integer(index))
		}
	}
	return indices
}

func formWriteInspectionFailure(outcome backendInspection) backendFormWrite {
	if outcome.Failure == nil {
		return failedFormWrite(StateFailed, FailureVerificationStructural, "document form structure is unavailable")
	}
	return failedFormWrite(outcome.State, outcome.Failure.Code, outcome.Failure.Message)
}

func classifyPDFCPUFormWriteError(err error) backendFormWrite {
	message := strings.ToLower(err.Error())
	switch {
	case isPDFCPUResourceLimit(err):
		return failedFormWrite(StateFailed, FailureLimitExceeded, "document form write exceeded a limit")
	case strings.Contains(message, "font") || strings.Contains(message, "appearance"):
		return failedFormWrite(
			StateUnsupported,
			FailureAppearanceUnavailable,
			"document form appearance resources are unavailable",
		)
	default:
		return failedFormWrite(StateFailed, FailureWriteFailed, "document form candidate could not be written")
	}
}

func preparePDFCPUFormWrite(
	source form.Form,
	facts FormFieldsFacts,
	request NormalizedFillRequest,
) (form.Form, map[string]pdfCPUFormValue, map[string]pdfCPUFormBinding, *Failure) {
	fields := make(map[string]FormField, len(facts.Fields))
	for _, field := range facts.Fields {
		fields[field.ID] = field
	}
	assignments := make(map[string]FormFillAssignment, len(request.Assignments))
	for _, assignment := range request.Assignments {
		assignments[assignment.FieldID] = assignment
	}
	target := form.Form{}
	baseline := map[string]pdfCPUFormValue{}
	bindings := map[string]pdfCPUFormBinding{}
	add := func(
		backendID string,
		kind FormFieldKind,
		current pdfCPUFormValue,
		apply func(pdfCPUFormValue),
	) *Failure {
		if backendID == "" {
			return &Failure{Code: FailureVerificationStructural, Message: "document form structure changed"}
		}
		if _, duplicate := baseline[backendID]; duplicate {
			return &Failure{Code: FailureFieldAmbiguous, Message: "document form field identity is ambiguous"}
		}
		baseline[backendID] = current
		opaqueID := opaqueFormFieldID(request.SourceSHA256, kind, backendID)
		assignment, selected := assignments[opaqueID]
		if !selected {
			return nil
		}
		field, found := fields[opaqueID]
		if !found || field.Kind != kind {
			return &Failure{Code: FailureFieldNotFound, Message: "document form field was not found"}
		}
		expected, failure := pdfCPUExpectedFormValue(field, assignment.Value)
		if failure != nil {
			return failure
		}
		apply(expected)
		bindings[opaqueID] = pdfCPUFormBinding{
			backendID:  backendID,
			field:      field,
			expected:   expected,
			normalized: assignment.Value,
		}
		return nil
	}
	for _, item := range source.TextFields {
		if item == nil {
			return form.Form{}, nil, nil, malformedFormField()
		}
		copy := *item
		if failure := add(
			item.ID,
			FormFieldText,
			textPDFCPUFormValue(FormFieldText, item.Value),
			func(value pdfCPUFormValue) {
				copy.Value = value.text
				target.TextFields = append(target.TextFields, &copy)
			},
		); failure != nil {
			return form.Form{}, nil, nil, failure
		}
	}
	for _, item := range source.DateFields {
		if item == nil {
			return form.Form{}, nil, nil, malformedFormField()
		}
		copy := *item
		if failure := add(
			item.ID,
			FormFieldDate,
			textPDFCPUFormValue(FormFieldDate, item.Value),
			func(value pdfCPUFormValue) {
				copy.Value = value.text
				target.DateFields = append(target.DateFields, &copy)
			},
		); failure != nil {
			return form.Form{}, nil, nil, failure
		}
	}
	for _, item := range source.CheckBoxes {
		if item == nil {
			return form.Form{}, nil, nil, malformedFormField()
		}
		copy := *item
		if failure := add(item.ID, FormFieldCheckbox, boolPDFCPUFormValue(item.Value), func(value pdfCPUFormValue) {
			copy.Value = value.checked
			target.CheckBoxes = append(target.CheckBoxes, &copy)
		}); failure != nil {
			return form.Form{}, nil, nil, failure
		}
	}
	for _, item := range source.RadioButtonGroups {
		if item == nil {
			return form.Form{}, nil, nil, malformedFormField()
		}
		copy := *item
		if failure := add(
			item.ID,
			FormFieldRadio,
			choicesPDFCPUFormValue(FormFieldRadio, item.Value),
			func(value pdfCPUFormValue) {
				copy.Value = ""
				if len(value.choices) == 1 {
					copy.Value = value.choices[0]
				}
				target.RadioButtonGroups = append(target.RadioButtonGroups, &copy)
			},
		); failure != nil {
			return form.Form{}, nil, nil, failure
		}
	}
	for _, item := range source.ComboBoxes {
		if item == nil {
			return form.Form{}, nil, nil, malformedFormField()
		}
		copy := *item
		if failure := add(
			item.ID,
			FormFieldCombo,
			choicesPDFCPUFormValue(FormFieldCombo, item.Value),
			func(value pdfCPUFormValue) {
				copy.Value = ""
				if len(value.choices) == 1 {
					copy.Value = value.choices[0]
				}
				target.ComboBoxes = append(target.ComboBoxes, &copy)
			},
		); failure != nil {
			return form.Form{}, nil, nil, failure
		}
	}
	for _, item := range source.ListBoxes {
		if item == nil {
			return form.Form{}, nil, nil, malformedFormField()
		}
		copy := *item
		if failure := add(
			item.ID,
			FormFieldList,
			choicesPDFCPUFormValue(FormFieldList, item.Values...),
			func(value pdfCPUFormValue) {
				copy.Values = append([]string(nil), value.choices...)
				target.ListBoxes = append(target.ListBoxes, &copy)
			},
		); failure != nil {
			return form.Form{}, nil, nil, failure
		}
	}
	if len(bindings) != len(assignments) || len(baseline) != len(facts.Fields) {
		return form.Form{}, nil, nil, &Failure{
			Code: FailureVerificationStructural, Message: "document form field identity changed",
		}
	}
	return target, baseline, bindings, nil
}

func pdfCPUExpectedFormValue(field FormField, value FormValue) (pdfCPUFormValue, *Failure) {
	switch field.Kind {
	case FormFieldText, FormFieldDate:
		if value.Text == nil {
			return pdfCPUFormValue{}, &Failure{
				Code: FailureFieldValueInvalid, Message: "document form field value is invalid",
			}
		}
		return textPDFCPUFormValue(field.Kind, *value.Text), nil
	case FormFieldCheckbox:
		if value.Checked == nil {
			return pdfCPUFormValue{}, &Failure{
				Code: FailureFieldValueInvalid, Message: "document form field value is invalid",
			}
		}
		return boolPDFCPUFormValue(*value.Checked), nil
	case FormFieldRadio, FormFieldCombo, FormFieldList:
		displays := make([]string, 0, len(value.Choices))
		for _, selected := range value.Choices {
			display, found := pdfCPUChoiceDisplay(field, selected)
			if !found {
				return pdfCPUFormValue{}, &Failure{
					Code: FailureChoiceInvalid, Message: "document form choice is invalid",
				}
			}
			displays = append(displays, display)
		}
		return choicesPDFCPUFormValue(field.Kind, displays...), nil
	default:
		return pdfCPUFormValue{}, &Failure{
			Code: FailureFieldUnsupported, Message: "document form field is unsupported",
		}
	}
}

func pdfCPUChoiceDisplay(field FormField, selected string) (string, bool) {
	for _, option := range field.Options {
		if option.Export == selected {
			return strings.TrimSpace(option.Display), true
		}
	}
	return "", false
}

func textPDFCPUFormValue(kind FormFieldKind, value string) pdfCPUFormValue {
	return pdfCPUFormValue{kind: kind, text: value}
}

func boolPDFCPUFormValue(value bool) pdfCPUFormValue {
	return pdfCPUFormValue{kind: FormFieldCheckbox, checked: value}
}

func choicesPDFCPUFormValue(kind FormFieldKind, values ...string) pdfCPUFormValue {
	choices := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || (kind == FormFieldRadio && value == "Off") {
			continue
		}
		choices = append(choices, value)
	}
	return pdfCPUFormValue{kind: kind, choices: choices}
}

func verifyPDFCPUFormCandidate(
	candidate []byte,
	request WorkerRequest,
	sourceInspection InspectionFacts,
	sourceFields FormFieldsFacts,
	baseline map[string]pdfCPUFormValue,
	bindings map[string]pdfCPUFormBinding,
	hybrid bool,
) backendFormWrite {
	if len(candidate) == 0 || int64(len(candidate)) > DefaultMaxArtifactBytes ||
		!bytes.HasPrefix(candidate, []byte("%PDF-")) {
		return failedFormWrite(StateFailed, FailureWriteFailed, "document form candidate is invalid")
	}
	digest := sha256.Sum256(candidate)
	outputSHA256 := hex.EncodeToString(digest[:])
	inspection := newInspectionBackend().Inspect(bytes.NewReader(candidate), request.Limits)
	if inspection.State != StateSucceeded || inspection.Facts == nil ||
		!formWriteInspectionMatches(sourceInspection, *inspection.Facts, hybrid) {
		return failedFormWrite(
			StateFailed,
			FailureVerificationStructural,
			"document form candidate failed structural verification",
		)
	}
	outputFields := newFormFieldsBackend().Fields(bytes.NewReader(candidate), request.Limits, outputSHA256)
	if outputFields.State != StateSucceeded || outputFields.Facts == nil ||
		len(outputFields.Facts.Fields) != len(baseline) {
		return failedFormWrite(
			StateFailed,
			FailureVerificationStructural,
			"document form candidate failed field verification",
		)
	}
	context, failure := readFormContext(bytes.NewReader(candidate), request.Limits)
	if failure != nil {
		return failedFormWrite(
			StateFailed,
			FailureVerificationStructural,
			"document form candidate failed structural verification",
		)
	}
	group, present, err := form.ExportForm(context.XRefTable, "")
	if err != nil || !present || group == nil || len(group.Forms) != 1 {
		return failedFormWrite(
			StateFailed,
			FailureVerificationStructural,
			"document form candidate failed field verification",
		)
	}
	values, failure := pdfCPUExportedFormValues(group.Forms[0])
	if failure != nil || len(values) != len(baseline) {
		return failedFormWrite(
			StateFailed,
			FailureVerificationStructural,
			"document form candidate failed field verification",
		)
	}
	if !formFieldStructureMatches(sourceFields, *outputFields.Facts, baseline, request.Input.SHA256, outputSHA256) {
		return failedFormWrite(
			StateFailed,
			FailureVerificationStructural,
			"document form candidate changed field or widget structure",
		)
	}
	selectedBackendIDs := make(map[string]struct{}, len(bindings))
	checkedWidgets := 0
	appearanceWidgets := 0
	for _, binding := range bindings {
		actual, found := values[binding.backendID]
		if !found ||
			((binding.field.Kind != FormFieldCombo && binding.field.Kind != FormFieldList) &&
				!equalPDFCPUFormValue(actual, binding.expected)) {
			return failedFormWrite(
				StateFailed,
				FailureVerificationStructural,
				"document form candidate value verification failed",
			)
		}
		if (binding.field.Kind == FormFieldCombo || binding.field.Kind == FormFieldList) &&
			!pdfCPUCanonicalChoiceValueMatches(context, binding) {
			return failedFormWrite(
				StateFailed,
				FailureVerificationStructural,
				"document form candidate choice value verification failed",
			)
		}
		if (binding.field.Kind == FormFieldCheckbox || binding.field.Kind == FormFieldRadio) &&
			!pdfCPUButtonStateMatches(context, binding) {
			return failedFormWrite(
				StateFailed,
				FailureVerificationStructural,
				"document form candidate button state verification failed",
			)
		}
		selectedBackendIDs[binding.backendID] = struct{}{}
		checkedWidgets += len(binding.field.Widgets)
		count, appearanceFailure := countPDFCPUFieldAppearances(context, binding.backendID)
		if appearanceFailure != nil || count != len(binding.field.Widgets) {
			return failedFormWrite(
				StateUnsupported,
				FailureAppearanceUnavailable,
				"document form candidate appearance is unavailable",
			)
		}
		appearanceWidgets += count
	}
	unchanged := 0
	for backendID, before := range baseline {
		if _, selected := selectedBackendIDs[backendID]; selected {
			continue
		}
		after, found := values[backendID]
		if !found || !equalPDFCPUFormValue(before, after) {
			return failedFormWrite(
				StateFailed,
				FailureVerificationStructural,
				"document form candidate changed an unassigned field",
			)
		}
		unchanged++
	}
	visual, failure := verifyPopplerFormCandidate(candidate, request, context, group.Forms[0], bindings)
	if failure != nil {
		return failedFormWrite(failureState(failure.Code), failure.Code, failure.Message)
	}
	outputInspection := *inspection.Facts
	independentVisualBackend := BackendIdentity{}
	independentVisualAssertions := 0
	independentRenderedPages := 0
	structuralAssertions := formWriteStructuralAssertionCount
	if hybrid {
		flattened, flattenFailure := flattenAndVerifyPDFCPUHybridCandidate(
			candidate,
			request,
			sourceInspection,
		)
		if flattenFailure != nil {
			return failedFormWrite(
				failureState(flattenFailure.Code),
				flattenFailure.Code,
				flattenFailure.Message,
			)
		}
		candidate = flattened.Candidate
		outputInspection = flattened.Inspection
		visual.Assertions += flattened.PopplerAssertions
		visual.RenderedPages = flattened.PopplerRenderedPages
		independentVisualBackend = ghostscriptIdentity()
		independentVisualAssertions = flattened.IndependentAssertions
		independentRenderedPages = flattened.IndependentRenderedPages
		structuralAssertions = hybridWriteStructuralAssertionCount
		digest = sha256.Sum256(candidate)
		outputSHA256 = hex.EncodeToString(digest[:])
	}
	artifact := Artifact{
		Ref:          workerArtifactRef(request.OperationID, filledCandidateArtifactName),
		Kind:         filledCandidateArtifactKind,
		ContentType:  "application/pdf",
		Size:         int64(len(candidate)),
		SHA256:       outputSHA256,
		SourceSHA256: request.Input.SHA256,
		Pages:        append([]int(nil), request.Fill.AffectedPages...),
	}
	facts := &FormWriteFacts{
		Backend: BackendIdentity{
			Name: PDFCPUBackendName, Version: PDFCPUBackendVersion, Role: "production",
			IsolationMode: "one_shot_process",
		},
		VisualBackend:               popplerIdentity(),
		IndependentVisualBackend:    independentVisualBackend,
		SourceSHA256:                request.Input.SHA256,
		RequestSHA256:               request.Fill.RequestSHA256,
		OutputSHA256:                outputSHA256,
		OutputSize:                  int64(len(candidate)),
		AffectedPages:               append([]int(nil), request.Fill.AffectedPages...),
		StructuralAssertions:        structuralAssertions,
		CheckedFields:               len(bindings),
		CheckedWidgets:              checkedWidgets,
		UnchangedFields:             unchanged,
		AppearanceWidgets:           appearanceWidgets,
		VisualAssertions:            visual.Assertions,
		RenderedPages:               visual.RenderedPages,
		IndependentVisualAssertions: independentVisualAssertions,
		IndependentRenderedPages:    independentRenderedPages,
		Output:                      formOutputFacts(sourceInspection, outputInspection, hybrid),
	}
	return backendFormWrite{
		State: StateSucceeded, Facts: facts, Candidate: candidate,
		Artifacts: []WorkerArtifact{{Name: filledCandidateArtifactName, Artifact: artifact}},
	}
}

func pdfCPUButtonStateMatches(context *model.Context, binding pdfCPUFormBinding) bool {
	field, err := pdfCPUFieldDict(context, binding.backendID)
	if err != nil {
		return false
	}
	valueName := field.NameEntry("V")
	value := ""
	if valueName != nil {
		value, err = types.DecodeName(*valueName)
		if err != nil {
			return false
		}
	}
	widgets := field.ArrayEntry("Kids")
	if len(widgets) == 0 {
		widgets = []types.Object{field}
	}
	if binding.field.Kind == FormFieldCheckbox {
		if valueName == nil {
			return false
		}
		expected := binding.normalized.Checked != nil && *binding.normalized.Checked
		if expected != (value != "Off") || len(widgets) != 1 {
			return false
		}
		widget, dereferenceErr := context.DereferenceDict(widgets[0])
		if dereferenceErr != nil || widget == nil {
			return false
		}
		appearanceState := widget.NameEntry("AS")
		return appearanceState != nil && *appearanceState == *valueName
	}
	if len(binding.normalized.Choices) == 0 {
		if valueName != nil {
			decodedValue, decodeErr := types.DecodeName(*valueName)
			if decodeErr != nil || decodedValue != "Off" {
				return false
			}
		}
		for _, widgetObject := range widgets {
			widget, dereferenceErr := context.DereferenceDict(widgetObject)
			if dereferenceErr != nil || widget == nil {
				return false
			}
			appearanceState := widget.NameEntry("AS")
			if appearanceState == nil {
				return false
			}
			state, decodeErr := types.DecodeName(*appearanceState)
			if decodeErr != nil || state != "Off" {
				return false
			}
		}
		return true
	}
	if valueName == nil || len(binding.normalized.Choices) != 1 || value != binding.normalized.Choices[0] {
		return false
	}
	matched := 0
	for _, widgetObject := range widgets {
		widget, dereferenceErr := context.DereferenceDict(widgetObject)
		if dereferenceErr != nil || widget == nil {
			return false
		}
		appearanceState := widget.NameEntry("AS")
		if appearanceState == nil {
			return false
		}
		state, decodeErr := types.DecodeName(*appearanceState)
		if decodeErr != nil {
			return false
		}
		contains, valid := pdfCPUWidgetContainsButtonState(context, widget, value)
		if !valid || (contains && state != value) || (!contains && state != "Off") {
			return false
		}
		if contains {
			matched++
		}
	}
	return matched == 1
}

func pdfCPUWidgetContainsButtonState(context *model.Context, widget types.Dict, expected string) (bool, bool) {
	appearanceObject, present := widget.Find("AP")
	if !present {
		return false, false
	}
	appearance, err := context.DereferenceDict(appearanceObject)
	if err != nil || appearance == nil {
		return false, false
	}
	normalObject, present := appearance.Find("N")
	if !present {
		return false, false
	}
	normal, err := context.DereferenceDict(normalObject)
	if err != nil || normal == nil {
		return false, false
	}
	found := false
	for name := range normal {
		decoded, decodeErr := types.DecodeName(name)
		if decodeErr != nil {
			return false, false
		}
		if decoded == expected {
			found = true
		}
	}
	return found, true
}

func applyPDFCPUCanonicalChoiceValues(
	context *model.Context,
	bindings map[string]pdfCPUFormBinding,
) error {
	for _, binding := range bindings {
		if binding.field.Kind == FormFieldRadio && len(binding.normalized.Choices) == 0 {
			field, err := pdfCPUFieldDict(context, binding.backendID)
			if err != nil {
				return err
			}
			field["V"] = types.Name("Off")
			widgets := field.ArrayEntry("Kids")
			if len(widgets) == 0 {
				widgets = []types.Object{field}
			}
			for _, widgetObject := range widgets {
				widget, dereferenceErr := context.DereferenceDict(widgetObject)
				if dereferenceErr != nil || widget == nil {
					return errors.New("form radio widget is unavailable")
				}
				widget["AS"] = types.Name("Off")
			}
			continue
		}
		if binding.field.Kind != FormFieldCombo && binding.field.Kind != FormFieldList {
			continue
		}
		field, err := pdfCPUFieldDict(context, binding.backendID)
		if err != nil {
			return err
		}
		values := binding.normalized.Choices
		if len(values) == 0 {
			delete(field, "V")
			delete(field, "I")
			continue
		}
		encoded := make(types.Array, 0, len(values))
		indices := make(types.Array, 0, len(values))
		for _, value := range values {
			index := pdfCPUChoiceIndex(binding.field, value)
			if index < 0 {
				return errors.New("form choice is unavailable")
			}
			literal, encodeErr := types.EscapedUTF16String(value)
			if encodeErr != nil {
				return encodeErr
			}
			encoded = append(encoded, types.StringLiteral(*literal))
			indices = append(indices, types.Integer(index))
		}
		if binding.field.Kind == FormFieldList && binding.field.MultiSelect {
			field["V"] = encoded
		} else {
			field["V"] = encoded[0]
		}
		field["I"] = indices
	}
	return nil
}

func pdfCPUCanonicalChoiceValueMatches(context *model.Context, binding pdfCPUFormBinding) bool {
	field, err := pdfCPUFieldDict(context, binding.backendID)
	if err != nil {
		return false
	}
	object, found := field.Find("V")
	if !found {
		return len(binding.normalized.Choices) == 0 && len(field.ArrayEntry("I")) == 0
	}
	object, err = context.Dereference(object)
	if err != nil {
		return false
	}
	actual := make([]string, 0, len(binding.normalized.Choices))
	switch value := object.(type) {
	case types.StringLiteral, types.HexLiteral:
		decoded, decodeErr := types.StringOrHexLiteral(value)
		if decodeErr != nil || decoded == nil {
			return false
		}
		actual = append(actual, *decoded)
	case types.Array:
		for _, item := range value {
			decoded, decodeErr := types.StringOrHexLiteral(item)
			if decodeErr != nil || decoded == nil {
				return false
			}
			actual = append(actual, *decoded)
		}
	default:
		return false
	}
	if !slices.Equal(actual, binding.normalized.Choices) {
		return false
	}
	indices := field.ArrayEntry("I")
	if len(indices) != len(actual) {
		return false
	}
	for index, item := range indices {
		position, ok := item.(types.Integer)
		if !ok || int(position) != pdfCPUChoiceIndex(binding.field, actual[index]) {
			return false
		}
	}
	return true
}

func pdfCPUChoiceIndex(field FormField, export string) int {
	for index, option := range field.Options {
		if option.Export == export {
			return index
		}
	}
	return -1
}

func pdfCPUFieldDict(context *model.Context, backendID string) (types.Dict, error) {
	numbers, failure := fieldObjectNumbers(backendID)
	if failure != nil {
		return nil, errors.New("form field identity is invalid")
	}
	object, err := context.FindObject(numbers[len(numbers)-1])
	if err != nil {
		return nil, err
	}
	field, err := context.DereferenceDict(object)
	if err != nil || field == nil {
		return nil, errors.New("form field structure is invalid")
	}
	return field, nil
}

func formFieldStructureMatches(
	source FormFieldsFacts,
	output FormFieldsFacts,
	baseline map[string]pdfCPUFormValue,
	sourceSHA256 string,
	outputSHA256 string,
) bool {
	sourceByID := make(map[string]FormField, len(source.Fields))
	for _, field := range source.Fields {
		sourceByID[field.ID] = field
	}
	outputByID := make(map[string]FormField, len(output.Fields))
	for _, field := range output.Fields {
		outputByID[field.ID] = field
	}
	for backendID, value := range baseline {
		sourceField, sourceFound := sourceByID[opaqueFormFieldID(sourceSHA256, value.kind, backendID)]
		outputField, outputFound := outputByID[opaqueFormFieldID(outputSHA256, value.kind, backendID)]
		if !sourceFound || !outputFound || !equalFormFieldStructure(sourceField, outputField) {
			return false
		}
	}
	return len(sourceByID) == len(baseline) && len(outputByID) == len(baseline)
}

func equalFormFieldStructure(source FormField, output FormField) bool {
	if source.Name != output.Name || source.AlternateName != output.AlternateName || source.Kind != output.Kind ||
		source.ReadOnly != output.ReadOnly || source.Required != output.Required ||
		source.Multiline != output.Multiline || source.MultiSelect != output.MultiSelect ||
		source.Editable != output.Editable || source.MaxLength != output.MaxLength ||
		source.DateFormat != output.DateFormat || source.HasDefault != output.HasDefault ||
		!slices.Equal(source.Options, output.Options) || len(source.Widgets) != len(output.Widgets) {
		return false
	}
	for index := range source.Widgets {
		if source.Widgets[index].Page != output.Widgets[index].Page ||
			source.Widgets[index].Ordinal != output.Widgets[index].Ordinal {
			return false
		}
	}
	return true
}

func formWriteInspectionMatches(source InspectionFacts, output InspectionFacts, hybrid bool) bool {
	if hybrid {
		return output.Encryption.State == source.Encryption.State &&
			output.Encryption.PasswordRequired == source.Encryption.PasswordRequired &&
			output.Encryption.OperationPermissions == source.Encryption.OperationPermissions &&
			output.Signatures.State == FactAbsent && output.Restrictions.UsageRights == FactAbsent &&
			output.Restrictions.DocMDP == FactAbsent && output.Restrictions.FieldMDP == FactAbsent &&
			output.XFA.State == FactAbsent && output.AcroForm.State == FactPresent &&
			sameFormPageAndFieldCounts(source, output)
	}
	return output.Encryption.State == FactAbsent && output.Signatures.State == FactAbsent &&
		output.Restrictions.State == FactAbsent && output.XFA.State == FactAbsent &&
		output.AcroForm.State == FactPresent && sameFormPageAndFieldCounts(source, output)
}

func sameFormPageAndFieldCounts(source InspectionFacts, output InspectionFacts) bool {
	return source.PageCount.State == FactPresent &&
		output.PageCount.State == FactPresent && source.PageCount.Value != nil && output.PageCount.Value != nil &&
		*source.PageCount.Value == *output.PageCount.Value && source.AcroForm.FieldCount.State == FactPresent &&
		output.AcroForm.FieldCount.State == FactPresent && source.AcroForm.FieldCount.Value != nil &&
		output.AcroForm.FieldCount.Value != nil && *source.AcroForm.FieldCount.Value == *output.AcroForm.FieldCount.Value
}

func formOutputFacts(source, output InspectionFacts, hybrid bool) FormOutputFacts {
	pageCount := 0
	if output.PageCount.Value != nil {
		pageCount = *output.PageCount.Value
	}
	facts := FormOutputFacts{
		Mode: FormOutputEditableAcroForm, PageCount: pageCount,
		AcroForm: output.AcroForm.State, XFA: output.XFA.State, Encryption: output.Encryption.State,
		OperationPermissions: output.Encryption.OperationPermissions,
		ContentSignatures:    output.Signatures.Content.State,
		UsageRights:          output.Signatures.UsageRights.State,
		Actions:              output.Actions.State,
	}
	if !hybrid {
		return facts
	}
	facts.Mode = FormOutputFlattenedPrint
	facts.Normalizations = []string{"xfa_removed", "acroform_flattened"}
	if source.HybridForm.Scripts == FactPresent {
		facts.Normalizations = append(facts.Normalizations, "xfa_scripts_removed")
	}
	if source.Signatures.UsageRights.State == FactPresent || source.Restrictions.UsageRights == FactPresent {
		facts.Normalizations = append(facts.Normalizations, "usage_rights_removed")
	}
	if source.Encryption.State == FactPresent {
		facts.Normalizations = append(facts.Normalizations, "encryption_preserved")
	}
	return facts
}

func pdfCPUExportedFormValues(exported form.Form) (map[string]pdfCPUFormValue, *Failure) {
	values := map[string]pdfCPUFormValue{}
	add := func(id string, value pdfCPUFormValue) *Failure {
		if id == "" {
			return &Failure{Code: FailureVerificationStructural, Message: "document form field identity changed"}
		}
		if _, duplicate := values[id]; duplicate {
			return &Failure{Code: FailureFieldAmbiguous, Message: "document form field identity is ambiguous"}
		}
		values[id] = value
		return nil
	}
	for _, item := range exported.TextFields {
		if item == nil {
			return nil, malformedFormField()
		}
		if failure := add(item.ID, textPDFCPUFormValue(FormFieldText, item.Value)); failure != nil {
			return nil, failure
		}
	}
	for _, item := range exported.DateFields {
		if item == nil {
			return nil, malformedFormField()
		}
		if failure := add(item.ID, textPDFCPUFormValue(FormFieldDate, item.Value)); failure != nil {
			return nil, failure
		}
	}
	for _, item := range exported.CheckBoxes {
		if item == nil {
			return nil, malformedFormField()
		}
		if failure := add(item.ID, boolPDFCPUFormValue(item.Value)); failure != nil {
			return nil, failure
		}
	}
	for _, item := range exported.RadioButtonGroups {
		if item == nil {
			return nil, malformedFormField()
		}
		if failure := add(item.ID, choicesPDFCPUFormValue(FormFieldRadio, item.Value)); failure != nil {
			return nil, failure
		}
	}
	for _, item := range exported.ComboBoxes {
		if item == nil {
			return nil, malformedFormField()
		}
		if failure := add(item.ID, choicesPDFCPUFormValue(FormFieldCombo, item.Value)); failure != nil {
			return nil, failure
		}
	}
	for _, item := range exported.ListBoxes {
		if item == nil {
			return nil, malformedFormField()
		}
		if failure := add(item.ID, choicesPDFCPUFormValue(FormFieldList, item.Values...)); failure != nil {
			return nil, failure
		}
	}
	return values, nil
}

func equalPDFCPUFormValue(left pdfCPUFormValue, right pdfCPUFormValue) bool {
	return left.kind == right.kind && left.text == right.text && left.checked == right.checked &&
		slices.Equal(left.choices, right.choices)
}

func countPDFCPUFieldAppearances(context *model.Context, backendID string) (int, *Failure) {
	numbers, failure := fieldObjectNumbers(backendID)
	if failure != nil {
		return 0, failure
	}
	fieldObject, err := context.FindObject(numbers[len(numbers)-1])
	if err != nil {
		return 0, malformedFormField()
	}
	field, err := context.DereferenceDict(fieldObject)
	if err != nil || field == nil {
		return 0, malformedFormField()
	}
	widgets := field.ArrayEntry("Kids")
	if len(widgets) == 0 {
		widgets = []types.Object{fieldObject}
	}
	count := 0
	for _, widgetObject := range widgets {
		widget, err := context.DereferenceDict(widgetObject)
		if err != nil || widget == nil {
			return 0, malformedFormField()
		}
		appearanceObject, present := widget.Find("AP")
		if !present {
			return 0, &Failure{Code: FailureAppearanceUnavailable, Message: "PDF field appearance is unavailable"}
		}
		appearance, err := context.DereferenceDict(appearanceObject)
		if err != nil || appearance == nil {
			return 0, &Failure{Code: FailureAppearanceUnavailable, Message: "PDF field appearance is unavailable"}
		}
		if normal, found := appearance.Find("N"); !found || normal == nil {
			return 0, &Failure{Code: FailureAppearanceUnavailable, Message: "PDF field appearance is unavailable"}
		}
		count++
	}
	return count, nil
}

var _ io.Writer = (*boundedFormWriteBuffer)(nil)
