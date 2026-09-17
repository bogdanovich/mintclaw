package main

import (
	"fmt"
	"image"
	_ "image/png"
	"os"
	"strconv"
)

func main() {
	if len(os.Args) != 4 {
		fail("usage: document-pixel-compare <production.png> <oracle.png> <maximum-mean-difference>")
	}
	maximum, err := strconv.ParseFloat(os.Args[3], 64)
	if err != nil || maximum < 0 || maximum > 1 {
		fail("pixel tolerance must be between zero and one")
	}
	production := decode(os.Args[1])
	oracle := decode(os.Args[2])
	if production.Bounds() != oracle.Bounds() {
		fail(fmt.Sprintf("image dimensions differ: %v != %v", production.Bounds(), oracle.Bounds()))
	}
	var difference, samples uint64
	for y := production.Bounds().Min.Y; y < production.Bounds().Max.Y; y++ {
		for x := production.Bounds().Min.X; x < production.Bounds().Max.X; x++ {
			pr, pg, pb, _ := production.At(x, y).RGBA()
			or, og, ob, _ := oracle.At(x, y).RGBA()
			difference += channelDifference(pr, or) + channelDifference(pg, og) + channelDifference(pb, ob)
			samples += 3
		}
	}
	mean := float64(difference) / float64(samples) / 65_535
	if mean > maximum {
		fail(fmt.Sprintf("mean normalized pixel difference %.6f exceeds %.6f", mean, maximum))
	}
	fmt.Printf("mean_difference=%.6f tolerance=%.6f\n", mean, maximum)
}

func decode(path string) image.Image {
	file, err := os.Open(path)
	if err != nil {
		fail(err.Error())
	}
	defer func() { _ = file.Close() }()
	decoded, _, err := image.Decode(file)
	if err != nil {
		fail(err.Error())
	}
	return decoded
}

func channelDifference(left, right uint32) uint64 {
	if left >= right {
		return uint64(left - right)
	}
	return uint64(right - left)
}

func fail(message string) {
	_, _ = fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
