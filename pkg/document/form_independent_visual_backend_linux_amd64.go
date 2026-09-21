//go:build linux && amd64

package document

import (
	"bytes"
	"image"
	"image/png"
	"strconv"
)

const (
	ghostscriptExecutable = "/usr/bin/gs"
	ghostscriptSHA256     = "7c3d71005d09340a30d9436456f212d2ef499f58378ae6763f55da9fbaf13b49"
)

func ghostscriptBackendAvailable() bool {
	return executableSHA256(ghostscriptExecutable) == ghostscriptSHA256
}

func ghostscriptFormPage(data []byte, page, expectedWidth, expectedHeight int) (image.Image, *Failure) {
	return ghostscriptFormPageAtDPI(data, page, expectedWidth, expectedHeight, DefaultRenderDPI)
}

func ghostscriptFormPageAtDPI(
	data []byte,
	page int,
	expectedWidth int,
	expectedHeight int,
	dpi int,
) (image.Image, *Failure) {
	command, executable, err := newVerifiedDocumentCommand(
		"mintclaw-ghostscript",
		ghostscriptExecutable,
		ghostscriptSHA256,
		"-dSAFER",
		"-dBATCH",
		"-dNOPAUSE",
		"-dQUIET",
		"-dNumRenderingThreads=1",
		"-dFirstPage="+strconv.Itoa(page),
		"-dLastPage="+strconv.Itoa(page),
		"-sDEVICE=png16m",
		"-dUseCropBox",
		"-r"+strconv.Itoa(dpi),
		"-sOutputFile=-",
		"-",
	)
	if err != nil {
		return nil, &Failure{Code: FailureBackendUnavailable, Message: "independent visual backend is unavailable"}
	}
	defer func() { _ = executable.Close() }()
	stdout := newBoundedWorkerBuffer(int(DefaultMaxArtifactBytes))
	stderr := newBoundedWorkerBuffer(popplerStderrLimit)
	command.Env = documentBackendEnvironment()
	command.Stdin = bytes.NewReader(data)
	command.Stdout = stdout
	command.Stderr = stderr
	if err = command.Run(); err != nil || stdout.exceeded || stderr.exceeded {
		return nil, &Failure{Code: FailureVerificationVisual, Message: "independent visual verification failed"}
	}
	reader := bytes.NewReader(stdout.Bytes())
	configuration, err := png.DecodeConfig(reader)
	if err != nil || configuration.Width != expectedWidth || configuration.Height != expectedHeight ||
		configuration.Width <= 0 || configuration.Height <= 0 ||
		int64(configuration.Width)*int64(configuration.Height) > DefaultMaxPixelsPerPage {
		return nil, &Failure{Code: FailureVerificationVisual, Message: "independent visual verification failed"}
	}
	reader = bytes.NewReader(stdout.Bytes())
	rendered, err := png.Decode(reader)
	if err != nil || reader.Len() != 0 || rendered.Bounds().Dx() != expectedWidth ||
		rendered.Bounds().Dy() != expectedHeight {
		return nil, &Failure{Code: FailureVerificationVisual, Message: "independent visual verification failed"}
	}
	return rendered, nil
}
