//go:build (linux && amd64) || (darwin && (amd64 || arm64))

package document

import (
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/form"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

const (
	minimumVisibleRasterPixels = 4
	visualCoordinateTolerance  = 0.75
	visualPixelDeltaThreshold  = uint32(0x0800)
)

type formVisualEvidence struct {
	Assertions    int
	RenderedPages int
}

type formVisualAssertionKind uint8

const (
	formVisualAssertionStructural formVisualAssertionKind = iota
	formVisualAssertionExactText
	formVisualAssertionListSelection
	formVisualAssertionSelectedButton
)

type formVisualWidget struct {
	objectNumber int
	page         int
	rect         types.Rectangle
	expectedText []string
	assertion    formVisualAssertionKind
}

type formVisualAnnotation struct {
	objectNumber int
	page         int
	rect         types.Rectangle
}

func collectFormVisualWidgets(
	context *model.Context,
	exported form.Form,
	bindings map[string]pdfCPUFormBinding,
) ([]formVisualWidget, *Failure) {
	locations, failure := collectFormWidgetLocations(context, exported)
	if failure != nil {
		return nil, visualVerificationFailure()
	}
	widgets := make([]formVisualWidget, 0)
	for _, binding := range bindings {
		fieldLocations := locations[binding.backendID]
		if len(fieldLocations) != len(binding.field.Widgets) {
			return nil, visualVerificationFailure()
		}
		for _, location := range fieldLocations {
			objectNumber, err := strconv.Atoi(location.objectID)
			if err != nil || objectNumber <= 0 {
				return nil, visualVerificationFailure()
			}
			object, err := context.FindObject(objectNumber)
			if err != nil {
				return nil, visualVerificationFailure()
			}
			widget, err := context.DereferenceDict(object)
			if err != nil || widget == nil {
				return nil, visualVerificationFailure()
			}
			rectObject, present := widget.Find("Rect")
			if !present {
				return nil, visualVerificationFailure()
			}
			rectArray, err := context.DereferenceArray(rectObject)
			if err != nil || !validFormVisualRectArray(rectArray) {
				return nil, visualVerificationFailure()
			}
			rect := types.RectForArray(rectArray)
			if !validFormVisualRectangle(rect) {
				return nil, visualVerificationFailure()
			}
			expected, assertion := formVisualExpectation(binding, widget)
			widgets = append(widgets, formVisualWidget{
				objectNumber: objectNumber, page: location.page, rect: *rect,
				expectedText: expected, assertion: assertion,
			})
		}
	}
	annotations, failure := collectFormVisualAnnotations(context)
	if failure != nil || formVisualWidgetsOverlapAnnotations(widgets, annotations) {
		return nil, visualVerificationFailure()
	}
	sort.Slice(widgets, func(left, right int) bool {
		if widgets[left].page != widgets[right].page {
			return widgets[left].page < widgets[right].page
		}
		if widgets[left].rect.LL.Y != widgets[right].rect.LL.Y {
			return widgets[left].rect.LL.Y > widgets[right].rect.LL.Y
		}
		return widgets[left].rect.LL.X < widgets[right].rect.LL.X
	})
	return widgets, nil
}

func formVisualExpectation(
	binding pdfCPUFormBinding,
	widget types.Dict,
) ([]string, formVisualAssertionKind) {
	switch binding.field.Kind {
	case FormFieldText, FormFieldDate:
		return []string{binding.expected.text}, formVisualAssertionExactText
	case FormFieldCombo:
		return append([]string(nil), binding.expected.choices...), formVisualAssertionExactText
	case FormFieldList:
		return append([]string(nil), binding.expected.choices...), formVisualAssertionListSelection
	case FormFieldCheckbox:
		if binding.expected.checked {
			return nil, formVisualAssertionSelectedButton
		}
		return nil, formVisualAssertionStructural
	case FormFieldRadio:
		state := widget.NameEntry("AS")
		if state != nil && *state != "Off" {
			return nil, formVisualAssertionSelectedButton
		}
		return nil, formVisualAssertionStructural
	default:
		return nil, formVisualAssertionStructural
	}
}

func formVisualWidgetsOverlapAnnotations(
	widgets []formVisualWidget,
	annotations []formVisualAnnotation,
) bool {
	for _, widget := range widgets {
		for _, annotation := range annotations {
			if widget.objectNumber == annotation.objectNumber || widget.page != annotation.page {
				continue
			}
			if formVisualRectanglesOverlap(widget.rect, annotation.rect) {
				return true
			}
		}
	}
	return false
}

func formVisualRectanglesOverlap(left types.Rectangle, right types.Rectangle) bool {
	xOverlap := math.Min(left.UR.X, right.UR.X) - math.Max(left.LL.X, right.LL.X)
	yOverlap := math.Min(left.UR.Y, right.UR.Y) - math.Max(left.LL.Y, right.LL.Y)
	return xOverlap > visualCoordinateTolerance && yOverlap > visualCoordinateTolerance
}

func collectFormVisualAnnotations(context *model.Context) ([]formVisualAnnotation, *Failure) {
	annotations := make([]formVisualAnnotation, 0)
	for page := 1; page <= context.PageCount; page++ {
		pageDictionary, _, _, err := context.PageDict(page, false)
		if err != nil {
			return nil, visualVerificationFailure()
		}
		annotationObject, found := pageDictionary.Find("Annots")
		if !found {
			continue
		}
		pageAnnotations, err := context.DereferenceArray(annotationObject)
		if err != nil || len(annotations)+len(pageAnnotations) > DefaultMaxFieldWidgets {
			return nil, visualVerificationFailure()
		}
		for _, annotationObject := range pageAnnotations {
			indirect, ok := annotationObject.(types.IndirectRef)
			if !ok {
				return nil, visualVerificationFailure()
			}
			annotation, err := context.DereferenceDict(indirect)
			if err != nil || annotation == nil {
				return nil, visualVerificationFailure()
			}
			rectObject, present := annotation.Find("Rect")
			if !present {
				return nil, visualVerificationFailure()
			}
			rectArray, err := context.DereferenceArray(rectObject)
			if err != nil || !validFormVisualRectArray(rectArray) {
				return nil, visualVerificationFailure()
			}
			rect := types.RectForArray(rectArray)
			if !validFormVisualRectangle(rect) {
				return nil, visualVerificationFailure()
			}
			annotations = append(annotations, formVisualAnnotation{
				objectNumber: indirect.ObjectNumber.Value(), page: page, rect: *rect,
			})
		}
	}
	return annotations, nil
}

func formVisualPageDimensions(crop types.Rectangle, priorPixels int64) (int, int, int64, *Failure) {
	width, height, failure := boundedPageDimensions(
		crop.Width(),
		crop.Height(),
		DefaultRenderDPI,
		HardMaxRenderEdge,
		0,
	)
	if failure != nil {
		return 0, 0, 0, failure
	}
	pixels := int64(width) * int64(height)
	if pixels > DefaultMaxPixelsPerPage || pixels > DefaultMaxRenderPixels/2 {
		return 0, 0, 0, &Failure{Code: FailureRenderLimit, Message: "document page exceeds the visual render limit"}
	}
	chargedPixels := pixels * 2
	if priorPixels > DefaultMaxRenderPixels-chargedPixels {
		return 0, 0, 0, &Failure{Code: FailureRenderLimit, Message: "document page exceeds the visual render limit"}
	}
	return width, height, chargedPixels, nil
}

func visualTextLooksClipped(expected string, actual string) bool {
	return actual != "" && len(actual) < len(expected) &&
		(strings.HasPrefix(expected, actual) || strings.HasSuffix(expected, actual))
}

func normalizeVisualText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func colorDelta(left uint32, right uint32) uint32 {
	if left >= right {
		return left - right
	}
	return right - left
}

func validFormVisualRectArray(array types.Array) bool {
	if len(array) != 4 {
		return false
	}
	for _, value := range array {
		switch value.(type) {
		case types.Integer, types.Float:
		default:
			return false
		}
	}
	return true
}

func validFormVisualRectangle(rect *types.Rectangle) bool {
	return rect != nil && !math.IsNaN(rect.LL.X) && !math.IsNaN(rect.LL.Y) &&
		!math.IsNaN(rect.UR.X) && !math.IsNaN(rect.UR.Y) &&
		!math.IsInf(rect.LL.X, 0) && !math.IsInf(rect.LL.Y, 0) &&
		!math.IsInf(rect.UR.X, 0) && !math.IsInf(rect.UR.Y, 0) && rect.Width() > 0 && rect.Height() > 0
}

func formVisualRectangleWithin(rect types.Rectangle, crop types.Rectangle) bool {
	return rect.LL.X >= crop.LL.X-visualCoordinateTolerance &&
		rect.LL.Y >= crop.LL.Y-visualCoordinateTolerance &&
		rect.UR.X <= crop.UR.X+visualCoordinateTolerance &&
		rect.UR.Y <= crop.UR.Y+visualCoordinateTolerance
}

func visualVerificationFailure() *Failure {
	return &Failure{Code: FailureVerificationVisual, Message: "document form candidate failed visual verification"}
}
