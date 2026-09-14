//go:build linux && amd64

package document

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	pdfcpuapi "github.com/pdfcpu/pdfcpu/pkg/api"
	pdfcpucore "github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/form"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/primitives"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

type pdfCPUFormFieldsBackend struct{}

func newFormFieldsBackend() formFieldsBackend { return pdfCPUFormFieldsBackend{} }

func failedFormFields(state State, code FailureCode, message string) backendFormFields {
	return backendFormFields{
		State:   state,
		Failure: &Failure{Code: code, Message: message},
	}
}

func (pdfCPUFormFieldsBackend) Fields(
	reader io.ReadSeeker,
	limits Limits,
	sourceSHA256 string,
) backendFormFields {
	if reader == nil {
		return failedFormFields(
			StateUnavailable,
			FailureBackendUnavailable,
			"document form backend is unavailable",
		)
	}
	context, failure := readFormContext(reader, limits)
	if failure != nil {
		return failedFormFields(failureState(failure.Code), failure.Code, failure.Message)
	}
	formGroup, present, err := form.ExportForm(context.XRefTable, "")
	if err != nil {
		return failedFormFields(StateFailed, FailureMalformedPDF, "PDF form structure is malformed")
	}
	if !present || formGroup == nil || len(formGroup.Forms) != 1 {
		// The inspection gate has already proved that an AcroForm with at least one
		// field exists. pdfcpu reports present=false when none of those fields can
		// be represented by its export model (for example, a signature-only form).
		return failedFormFields(StateUnsupported, FailureFieldUnsupported, "PDF contains unsupported form fields")
	}
	facts, failure := normalizePDFCPUFields(context, formGroup.Forms[0], sourceSHA256)
	if failure != nil {
		return failedFormFields(failureState(failure.Code), failure.Code, failure.Message)
	}
	return backendFormFields{State: StateSucceeded, Facts: facts}
}

func readFormContext(reader io.ReadSeeker, limits Limits) (*model.Context, *Failure) {
	configuration := model.NewDefaultConfiguration()
	configuration.Cmd = model.LISTFORMFIELDS
	configuration.ValidationMode = model.ValidationRelaxed
	configuration.Limits.MaxStreamBytes = limits.MaxInputBytes
	configuration.Limits.MaxDecodeBytes = limits.MaxContentBytes
	configuration.Limits.MaxImageBytes = limits.MaxContentBytes
	configuration.Limits.MaxImagePixels = limits.MaxContentBytes / 4
	configuration.Limits.MaxObjectCount = limits.MaxObjects
	configuration.Limits.MaxObjectStreamCount = limits.MaxObjects
	configuration.Limits.MaxObjectStreamFirst = limits.MaxContentBytes
	configuration.Limits.MaxXRefEntries = limits.MaxObjects
	configuration.Limits.MaxRecursionDepth = limits.MaxRecursionDepth
	context, err := pdfcpuapi.ReadContext(reader, configuration)
	if errors.Is(err, pdfcpucore.ErrWrongPassword) {
		return nil, &Failure{
			Code:    FailurePasswordRequired,
			Message: "document form discovery requires a protected password input",
		}
	}
	if isPDFCPUResourceLimit(err) {
		return nil, &Failure{Code: FailureInspectionLimit, Message: "document exceeds an inspection limit"}
	}
	if err != nil || context == nil || context.XRefTable == nil {
		return nil, &Failure{Code: FailureMalformedPDF, Message: "PDF structure is malformed or unsupported"}
	}
	root, err := context.Catalog()
	if err != nil || root == nil {
		return nil, &Failure{Code: FailureMalformedPDF, Message: "PDF catalog is malformed"}
	}
	if err = boundCatalogMetadata(context, root, limits.MaxContentBytes); err != nil {
		if isPDFCPUResourceLimit(err) {
			return nil, &Failure{Code: FailureInspectionLimit, Message: "document exceeds an inspection limit"}
		}
		return nil, &Failure{Code: FailureMalformedPDF, Message: "PDF catalog metadata is malformed"}
	}
	if err = pdfcpuapi.ValidateContext(context); err != nil {
		if isPDFCPUResourceLimit(err) {
			return nil, &Failure{Code: FailureInspectionLimit, Message: "document exceeds an inspection limit"}
		}
		return nil, &Failure{Code: FailureMalformedPDF, Message: "PDF structure is malformed or unsupported"}
	}
	if context.PageCount < 1 || context.PageCount > limits.MaxPages {
		return nil, &Failure{Code: FailureInspectionLimit, Message: "document exceeds the inspection page limit"}
	}
	return context, nil
}

