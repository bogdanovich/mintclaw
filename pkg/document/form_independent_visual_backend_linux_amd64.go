//go:build linux && amd64

package document

import (
	"bytes"
	"image"
	"image/png"
	"strconv"
)

func ghostscriptBackendAvailable() bool {
	return nativeBackendAvailable(GhostscriptBackendName)
}

func ghostscriptFormPageAtDPI(
	data []byte,
	page int,
	expectedWidth int,
	expectedHeight int,
	dpi int,
) (image.Image, *Failure) {
	expectedExecutable, ok := nativeBackendExecutable(GhostscriptBackendName, ghostscriptExecutableName)
	if !ok {
		return nil, &Failure{Code: FailureBackendUnavailable, Message: "independent visual backend is unavailable"}
	}
	command, executableSnapshot, err := newVerifiedDocumentCommand(
		"mintclaw-ghostscript",
		expectedExecutable.Path,
		expectedExecutable.SHA256,
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
	defer func() { _ = executableSnapshot.Close() }()
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
