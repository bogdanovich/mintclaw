//go:build linux && amd64

package document

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"image"
	"image/png"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	pdfcpuapi "github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/form"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

const (
	maximumFormBBoxBytes       = 2 * 1024 * 1024
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

type popplerBBoxHTML struct {
	Body popplerBBoxBody `xml:"body"`
}

type popplerBBoxBody struct {
	Document popplerBBoxDocument `xml:"doc"`
}

type popplerBBoxDocument struct {
	Pages []popplerBBoxPage `xml:"page"`
}

type popplerBBoxPage struct {
	Width  float64           `xml:"width,attr"`
	Height float64           `xml:"height,attr"`
	Flows  []popplerBBoxFlow `xml:"flow"`
}

type popplerBBoxFlow struct {
	Blocks []popplerBBoxBlock `xml:"block"`
}

type popplerBBoxBlock struct {
	Lines []popplerBBoxLine `xml:"line"`
}

type popplerBBoxLine struct {
	Words []popplerBBoxWord `xml:"word"`
}

type popplerBBoxWord struct {
	XMin  float64 `xml:"xMin,attr"`
	YMin  float64 `xml:"yMin,attr"`
	XMax  float64 `xml:"xMax,attr"`
	YMax  float64 `xml:"yMax,attr"`
	Value string  `xml:",chardata"`
}

type formVisualPage struct {
	crop           types.Rectangle
	text           popplerBBoxPage
	backgroundText popplerBBoxPage
	visible        image.Image
	background     image.Image
}

func verifyPopplerFormCandidate(
	candidate []byte,
	request WorkerRequest,
	context *model.Context,
	exported form.Form,
	bindings map[string]pdfCPUFormBinding,
) (*formVisualEvidence, *Failure) {
	if !readBackendAvailable() {
		return nil, &Failure{Code: FailureBackendUnavailable, Message: "document visual backend is unavailable"}
	}
	if request.Fill == nil || context == nil || len(bindings) == 0 ||
		len(request.Fill.AffectedPages) == 0 || len(request.Fill.AffectedPages) > DefaultMaxRenderPages {
		return nil, visualVerificationFailure()
	}
	boundaries, err := context.PageBoundaries(nil)
	if err != nil || len(boundaries) != context.PageCount {
		return nil, visualVerificationFailure()
	}
	widgets, failure := collectFormVisualWidgets(context, exported, bindings)
	if failure != nil {
		return nil, failure
	}
	backgroundCandidate, failure := formCandidateWithoutAnnotations(
		candidate,
		request.Limits,
		request.Fill.AffectedPages,
	)
	if failure != nil {
		return nil, failure
	}
	pages := make(map[int]*formVisualPage, len(request.Fill.AffectedPages))
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
		text, textFailure := popplerFormBBox(candidate, pageNumber, *crop)
		if textFailure != nil {
			return nil, textFailure
		}
		backgroundText, textFailure := popplerFormBBox(backgroundCandidate, pageNumber, *crop)
		if textFailure != nil {
			return nil, textFailure
		}
		visible, renderFailure := popplerFormPage(candidate, pageNumber, false, width, height)
		if renderFailure != nil {
			return nil, renderFailure
		}
		background, renderFailure := popplerFormPage(candidate, pageNumber, true, width, height)
		if renderFailure != nil {
			return nil, renderFailure
		}
		if visible.Bounds() != background.Bounds() || visible.Bounds().Dx() <= 0 || visible.Bounds().Dy() <= 0 {
			return nil, visualVerificationFailure()
		}
		pages[pageNumber] = &formVisualPage{
			crop: *crop, text: *text, backgroundText: *backgroundText,
			visible: visible, background: background,
		}
	}
	for _, widget := range widgets {
		page := pages[widget.page]
		if page == nil || !formVisualRectangleWithin(widget.rect, page.crop) {
			return nil, visualVerificationFailure()
		}
		if assertionFailure := verifyFormWidgetAppearance(page, widget); assertionFailure != nil {
			return nil, assertionFailure
		}
	}
	return &formVisualEvidence{
		Assertions: len(request.Fill.AffectedPages) + len(widgets), RenderedPages: len(pages),
	}, nil
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

func formVisualWidgetsOverlap(widgets []formVisualWidget) bool {
	for left := range widgets {
		for right := left + 1; right < len(widgets); right++ {
			if widgets[left].page != widgets[right].page {
				continue
			}
			if formVisualRectanglesOverlap(widgets[left].rect, widgets[right].rect) {
				return true
			}
		}
	}
	return false
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

func formCandidateWithoutAnnotations(data []byte, limits Limits, pages []int) ([]byte, *Failure) {
	context, failure := readFormContext(bytes.NewReader(data), limits)
	if failure != nil {
		return nil, visualVerificationFailure()
	}
	context.Cmd = model.REMOVEANNOTATIONS
	for _, page := range pages {
		pageDictionary, _, _, err := context.PageDict(page, false)
		if err != nil || pageDictionary == nil {
			return nil, visualVerificationFailure()
		}
		delete(pageDictionary, "Annots")
	}
	output := &boundedFormWriteBuffer{maximum: DefaultMaxArtifactBytes}
	if err := pdfcpuapi.WriteContext(context, output); err != nil || output.Len() == 0 {
		return nil, visualVerificationFailure()
	}
	return append([]byte(nil), output.Bytes()...), nil
}

func popplerFormBBox(data []byte, page int, crop types.Rectangle) (*popplerBBoxPage, *Failure) {
	command, executable, err := newVerifiedPopplerCommand(
		popplerTextExecutable,
		popplerTextSHA256,
		"-f", strconv.Itoa(page),
		"-l", strconv.Itoa(page),
		"-bbox-layout",
		"-cropbox",
		"-enc", "UTF-8",
		"-",
		"-",
	)
	if err != nil {
		return nil, &Failure{Code: FailureBackendUnavailable, Message: "document visual backend is unavailable"}
	}
	defer func() { _ = executable.Close() }()
	command.Env = documentBackendEnvironment()
	command.Stdin = bytes.NewReader(data)
	stdout := newBoundedWorkerBuffer(maximumFormBBoxBytes)
	stderr := newBoundedWorkerBuffer(popplerStderrLimit)
	command.Stdout = stdout
	command.Stderr = stderr
	if err = command.Run(); err != nil || stdout.exceeded || stderr.exceeded {
		return nil, visualVerificationFailure()
	}
	var document popplerBBoxHTML
	decoder := xml.NewDecoder(bytes.NewReader(stdout.Bytes()))
	if err = decoder.Decode(&document); err != nil || len(document.Body.Document.Pages) != 1 {
		return nil, visualVerificationFailure()
	}
	result := document.Body.Document.Pages[0]
	if math.Abs(result.Width-crop.Width()) > visualCoordinateTolerance ||
		math.Abs(result.Height-crop.Height()) > visualCoordinateTolerance {
		return nil, visualVerificationFailure()
	}
	return &result, nil
}

func popplerFormPage(
	data []byte,
	page int,
	hideAnnotations bool,
	expectedWidth int,
	expectedHeight int,
) (image.Image, *Failure) {
	return popplerFormPageAtDPI(
		data, page, hideAnnotations, expectedWidth, expectedHeight, DefaultRenderDPI,
	)
}

func popplerFormPageAtDPI(
	data []byte,
	page int,
	hideAnnotations bool,
	expectedWidth int,
	expectedHeight int,
	dpi int,
) (image.Image, *Failure) {
	directory, err := os.MkdirTemp(".", ".form-visual-")
	if err != nil {
		return nil, visualVerificationFailure()
	}
	defer func() { _ = os.RemoveAll(directory) }()
	prefix := fmt.Sprintf(".form-visual-%04d", page)
	if hideAnnotations {
		prefix += "-background"
	} else {
		prefix += "-visible"
	}
	prefix = directory + "/" + prefix
	filename := prefix + ".png"
	arguments := []string{
		"-f", strconv.Itoa(page),
		"-l", strconv.Itoa(page),
		"-singlefile",
		"-png",
		"-cropbox",
		"-r", strconv.Itoa(dpi),
	}
	if hideAnnotations {
		arguments = append(arguments, "-hide-annotations")
	}
	arguments = append(arguments, "-", prefix)
	command, executable, err := newVerifiedPopplerCommand(
		popplerRenderExecutable,
		popplerRenderSHA256,
		arguments...,
	)
	if err != nil {
		return nil, &Failure{Code: FailureBackendUnavailable, Message: "document visual backend is unavailable"}
	}
	defer func() { _ = executable.Close() }()
	command.Env = documentBackendEnvironment()
	command.Stdin = bytes.NewReader(data)
	command.Stdout = io.Discard
	stderr := newBoundedWorkerBuffer(popplerStderrLimit)
	command.Stderr = stderr
	if err = command.Run(); err != nil || stderr.exceeded {
		return nil, visualVerificationFailure()
	}
	file, err := openSourceNoFollow(filename)
	if err != nil {
		return nil, visualVerificationFailure()
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > DefaultMaxArtifactBytes {
		return nil, visualVerificationFailure()
	}
	payload, err := io.ReadAll(io.LimitReader(file, DefaultMaxArtifactBytes+1))
	if err != nil || int64(len(payload)) != info.Size() || !bytes.HasPrefix(payload, []byte("\x89PNG\r\n\x1a\n")) {
		return nil, visualVerificationFailure()
	}
	configuration, err := png.DecodeConfig(bytes.NewReader(payload))
	if err != nil || configuration.Width != expectedWidth || configuration.Height != expectedHeight ||
		configuration.Width <= 0 || configuration.Height <= 0 || configuration.Width > HardMaxRenderEdge ||
		configuration.Height > HardMaxRenderEdge ||
		int64(configuration.Width)*int64(configuration.Height) > DefaultMaxPixelsPerPage {
		return nil, visualVerificationFailure()
	}
	reader := bytes.NewReader(payload)
	rendered, err := png.Decode(reader)
	if err != nil || reader.Len() != 0 || rendered.Bounds().Dx() != expectedWidth ||
		rendered.Bounds().Dy() != expectedHeight {
		return nil, visualVerificationFailure()
	}
	return rendered, nil
}

func verifyFormWidgetAppearance(page *formVisualPage, widget formVisualWidget) *Failure {
	switch widget.assertion {
	case formVisualAssertionStructural:
		return nil
	case formVisualAssertionSelectedButton:
		if formSelectedButtonVisible(page, widget.rect) {
			return nil
		}
		return &Failure{
			Code: FailureAppearanceStale, Message: "document form selected button appearance is stale",
		}
	case formVisualAssertionExactText, formVisualAssertionListSelection:
		matchedWords, failure := verifyFormWidgetText(page, widget)
		if failure != nil {
			return failure
		}
		if !formExpectedWordsVisible(page, matchedWords) {
			return &Failure{
				Code: FailureAppearanceStale, Message: "document form appearance did not render visibly",
			}
		}
		if !formExpectedWordsAbsentFromBackground(page, matchedWords) {
			return &Failure{
				Code: FailureAppearanceStale, Message: "document form appearance cannot be isolated",
			}
		}
		if widget.assertion == formVisualAssertionListSelection &&
			!formListSelectionVisible(page, widget.rect, matchedWords) {
			return &Failure{
				Code: FailureAppearanceStale, Message: "document form list selection appearance is stale",
			}
		}
		return nil
	default:
		return visualVerificationFailure()
	}
}

func verifyFormWidgetText(page *formVisualPage, widget formVisualWidget) ([][]popplerBBoxWord, *Failure) {
	if len(widget.expectedText) == 0 {
		return nil, nil
	}
	bbox := formWidgetBBox(widget.rect, page.crop)
	words := make([]string, 0)
	contained := make([]popplerBBoxWord, 0)
	lines := make([][]popplerBBoxWord, 0)
	for _, flow := range page.text.Flows {
		for _, block := range flow.Blocks {
			for _, line := range block.Lines {
				lineWords := make([]popplerBBoxWord, 0, len(line.Words))
				for _, word := range line.Words {
					if !bboxIntersectsWord(bbox, word) {
						continue
					}
					if !bboxContainsWord(bbox, word) {
						return nil, &Failure{Code: FailureContentClipped, Message: "document form content is clipped"}
					}
					words = append(words, word.Value)
					contained = append(contained, word)
					lineWords = append(lineWords, word)
				}
				if len(lineWords) > 0 {
					lines = append(lines, lineWords)
				}
			}
		}
	}
	actual := normalizeVisualText(strings.Join(words, " "))
	if widget.assertion == formVisualAssertionExactText {
		expected := ""
		if len(widget.expectedText) == 1 {
			expected = normalizeVisualText(widget.expectedText[0])
		}
		if actual != expected {
			if visualTextLooksClipped(expected, actual) {
				return nil, &Failure{Code: FailureContentClipped, Message: "document form content is clipped"}
			}
			return nil, &Failure{Code: FailureAppearanceStale, Message: "document form appearance is stale"}
		}
		if expected == "" {
			return nil, nil
		}
		return [][]popplerBBoxWord{contained}, nil
	}
	matches := make([][]popplerBBoxWord, 0, len(widget.expectedText))
	for _, expected := range widget.expectedText {
		normalized := normalizeVisualText(expected)
		match, found := uniqueVisualWordMatch(lines, normalized)
		if normalized == "" || !found {
			return nil, &Failure{Code: FailureAppearanceStale, Message: "document form appearance is stale"}
		}
		matches = append(matches, match)
	}
	return matches, nil
}

func uniqueVisualWordMatch(lines [][]popplerBBoxWord, expected string) ([]popplerBBoxWord, bool) {
	var match []popplerBBoxWord
	for _, line := range lines {
		for start := range line {
			for end := start + 1; end <= len(line); end++ {
				values := make([]string, 0, end-start)
				for _, word := range line[start:end] {
					values = append(values, word.Value)
				}
				if normalizeVisualText(strings.Join(values, " ")) != expected {
					continue
				}
				if match != nil {
					return nil, false
				}
				match = append([]popplerBBoxWord(nil), line[start:end]...)
			}
		}
	}
	return match, match != nil
}

func formExpectedWordsVisible(page *formVisualPage, matches [][]popplerBBoxWord) bool {
	for _, words := range matches {
		if len(words) == 0 {
			return false
		}
		for _, word := range words {
			rect := *types.NewRectangle(word.XMin, word.YMin, word.XMax, word.YMax)
			if formPopplerBBoxChangedPixels(page, rect) < minimumVisibleRasterPixels {
				return false
			}
		}
	}
	return true
}

func formExpectedWordsAbsentFromBackground(page *formVisualPage, matches [][]popplerBBoxWord) bool {
	for _, words := range matches {
		for _, matched := range words {
			rect := *types.NewRectangle(matched.XMin, matched.YMin, matched.XMax, matched.YMax)
			for _, flow := range page.backgroundText.Flows {
				for _, block := range flow.Blocks {
					for _, line := range block.Lines {
						for _, background := range line.Words {
							if bboxIntersectsWord(rect, background) {
								return false
							}
						}
					}
				}
			}
		}
	}
	return true
}

func formListSelectionVisible(
	page *formVisualPage,
	widgetRect types.Rectangle,
	matches [][]popplerBBoxWord,
) bool {
	widget := formWidgetBBox(widgetRect, page.crop)
	for _, words := range matches {
		if len(words) == 0 {
			return false
		}
		yMin, yMax := words[0].YMin, words[0].YMax
		for _, word := range words[1:] {
			yMin = math.Min(yMin, word.YMin)
			yMax = math.Max(yMax, word.YMax)
		}
		row := *types.NewRectangle(widget.LL.X+2, yMin, widget.UR.X-2, yMax)
		if !formPopplerBBoxHasHorizontalFill(page, row) {
			return false
		}
	}
	return true
}

func formSelectedButtonVisible(page *formVisualPage, widgetRect types.Rectangle) bool {
	widget := formWidgetBBox(widgetRect, page.crop)
	insetX := widget.Width() / 4
	insetY := widget.Height() / 4
	interior := *types.NewRectangle(
		widget.LL.X+insetX,
		widget.LL.Y+insetY,
		widget.UR.X-insetX,
		widget.UR.Y-insetY,
	)
	return validFormVisualRectangle(&interior) &&
		formPopplerBBoxChangedPixels(page, interior) >= minimumVisibleRasterPixels
}

func visualTextLooksClipped(expected string, actual string) bool {
	return actual != "" && len(actual) < len(expected) &&
		(strings.HasPrefix(expected, actual) || strings.HasSuffix(expected, actual))
}

func normalizeVisualText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func formWidgetBBox(rect types.Rectangle, crop types.Rectangle) types.Rectangle {
	return *types.NewRectangle(
		rect.LL.X-crop.LL.X,
		crop.UR.Y-rect.UR.Y,
		rect.UR.X-crop.LL.X,
		crop.UR.Y-rect.LL.Y,
	)
}

func bboxIntersectsWord(rect types.Rectangle, word popplerBBoxWord) bool {
	return word.XMax >= rect.LL.X-visualCoordinateTolerance &&
		word.XMin <= rect.UR.X+visualCoordinateTolerance &&
		word.YMax >= rect.LL.Y-visualCoordinateTolerance &&
		word.YMin <= rect.UR.Y+visualCoordinateTolerance
}

func bboxContainsWord(rect types.Rectangle, word popplerBBoxWord) bool {
	return word.XMin >= rect.LL.X-visualCoordinateTolerance &&
		word.XMax <= rect.UR.X+visualCoordinateTolerance &&
		word.YMin >= rect.LL.Y-visualCoordinateTolerance &&
		word.YMax <= rect.UR.Y+visualCoordinateTolerance
}

func formPopplerBBoxChangedPixels(page *formVisualPage, rect types.Rectangle) int {
	region, ok := formPopplerBBoxPixelRegion(page, rect)
	if !ok {
		return 0
	}
	changed := 0
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			if formVisualPixelChanged(page, x, y) {
				changed++
			}
		}
	}
	return changed
}

func formPopplerBBoxHasHorizontalFill(page *formVisualPage, rect types.Rectangle) bool {
	region, ok := formPopplerBBoxPixelRegion(page, rect)
	if !ok || region.Dx() < minimumVisibleRasterPixels {
		return false
	}
	for y := region.Min.Y; y < region.Max.Y; y++ {
		changed := 0
		for x := region.Min.X; x < region.Max.X; x++ {
			if formVisualPixelChanged(page, x, y) {
				changed++
			}
		}
		if changed*10 >= region.Dx()*6 {
			return true
		}
	}
	return false
}

func formPopplerBBoxPixelRegion(page *formVisualPage, rect types.Rectangle) (image.Rectangle, bool) {
	bounds := page.visible.Bounds()
	if bounds != page.background.Bounds() {
		return image.Rectangle{}, false
	}
	xScale := float64(bounds.Dx()) / page.crop.Width()
	yScale := float64(bounds.Dy()) / page.crop.Height()
	x0 := bounds.Min.X + int(math.Floor(rect.LL.X*xScale))
	x1 := bounds.Min.X + int(math.Ceil(rect.UR.X*xScale))
	y0 := bounds.Min.Y + int(math.Floor(rect.LL.Y*yScale))
	y1 := bounds.Min.Y + int(math.Ceil(rect.UR.Y*yScale))
	region := image.Rect(x0, y0, x1, y1).Intersect(bounds)
	return region, region.Dx() > 0 && region.Dy() > 0
}

func formVisualPixelChanged(page *formVisualPage, x int, y int) bool {
	visibleR, visibleG, visibleB, visibleA := page.visible.At(x, y).RGBA()
	backgroundR, backgroundG, backgroundB, backgroundA := page.background.At(x, y).RGBA()
	return colorDelta(visibleR, backgroundR) > visualPixelDeltaThreshold ||
		colorDelta(visibleG, backgroundG) > visualPixelDeltaThreshold ||
		colorDelta(visibleB, backgroundB) > visualPixelDeltaThreshold ||
		colorDelta(visibleA, backgroundA) > visualPixelDeltaThreshold
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
