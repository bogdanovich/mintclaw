//go:build linux && amd64

package document

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	pdfcpuapi "github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/filter"
	pdfcpucore "github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/form"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

const maxXFASubtypeBytes = int64(1024 * 1024)

type pdfCPUInspectionBackend struct{}

func newInspectionBackend() inspectionBackend {
	return pdfCPUInspectionBackend{}
}

func (pdfCPUInspectionBackend) Inspect(reader io.ReadSeeker, limits Limits) backendInspection {
	facts := defaultInspectionFacts()
	if reader == nil {
		return failedInspection(FailureBackendUnavailable, "document inspection backend is unavailable")
	}
	configuration := model.NewDefaultConfiguration()
	configuration.Cmd = model.VALIDATE
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
		facts.Encryption.State = FactPresent
		facts.Encryption.PasswordRequired = FactPresent
		return backendInspection{
			State: StateUnsupported,
			Facts: facts,
			Failure: &Failure{
				Code:    FailurePasswordRequired,
				Message: "document inspection requires a protected password input",
			},
		}
	}
	if isPDFCPUResourceLimit(err) {
		return failedInspection(FailureInspectionLimit, "document exceeds an inspection limit")
	}
	if err != nil || context == nil || context.XRefTable == nil {
		return failedInspection(FailureMalformedPDF, "PDF structure is malformed or unsupported")
	}
	preValidationRoot, err := context.Catalog()
	if err != nil || preValidationRoot == nil {
		return failedInspection(FailureMalformedPDF, "PDF catalog is malformed")
	}
	catalogAcroForm, catalogHasAcroForm := preValidationRoot.Find("AcroForm")
	if catalogAcroForm != nil {
		catalogAcroForm = catalogAcroForm.Clone()
	}
	if err = boundCatalogMetadata(context, preValidationRoot, limits.MaxContentBytes); err != nil {
		if isPDFCPUResourceLimit(err) {
			return failedInspection(FailureInspectionLimit, "document exceeds an inspection limit")
		}
		return failedInspection(FailureMalformedPDF, "PDF catalog metadata is malformed")
	}
	if err = pdfcpuapi.ValidateContext(context); err != nil {
		if isPDFCPUResourceLimit(err) {
			return failedInspection(FailureInspectionLimit, "document exceeds an inspection limit")
		}
		return failedInspection(FailureMalformedPDF, "PDF structure is malformed or unsupported")
	}
	if context.PageCount < 0 || context.PageCount > limits.MaxPages {
		return failedInspection(FailureInspectionLimit, "document exceeds the inspection page limit")
	}

	version := context.VersionString()
	facts.PDFVersion = StringFact{State: FactPresent, Value: version}
	facts.PageCount = knownInteger(context.PageCount)
	inspectEncryption(context, facts)
	inspectSignatures(context, facts)
	root, err := context.Catalog()
	if err != nil || root == nil {
		return failedInspection(FailureMalformedPDF, "PDF catalog is malformed")
	}
	inspectRestrictions(context, root, facts)
	if err = inspectForms(context, catalogAcroForm, catalogHasAcroForm, facts); err != nil {
		if isPDFCPUResourceLimit(err) || strings.Contains(strings.ToLower(err.Error()), "inspection limit") {
			return failedInspection(FailureInspectionLimit, "document exceeds an inspection limit")
		}
		return failedInspection(FailureMalformedPDF, "PDF form structure is malformed")
	}
	if err = inspectText(context, limits, facts); err != nil {
		return failedInspection(FailureInspectionLimit, "document exceeds the decoded content limit")
	}

	return backendInspection{State: StateSucceeded, Facts: facts}
}

// boundCatalogMetadata pre-decodes the one stream that pdfcpu validation decodes
// without consulting Configuration.Limits.MaxDecodeBytes. Persisting the bounded
// result makes the validator reuse Content instead of invoking its 512 MiB default.
func boundCatalogMetadata(context *model.Context, root types.Dict, limit int64) error {
	metadata, found := root.Find("Metadata")
	if !found || metadata == nil {
		return nil
	}
	stream, _, err := context.DereferenceStreamDict(metadata)
	if err != nil || stream == nil {
		return err
	}
	content, err := decodeBoundedStream(*stream, limit)
	if errors.Is(err, filter.ErrUnsupportedFilter) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("decode catalog metadata: %w", err)
	}
	stream.Content = content
	switch value := metadata.(type) {
	case types.IndirectRef:
		entry, ok := context.FindTableEntry(value.ObjectNumber.Value(), value.GenerationNumber.Value())
		if !ok || entry == nil || entry.Free {
			return errors.New("catalog metadata object is unavailable")
		}
		entry.Object = *stream
	case types.StreamDict:
		root["Metadata"] = *stream
	}
	return nil
}

