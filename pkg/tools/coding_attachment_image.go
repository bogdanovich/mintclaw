package tools

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"

	_ "golang.org/x/image/webp"

	"github.com/bogdanovich/mintclaw/pkg/media"
)

const (
	maxAttachmentImageDimension = 16384
	maxAttachmentImagePixels    = 40_000_000
	maxAttachmentGIFFrames      = 256
)

type attachmentImageKind uint8

const (
	attachmentNotImage attachmentImageKind = iota
	attachmentImageValid
	attachmentImageInvalid
)

type attachmentImageInspection struct {
	kind        attachmentImageKind
	contentType string
	err         error
}

func inspectAttachmentImage(data []byte) attachmentImageInspection {
	contentType := media.DetectSupportedImageContentType(data)
	if contentType == "" {
		return attachmentImageInspection{kind: attachmentNotImage}
	}
	inspection := attachmentImageInspection{
		kind:        attachmentImageInvalid,
		contentType: contentType,
	}
	var expectedFormat string
	switch contentType {
	case "image/png":
		expectedFormat = "png"
	case "image/jpeg":
		expectedFormat = "jpeg"
	case "image/gif":
		expectedFormat = "gif"
	case "image/webp":
		expectedFormat = "webp"
	default:
		inspection.err = fmt.Errorf("unsupported image format")
		return inspection
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		inspection.err = fmt.Errorf("decode header: %w", err)
		return inspection
	}
	if format != expectedFormat {
		inspection.err = fmt.Errorf("signature format %q does not match decoder format %q", expectedFormat, format)
		return inspection
	}
	if !validAttachmentImageDimensions(config.Width, config.Height) {
		inspection.err = fmt.Errorf("dimensions %dx%d exceed the image budget", config.Width, config.Height)
		return inspection
	}
	if expectedFormat == "gif" {
		inspection.err = validateBoundedGIF(data, gif.DecodeAll)
	} else {
		inspection.err = validateBoundedStillImage(data, expectedFormat)
	}
	if inspection.err == nil {
		inspection.kind = attachmentImageValid
	}
	return inspection
}

func validateBoundedStillImage(data []byte, expectedFormat string) error {
	decoded, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("decode body: %w", err)
	}
	if format != expectedFormat {
		return fmt.Errorf("body format %q does not match %q", format, expectedFormat)
	}
	bounds := decoded.Bounds()
	if !validAttachmentImageDimensions(bounds.Dx(), bounds.Dy()) {
		return fmt.Errorf("decoded dimensions %dx%d exceed the image budget", bounds.Dx(), bounds.Dy())
	}
	return nil
}

type gifDecodeAllFunc func(io.Reader) (*gif.GIF, error)

func validateBoundedGIF(data []byte, decodeAll gifDecodeAllFunc) error {
	preflight, err := preflightGIF(data)
	if err != nil {
		return err
	}
	decoded, err := decodeAll(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("decode GIF body: %w", err)
	}
	if len(decoded.Image) != preflight.frames {
		return fmt.Errorf(
			"decoded GIF frame count %d does not match preflight count %d",
			len(decoded.Image),
			preflight.frames,
		)
	}
	var totalPixels uint64
	for _, frame := range decoded.Image {
		bounds := frame.Bounds()
		if !validAttachmentImageDimensions(bounds.Dx(), bounds.Dy()) {
			return fmt.Errorf("decoded GIF frame dimensions %dx%d exceed the image budget", bounds.Dx(), bounds.Dy())
		}
		totalPixels += uint64(bounds.Dx()) * uint64(bounds.Dy())
		if totalPixels > maxAttachmentImagePixels {
			return fmt.Errorf("decoded GIF frames exceed the cumulative pixel budget")
		}
	}
	return nil
}

type gifPreflightResult struct {
	frames int
}

