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
	if failure := verifyFormWidgetText(page, widget); failure == nil || failure.Code != FailureAppearanceStale {
		t.Fatalf("stale failure = %#v", failure)
	}

	widget.expectedText = []string{"old value continues"}
	if failure := verifyFormWidgetText(page, widget); failure == nil || failure.Code != FailureContentClipped {
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
	if failure := verifyFormWidgetText(page, widget); failure == nil || failure.Code != FailureContentClipped {
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
