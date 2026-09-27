//go:build (linux && amd64) || (darwin && (amd64 || arm64))

package document

import (
	"context"
	"image"
	"image/draw"
	"math"
	"strings"
	"unicode"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/enums"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/responses"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/form"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

type pdfiumFormVisualPage struct {
	crop       types.Rectangle
	visible    image.Image
	background image.Image
	widgets    []pdfiumFormVisualWidget
}

type pdfiumFormVisualWidget struct {
	rect           types.Rectangle
	text           string
	backgroundText string
	characters     []pdfiumFormVisualCharacter
}

type pdfiumFormVisualCharacter struct {
	value      rune
	rect       types.Rectangle
	whitespace bool
}

type pdfiumFormVisualTextUnit struct {
	value rune
	rect  *types.Rectangle
}

type pdfiumFormVisualRow struct {
	text string
	rect types.Rectangle
}

func verifyPDFiumFormCandidate(
	candidate []byte,
	request WorkerRequest,
	pdfContext *model.Context,
	exported form.Form,
	bindings map[string]pdfCPUFormBinding,
) (evidence *formVisualEvidence, failure *Failure) {
	if request.Fill == nil || pdfContext == nil || len(bindings) == 0 ||
		len(request.Fill.AffectedPages) == 0 || len(request.Fill.AffectedPages) > DefaultMaxRenderPages {
		return nil, visualVerificationFailure()
	}
	boundaries, err := pdfContext.PageBoundaries(nil)
	if err != nil || len(boundaries) != pdfContext.PageCount {
		return nil, visualVerificationFailure()
	}
	widgets, failure := collectFormVisualWidgets(pdfContext, exported, bindings)
	if failure != nil {
		return nil, failure
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultReadWorkerTimeout)
	defer cancel()
	pool, err := newPortablePDFiumPool(ctx)
	if err != nil || pool == nil {
		return nil, portableFormVisualUnavailable()
	}
	defer func() {
		if pool.Close() != nil {
			evidence = nil
			failure = portableFormVisualCleanupFailure()
		}
	}()
	instance, err := pool.GetInstanceWithContext(ctx)
	if err != nil || instance == nil {
		return nil, portableFormVisualUnavailable()
	}
	defer func() {
		if instance.Close() != nil {
			evidence = nil
			failure = portableFormVisualCleanupFailure()
		}
	}()
	document, err := instance.OpenDocument(&requests.OpenDocument{File: &candidate})
	if err != nil || document == nil {
		return nil, visualVerificationFailure()
	}
	defer func() {
		if _, closeErr := instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{
			Document: document.Document,
		}); closeErr != nil {
			evidence = nil
			failure = portableFormVisualCleanupFailure()
		}
	}()

	pages := make(map[int]*pdfiumFormVisualPage, len(request.Fill.AffectedPages))
	var totalPixels int64
	for _, pageNumber := range request.Fill.AffectedPages {
		if pageNumber < 1 || pageNumber > len(boundaries) || boundaries[pageNumber-1].Rot%360 != 0 {
			return nil, visualVerificationFailure()
		}
		crop := boundaries[pageNumber-1].CropBox()
		if !validFormVisualRectangle(crop) {
			return nil, visualVerificationFailure()
		}
		width, height, chargedPixels, boundsFailure := formVisualPageDimensions(*crop, totalPixels)
		if boundsFailure != nil {
			return nil, boundsFailure
		}
		totalPixels += chargedPixels
		page, pageFailure := loadPDFiumFormVisualPage(
			instance,
			document.Document,
			pageNumber,
			*crop,
			width,
			height,
		)
		if pageFailure != nil {
			return nil, pageFailure
		}
		pages[pageNumber] = page
	}
	for _, widget := range widgets {
		page := pages[widget.page]
		if page == nil || !formVisualRectangleWithin(widget.rect, page.crop) {
			return nil, visualVerificationFailure()
		}
		readback, found := matchingPDFiumFormVisualWidget(page.widgets, widget.rect)
		if !found {
			return nil, visualVerificationFailure()
		}
		if assertionFailure := verifyPDFiumWidgetAppearance(page, widget, readback); assertionFailure != nil {
			return nil, assertionFailure
		}
	}
	return &formVisualEvidence{
		Assertions: len(request.Fill.AffectedPages) + len(widgets), RenderedPages: len(pages),
	}, nil
}

