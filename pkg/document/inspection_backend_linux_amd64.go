//go:build linux && amd64

package document

import (
	"bytes"
	"errors"
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
	context, err := pdfcpuapi.ReadAndValidate(reader, configuration)
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
	if err = inspectForms(context, root, facts); err != nil {
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
	docMDP := stateForBool(context.CertifiedSigObjNr > 0 || hasTransformMethod(context, "DocMDP"))
	fieldMDP := stateForBool(hasTransformMethod(context, "FieldMDP"))
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

func inspectForms(context *model.Context, root types.Dict, facts *InspectionFacts) error {
	acroForm, present, err := findAcroFormDictionary(context, root)
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

func findAcroFormDictionary(context *model.Context, root types.Dict) (types.Dict, bool, error) {
	if len(context.Form) > 0 {
		return context.Form, true, nil
	}
	if acroObject, present := root.Find("AcroForm"); present && acroObject != nil {
		acroForm, err := context.DereferenceDict(acroObject)
		if err != nil || acroForm == nil {
			return nil, false, errors.New("invalid AcroForm dictionary")
		}
		return acroForm, true, nil
	}
	// pdfcpu removes the catalog entry after validation. An XFA-only form with
	// an empty Fields array is not retained in Context.Form, so recover only
	// the structurally unique AcroForm dictionary from the validated xref table.
	for _, entry := range context.Table {
		if entry == nil || entry.Free || entry.Object == nil {
			continue
		}
		dictionary, ok := entry.Object.(types.Dict)
		if !ok {
			continue
		}
		_, hasFields := dictionary.Find("Fields")
		_, hasXFA := dictionary.Find("XFA")
		if hasFields && hasXFA {
			return dictionary, true, nil
		}
	}
	return nil, false, nil
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
	if err := stream.DecodeWithLimit(limit); err != nil {
		return nil, err
	}
	if int64(len(stream.Content)) > limit {
		return nil, errors.New("decoded stream exceeds inspection limit")
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
		content, err := pdfcpucore.ExtractPageContent(context, page)
		if err != nil {
			if isPDFCPUResourceLimit(err) {
				return err
			}
			facts.ExtractableText.PagesUnknown++
			continue
		}
		data, err := io.ReadAll(io.LimitReader(content, remaining+1))
		if err != nil || int64(len(data)) > remaining {
			return errors.New("decoded page content exceeds limit")
		}
		remaining -= int64(len(data))
		if hasTextShowingOperator(data) {
			facts.ExtractableText.PagesWithText++
		} else {
			facts.ExtractableText.PagesWithoutText++
		}
	}
	facts.ExtractableText.State = textFactState(facts.ExtractableText)
	if facts.ExtractableText.PagesWithText > 0 {
		facts.Warnings = append(facts.Warnings, "text_signal_uses_content_stream_operators")
	}
	return nil
}

func hasTextShowingOperator(data []byte) bool {
	inTextObject := false
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
		case '(':
			index = skipPDFLiteralString(data, index+1)
			continue
		case '<':
			if index+1 < len(data) && data[index+1] == '<' {
				index += 2
				continue
			}
			index++
			for index < len(data) && data[index] != '>' {
				index++
			}
			index++
			continue
		}
		start := index
		for index < len(data) && !isPDFContentDelimiter(data[index]) {
			index++
		}
		token := string(data[start:index])
		switch token {
		case "BT":
			inTextObject = true
		case "ET":
			inTextObject = false
		case "Tj", "TJ", "'", "\"":
			if inTextObject {
				return true
			}
		}
		if index == start {
			index++
		}
	}
	return false
}

func skipPDFContentSpace(data []byte, index int) int {
	for index < len(data) {
		switch data[index] {
		case 0, '\t', '\n', '\f', '\r', ' ', '[', ']', '{', '}':
			index++
		default:
			return index
		}
	}
	return index
}

func skipPDFLiteralString(data []byte, index int) int {
	depth := 1
	for index < len(data) && depth > 0 {
		switch data[index] {
		case '\\':
			index += 2
			continue
		case '(':
			depth++
		case ')':
			depth--
		}
		index++
	}
	return index
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

func hasTransformMethod(context *model.Context, name string) bool {
	for _, entry := range context.Table {
		if entry == nil || entry.Free || entry.Object == nil {
			continue
		}
		if dictionaryHasName(entry.Object, "TransformMethod", name) {
			return true
		}
	}
	return false
}

func dictionaryHasName(object types.Object, key, name string) bool {
	return objectContainsName(object, key, name, 0)
}

func objectContainsName(object types.Object, key, name string, depth int) bool {
	if depth > 8 {
		return false
	}
	var dictionary types.Dict
	switch value := object.(type) {
	case types.Dict:
		dictionary = value
	case types.StreamDict:
		dictionary = value.Dict
	case types.Array:
		for _, entry := range value {
			if objectContainsName(entry, key, name, depth+1) {
				return true
			}
		}
		return false
	default:
		return false
	}
	entry := dictionary.NameEntry(key)
	if entry != nil && *entry == name {
		return true
	}
	for _, value := range dictionary {
		if objectContainsName(value, key, name, depth+1) {
			return true
		}
	}
	return false
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
