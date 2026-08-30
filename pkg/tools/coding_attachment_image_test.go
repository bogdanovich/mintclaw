package tools

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"io"
	"testing"
)

func TestInspectAttachmentImageAcceptsAnimatedGIF(t *testing.T) {
	palette := color.Palette{color.Black, color.White}
	first := image.NewPaletted(image.Rect(0, 0, 1, 1), palette)
	second := image.NewPaletted(image.Rect(0, 0, 1, 1), palette)
	second.SetColorIndex(0, 0, 1)
	var encoded bytes.Buffer
	if err := gif.EncodeAll(&encoded, &gif.GIF{
		Image: []*image.Paletted{first, second},
		Delay: []int{0, 1},
	}); err != nil {
		t.Fatal(err)
	}

	inspection := inspectAttachmentImage(encoded.Bytes())
	if inspection.kind != attachmentImageValid || inspection.contentType != "image/gif" || inspection.err != nil {
		t.Fatalf("animated GIF inspection = %+v", inspection)
	}
}

func TestValidateBoundedGIFRejectsFrameBombBeforeDecode(t *testing.T) {
	data := []byte{
		'G', 'I', 'F', '8', '9', 'a',
		1, 0, 1, 0, 0x80, 0, 0,
		0, 0, 0, 0xff, 0xff, 0xff,
	}
	frame := []byte{
		0x2c,
		0, 0, 0, 0, 1, 0, 1, 0, 0,
		2,
		0,
	}
	for range maxAttachmentGIFFrames + 1 {
		data = append(data, frame...)
	}
	data = append(data, 0x3b)

	decodeCalled := false
	err := validateBoundedGIF(data, func(io.Reader) (*gif.GIF, error) {
		decodeCalled = true
		return nil, errors.New("decode must not run")
	})
	if err == nil {
		t.Fatal("oversized GIF frame count was accepted")
	}
	if decodeCalled {
		t.Fatal("GIF DecodeAll ran before the allocation-free frame budget check")
	}
}