func isPDFCPUResourceLimit(err error) bool {
	return errors.Is(err, filter.ErrDecodeLimitExceeded) ||
		errors.Is(err, model.ErrMaxRecursionDepthExceeded) ||
		(err != nil && strings.Contains(strings.ToLower(err.Error()), "exceeds limit"))
}

func knownInteger(value int) IntegerFact {
	return IntegerFact{State: FactPresent, Value: &value}
}

func inspectEncryption(context *model.Context, facts *InspectionFacts) {
	if context.Encrypt == nil {
		facts.Encryption = EncryptionFacts{
			State:            FactAbsent,
			PasswordRequired: FactAbsent,
			Permissions:      StringFact{State: FactAbsent},
		}
		return
	}
	permissions := ""
	permissionState := FactUnknown
	if context.E != nil {
		permissionState = FactPresent
		permissions = "restricted"
		if model.PermissionFlags(context.E.P) == model.PermissionsAll {
			permissions = "full"
		}
	}
	facts.Encryption = EncryptionFacts{
		State:            FactPresent,
		PasswordRequired: FactAbsent,
		Permissions:      StringFact{State: permissionState, Value: permissions},
	}
}

func inspectSignatures(context *model.Context, facts *InspectionFacts) {
	count := 0
	certified := false
	documentTimestamp := false
	for _, signatures := range context.Signatures {
		for _, signature := range signatures {
			if !signature.Signed {
				continue
			}
			count++
			certified = certified || signature.Certified
			documentTimestamp = documentTimestamp || signature.Type == model.SigTypeDTS
		}
	}
	if len(context.URSignature) > 0 {
		count++
	}
	certified = certified || context.CertifiedSigObjNr > 0
	if count == 0 {
		facts.Signatures = SignatureFacts{
			State:       FactAbsent,
			Count:       knownInteger(0),
			Certified:   FactAbsent,
			Timestamped: FactAbsent,
		}
		return
	}
	facts.Signatures = SignatureFacts{
		State:       FactPresent,
		Count:       knownInteger(count),
		Certified:   stateForBool(certified),
		Timestamped: FactUnknown,
	}
	if documentTimestamp {
		facts.Signatures.Timestamped = FactPresent
	}
}

func inspectRestrictions(context *model.Context, root types.Dict, facts *InspectionFacts) {
	encryptedPermissions := FactAbsent
	if context.Encrypt != nil {
		encryptedPermissions = FactUnknown
		if context.E != nil {
			encryptedPermissions = stateForBool(model.PermissionFlags(context.E.P) != model.PermissionsAll)
		}
	}
	docMDP := linkedSignatureTransformState(context, "DocMDP")
	if context.CertifiedSigObjNr > 0 {
		docMDP = FactPresent
	}
	fieldMDP := linkedSignatureTransformState(context, "FieldMDP")
	usageRights := stateForBool(len(context.URSignature) > 0 || hasSignatureType(context, model.SigTypeUR))
	_, readerExtensions := root.Find("Extensions")
	facts.Restrictions = RestrictionFacts{
		EncryptedPermissions: encryptedPermissions,
		DocMDP:               docMDP,
		FieldMDP:             fieldMDP,
		UsageRights:          usageRights,
		ReaderExtensions:     stateForBool(readerExtensions),
	}
	facts.Restrictions.State = aggregatePresence(
		encryptedPermissions,
		docMDP,
		fieldMDP,
		usageRights,
		facts.Restrictions.ReaderExtensions,
	)
}