func loadPDFiumFormVisualPage(
	instance pdfium.Pdfium,
	document references.FPDF_DOCUMENT,
	pageNumber int,
	crop types.Rectangle,
	width int,
	height int,
) (*pdfiumFormVisualPage, *Failure) {
	pageRequest := requests.Page{ByIndex: &requests.PageByIndex{Document: document, Index: pageNumber - 1}}
	visible, failure := renderPDFiumFormVisualPage(instance, document, pageRequest, true, width, height)
	if failure != nil {
		return nil, failure
	}
	background, failure := renderPDFiumFormVisualPage(instance, document, pageRequest, false, width, height)
	if failure != nil {
		return nil, failure
	}
	if visible.Bounds() != background.Bounds() || visible.Bounds().Dx() <= 0 || visible.Bounds().Dy() <= 0 {
		return nil, visualVerificationFailure()
	}
	readbacks, failure := readPDFiumFormVisualWidgets(instance, document, pageNumber)
	if failure != nil {
		return nil, failure
	}
	return &pdfiumFormVisualPage{
		crop: crop, visible: visible, background: background, widgets: readbacks,
	}, nil
}

func renderPDFiumFormVisualPage(
	instance pdfium.Pdfium,
	document references.FPDF_DOCUMENT,
	page requests.Page,
	renderForm bool,
	width int,
	height int,
) (image.Image, *Failure) {
	response, err := instance.RenderPageInDPI(&requests.RenderPageInDPI{
		Page: page, DPI: DefaultRenderDPI, RenderForm: renderForm, Document: &document,
	})
	if err != nil || response == nil {
		return nil, visualVerificationFailure()
	}
	defer response.Cleanup()
	if response.Result.RenderedImage == nil || response.Result.Width != width || response.Result.Height != height {
		return nil, visualVerificationFailure()
	}
	bounds := response.Result.RenderedImage.Bounds()
	copy := image.NewRGBA(bounds)
	draw.Draw(copy, bounds, response.Result.RenderedImage, bounds.Min, draw.Src)
	return copy, nil
}

func readPDFiumFormVisualWidgets(
	instance pdfium.Pdfium,
	document references.FPDF_DOCUMENT,
	pageNumber int,
) (widgets []pdfiumFormVisualWidget, failure *Failure) {
	loaded, err := instance.FPDF_LoadPage(&requests.FPDF_LoadPage{Document: document, Index: pageNumber - 1})
	if err != nil || loaded == nil {
		return nil, visualVerificationFailure()
	}
	page := requests.Page{ByReference: &loaded.Page}
	pageClosed := false
	defer func() {
		if pageClosed {
			return
		}
		if _, closeErr := instance.FPDF_ClosePage(&requests.FPDF_ClosePage{Page: loaded.Page}); closeErr != nil {
			widgets = nil
			failure = portableFormVisualCleanupFailure()
		}
	}()
	rects, failure := readPDFiumFormWidgetRectangles(instance, page)
	if failure != nil {
		return nil, failure
	}
	background, failure := readPDFiumFormPageCharacters(instance, page)
	if failure != nil {
		return nil, failure
	}
	flattened, err := instance.FPDFPage_Flatten(&requests.FPDFPage_Flatten{
		Page: page, Usage: requests.FPDFPage_FlattenUsageNormalDisplay,
	})
	if err != nil || flattened == nil || flattened.Result != responses.FPDFPage_FlattenResultSuccess {
		return nil, visualVerificationFailure()
	}
	if _, err = instance.FPDF_ClosePage(&requests.FPDF_ClosePage{Page: loaded.Page}); err != nil {
		return nil, portableFormVisualCleanupFailure()
	}
	pageClosed = true
	reloaded, err := instance.FPDF_LoadPage(&requests.FPDF_LoadPage{Document: document, Index: pageNumber - 1})
	if err != nil || reloaded == nil {
		return nil, visualVerificationFailure()
	}
	page = requests.Page{ByReference: &reloaded.Page}
	defer func() {
		if _, closeErr := instance.FPDF_ClosePage(&requests.FPDF_ClosePage{Page: reloaded.Page}); closeErr != nil {
			widgets = nil
			failure = portableFormVisualCleanupFailure()
		}
	}()
	visible, failure := readPDFiumFormPageCharacters(instance, page)
	if failure != nil {
		return nil, failure
	}
	result := make([]pdfiumFormVisualWidget, 0, len(rects))
	for _, rect := range rects {
		text, clipped := pdfiumFormTextWithin(visible, rect)
		if clipped {
			return nil, &Failure{Code: FailureContentClipped, Message: "document form content is clipped"}
		}
		backgroundText, _ := pdfiumFormTextWithin(background, rect)
		result = append(result, pdfiumFormVisualWidget{
			rect: rect, text: text, backgroundText: backgroundText, characters: visible,
		})
	}
	return result, nil
}

