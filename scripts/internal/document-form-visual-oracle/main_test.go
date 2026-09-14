package main

import (
	"image"
	"image/color"
	"testing"
)

func TestDifferenceFingerprintTracksVisibleChanges(t *testing.T) {
	background := image.NewRGBA(image.Rect(0, 0, 8, 8))
	visible := image.NewRGBA(image.Rect(0, 0, 8, 8))
	visible.Set(1, 1, color.White)
	visible.Set(6, 6, color.White)

	fingerprint, changed := differenceFingerprint(visible, background, 2, 2)
	if changed != 2 || len(fingerprint) != 4 || fingerprint[0] == 0 || fingerprint[3] == 0 ||
		fingerprint[1] != 0 || fingerprint[2] != 0 {
		t.Fatalf("fingerprint=%v changed=%d", fingerprint, changed)
	}
	if difference := meanFingerprintDifference(fingerprint, append([]uint16(nil), fingerprint...)); difference != 0 {
		t.Fatalf("same fingerprint difference=%f", difference)
	}
}
