package main

import (
	"encoding/json"
	"fmt"
	"image"
	_ "image/png"
	"math"
	"os"
	"strconv"
)

const pixelDeltaThreshold = uint32(0x0800)

type manifest struct {
	GridWidth         int          `json:"grid_width"`
	GridHeight        int          `json:"grid_height"`
	MaximumDifference float64      `json:"maximum_mean_difference"`
	Pages             []pageGolden `json:"pages"`
}

type pageGolden struct {
	Page             int      `json:"page"`
	Width            int      `json:"width"`
	Height           int      `json:"height"`
	MinimumChanged   int      `json:"minimum_changed_pixels"`
	DifferenceGolden []uint16 `json:"difference_golden"`
}

func main() {
	if len(os.Args) < 2 {
		fail("usage: document-form-visual-oracle capture|compare ...")
	}
	switch os.Args[1] {
	case "capture":
		capture()
	case "compare":
		compare()
	default:
		fail("usage: document-form-visual-oracle capture|compare ...")
	}
}

func capture() {
	if len(os.Args) < 6 || (len(os.Args)-4)%2 != 0 {
		fail("usage: document-form-visual-oracle capture <grid> <tolerance> <visible.png> <background.png> [...]")
	}
	grid, err := strconv.Atoi(os.Args[2])
	if err != nil || grid < 4 || grid > 128 {
		fail("grid must be between 4 and 128")
	}
	tolerance, err := strconv.ParseFloat(os.Args[3], 64)
	if err != nil || tolerance <= 0 || tolerance > 1 {
		fail("tolerance must be between zero and one")
	}
	result := manifest{GridWidth: grid, GridHeight: grid, MaximumDifference: tolerance}
	for index := 4; index < len(os.Args); index += 2 {
		visible := decode(os.Args[index])
		background := decode(os.Args[index+1])
		fingerprint, changed := differenceFingerprint(visible, background, grid, grid)
		result.Pages = append(result.Pages, pageGolden{
			Page:             len(result.Pages) + 1,
			Width:            visible.Bounds().Dx(),
			Height:           visible.Bounds().Dy(),
			MinimumChanged:   max(1, changed/2),
			DifferenceGolden: fingerprint,
		})
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fail("could not encode visual golden")
	}
	_, _ = os.Stdout.Write(append(encoded, '\n'))
}

func compare() {
	if len(os.Args) < 5 || (len(os.Args)-3)%2 != 0 {
		fail("usage: document-form-visual-oracle compare <manifest.json> <visible.png> <background.png> [...]")
	}
	data, err := os.ReadFile(os.Args[2])
	if err != nil {
		fail("could not read visual manifest")
	}
	var expected manifest
	if err = json.Unmarshal(data, &expected); err != nil || expected.GridWidth < 4 || expected.GridHeight < 4 ||
		expected.MaximumDifference <= 0 || expected.MaximumDifference > 1 ||
		len(expected.Pages) != (len(os.Args)-3)/2 {
		fail("visual manifest is invalid")
	}
	for index, page := range expected.Pages {
		visible := decode(os.Args[3+index*2])
		background := decode(os.Args[4+index*2])
		if visible.Bounds() != background.Bounds() || visible.Bounds().Dx() != page.Width ||
			visible.Bounds().Dy() != page.Height || page.Page != index+1 || page.MinimumChanged <= 0 {
			fail(fmt.Sprintf("page %d render dimensions differ from the golden", index+1))
		}
		actual, changed := differenceFingerprint(visible, background, expected.GridWidth, expected.GridHeight)
		if len(actual) != len(page.DifferenceGolden) || changed < page.MinimumChanged {
			fail(fmt.Sprintf("page %d form appearance is not visibly populated", page.Page))
		}
		difference := meanFingerprintDifference(actual, page.DifferenceGolden)
		if difference > expected.MaximumDifference {
			fail(fmt.Sprintf(
				"page %d mean normalized difference %.6f exceeds %.6f",
				page.Page,
				difference,
				expected.MaximumDifference,
			))
		}
		fmt.Printf("page=%d changed_pixels=%d mean_difference=%.6f\n", page.Page, changed, difference)
	}
}

func differenceFingerprint(visible image.Image, background image.Image, columns int, rows int) ([]uint16, int) {
	if visible.Bounds() != background.Bounds() || columns <= 0 || rows <= 0 ||
		visible.Bounds().Dx() < columns || visible.Bounds().Dy() < rows {
		fail("render pair dimensions differ")
	}
	bounds := visible.Bounds()
	values := make([]uint16, 0, columns*rows)
	changed := 0
	for row := 0; row < rows; row++ {
		y0 := bounds.Min.Y + row*bounds.Dy()/rows
		y1 := bounds.Min.Y + (row+1)*bounds.Dy()/rows
		for column := 0; column < columns; column++ {
			x0 := bounds.Min.X + column*bounds.Dx()/columns
			x1 := bounds.Min.X + (column+1)*bounds.Dx()/columns
			var total uint64
			var samples uint64
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					visibleR, visibleG, visibleB, _ := visible.At(x, y).RGBA()
					backgroundR, backgroundG, backgroundB, _ := background.At(x, y).RGBA()
					deltaR := channelDifference(visibleR, backgroundR)
					deltaG := channelDifference(visibleG, backgroundG)
					deltaB := channelDifference(visibleB, backgroundB)
					total += uint64(deltaR) + uint64(deltaG) + uint64(deltaB)
					samples += 3
					if deltaR > pixelDeltaThreshold || deltaG > pixelDeltaThreshold ||
						deltaB > pixelDeltaThreshold {
						changed++
					}
				}
			}
			values = append(values, uint16(math.Round(float64(total)/float64(samples))))
		}
	}
	return values, changed
}

func meanFingerprintDifference(left []uint16, right []uint16) float64 {
	if len(left) == 0 || len(left) != len(right) {
		return 1
	}
	var total uint64
	for index := range left {
		total += uint64(channelDifference(uint32(left[index]), uint32(right[index])))
	}
	return float64(total) / float64(len(left)) / 65_535
}

func decode(path string) image.Image {
	file, err := os.Open(path)
	if err != nil {
		fail("could not open rendered page")
	}
	defer func() { _ = file.Close() }()
	decoded, _, err := image.Decode(file)
	if err != nil {
		fail("could not decode rendered page")
	}
	return decoded
}

func channelDifference(left uint32, right uint32) uint32 {
	if left >= right {
		return left - right
	}
	return right - left
}

func fail(message string) {
	_, _ = fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