func inspectForms(
	context *model.Context,
	catalogAcroForm types.Object,
	catalogHasAcroForm bool,
	facts *InspectionFacts,
) error {
	acroForm, present, err := findAcroFormDictionary(context, catalogAcroForm, catalogHasAcroForm)
	if err != nil {
		return err
	}
	if !present {
		facts.AcroForm = AcroFormFacts{State: FactAbsent, FieldCount: knownInteger(0)}
		facts.XFA = XFAFacts{
			State:          FactAbsent,
			Representation: StringFact{State: FactAbsent},
			Rendering:      StringFact{State: FactAbsent},
		}
		return nil
	}
	facts.AcroForm.State = FactPresent
	fields, _, fieldErr := form.FormFields(context)
	if fieldErr == nil {
		facts.AcroForm.FieldCount = knownInteger(len(fields))
	} else if fieldObject, found := acroForm.Find("Fields"); found {
		fieldArray, arrayErr := context.DereferenceArray(fieldObject)
		if arrayErr != nil || fieldArray == nil {
			facts.AcroForm.FieldCount = IntegerFact{State: FactUnknown}
			facts.Warnings = append(facts.Warnings, "acroform_field_count_unknown")
		} else {
			facts.AcroForm.FieldCount = knownInteger(len(fieldArray))
		}
	} else {
		facts.AcroForm.FieldCount = IntegerFact{State: FactUnknown}
		facts.Warnings = append(facts.Warnings, "acroform_field_count_unknown")
	}

	xfaObject, xfaPresent := acroForm.Find("XFA")
	if !xfaPresent || xfaObject == nil {
		facts.XFA = XFAFacts{
			State:          FactAbsent,
			Representation: StringFact{State: FactAbsent},
			Rendering:      StringFact{State: FactAbsent},
		}
		return nil
	}
	facts.XFA.State = FactPresent
	representation, payload, err := inspectXFAObject(context, xfaObject)
	if err != nil {
		return err
	}
	facts.XFA.Representation = StringFact{State: FactPresent, Value: representation}
	facts.XFA.Rendering = classifyXFARendering(payload)
	return nil
}

func findAcroFormDictionary(
	context *model.Context,
	catalogAcroForm types.Object,
	catalogHasAcroForm bool,
) (types.Dict, bool, error) {
	if !catalogHasAcroForm {
		return nil, false, nil
	}
	if catalogAcroForm == nil {
		return nil, false, errors.New("invalid AcroForm dictionary")
	}
	acroForm, err := context.DereferenceDict(catalogAcroForm)
	if err != nil || acroForm == nil {
		return nil, false, errors.New("invalid AcroForm dictionary")
	}
	return acroForm, true, nil
}

func inspectXFAObject(context *model.Context, object types.Object) (string, []byte, error) {
	if stream, _, err := context.DereferenceStreamDict(object); err == nil && stream != nil {
		payload, decodeErr := decodeBoundedStream(*stream, maxXFASubtypeBytes)
		return "stream", payload, decodeErr
	}
	array, err := context.DereferenceArray(object)
	if err != nil || array == nil || len(array)%2 != 0 {
		return "", nil, errors.New("invalid XFA packet array")
	}
	var payload bytes.Buffer
	for index := 1; index < len(array); index += 2 {
		stream, _, streamErr := context.DereferenceStreamDict(array[index])
		if streamErr != nil || stream == nil {
			return "", nil, errors.New("invalid XFA packet stream")
		}
		remaining := maxXFASubtypeBytes - int64(payload.Len())
		if remaining <= 0 {
			return "", nil, errors.New("XFA packets exceed inspection limit")
		}
		part, decodeErr := decodeBoundedStream(*stream, remaining)
		if decodeErr != nil {
			return "", nil, decodeErr
		}
		_, _ = payload.Write(part)
	}
	return "packet_array", payload.Bytes(), nil
}

func decodeBoundedStream(stream types.StreamDict, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("decoded stream exceeds inspection limit: %w", filter.ErrDecodeLimitExceeded)
	}
	if err := stream.DecodeWithLimit(limit); err != nil {
		return nil, err
	}
	if int64(len(stream.Content)) > limit {
		return nil, fmt.Errorf("decoded stream exceeds inspection limit: %w", filter.ErrDecodeLimitExceeded)
	}
	return stream.Content, nil
}