func readPDFiumFormWidgetRectangles(
	instance pdfium.Pdfium,
	page requests.Page,
) ([]types.Rectangle, *Failure) {
	count, err := instance.FPDFPage_GetAnnotCount(&requests.FPDFPage_GetAnnotCount{Page: page})
	if err != nil || count == nil || count.Count < 0 || count.Count > DefaultMaxFieldWidgets {
		return nil, visualVerificationFailure()
	}
	result := make([]types.Rectangle, 0, count.Count)
	for index := 0; index < count.Count; index++ {
		annotation, annotationErr := instance.FPDFPage_GetAnnot(&requests.FPDFPage_GetAnnot{Page: page, Index: index})
		if annotationErr != nil || annotation == nil {
			return nil, visualVerificationFailure()
		}
		subtype, subtypeErr := instance.FPDFAnnot_GetSubtype(&requests.FPDFAnnot_GetSubtype{
			Annotation: annotation.Annotation,
		})
		var rect *responses.FPDFAnnot_GetRect
		if subtypeErr == nil && subtype != nil && subtype.Subtype == enums.FPDF_ANNOT_SUBTYPE_WIDGET {
			rect, annotationErr = instance.FPDFAnnot_GetRect(&requests.FPDFAnnot_GetRect{
				Annotation: annotation.Annotation,
			})
		}
		_, closeErr := instance.FPDFPage_CloseAnnot(&requests.FPDFPage_CloseAnnot{
			Annotation: annotation.Annotation,
		})
		if closeErr != nil {
			return nil, portableFormVisualCleanupFailure()
		}
		if subtypeErr != nil || subtype == nil || annotationErr != nil {
			return nil, visualVerificationFailure()
		}
		if subtype.Subtype != enums.FPDF_ANNOT_SUBTYPE_WIDGET {
			continue
		}
		if rect == nil {
			return nil, visualVerificationFailure()
		}
		value := *types.NewRectangle(
			float64(rect.Rect.Left),
			float64(rect.Rect.Bottom),
			float64(rect.Rect.Right),
			float64(rect.Rect.Top),
		)
		if !validFormVisualRectangle(&value) {
			return nil, visualVerificationFailure()
		}
		result = append(result, value)
	}
	return result, nil
}