func normalizePDFCPUFields(
	context *model.Context,
	exported form.Form,
	sourceSHA256 string,
) (*FormFieldsFacts, *Failure) {
	limits := defaultFormFieldLimits()
	widgetLocations, failure := collectFormWidgetLocations(context, exported)
	if failure != nil {
		return nil, failure
	}
	fields := make([]FormField, 0)
	seenBackendIDs := map[string]struct{}{}
	appendField := func(field FormField, backendID string) *Failure {
		if _, found := seenBackendIDs[backendID]; found {
			return &Failure{Code: FailureFormUnsupported, Message: "PDF form field identity is ambiguous"}
		}
		seenBackendIDs[backendID] = struct{}{}
		if len(fields) >= limits.MaxFields {
			return &Failure{Code: FailureInspectionLimit, Message: "PDF form exceeds the field limit"}
		}
		fields = append(fields, field)
		return nil
	}

	for _, item := range exported.TextFields {
		if item == nil {
			return nil, malformedFormField()
		}
		field, failure := normalizeFieldBase(
			context,
			sourceSHA256,
			item.ID,
			item.Name,
			item.AltName,
			FormFieldText,
			widgetLocations[item.ID],
		)
		if failure != nil {
			return nil, failure
		}
		field.Multiline = item.Multiline
		field.MaxLength = item.MaxLen
		if field.MaxLength < 0 {
			return nil, malformedFormField()
		}
		if failure = appendField(field, item.ID); failure != nil {
			return nil, failure
		}
	}
	for _, item := range exported.DateFields {
		if item == nil {
			return nil, malformedFormField()
		}
		field, failure := normalizeFieldBase(
			context,
			sourceSHA256,
			item.ID,
			item.Name,
			item.AltName,
			FormFieldDate,
			widgetLocations[item.ID],
		)
		if failure != nil || !validFieldText(item.Format, limits.MaxTextBytes) {
			if failure != nil {
				return nil, failure
			}
			return nil, &Failure{Code: FailureFieldUnsupported, Message: "PDF date field format is unsupported"}
		}
		field.DateFormat = item.Format
		if failure = validateFieldActions(
			context,
			item.ID,
			widgetLocations[item.ID],
			field.Kind,
			field.DateFormat,
		); failure != nil {
			return nil, failure
		}
		if failure = appendField(field, item.ID); failure != nil {
			return nil, failure
		}
	}
	for _, item := range exported.CheckBoxes {
		if item == nil {
			return nil, malformedFormField()
		}
		field, failure := normalizeFieldBase(
			context,
			sourceSHA256,
			item.ID,
			item.Name,
			item.AltName,
			FormFieldCheckbox,
			widgetLocations[item.ID],
		)
		if failure != nil {
			return nil, failure
		}
		flags, flagsFailure := fieldFlags(context, item.ID)
		if flagsFailure != nil {
			return nil, flagsFailure
		}
		if primitives.FieldFlags(flags)&primitives.FieldPushbutton > 0 {
			return nil, &Failure{Code: FailureFieldUnsupported, Message: "PDF pushbutton fields are unsupported"}
		}
		if failure = appendField(field, item.ID); failure != nil {
			return nil, failure
		}
	}
	for _, item := range exported.RadioButtonGroups {
		if item == nil {
			return nil, malformedFormField()
		}
		field, failure := normalizeFieldBase(
			context,
			sourceSHA256,
			item.ID,
			item.Name,
			item.AltName,
			FormFieldRadio,
			widgetLocations[item.ID],
		)
		if failure != nil {
			return nil, failure
		}
		field.Options, failure = normalizedOptions(item.Options, limits)
		if failure != nil || len(field.Options) == 0 {
			if failure != nil {
				return nil, failure
			}
			return nil, &Failure{Code: FailureFieldUnsupported, Message: "PDF radio field has no supported options"}
		}
		if failure = appendField(field, item.ID); failure != nil {
			return nil, failure
		}
	}
	for _, item := range exported.ComboBoxes {
		if item == nil {
			return nil, malformedFormField()
		}
		field, failure := normalizeFieldBase(
			context,
			sourceSHA256,
			item.ID,
			item.Name,
			item.AltName,
			FormFieldCombo,
			widgetLocations[item.ID],
		)
		if failure != nil {
			return nil, failure
		}
		field.Editable = item.Editable
		field.Options, failure = choiceOptions(context, item.ID, item.Options, limits)
		if failure != nil {
			return nil, failure
		}
		if !field.Editable && len(field.Options) == 0 {
			return nil, &Failure{Code: FailureFieldUnsupported, Message: "PDF combo field has no supported options"}
		}
		if failure = appendField(field, item.ID); failure != nil {
			return nil, failure
		}
	}
	for _, item := range exported.ListBoxes {
		if item == nil {
			return nil, malformedFormField()
		}
		field, failure := normalizeFieldBase(
			context,
			sourceSHA256,
			item.ID,
			item.Name,
			item.AltName,
			FormFieldList,
			widgetLocations[item.ID],
		)
		if failure != nil {
			return nil, failure
		}
		field.MultiSelect = item.Multi
		field.Options, failure = choiceOptions(context, item.ID, item.Options, limits)
		if failure != nil || len(field.Options) == 0 {
			if failure != nil {
				return nil, failure
			}
			return nil, &Failure{Code: FailureFieldUnsupported, Message: "PDF list field has no supported options"}
		}
		if failure = appendField(field, item.ID); failure != nil {
			return nil, failure
		}
	}
	if len(fields) == 0 {
		return nil, &Failure{Code: FailureFieldUnsupported, Message: "PDF has no supported AcroForm fields"}
	}
	if expected, _, err := form.FormFields(context); err != nil || len(expected) != len(fields) {
		return nil, &Failure{Code: FailureFieldUnsupported, Message: "PDF contains unsupported form fields"}
	}
	totalWidgets := 0
	for _, field := range fields {
		totalWidgets += len(field.Widgets)
	}
	if totalWidgets > limits.MaxWidgets {
		return nil, &Failure{Code: FailureInspectionLimit, Message: "PDF form exceeds the widget limit"}
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].ID < fields[j].ID })
	facts := &FormFieldsFacts{
		Backend: BackendIdentity{
			Name: PDFCPUBackendName, Version: PDFCPUBackendVersion, Role: "production",
			IsolationMode: "one_shot_process",
		},
		Limits: limits,
		Fields: fields,
	}
	if !formFieldsReportWithinLimit(*facts) {
		return nil, &Failure{Code: FailureInspectionLimit, Message: "PDF form report exceeds the metadata limit"}
	}
	return facts, nil
}

