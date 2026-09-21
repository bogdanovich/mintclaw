//go:build linux && amd64

package document

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"math"

	pdfcpuapi "github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

const (
	hybridFlattenDPI    = 96
	maxHybridPixelDelta = 0.002
)

type hybridFlattenEvidence struct {
	Candidate                []byte
	Inspection               InspectionFacts
	PopplerAssertions        int
	PopplerRenderedPages     int
	IndependentAssertions    int
	IndependentRenderedPages int
}

func normalizePDFCPUHybridContext(context *model.Context) error {
	root, err := context.Catalog()
	if err != nil || root == nil {
		return errors.New("hybrid form catalog is invalid")
	}
	formObject, present := root.Find("AcroForm")
	if !present {
		return errors.New("hybrid form dictionary is unavailable")
	}
	formDictionary, err := context.DereferenceDict(formObject)
	if err != nil || formDictionary == nil {
		return errors.New("hybrid form dictionary is invalid")
	}
	if _, present = formDictionary.Find("XFA"); !present {
		return errors.New("hybrid form XFA packet is unavailable")
	}
	delete(formDictionary, "XFA")
	delete(root, "NeedsRendering")
	if permissionsObject, found := root.Find("Perms"); found {
		permissions, dereferenceErr := context.DereferenceDict(permissionsObject)
		if dereferenceErr != nil || permissions == nil || len(permissions) != 1 {
			return errors.New("hybrid form usage rights are invalid")
		}
		usageRightsObject, found := permissions.Find("UR3")
		if !found {
			return errors.New("hybrid form usage rights are invalid")
		}
		if reference, ok := usageRightsObject.(types.IndirectRef); ok {
			if err = context.DeleteObject(reference); err != nil {
				return errors.New("hybrid form usage rights could not be removed")
			}
		}
		if reference, ok := permissionsObject.(types.IndirectRef); ok {
			if err = context.DeleteObject(reference); err != nil {
				return errors.New("hybrid form permissions could not be removed")
			}
		}
		delete(root, "Perms")
	}
	context.URSignature = nil
	context.URSignatureIncrement = 0
	return nil
}

func flattenAndVerifyPDFCPUHybridCandidate(
	editable []byte,
	request WorkerRequest,
	sourceInspection InspectionFacts,
) (*hybridFlattenEvidence, *Failure) {
	if request.Fill == nil || !ghostscriptBackendAvailable() || len(editable) == 0 {
		return nil, &Failure{Code: FailureBackendUnavailable, Message: "independent visual backend is unavailable"}
	}
	context, failure := readFormContext(bytes.NewReader(editable), request.Limits)
	if failure != nil {
		return nil, structuralVerificationFailure()
	}
	if context.PageCount < 1 || context.PageCount > maxHybridFlattenPages {
		return nil, &Failure{Code: FailureRenderLimit, Message: "hybrid form exceeds the flatten page limit"}
	}
	if err := flattenPDFCPUWidgets(context); err != nil {
		return nil, &Failure{Code: FailureVerificationStructural, Message: "hybrid form could not be flattened"}
	}
	if err := pdfcpuapi.ValidateContext(context); err != nil {
		return nil, structuralVerificationFailure()
	}
	output := &boundedFormWriteBuffer{maximum: DefaultMaxArtifactBytes}
	if err := pdfcpuapi.WriteContext(context, output); err != nil || output.Len() == 0 {
		return nil, &Failure{Code: FailureWriteFailed, Message: "hybrid form derivative could not be written"}
	}
	flattened := append([]byte(nil), output.Bytes()...)
	inspection := newInspectionBackend().Inspect(bytes.NewReader(flattened), request.Limits)
	if inspection.State != StateSucceeded || inspection.Facts == nil ||
		!flattenedHybridInspectionMatches(sourceInspection, *inspection.Facts) {
		return nil, structuralVerificationFailure()
	}
	visual, failure := verifyHybridFlattenedRendering(editable, flattened, context)
	if failure != nil {
		return nil, failure
	}
	visual.Candidate = flattened
	visual.Inspection = *inspection.Facts
	return visual, nil
}

func structuralVerificationFailure() *Failure {
	return &Failure{
		Code:    FailureVerificationStructural,
		Message: "hybrid form derivative failed structural verification",
	}
}