func readPDFiumFormPageCharacters(
	instance pdfium.Pdfium,
	page requests.Page,
) (characters []pdfiumFormVisualCharacter, failure *Failure) {
	textPage, err := instance.FPDFText_LoadPage(&requests.FPDFText_LoadPage{Page: page})
	if err != nil || textPage == nil {
		return nil, visualVerificationFailure()
	}
	defer func() {
		if _, closeErr := instance.FPDFText_ClosePage(&requests.FPDFText_ClosePage{
			TextPage: textPage.TextPage,
		}); closeErr != nil {
			characters = nil
			failure = portableFormVisualCleanupFailure()
		}
	}()
	count, err := instance.FPDFText_CountChars(&requests.FPDFText_CountChars{TextPage: textPage.TextPage})
	if err != nil || count == nil || count.Count < 0 || count.Count > DefaultMaxExtractChars {
		return nil, visualVerificationFailure()
	}
	result := make([]pdfiumFormVisualCharacter, 0, count.Count)
	for index := 0; index < count.Count; index++ {
		value, unicodeErr := instance.FPDFText_GetUnicode(&requests.FPDFText_GetUnicode{
			TextPage: textPage.TextPage, Index: index,
		})
		box, boxErr := instance.FPDFText_GetCharBox(&requests.FPDFText_GetCharBox{
			TextPage: textPage.TextPage, Index: index,
		})
		if unicodeErr != nil || value == nil || value.Unicode == 0 || boxErr != nil || box == nil {
			return nil, visualVerificationFailure()
		}
		rect := *types.NewRectangle(box.Left, box.Bottom, box.Right, box.Top)
		if unicode.IsSpace(rune(value.Unicode)) {
			result = append(result, pdfiumFormVisualCharacter{
				value: rune(value.Unicode), rect: rect, whitespace: true,
			})
			continue
		}
		if !validFormVisualRectangle(&rect) {
			return nil, visualVerificationFailure()
		}
		result = append(result, pdfiumFormVisualCharacter{value: rune(value.Unicode), rect: rect})
	}
	return result, nil
}

func pdfiumFormTextWithin(
	characters []pdfiumFormVisualCharacter,
	rect types.Rectangle,
) (string, bool) {
	units, clipped := pdfiumFormTextUnitsWithin(characters, rect)
	var text strings.Builder
	for _, unit := range units {
		text.WriteRune(unit.value)
	}
	return text.String(), clipped
}

func pdfiumFormTextUnitsWithin(
	characters []pdfiumFormVisualCharacter,
	rect types.Rectangle,
) ([]pdfiumFormVisualTextUnit, bool) {
	result := make([]pdfiumFormVisualTextUnit, 0, len(characters))
	var pendingWhitespace strings.Builder
	clipped := false
	for _, character := range characters {
		if character.whitespace {
			if len(result) > 0 {
				pendingWhitespace.WriteRune(character.value)
			}
			continue
		}
		if !formVisualRectangleWithin(character.rect, rect) {
			if pdfiumFormRectanglesIntersect(character.rect, rect) {
				clipped = true
				pendingWhitespace.Reset()
			}
			continue
		}
		if pendingWhitespace.Len() > 0 {
			for _, character := range pendingWhitespace.String() {
				result = append(result, pdfiumFormVisualTextUnit{value: character})
			}
		}
		pendingWhitespace.Reset()
		characterRect := character.rect
		result = append(result, pdfiumFormVisualTextUnit{value: character.value, rect: &characterRect})
	}
	return result, clipped
}

func pdfiumFormRectanglesIntersect(left types.Rectangle, right types.Rectangle) bool {
	return math.Min(left.UR.X, right.UR.X) > math.Max(left.LL.X, right.LL.X) &&
		math.Min(left.UR.Y, right.UR.Y) > math.Max(left.LL.Y, right.LL.Y)
}

func matchingPDFiumFormVisualWidget(
	widgets []pdfiumFormVisualWidget,
	expected types.Rectangle,
) (pdfiumFormVisualWidget, bool) {
	var result pdfiumFormVisualWidget
	found := false
	for _, widget := range widgets {
		if math.Abs(widget.rect.LL.X-expected.LL.X) > visualCoordinateTolerance ||
			math.Abs(widget.rect.LL.Y-expected.LL.Y) > visualCoordinateTolerance ||
			math.Abs(widget.rect.UR.X-expected.UR.X) > visualCoordinateTolerance ||
			math.Abs(widget.rect.UR.Y-expected.UR.Y) > visualCoordinateTolerance {
			continue
		}
		if found {
			return pdfiumFormVisualWidget{}, false
		}
		result = widget
		found = true
	}
	return result, found
}