func normalizeFieldBase(
	context *model.Context,
	sourceSHA256 string,
	backendID string,
	name string,
	alternateName string,
	kind FormFieldKind,
	locations []backendWidgetLocation,
) (FormField, *Failure) {
	limits := defaultFormFieldLimits()
	if !validBackendFieldID(backendID) || !validFieldText(name, limits.MaxTextBytes) ||
		!validFieldText(alternateName, limits.MaxTextBytes) || len(locations) == 0 {
		return FormField{}, malformedFormField()
	}
	flags, failure := fieldFlags(context, backendID)
	if failure != nil {
		return FormField{}, failure
	}
	hasDefault, failure := fieldHasEntry(context, backendID, "DV")
	if failure != nil {
		return FormField{}, failure
	}
	hasValue, failure := fieldHasEntry(context, backendID, "V")
	if failure != nil {
		return FormField{}, failure
	}
	field := FormField{
		ID:            opaqueFormFieldID(sourceSHA256, kind, backendID),
		Name:          name,
		AlternateName: alternateName,
		Kind:          kind,
		ReadOnly:      primitives.FieldFlags(flags)&primitives.FieldReadOnly > 0,
		Required:      primitives.FieldFlags(flags)&primitives.FieldRequired > 0,
		HasDefault:    hasDefault,
		HasValue:      hasValue,
	}
	if kind != FormFieldDate {
		if failure = validateFieldActions(context, backendID, locations, kind, ""); failure != nil {
			return FormField{}, failure
		}
	}
	for index, location := range locations {
		if location.page < 1 || location.page > context.PageCount || location.objectID == "" {
			return FormField{}, malformedFormField()
		}
		ordinal := index + 1
		field.Widgets = append(field.Widgets, FormFieldWidget{
			ID:      opaqueFormWidgetID(field.ID, location.objectID, location.page, ordinal),
			Page:    location.page,
			Ordinal: ordinal,
		})
	}
	return field, nil
}