func classifyXFARendering(payload []byte) StringFact {
	normalized := strings.ToLower(string(payload))
	switch {
	case strings.Contains(normalized, "<dynamicrender>required</dynamicrender>"),
		strings.Contains(normalized, "<dynamicrender>required</dynamicrender"):
		return StringFact{State: FactPresent, Value: "dynamic"}
	case strings.Contains(normalized, "<dynamicrender>forbidden</dynamicrender>"),
		strings.Contains(normalized, "<dynamicrender>forbidden</dynamicrender"):
		return StringFact{State: FactPresent, Value: "static"}
	default:
		return StringFact{State: FactUnknown}
	}
}

func inspectText(context *model.Context, limits Limits, facts *InspectionFacts) error {
	remaining := limits.MaxContentBytes
	for page := 1; page <= context.PageCount; page++ {
		data, err := boundedPageContent(context, page, remaining)
		if err != nil {
			if isPDFCPUResourceLimit(err) {
				return err
			}
			facts.ExtractableText.PagesUnknown++
			continue
		}
		remaining -= int64(len(data))
		switch textShowingOperatorState(data) {
		case FactPresent:
			facts.ExtractableText.PagesWithText++
		case FactAbsent:
			facts.ExtractableText.PagesWithoutText++
		default:
			facts.ExtractableText.PagesUnknown++
		}
	}
	facts.ExtractableText.State = textFactState(facts.ExtractableText)
	if facts.ExtractableText.PagesWithText > 0 {
		facts.Warnings = append(facts.Warnings, "text_signal_uses_content_stream_operators")
	}
	return nil
}

func boundedPageContent(context *model.Context, page int, limit int64) ([]byte, error) {
	pageDictionary, _, _, err := context.PageDict(page, false)
	if err != nil {
		return nil, fmt.Errorf("page %d dictionary: %w", page, err)
	}
	contentObject, found := pageDictionary.Find("Contents")
	if !found || contentObject == nil {
		return nil, nil
	}
	contentObject, err = context.Dereference(contentObject)
	if err != nil {
		return nil, fmt.Errorf("page %d content: %w", page, err)
	}
	if contentObject == nil {
		return nil, nil
	}
	content := make([]byte, 0, int(limit))
	appendStream := func(stream types.StreamDict) error {
		remaining := limit - int64(len(content))
		decoded, decodeErr := decodeBoundedStream(stream, remaining)
		if decodeErr != nil {
			return decodeErr
		}
		content = append(content, decoded...)
		return nil
	}
	switch value := contentObject.(type) {
	case types.StreamDict:
		if err = appendStream(value); err != nil {
			return nil, fmt.Errorf("page %d content decode: %w", page, err)
		}
	case types.Array:
		for _, item := range value {
			if item == nil {
				continue
			}
			stream, _, streamErr := context.DereferenceStreamDict(item)
			if streamErr != nil {
				return nil, fmt.Errorf("page %d content stream: %w", page, streamErr)
			}
			if stream == nil {
				continue
			}
			if err = appendStream(*stream); err != nil {
				return nil, fmt.Errorf("page %d content decode: %w", page, err)
			}
		}
	default:
		return nil, fmt.Errorf("page %d content must be a stream or array", page)
	}
	return content, nil
}