func verifyPDFiumWidgetAppearance(
	page *pdfiumFormVisualPage,
	widget formVisualWidget,
	readback pdfiumFormVisualWidget,
) *Failure {
	actual := normalizeVisualText(readback.text)
	switch widget.assertion {
	case formVisualAssertionStructural:
		return nil
	case formVisualAssertionSelectedButton:
		if pdfiumWidgetInteriorChanged(page, widget.rect) {
			return nil
		}
		return &Failure{Code: FailureAppearanceStale, Message: "document form selected button appearance is stale"}
	case formVisualAssertionUnselectedButton:
		if !pdfiumWidgetInteriorChanged(page, widget.rect) {
			return nil
		}
		return &Failure{Code: FailureAppearanceStale, Message: "document form unselected button appearance is stale"}
	case formVisualAssertionExactText:
		expected := ""
		if len(widget.expectedText) == 1 {
			expected = normalizeVisualText(widget.expectedText[0])
		}
		if actual != expected {
			if visualTextLooksClipped(expected, actual) {
				return &Failure{Code: FailureContentClipped, Message: "document form content is clipped"}
			}
			return &Failure{Code: FailureAppearanceStale, Message: "document form appearance is stale"}
		}
		if expected != "" && strings.Contains(normalizeVisualText(readback.backgroundText), expected) {
			return &Failure{Code: FailureAppearanceStale, Message: "document form appearance cannot be isolated"}
		}
		if expected != "" && pdfiumWidgetChangedPixels(page, widget.rect) < minimumVisibleRasterPixels {
			return &Failure{Code: FailureAppearanceStale, Message: "document form appearance did not render visibly"}
		}
		return nil
	case formVisualAssertionListSelection:
		rows, clipped := pdfiumFormVisualRows(readback.characters, widget.rect)
		if clipped {
			return &Failure{Code: FailureContentClipped, Message: "document form content is clipped"}
		}
		selections := make([]formVisualSelectionRow, 0, len(rows))
		for _, row := range rows {
			selections = append(selections, formVisualSelectionRow{
				text: row.text, selected: pdfiumWidgetHasHorizontalFill(page, row.rect),
			})
		}
		if !formVisualListSelectionMatches(widget.expectedText, selections) {
			return &Failure{
				Code: FailureAppearanceStale, Message: "document form list selection appearance is stale",
			}
		}
		for _, expected := range widget.expectedText {
			if strings.Contains(normalizeVisualText(readback.backgroundText), normalizeVisualText(expected)) {
				return &Failure{Code: FailureAppearanceStale, Message: "document form appearance cannot be isolated"}
			}
		}
		return nil
	default:
		return visualVerificationFailure()
	}
}

func pdfiumFormVisualRows(
	characters []pdfiumFormVisualCharacter,
	widget types.Rectangle,
) ([]pdfiumFormVisualRow, bool) {
	units, clipped := pdfiumFormTextUnitsWithin(characters, widget)
	rows := make([]pdfiumFormVisualRow, 0)
	var text strings.Builder
	var pending strings.Builder
	var bounds *types.Rectangle
	flush := func() {
		if bounds == nil {
			text.Reset()
			pending.Reset()
			return
		}
		padding := bounds.Height() / 10
		row := *types.NewRectangle(
			widget.LL.X,
			math.Max(widget.LL.Y, bounds.LL.Y-padding),
			widget.UR.X,
			math.Min(widget.UR.Y, bounds.UR.Y+padding),
		)
		if normalized := normalizeVisualText(text.String()); normalized != "" && validFormVisualRectangle(&row) {
			rows = append(rows, pdfiumFormVisualRow{text: normalized, rect: row})
		}
		text.Reset()
		pending.Reset()
		bounds = nil
	}
	for _, unit := range units {
		if unit.rect == nil {
			if bounds != nil {
				pending.WriteRune(unit.value)
			}
			continue
		}
		if bounds != nil && !pdfiumFormVisualSameRow(*bounds, *unit.rect) {
			flush()
		}
		if bounds == nil {
			value := *unit.rect
			bounds = &value
		} else {
			text.WriteString(pending.String())
			bounds.LL.X = math.Min(bounds.LL.X, unit.rect.LL.X)
			bounds.LL.Y = math.Min(bounds.LL.Y, unit.rect.LL.Y)
			bounds.UR.X = math.Max(bounds.UR.X, unit.rect.UR.X)
			bounds.UR.Y = math.Max(bounds.UR.Y, unit.rect.UR.Y)
		}
		pending.Reset()
		text.WriteRune(unit.value)
	}
	flush()
	return rows, clipped
}