func flattenPDFCPUWidgets(context *model.Context) error {
	root, err := context.Catalog()
	if err != nil || root == nil {
		return errors.New("missing catalog")
	}
	if _, present := root.Find("AcroForm"); !present {
		return errors.New("missing AcroForm")
	}
	for pageNumber := 1; pageNumber <= context.PageCount; pageNumber++ {
		page, _, attributes, pageErr := context.PageDict(pageNumber, false)
		if pageErr != nil || page == nil || attributes == nil {
			return fmt.Errorf("page %d is unavailable", pageNumber)
		}
		if attributes.Rotate%360 != 0 {
			return fmt.Errorf("page %d rotation is unsupported", pageNumber)
		}
		annotationsObject, found := page.Find("Annots")
		if !found {
			continue
		}
		annotations, dereferenceErr := context.DereferenceArray(annotationsObject)
		if dereferenceErr != nil {
			return fmt.Errorf("page %d annotations are invalid", pageNumber)
		}
		resources, resourceErr := clonePDFCPUPageResources(context, attributes.Resources)
		if resourceErr != nil {
			return fmt.Errorf("page %d resources are invalid", pageNumber)
		}
		var content bytes.Buffer
		remaining := make(types.Array, 0, len(annotations))
		flattened := 0
		for _, annotationObject := range annotations {
			annotation, annotationErr := context.DereferenceDict(annotationObject)
			if annotationErr != nil || annotation == nil {
				return fmt.Errorf("page %d annotation is invalid", pageNumber)
			}
			subtype := annotation.NameEntry("Subtype")
			if subtype == nil || *subtype != "Widget" {
				remaining = append(remaining, annotationObject)
				continue
			}
			if err = appendPDFCPUWidgetAppearance(context, resources, annotation, &content); err != nil {
				return fmt.Errorf("page %d widget appearance: %w", pageNumber, err)
			}
			flattened++
		}
		if flattened == 0 {
			continue
		}
		page["Resources"] = resources
		if err = appendPDFCPUPageContent(context, page, content.Bytes()); err != nil {
			return fmt.Errorf("page %d flattened content: %w", pageNumber, err)
		}
		if len(remaining) == 0 {
			delete(page, "Annots")
		} else {
			page["Annots"] = remaining
		}
	}
	delete(root, "AcroForm")
	delete(root, "NeedsRendering")
	delete(root, "Perms")
	context.URSignature = nil
	context.URSignatureIncrement = 0
	return nil
}

func clonePDFCPUPageResources(context *model.Context, source types.Dict) (types.Dict, error) {
	resources := types.Dict{}
	if source != nil {
		resources = source.Clone().(types.Dict)
	}
	object, found := resources.Find("XObject")
	if !found {
		resources["XObject"] = types.Dict{}
		return resources, nil
	}
	xObjects, err := context.DereferenceDict(object)
	if err != nil || xObjects == nil {
		return nil, errors.New("XObject resources are invalid")
	}
	resources["XObject"] = xObjects.Clone().(types.Dict)
	return resources, nil
}

func appendPDFCPUWidgetAppearance(
	context *model.Context,
	resources types.Dict,
	widget types.Dict,
	content *bytes.Buffer,
) error {
	appearanceObject, found, err := pdfCPUNormalAppearanceObject(context, widget)
	if err != nil || !found {
		return errors.New("normal appearance is unavailable")
	}
	appearanceReference, ok := appearanceObject.(types.IndirectRef)
	if !ok {
		appearanceReferencePointer, referenceErr := context.IndRefForNewObject(appearanceObject)
		if referenceErr != nil || appearanceReferencePointer == nil {
			return errors.New("normal appearance cannot be referenced")
		}
		appearanceReference = *appearanceReferencePointer
	}
	appearance, err := context.DereferenceXObjectDict(appearanceReference)
	if err != nil || appearance == nil || !pdfCPUIdentityAppearanceMatrix(context, appearance.Dict) {
		return errors.New("normal appearance matrix is unsupported")
	}
	boundsObject, found := appearance.Dict.Find("BBox")
	if !found {
		return errors.New("normal appearance bounds are unavailable")
	}
	boundsArray, err := context.DereferenceArray(boundsObject)
	if err != nil || len(boundsArray) != 4 {
		return errors.New("normal appearance bounds are invalid")
	}
	bounds := types.RectForArray(boundsArray)
	rectObject, found := widget.Find("Rect")
	if !found {
		return errors.New("widget rectangle is unavailable")
	}
	rectArray, err := context.DereferenceArray(rectObject)
	if err != nil || len(rectArray) != 4 {
		return errors.New("widget rectangle is invalid")
	}
	rect := types.RectForArray(rectArray)
	if bounds == nil || rect == nil || !bounds.Visible() || !rect.Visible() {
		return errors.New("widget geometry is invalid")
	}
	xObjects := resources.DictEntry("XObject")
	if xObjects == nil {
		return errors.New("XObject resources are unavailable")
	}
	identifier := xObjects.NewIDForPrefix("Fm", 0)
	xObjects[identifier] = appearanceReference
	sx := rect.Width() / bounds.Width()
	sy := rect.Height() / bounds.Height()
	tx := rect.LL.X - bounds.LL.X*sx
	ty := rect.LL.Y - bounds.LL.Y*sy
	_, _ = fmt.Fprintf(content, " q %.5f 0 0 %.5f %.5f %.5f cm /%s Do Q ", sx, sy, tx, ty, identifier)
	return nil
}