func preflightGIF(data []byte) (gifPreflightResult, error) {
	if len(data) < 13 || (string(data[:6]) != "GIF87a" && string(data[:6]) != "GIF89a") {
		return gifPreflightResult{}, fmt.Errorf("invalid GIF header")
	}
	screenWidth := int(binary.LittleEndian.Uint16(data[6:8]))
	screenHeight := int(binary.LittleEndian.Uint16(data[8:10]))
	if !validAttachmentImageDimensions(screenWidth, screenHeight) {
		return gifPreflightResult{}, fmt.Errorf(
			"GIF screen dimensions %dx%d exceed the image budget",
			screenWidth,
			screenHeight,
		)
	}
	offset := 13
	if data[10]&0x80 != 0 {
		colorTableBytes := 3 * (1 << ((data[10] & 0x07) + 1))
		if err := advanceGIFOffset(data, &offset, colorTableBytes); err != nil {
			return gifPreflightResult{}, fmt.Errorf("GIF global color table: %w", err)
		}
	}
	result := gifPreflightResult{}
	var totalPixels uint64
	for {
		if offset >= len(data) {
			return gifPreflightResult{}, fmt.Errorf("GIF is missing its trailer")
		}
		blockType := data[offset]
		offset++
		switch blockType {
		case 0x21:
			if err := advanceGIFOffset(data, &offset, 1); err != nil {
				return gifPreflightResult{}, fmt.Errorf("GIF extension label: %w", err)
			}
			if err := skipGIFSubBlocks(data, &offset); err != nil {
				return gifPreflightResult{}, fmt.Errorf("GIF extension: %w", err)
			}
		case 0x2c:
			if err := advanceGIFOffset(data, &offset, 9); err != nil {
				return gifPreflightResult{}, fmt.Errorf("GIF frame descriptor: %w", err)
			}
			descriptor := data[offset-9 : offset]
			left := int(binary.LittleEndian.Uint16(descriptor[0:2]))
			top := int(binary.LittleEndian.Uint16(descriptor[2:4]))
			width := int(binary.LittleEndian.Uint16(descriptor[4:6]))
			height := int(binary.LittleEndian.Uint16(descriptor[6:8]))
			if !validAttachmentImageDimensions(width, height) || left+width > screenWidth || top+height > screenHeight {
				return gifPreflightResult{}, fmt.Errorf("GIF frame dimensions or position exceed the image budget")
			}
			result.frames++
			totalPixels += uint64(width) * uint64(height)
			if result.frames > maxAttachmentGIFFrames || totalPixels > maxAttachmentImagePixels {
				return gifPreflightResult{}, fmt.Errorf("GIF frame count or cumulative pixels exceed the image budget")
			}
			if descriptor[8]&0x80 != 0 {
				colorTableBytes := 3 * (1 << ((descriptor[8] & 0x07) + 1))
				if err := advanceGIFOffset(data, &offset, colorTableBytes); err != nil {
					return gifPreflightResult{}, fmt.Errorf("GIF local color table: %w", err)
				}
			}
			if err := advanceGIFOffset(data, &offset, 1); err != nil {
				return gifPreflightResult{}, fmt.Errorf("GIF LZW code size: %w", err)
			}
			if err := skipGIFSubBlocks(data, &offset); err != nil {
				return gifPreflightResult{}, fmt.Errorf("GIF image data: %w", err)
			}
		case 0x3b:
			if result.frames == 0 {
				return gifPreflightResult{}, fmt.Errorf("GIF contains no frames")
			}
			return result, nil
		default:
			return gifPreflightResult{}, fmt.Errorf("GIF contains unknown block type 0x%02x", blockType)
		}
	}
}

func skipGIFSubBlocks(data []byte, offset *int) error {
	for {
		if *offset >= len(data) {
			return io.ErrUnexpectedEOF
		}
		length := int(data[*offset])
		*offset += 1
		if length == 0 {
			return nil
		}
		if err := advanceGIFOffset(data, offset, length); err != nil {
			return err
		}
	}
}

func advanceGIFOffset(data []byte, offset *int, count int) error {
	if count < 0 || *offset < 0 || count > len(data)-*offset {
		return io.ErrUnexpectedEOF
	}
	*offset += count
	return nil
}

func validAttachmentImageDimensions(width, height int) bool {
	if width <= 0 || height <= 0 || width > maxAttachmentImageDimension || height > maxAttachmentImageDimension {
		return false
	}
	return uint64(width)*uint64(height) <= maxAttachmentImagePixels
}