func validBackendFieldID(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, part := range strings.Split(value, ".") {
		number, err := strconv.Atoi(part)
		if err != nil || number <= 0 {
			return false
		}
	}
	return true
}

func fieldObjectNumbers(backendID string) ([]int, *Failure) {
	if !validBackendFieldID(backendID) {
		return nil, malformedFormField()
	}
	parts := strings.Split(backendID, ".")
	numbers := make([]int, 0, len(parts))
	for _, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil {
			return nil, malformedFormField()
		}
		numbers = append(numbers, number)
	}
	return numbers, nil
}

func fieldFlags(context *model.Context, backendID string) (int, *Failure) {
	numbers, failure := fieldObjectNumbers(backendID)
	if failure != nil {
		return 0, failure
	}
	flags := 0
	for _, number := range numbers {
		object, err := context.FindObject(number)
		if err != nil {
			return 0, malformedFormField()
		}
		dictionary, err := context.DereferenceDict(object)
		if err != nil || dictionary == nil {
			return 0, malformedFormField()
		}
		if value := dictionary.IntEntry("Ff"); value != nil {
			flags = *value
		}
	}
	return flags, nil
}

func fieldHasEntry(context *model.Context, backendID string, key string) (bool, *Failure) {
	numbers, failure := fieldObjectNumbers(backendID)
	if failure != nil {
		return false, failure
	}
	for index := len(numbers) - 1; index >= 0; index-- {
		object, err := context.FindObject(numbers[index])
		if err != nil {
			return false, malformedFormField()
		}
		dictionary, err := context.DereferenceDict(object)
		if err != nil || dictionary == nil {
			return false, malformedFormField()
		}
		if _, found := dictionary.Find(key); found {
			return true, nil
		}
	}
	return false, nil
}