func pdfCPUNormalAppearanceObject(
	context *model.Context,
	widget types.Dict,
) (types.Object, bool, error) {
	appearanceObject, found := widget.Find("AP")
	if !found {
		return nil, false, nil
	}
	appearance, err := context.DereferenceDict(appearanceObject)
	if err != nil || appearance == nil {
		return nil, false, err
	}
	normalObject, found := appearance.Find("N")
	if !found {
		return nil, false, nil
	}
	dereferenced, err := context.Dereference(normalObject)
	if err != nil {
		return nil, false, err
	}
	states, isStates := dereferenced.(types.Dict)
	if !isStates {
		_, isStream := dereferenced.(types.StreamDict)
		return normalObject, isStream, nil
	}
	state := "Off"
	selectedState := false
	if selected := widget.NameEntry("AS"); selected != nil {
		if _, exists := states.Find(*selected); exists {
			state = *selected
			selectedState = true
		}
	}
	if !selectedState {
		for candidate := range states {
			if candidate != "Off" {
				state = candidate
				break
			}
		}
	}
	result, found := states.Find(state)
	return result, found, nil
}

func pdfCPUIdentityAppearanceMatrix(context *model.Context, appearance types.Dict) bool {
	object, found := appearance.Find("Matrix")
	if !found {
		return true
	}
	matrix, err := context.DereferenceArray(object)
	if err != nil || len(matrix) != 6 {
		return false
	}
	want := []float64{1, 0, 0, 1, 0, 0}
	for index, item := range matrix {
		var value float64
		switch number := item.(type) {
		case types.Integer:
			value = float64(number.Value())
		case types.Float:
			value = number.Value()
		default:
			return false
		}
		if math.Abs(value-want[index]) > 1e-9 {
			return false
		}
	}
	return true
}

func appendPDFCPUPageContent(context *model.Context, page types.Dict, content []byte) error {
	if len(content) == 0 {
		return errors.New("flattened content is empty")
	}
	stream, err := context.NewStreamDictForBuf(content)
	if err != nil || stream == nil {
		return errors.New("flattened content stream is unavailable")
	}
	reference, err := context.IndRefForNewObject(*stream)
	if err != nil || reference == nil {
		return errors.New("flattened content stream cannot be referenced")
	}
	existing, found := page.Find("Contents")
	if !found {
		page["Contents"] = *reference
		return nil
	}
	dereferenced, err := context.Dereference(existing)
	if err != nil {
		return errors.New("page content is invalid")
	}
	switch value := dereferenced.(type) {
	case types.StreamDict:
		page["Contents"] = types.Array{existing, *reference}
	case types.Array:
		page["Contents"] = append(value.Clone().(types.Array), *reference)
	default:
		return errors.New("page content type is unsupported")
	}
	return nil
}

func flattenedHybridInspectionMatches(source, output InspectionFacts) bool {
	return source.PageCount.State == FactPresent && source.PageCount.Value != nil &&
		output.PageCount.State == FactPresent && output.PageCount.Value != nil &&
		*source.PageCount.Value == *output.PageCount.Value && output.AcroForm.State == FactAbsent &&
		output.XFA.State == FactAbsent && output.Signatures.State == FactAbsent &&
		output.Signatures.Content.State == FactAbsent && output.Signatures.UsageRights.State == FactAbsent &&
		output.Restrictions.UsageRights == FactAbsent && output.Restrictions.DocMDP == FactAbsent &&
		output.Restrictions.FieldMDP == FactAbsent && output.Restrictions.ReaderExtensions == FactAbsent &&
		output.Actions.State == FactAbsent && output.Encryption.State == source.Encryption.State &&
		output.Encryption.PasswordRequired == source.Encryption.PasswordRequired &&
		output.Encryption.OperationPermissions == source.Encryption.OperationPermissions
}

