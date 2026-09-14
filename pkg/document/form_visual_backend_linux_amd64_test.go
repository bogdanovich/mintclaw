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
		assertion:    formVisualAssertionExactText,
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
		assertion:    formVisualAssertionExactText,
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

func TestExpectedWordsRequireTheirOwnRasterEvidence(t *testing.T) {
	background := image.NewRGBA(image.Rect(0, 0, 200, 200))
	visible := image.NewRGBA(image.Rect(0, 0, 200, 200))
	page := formVisualTextPage(popplerBBoxWord{
		XMin: 15, YMin: 25, XMax: 35, YMax: 40, Value: "expected",
	})
	page.visible = visible
	page.background = background
	widget := formVisualWidget{
		rect: *types.NewRectangle(10, 150, 100, 180), expectedText: []string{"expected"},
		assertion: formVisualAssertionExactText,
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

func TestSelectedButtonRequiresInteriorMark(t *testing.T) {
	for _, kind := range []string{"checkbox", "radio"} {
		t.Run(kind, func(t *testing.T) {
			background := image.NewRGBA(image.Rect(0, 0, 200, 200))
			visible := image.NewRGBA(image.Rect(0, 0, 200, 200))
			page := &formVisualPage{
				crop: *types.NewRectangle(0, 0, 100, 100), visible: visible, background: background,
			}
			widget := formVisualWidget{
				rect: *types.NewRectangle(10, 70, 30, 90), assertion: formVisualAssertionSelectedButton,
			}
			for pixel := 20; pixel < 60; pixel++ {
				visible.Set(pixel, 20, image.White)
				visible.Set(pixel, 59, image.White)
				visible.Set(20, pixel, image.White)
				visible.Set(59, pixel, image.White)
			}
			failure := verifyFormWidgetAppearance(page, widget)
			if failure == nil || failure.Code != FailureAppearanceStale {
				t.Fatalf("border-only selected button failure = %#v", failure)
			}
			for pixel := 39; pixel < 43; pixel++ {
				visible.Set(pixel, 40, image.White)
			}
			if failure = verifyFormWidgetAppearance(page, widget); failure != nil {
				t.Fatalf("interior selection mark failure = %#v", failure)
			}
		})
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