func textShowingOperatorState(data []byte) FactState {
	inTextObject := false
	containers := []byte{}
	for index := 0; index < len(data); {
		index = skipPDFContentSpace(data, index)
		if index >= len(data) {
			break
		}
		switch data[index] {
		case '%':
			for index < len(data) && data[index] != '\n' && data[index] != '\r' {
				index++
			}
			continue
		case '/':
			index++
			for index < len(data) && !isPDFContentDelimiter(data[index]) {
				index++
			}
			continue
		case '(':
			var closed bool
			index, closed = skipPDFLiteralString(data, index+1)
			if !closed {
				return FactUnknown
			}
			continue
		case '<':
			if index+1 < len(data) && data[index+1] == '<' {
				containers = append(containers, '>')
				index += 2
				continue
			}
			var closed bool
			index, closed = skipPDFHexString(data, index+1)
			if !closed {
				return FactUnknown
			}
			continue
		case '[':
			containers = append(containers, ']')
			index++
			continue
		case '{':
			containers = append(containers, '}')
			index++
			continue
		case ']':
			if len(containers) == 0 || containers[len(containers)-1] != ']' {
				return FactUnknown
			}
			containers = containers[:len(containers)-1]
			index++
			continue
		case '}':
			if len(containers) == 0 || containers[len(containers)-1] != '}' {
				return FactUnknown
			}
			containers = containers[:len(containers)-1]
			index++
			continue
		case '>':
			if index+1 >= len(data) || data[index+1] != '>' || len(containers) == 0 ||
				containers[len(containers)-1] != '>' {
				return FactUnknown
			}
			containers = containers[:len(containers)-1]
			index += 2
			continue
		}
		start := index
		for index < len(data) && !isPDFContentDelimiter(data[index]) {
			index++
		}
		token := string(data[start:index])
		if index == start {
			return FactUnknown
		}
		if len(containers) > 0 {
			continue
		}
		switch token {
		case "BI":
			// Inline image termination is filter-dependent and binary payload may
			// contain arbitrary operator-like bytes. Refuse to infer a text fact
			// from the remainder instead of interpreting image data as PDF syntax.
			return FactUnknown
		case "BT":
			inTextObject = true
		case "ET":
			inTextObject = false
		case "Tj", "TJ", "'", "\"":
			if inTextObject {
				return FactPresent
			}
		}
	}
	if inTextObject || len(containers) > 0 {
		return FactUnknown
	}
	return FactAbsent
}

func skipPDFContentSpace(data []byte, index int) int {
	for index < len(data) {
		switch data[index] {
		case 0, '\t', '\n', '\f', '\r', ' ':
			index++
		default:
			return index
		}
	}
	return index
}

func skipPDFLiteralString(data []byte, index int) (int, bool) {
	depth := 1
	for index < len(data) && depth > 0 {
		switch data[index] {
		case '\\':
			if index+1 >= len(data) {
				return len(data), false
			}
			index += 2
			continue
		case '(':
			depth++
		case ')':
			depth--
		}
		index++
	}
	return index, depth == 0
}

func skipPDFHexString(data []byte, index int) (int, bool) {
	for index < len(data) {
		if data[index] == '>' {
			return index + 1, true
		}
		index++
	}
	return index, false
}

func isPDFContentDelimiter(value byte) bool {
	switch value {
	case 0, '\t', '\n', '\f', '\r', ' ', '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

func stateForBool(value bool) FactState {
	if value {
		return FactPresent
	}
	return FactAbsent
}

func linkedSignatureTransformState(context *model.Context, name string) FactState {
	unknown := false
	for _, signatures := range context.Signatures {
		for _, signature := range signatures {
			if !signature.Signed || signature.ObjNr <= 0 {
				continue
			}
			entry := context.Table[signature.ObjNr]
			if entry == nil || entry.Free || entry.Object == nil {
				unknown = true
				continue
			}
			fieldDictionary, ok := entry.Object.(types.Dict)
			if !ok {
				unknown = true
				continue
			}
			signatureObject, found := fieldDictionary.Find("V")
			if !found || signatureObject == nil {
				unknown = true
				continue
			}
			signatureDictionary, signatureErr := context.DereferenceDict(signatureObject)
			if signatureErr != nil || signatureDictionary == nil {
				unknown = true
				continue
			}
			referenceObject, found := signatureDictionary.Find("Reference")
			if !found || referenceObject == nil {
				continue
			}
			references, err := context.DereferenceArray(referenceObject)
			if err != nil || references == nil {
				unknown = true
				continue
			}
			for _, reference := range references {
				referenceDictionary, referenceErr := context.DereferenceDict(reference)
				if referenceErr != nil || referenceDictionary == nil {
					unknown = true
					continue
				}
				method := referenceDictionary.NameEntry("TransformMethod")
				if method != nil && *method == name {
					return FactPresent
				}
			}
		}
	}
	if unknown {
		return FactUnknown
	}
	return FactAbsent
}

func hasSignatureType(context *model.Context, signatureType int) bool {
	for _, signatures := range context.Signatures {
		for _, signature := range signatures {
			if signature.Signed && signature.Type == signatureType {
				return true
			}
		}
	}
	return false
}