func validateFieldActions(
	context *model.Context,
	backendID string,
	locations []backendWidgetLocation,
	kind FormFieldKind,
	dateFormat string,
) *Failure {
	numbers, failure := fieldObjectNumbers(backendID)
	if failure != nil {
		return failure
	}
	for _, location := range locations {
		number, err := strconv.Atoi(location.objectID)
		if err != nil || number <= 0 {
			return malformedFormField()
		}
		numbers = append(numbers, number)
	}
	seenObjects := map[int]struct{}{}
	foundDateFormat := false
	for _, number := range numbers {
		if _, seen := seenObjects[number]; seen {
			continue
		}
		seenObjects[number] = struct{}{}
		object, err := context.FindObject(number)
		if err != nil {
			return malformedFormField()
		}
		dictionary, err := context.DereferenceDict(object)
		if err != nil || dictionary == nil {
			return malformedFormField()
		}
		if _, present := dictionary.Find("A"); present {
			return unsupportedFieldAction()
		}
		additionalObject, present := dictionary.Find("AA")
		if !present {
			continue
		}
		if kind != FormFieldDate || foundDateFormat {
			return unsupportedFieldAction()
		}
		additional, err := context.DereferenceDict(additionalObject)
		if err != nil || additional == nil || len(additional) != 1 {
			return unsupportedFieldAction()
		}
		actionObject, present := additional.Find("F")
		if !present {
			return unsupportedFieldAction()
		}
		action, err := context.DereferenceDict(actionObject)
		if err != nil || action == nil {
			return unsupportedFieldAction()
		}
		for key := range action {
			if key != "S" && key != "JS" && key != "Type" {
				return unsupportedFieldAction()
			}
		}
		if subtype := action.NameEntry("S"); subtype == nil || *subtype != "JavaScript" {
			return unsupportedFieldAction()
		}
		if actionType := action.NameEntry("Type"); actionType != nil && *actionType != "Action" {
			return unsupportedFieldAction()
		}
		javascriptObject, present := action.Find("JS")
		if !present || dateFormat == "" || strings.ContainsAny(dateFormat, `"\`) {
			return unsupportedFieldAction()
		}
		javascript, javascriptFailure := decodeFieldString(
			context,
			javascriptObject,
			defaultFormFieldLimits().MaxTextBytes,
		)
		if javascriptFailure != nil || javascript != `AFDate_FormatEx("`+dateFormat+`")` {
			return unsupportedFieldAction()
		}
		foundDateFormat = true
	}
	if kind == FormFieldDate && !foundDateFormat {
		return unsupportedFieldAction()
	}
	return nil
}

func unsupportedFieldAction() *Failure {
	return &Failure{Code: FailureFieldUnsupported, Message: "PDF field actions are unsupported"}
}

func choiceOptions(
	context *model.Context,
	backendID string,
	fallback []string,
	limits FormFieldLimits,
) ([]FormFieldOption, *Failure) {
	numbers, failure := fieldObjectNumbers(backendID)
	if failure != nil {
		return nil, failure
	}
	for index := len(numbers) - 1; index >= 0; index-- {
		object, err := context.FindObject(numbers[index])
		if err != nil {
			return nil, malformedFormField()
		}
		dictionary, err := context.DereferenceDict(object)
		if err != nil || dictionary == nil {
			return nil, malformedFormField()
		}
		optionObject, found := dictionary.Find("Opt")
		if !found {
			continue
		}
		array, err := context.DereferenceArray(optionObject)
		if err != nil {
			return nil, malformedFormField()
		}
		if len(array) > limits.MaxOptions {
			return nil, &Failure{Code: FailureInspectionLimit, Message: "PDF form exceeds the option limit"}
		}
		options := make([]FormFieldOption, 0, len(array))
		for _, optionObject := range array {
			option, optionFailure := decodeChoiceOption(context, optionObject, limits.MaxTextBytes)
			if optionFailure != nil {
				return nil, optionFailure
			}
			options = append(options, option)
		}
		return options, nil
	}
	return normalizedOptions(fallback, limits)
}

func decodeChoiceOption(
	context *model.Context,
	object types.Object,
	maximum int,
) (FormFieldOption, *Failure) {
	resolved, err := context.Dereference(object)
	if err != nil {
		return FormFieldOption{}, malformedFormField()
	}
	if array, ok := resolved.(types.Array); ok {
		if len(array) != 2 {
			return FormFieldOption{}, malformedFormField()
		}
		exported, exportedFailure := decodeFieldString(context, array[0], maximum)
		if exportedFailure != nil {
			return FormFieldOption{}, exportedFailure
		}
		display, displayFailure := decodeFieldString(context, array[1], maximum)
		if displayFailure != nil {
			return FormFieldOption{}, displayFailure
		}
		return FormFieldOption{Export: exported, Display: display}, nil
	}
	value, failure := decodeFieldString(context, resolved, maximum)
	return FormFieldOption{Export: value, Display: value}, failure
}

func decodeFieldString(context *model.Context, object types.Object, maximum int) (string, *Failure) {
	resolved, err := context.Dereference(object)
	if err != nil {
		return "", malformedFormField()
	}
	value, err := types.StringOrHexLiteral(resolved)
	if err != nil || value == nil {
		return "", malformedFormField()
	}
	decoded := *value
	if decoded == "" || !validFieldText(decoded, maximum) {
		return "", &Failure{Code: FailureFieldUnsupported, Message: "PDF field option is unsupported"}
	}
	return decoded, nil
}

func normalizedOptions(values []string, limits FormFieldLimits) ([]FormFieldOption, *Failure) {
	if len(values) > limits.MaxOptions {
		return nil, &Failure{Code: FailureInspectionLimit, Message: "PDF form exceeds the option limit"}
	}
	options := make([]FormFieldOption, 0, len(values))
	for _, value := range values {
		if value == "" || !validFieldText(value, limits.MaxTextBytes) {
			return nil, &Failure{Code: FailureFieldUnsupported, Message: "PDF field option is unsupported"}
		}
		options = append(options, FormFieldOption{Export: value, Display: value})
	}
	return options, nil
}

func opaqueFormFieldID(sourceSHA256 string, kind FormFieldKind, backendID string) string {
	digest := sha256.Sum256([]byte("mintclaw.document.field.v1\x00" + sourceSHA256 + "\x00" +
		string(kind) + "\x00" + backendID))
	return "field_" + hex.EncodeToString(digest[:])
}

func opaqueFormWidgetID(fieldID string, backendObjectID string, page int, ordinal int) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf(
		"mintclaw.document.widget.v1\x00%s\x00%s\x00%d\x00%d",
		fieldID,
		backendObjectID,
		page,
		ordinal,
	)))
	return "widget_" + hex.EncodeToString(digest[:])
}

type backendWidgetLocation struct {
	objectID string
	page     int
}

func collectFormWidgetLocations(
	context *model.Context,
	exported form.Form,
) (map[string][]backendWidgetLocation, *Failure) {
	exportedIDs := map[string]struct{}{}
	add := func(id string) *Failure {
		if !validBackendFieldID(id) {
			return malformedFormField()
		}
		if _, duplicate := exportedIDs[id]; duplicate {
			return &Failure{Code: FailureFormUnsupported, Message: "PDF form field identity is ambiguous"}
		}
		exportedIDs[id] = struct{}{}
		return nil
	}
	for _, item := range exported.TextFields {
		if item == nil {
			return nil, malformedFormField()
		}
		if failure := add(item.ID); failure != nil {
			return nil, failure
		}
	}
	for _, item := range exported.DateFields {
		if item == nil {
			return nil, malformedFormField()
		}
		if failure := add(item.ID); failure != nil {
			return nil, failure
		}
	}
	for _, item := range exported.CheckBoxes {
		if item == nil {
			return nil, malformedFormField()
		}
		if failure := add(item.ID); failure != nil {
			return nil, failure
		}
	}
	for _, item := range exported.RadioButtonGroups {
		if item == nil {
			return nil, malformedFormField()
		}
		if failure := add(item.ID); failure != nil {
			return nil, failure
		}
	}
	for _, item := range exported.ComboBoxes {
		if item == nil {
			return nil, malformedFormField()
		}
		if failure := add(item.ID); failure != nil {
			return nil, failure
		}
	}
	for _, item := range exported.ListBoxes {
		if item == nil {
			return nil, malformedFormField()
		}
		if failure := add(item.ID); failure != nil {
			return nil, failure
		}
	}

	rootFields, err := form.Fields(context.XRefTable)
	if err != nil {
		return nil, malformedFormField()
	}
	rootIDs := map[int]struct{}{}
	for _, object := range rootFields {
		indirect, ok := object.(types.IndirectRef)
		if !ok {
			return nil, malformedFormField()
		}
		rootIDs[indirect.ObjectNumber.Value()] = struct{}{}
	}
	locations := make(map[string][]backendWidgetLocation, len(exportedIDs))
	seenWidgets := map[int]struct{}{}
	for page := 1; page <= context.PageCount; page++ {
		pageDictionary, _, _, err := context.PageDict(page, false)
		if err != nil {
			return nil, malformedFormField()
		}
		annotationObject, found := pageDictionary.Find("Annots")
		if !found {
			continue
		}
		annotations, err := context.DereferenceArray(annotationObject)
		if err != nil {
			return nil, malformedFormField()
		}
		for _, annotation := range annotations {
			indirect, ok := annotation.(types.IndirectRef)
			if !ok {
				return nil, malformedFormField()
			}
			widget, err := context.DereferenceDict(indirect)
			if err != nil || widget == nil {
				return nil, malformedFormField()
			}
			subtype := widget.NameEntry("Subtype")
			if subtype == nil || *subtype != "Widget" {
				continue
			}
			objectNumber := indirect.ObjectNumber.Value()
			if _, duplicate := seenWidgets[objectNumber]; duplicate {
				return nil, malformedFormField()
			}
			seenWidgets[objectNumber] = struct{}{}
			chain, failure := formObjectChain(context, indirect, rootIDs)
			if failure != nil {
				return nil, failure
			}
			backendID := ""
			for length := len(chain); length > 0; length-- {
				parts := make([]string, 0, length)
				for _, number := range chain[:length] {
					parts = append(parts, strconv.Itoa(number))
				}
				candidate := strings.Join(parts, ".")
				if _, found := exportedIDs[candidate]; found {
					backendID = candidate
					break
				}
			}
			if backendID == "" {
				return nil, &Failure{Code: FailureFieldUnsupported, Message: "PDF contains unsupported form fields"}
			}
			locations[backendID] = append(locations[backendID], backendWidgetLocation{
				objectID: strconv.Itoa(objectNumber),
				page:     page,
			})
		}
	}
	for backendID := range exportedIDs {
		if len(locations[backendID]) == 0 {
			return nil, &Failure{Code: FailureFieldUnsupported, Message: "PDF field has no page widget"}
		}
	}
	return locations, nil
}

func formObjectChain(
	context *model.Context,
	leaf types.IndirectRef,
	rootIDs map[int]struct{},
) ([]int, *Failure) {
	chain := make([]int, 0, 4)
	seen := map[int]struct{}{}
	current := leaf
	for depth := 0; depth <= DefaultMaxRecursionDepth; depth++ {
		number := current.ObjectNumber.Value()
		if _, duplicate := seen[number]; duplicate {
			return nil, malformedFormField()
		}
		seen[number] = struct{}{}
		chain = append(chain, number)
		dictionary, err := context.DereferenceDict(current)
		if err != nil || dictionary == nil {
			return nil, malformedFormField()
		}
		parent := dictionary.IndirectRefEntry("Parent")
		if parent == nil {
			if _, root := rootIDs[number]; !root {
				return nil, malformedFormField()
			}
			for left, right := 0, len(chain)-1; left < right; left, right = left+1, right-1 {
				chain[left], chain[right] = chain[right], chain[left]
			}
			return chain, nil
		}
		current = *parent
	}
	return nil, malformedFormField()
}

func malformedFormField() *Failure {
	return &Failure{Code: FailureMalformedPDF, Message: "PDF form structure is malformed"}
}
