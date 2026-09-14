//go:build linux && amd64

package document

import (
	"image"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func TestVerifyFormWidgetTextDistinguishesStaleAndClippedAppearances(t *testing.T) {
	page := formVisualTextPage(
		popplerBBoxWord{XMin: 15, YMin: 25, XMax: 35, YMax: 40, Value: "old"},
		popplerBBoxWord{XMin: 40, YMin: 25, XMax: 65, YMax: 40, Value: "value"},
	)
	widget := formVisualWidget{
		rect:         *types.NewRectangle(10, 150, 100, 180),
		expectedText: []string{"new value"},
		exactText:    true,
	}
	if _, failure := verifyFormWidgetText(page, widget); failure == nil || failure.Code != FailureAppearanceStale {
		t.Fatalf("stale failure = %#v", failure)
	}

	widget.expectedText = []string{"old value continues"}
	if _, failure := verifyFormWidgetText(page, widget); failure == nil || failure.Code != FailureContentClipped {
		t.Fatalf("clipped failure = %#v", failure)
	}
}

func TestVerifyFormWidgetTextRejectsGlyphOutsideWidget(t *testing.T) {
	page := formVisualTextPage(popplerBBoxWord{
		XMin: 15, YMin: 25, XMax: 105, YMax: 40, Value: "overflow",
	})
	widget := formVisualWidget{
		rect:         *types.NewRectangle(10, 150, 100, 180),
		expectedText: []string{"overflow"},
		exactText:    true,
	}
	if _, failure := verifyFormWidgetText(page, widget); failure == nil || failure.Code != FailureContentClipped {
		t.Fatalf("outside-widget failure = %#v", failure)
	}
}

func formVisualTextPage(words ...popplerBBoxWord) *formVisualPage {
	return &formVisualPage{
		crop: *types.NewRectangle(0, 0, 200, 200),
		text: popplerBBoxPage{Flows: []popplerBBoxFlow{{
			Blocks: []popplerBBoxBlock{{
				Lines: []popplerBBoxLine{{Words: words}},
			}},
		}}},
	}
}

func TestFormWidgetChangedPixelsUsesOnlyWidgetRegion(t *testing.T) {
	background := image.NewRGBA(image.Rect(0, 0, 200, 200))
	visible := image.NewRGBA(image.Rect(0, 0, 200, 200))
	visible.Set(150, 150, image.White)
	page := &formVisualPage{
		crop:       *types.NewRectangle(0, 0, 100, 100),
		visible:    visible,
		background: background,
	}
	rect := *types.NewRectangle(10, 80, 20, 90)
	if changed := formWidgetChangedPixels(page, rect); changed != 0 {
		t.Fatalf("outside changed pixels = %d", changed)
	}
	visible.Set(30, 30, image.White)
	if changed := formWidgetChangedPixels(page, rect); changed != 1 {
		t.Fatalf("inside changed pixels = %d", changed)
	}
}

func TestExpectedWordsRequireTheirOwnRasterEvidence(t *testing.T) {
	background := image.NewRGBA(image.Rect(0, 0, 200, 200))
	visible := image.NewRGBA(image.Rect(0, 0, 200, 200))
	page := formVisualTextPage(popplerBBoxWord{
		XMin: 15, YMin: 25, XMax: 35, YMax: 40, Value: "expected",
	})
	page.visible = visible
	page.background = background
	widget := formVisualWidget{
		rect: *types.NewRectangle(10, 150, 100, 180), expectedText: []string{"expected"}, exactText: true,
	}
	matches, failure := verifyFormWidgetText(page, widget)
	if failure != nil {
		t.Fatal(failure)
	}
	for x := 90; x < 94; x++ {
		visible.Set(x, 25, image.White)
	}
	if formExpectedWordsVisible(page, matches) {
		t.Fatal("widget-border evidence was attributed to the expected word")
	}
	for x := 20; x < 24; x++ {
		visible.Set(x, 30, image.White)
	}
	if !formExpectedWordsVisible(page, matches) {
		t.Fatal("expected word raster evidence was not recognized")
	}
	page.backgroundText = popplerBBoxPage{Flows: []popplerBBoxFlow{{Blocks: []popplerBBoxBlock{{
		Lines: []popplerBBoxLine{{Words: []popplerBBoxWord{{
			XMin: 15, YMin: 25, XMax: 35, YMax: 40, Value: "expected",
		}}}},
	}}}}}
	if formExpectedWordsAbsentFromBackground(page, matches) {
		t.Fatal("static background text was attributed to the assigned widget")
	}
	page.backgroundText = popplerBBoxPage{}
	if !formExpectedWordsAbsentFromBackground(page, matches) {
		t.Fatal("annotation-only expected word was rejected")
	}
}

func TestFormVisualWidgetsRejectOverlappingAffectedRectangles(t *testing.T) {
	widgets := []formVisualWidget{
		{page: 1, rect: *types.NewRectangle(10, 10, 40, 40)},
		{page: 1, rect: *types.NewRectangle(30, 30, 60, 60)},
	}
	if !formVisualWidgetsOverlap(widgets) {
		t.Fatal("overlapping affected widgets were admitted")
	}
	widgets[1].rect = *types.NewRectangle(40, 10, 60, 40)
	if formVisualWidgetsOverlap(widgets) {
		t.Fatal("edge-adjacent widgets were treated as overlapping")
	}
	widgets[1].page = 2
	widgets[1].rect = *types.NewRectangle(30, 30, 60, 60)
	if formVisualWidgetsOverlap(widgets) {
		t.Fatal("widgets on different pages were treated as overlapping")
	}
}

func TestFormVisualWidgetsRejectOverlappingUnassignedAnnotations(t *testing.T) {
	widget := formVisualWidget{
		objectNumber: 10, page: 1, rect: *types.NewRectangle(10, 10, 40, 40),
	}
	annotations := []formVisualAnnotation{
		{objectNumber: 10, page: 1, rect: *types.NewRectangle(10, 10, 40, 40)},
		{objectNumber: 11, page: 1, rect: *types.NewRectangle(30, 30, 60, 60)},
	}
	if !formVisualWidgetsOverlapAnnotations([]formVisualWidget{widget}, annotations) {
		t.Fatal("overlapping unassigned annotation was admitted")
	}
	annotations[1].rect = *types.NewRectangle(40, 10, 60, 40)
	if formVisualWidgetsOverlapAnnotations([]formVisualWidget{widget}, annotations) {
		t.Fatal("self and edge-adjacent annotations were treated as overlapping")
	}
}

func TestFormListSelectionRequiresHorizontalRowFill(t *testing.T) {
	background := image.NewRGBA(image.Rect(0, 0, 200, 200))
	visible := image.NewRGBA(image.Rect(0, 0, 200, 200))
	page := &formVisualPage{
		crop: *types.NewRectangle(0, 0, 100, 100), visible: visible, background: background,
	}
	widget := *types.NewRectangle(10, 20, 90, 80)
	matches := [][]popplerBBoxWord{{{XMin: 15, YMin: 30, XMax: 25, YMax: 40, Value: "one"}}}
	for x := 30; x < 34; x++ {
		visible.Set(x, 65, image.White)
	}
	if formListSelectionVisible(page, widget, matches) {
		t.Fatal("option glyphs alone were accepted as a selection highlight")
	}
	for x := 24; x < 176; x++ {
		visible.Set(x, 70, image.White)
	}
	if !formListSelectionVisible(page, widget, matches) {
		t.Fatal("selected-row horizontal fill was not recognized")
	}
}

func TestFormVisualPageDimensionsFailBeforeOversizedRender(t *testing.T) {
	crop := *types.NewRectangle(0, 0, 612, 792)
	width, height, pixels, failure := formVisualPageDimensions(crop, 0)
	if failure != nil || width != 1224 || height != 1584 || pixels != 3_877_632 {
		t.Fatalf("ordinary preflight = %dx%d pixels=%d failure=%#v", width, height, pixels, failure)
	}
	oversized := *types.NewRectangle(0, 0, 3000, 792)
	if _, _, _, failure = formVisualPageDimensions(oversized, 0); failure == nil ||
		failure.Code != FailureRenderLimit {
		t.Fatalf("oversized page failure = %#v", failure)
	}
	if _, _, _, failure = formVisualPageDimensions(crop, DefaultMaxRenderPixels-pixels); failure != nil {
		t.Fatalf("exact two-raster aggregate boundary = %#v", failure)
	}
	if _, _, _, failure = formVisualPageDimensions(crop, DefaultMaxRenderPixels-pixels+1); failure == nil ||
		failure.Code != FailureRenderLimit {
		t.Fatalf("aggregate page failure = %#v", failure)
	}
}