func pdfiumFormVisualSameRow(left types.Rectangle, right types.Rectangle) bool {
	return math.Min(left.UR.Y, right.UR.Y)-math.Max(left.LL.Y, right.LL.Y) > 0
}

func pdfiumWidgetInteriorChanged(page *pdfiumFormVisualPage, rect types.Rectangle) bool {
	insetX := rect.Width() / 4
	insetY := rect.Height() / 4
	interior := *types.NewRectangle(
		rect.LL.X+insetX,
		rect.LL.Y+insetY,
		rect.UR.X-insetX,
		rect.UR.Y-insetY,
	)
	return validFormVisualRectangle(&interior) &&
		pdfiumWidgetChangedPixels(page, interior) >= minimumVisibleRasterPixels
}

func pdfiumWidgetChangedPixels(page *pdfiumFormVisualPage, rect types.Rectangle) int {
	region, ok := pdfiumWidgetPixelRegion(page, rect)
	if !ok {
		return 0
	}
	changed := 0
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			if pdfiumVisualPixelChanged(page, x, y) {
				changed++
			}
		}
	}
	return changed
}

func pdfiumWidgetHasHorizontalFill(page *pdfiumFormVisualPage, rect types.Rectangle) bool {
	region, ok := pdfiumWidgetPixelRegion(page, rect)
	if !ok || region.Dx() < minimumVisibleRasterPixels {
		return false
	}
	for y := region.Min.Y; y < region.Max.Y; y++ {
		changed := 0
		for x := region.Min.X; x < region.Max.X; x++ {
			if pdfiumVisualPixelChanged(page, x, y) {
				changed++
			}
		}
		if changed*10 >= region.Dx()*6 {
			return true
		}
	}
	return false
}

func pdfiumWidgetPixelRegion(page *pdfiumFormVisualPage, rect types.Rectangle) (image.Rectangle, bool) {
	bounds := page.visible.Bounds()
	if bounds != page.background.Bounds() {
		return image.Rectangle{}, false
	}
	xScale := float64(bounds.Dx()) / page.crop.Width()
	yScale := float64(bounds.Dy()) / page.crop.Height()
	x0 := bounds.Min.X + int(math.Floor((rect.LL.X-page.crop.LL.X)*xScale))
	x1 := bounds.Min.X + int(math.Ceil((rect.UR.X-page.crop.LL.X)*xScale))
	y0 := bounds.Min.Y + int(math.Floor((page.crop.UR.Y-rect.UR.Y)*yScale))
	y1 := bounds.Min.Y + int(math.Ceil((page.crop.UR.Y-rect.LL.Y)*yScale))
	region := image.Rect(x0, y0, x1, y1).Intersect(bounds)
	return region, region.Dx() > 0 && region.Dy() > 0
}

func pdfiumVisualPixelChanged(page *pdfiumFormVisualPage, x int, y int) bool {
	visibleR, visibleG, visibleB, visibleA := page.visible.At(x, y).RGBA()
	backgroundR, backgroundG, backgroundB, backgroundA := page.background.At(x, y).RGBA()
	return colorDelta(visibleR, backgroundR) > visualPixelDeltaThreshold ||
		colorDelta(visibleG, backgroundG) > visualPixelDeltaThreshold ||
		colorDelta(visibleB, backgroundB) > visualPixelDeltaThreshold ||
		colorDelta(visibleA, backgroundA) > visualPixelDeltaThreshold
}

func portableFormVisualUnavailable() *Failure {
	return &Failure{Code: FailureBackendUnavailable, Message: "portable document visual backend is unavailable"}
}

func portableFormVisualCleanupFailure() *Failure {
	return &Failure{Code: FailureInternal, Message: "portable document visual backend cleanup failed"}
}