func verifyHybridFlattenedRendering(
	editable []byte,
	flattened []byte,
	context *model.Context,
) (*hybridFlattenEvidence, *Failure) {
	boundaries, err := context.PageBoundaries(nil)
	if err != nil || len(boundaries) != context.PageCount {
		return nil, &Failure{Code: FailureVerificationVisual, Message: "hybrid form visual verification failed"}
	}
	var chargedPixels int64
	evidence := &hybridFlattenEvidence{}
	for pageNumber, boundary := range boundaries {
		if boundary.Rot%360 != 0 {
			return nil, &Failure{Code: FailureVerificationVisual, Message: "hybrid form visual verification failed"}
		}
		crop := boundary.CropBox()
		if crop == nil || !validFormVisualRectangle(crop) {
			return nil, &Failure{Code: FailureVerificationVisual, Message: "hybrid form visual verification failed"}
		}
		width, height, failure := boundedPageDimensions(
			crop.Width(), crop.Height(), hybridFlattenDPI, HardMaxRenderEdge, 0,
		)
		if failure != nil {
			return nil, failure
		}
		pixels := int64(width) * int64(height)
		if pixels > DefaultMaxPixelsPerPage || chargedPixels > DefaultMaxRenderPixels-pixels {
			return nil, &Failure{Code: FailureRenderLimit, Message: "hybrid form exceeds the visual render limit"}
		}
		chargedPixels += pixels
		page := pageNumber + 1
		before, renderFailure := popplerFormPageAtDPI(editable, page, false, width, height, hybridFlattenDPI)
		if renderFailure != nil {
			return nil, renderFailure
		}
		after, renderFailure := popplerFormPageAtDPI(flattened, page, false, width, height, hybridFlattenDPI)
		if renderFailure != nil || !formImagesEquivalent(before, after) {
			return nil, &Failure{
				Code:    FailureVerificationVisual,
				Message: "flattened form changed visible page content",
			}
		}
		evidence.PopplerAssertions++
		evidence.PopplerRenderedPages++
		independentBefore, independentFailure := ghostscriptFormPageAtDPI(
			editable, page, width, height, hybridFlattenDPI,
		)
		if independentFailure != nil {
			return nil, independentFailure
		}
		independentAfter, independentFailure := ghostscriptFormPageAtDPI(
			flattened, page, width, height, hybridFlattenDPI,
		)
		if independentFailure != nil || !formImagesEquivalent(independentBefore, independentAfter) {
			return nil, &Failure{Code: FailureVerificationVisual, Message: "independent flattened rendering changed"}
		}
		evidence.IndependentAssertions++
		evidence.IndependentRenderedPages++
	}
	return evidence, nil
}

func formImagesEquivalent(left, right image.Image) bool {
	if left == nil || right == nil || left.Bounds() != right.Bounds() {
		return false
	}
	total := left.Bounds().Dx() * left.Bounds().Dy()
	if total <= 0 {
		return false
	}
	maximumDifferent := int(math.Ceil(float64(total) * maxHybridPixelDelta))
	different := 0
	for y := left.Bounds().Min.Y; y < left.Bounds().Max.Y; y++ {
		for x := left.Bounds().Min.X; x < left.Bounds().Max.X; x++ {
			lr, lg, lb, la := left.At(x, y).RGBA()
			rr, rg, rb, ra := right.At(x, y).RGBA()
			if channelDelta(lr, rr) <= visualPixelDeltaThreshold &&
				channelDelta(lg, rg) <= visualPixelDeltaThreshold &&
				channelDelta(lb, rb) <= visualPixelDeltaThreshold &&
				channelDelta(la, ra) <= visualPixelDeltaThreshold {
				continue
			}
			different++
			if different > maximumDifferent {
				return false
			}
		}
	}
	return true
}

func channelDelta(left, right uint32) uint32 {
	if left > right {
		return left - right
	}
	return right - left
}
