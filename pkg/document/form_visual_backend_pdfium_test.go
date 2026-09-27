//go:build (linux && amd64) || (darwin && (amd64 || arm64)) || (windows && amd64)

package document

import (
	"image"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func TestPDFiumUnselectedButtonRejectsInteriorMark(t *testing.T) {
	for _, kind := range []string{"checkbox", "radio"} {
		t.Run(kind, func(t *testing.T) {
			background := image.NewRGBA(image.Rect(0, 0, 200, 200))
			visible := image.NewRGBA(image.Rect(0, 0, 200, 200))
			page := &pdfiumFormVisualPage{
				crop: *types.NewRectangle(0, 0, 100, 100), visible: visible, background: background,
			}
			widget := formVisualWidget{
				rect: *types.NewRectangle(10, 70, 30, 90), assertion: formVisualAssertionUnselectedButton,
			}
			if failure := verifyPDFiumWidgetAppearance(page, widget, pdfiumFormVisualWidget{}); failure != nil {
				t.Fatalf("empty unselected button failure = %#v", failure)
			}
			for pixel := 39; pixel < 43; pixel++ {
				visible.Set(pixel, 40, image.White)
			}
			failure := verifyPDFiumWidgetAppearance(page, widget, pdfiumFormVisualWidget{})
			if failure == nil || failure.Code != FailureAppearanceStale {
				t.Fatalf("stale unselected button failure = %#v", failure)
			}
		})
	}
}

func TestPDFiumListSelectionRequiresExactHighlightSet(t *testing.T) {
	characters := append(
		pdfiumFormVisualTestCharacters("one", 15, 60),
		pdfiumFormVisualTestCharacters("someone", 15, 40)...,
	)
	widget := formVisualWidget{
		rect: *types.NewRectangle(10, 20, 90, 80), expectedText: []string{"one"},
		assertion: formVisualAssertionListSelection,
	}
	tests := []struct {
		name       string
		highlights []int
		wantPass   bool
	}{
		{name: "exact", highlights: []int{70}, wantPass: true},
		{name: "superstring", highlights: []int{110}},
		{name: "extra highlight", highlights: []int{70, 110}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			background := image.NewRGBA(image.Rect(0, 0, 200, 200))
			visible := image.NewRGBA(image.Rect(0, 0, 200, 200))
			for _, y := range test.highlights {
				for x := 20; x < 180; x++ {
					visible.Set(x, y, image.White)
				}
			}
			page := &pdfiumFormVisualPage{
				crop: *types.NewRectangle(0, 0, 100, 100), visible: visible, background: background,
			}
			failure := verifyPDFiumWidgetAppearance(page, widget, pdfiumFormVisualWidget{
				text: "one someone", characters: characters,
			})
			if test.wantPass && failure != nil {
				t.Fatalf("exact selection failure = %#v", failure)
			}
			if !test.wantPass && (failure == nil || failure.Code != FailureAppearanceStale) {
				t.Fatalf("inexact selection failure = %#v", failure)
			}
		})
	}
}

func pdfiumFormVisualTestCharacters(value string, x float64, y float64) []pdfiumFormVisualCharacter {
	result := make([]pdfiumFormVisualCharacter, 0, len(value))
	for _, character := range value {
		result = append(result, pdfiumFormVisualCharacter{
			value: character, rect: *types.NewRectangle(x, y, x+4, y+10),
		})
		x += 5
	}
	return result
}
