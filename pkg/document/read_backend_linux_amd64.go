//go:build linux && amd64

package document

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const (
	popplerStderrLimit     = 8 * 1024
	verifiedExecutablePath = "/proc/self/fd/3"
)

var errPopplerTextOutputLimit = errors.New("document text output limit exceeded")

type popplerReadBackend struct{}

func newNativeReadBackend() readBackend {
	return popplerReadBackend{}
}

func readBackendAvailable() bool {
	return nativeBackendAvailable(PopplerBackendName)
}

func newVerifiedPopplerCommand(executable, expectedSHA256 string, arguments ...string) (*exec.Cmd, *os.File, error) {
	return newVerifiedDocumentCommand("mintclaw-poppler", executable, expectedSHA256, arguments...)
}

func newVerifiedDocumentCommand(
	snapshotName string,
	executable string,
	expectedSHA256 string,
	arguments ...string,
) (*exec.Cmd, *os.File, error) {
	source, err := openSourceNoFollow(executable)
	if err != nil {
		return nil, nil, errors.New("document read backend is unavailable")
	}
	defer func() { _ = source.Close() }()
	descriptor, err := unix.MemfdCreate(snapshotName, unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, nil, errors.New("document read backend cannot be isolated")
	}
	snapshot := os.NewFile(uintptr(descriptor), "mintclaw-poppler")
	if snapshot == nil {
		_ = unix.Close(descriptor)
		return nil, nil, errors.New("document read backend cannot be isolated")
	}
	fail := func(message string) (*exec.Cmd, *os.File, error) {
		_ = snapshot.Close()
		return nil, nil, errors.New(message)
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(snapshot, hash), io.LimitReader(source, maximumExecutableBytes+1))
	if err != nil || written <= 0 || written > maximumExecutableBytes ||
		hex.EncodeToString(hash.Sum(nil)) != expectedSHA256 {
		return fail("document read backend identity is not admitted")
	}
	if _, err = snapshot.Seek(0, io.SeekStart); err != nil {
		return fail("document read backend cannot be isolated")
	}
	seals := unix.F_SEAL_SEAL | unix.F_SEAL_SHRINK | unix.F_SEAL_GROW | unix.F_SEAL_WRITE
	if _, err = unix.FcntlInt(snapshot.Fd(), unix.F_ADD_SEALS, seals); err != nil {
		return fail("document read backend cannot be isolated")
	}
	command := exec.Command(verifiedExecutablePath, arguments...)
	command.ExtraFiles = []*os.File{snapshot}
	return command, snapshot, nil
}

func (popplerReadBackend) Extract(data []byte, request WorkerRequest) backendRead {
	if !readBackendAvailable() {
		return backendRead{
			State: StateUnavailable,
			Failure: &Failure{
				Code: FailureBackendUnavailable, Message: "document extraction backend is unavailable",
			},
		}
	}

	var artifact bytes.Buffer
	encoder := json.NewEncoder(&artifact)
	pages := make([]PageTextFacts, 0, len(request.Read.Pages))
	remaining := request.Read.Limits.MaxCharacters
	truncated := false
	for index, page := range request.Read.Pages {
		text, failure := popplerPageText(data, page, remaining)
		if failure != nil {
			return backendRead{State: StateFailed, Failure: failure}
		}
		text, characters, pageTruncated := boundExtractedPageText(
			text,
			remaining,
			index < len(request.Read.Pages)-1,
		)
		remaining -= characters
		if err := encoder.Encode(struct {
			Page      int    `json:"page"`
			Text      string `json:"text"`
			Truncated bool   `json:"truncated,omitempty"`
		}{Page: page, Text: text, Truncated: pageTruncated}); err != nil {
			return failedRead(FailureInternal, "document extraction artifact could not be encoded")
		}
		pages = append(pages, PageTextFacts{Page: page, Characters: characters, Truncated: pageTruncated})
		truncated = truncated || pageTruncated
		if remaining == 0 {
			break
		}
	}
	if artifact.Len() > int(request.Read.Limits.MaxArtifactBytes) {
		return failedRead(FailureExtractionLimit, "document extraction exceeded the artifact byte limit")
	}

	const name = "extracted-text.jsonl"
	if err := os.WriteFile(name, artifact.Bytes(), 0o600); err != nil {
		return failedRead(FailureInternal, "document extraction artifact could not be written")
	}
	pagesCovered := make([]int, 0, len(pages))
	totalCharacters := 0
	for _, page := range pages {
		pagesCovered = append(pagesCovered, page.Page)
		totalCharacters += page.Characters
	}
	if totalCharacters == 0 {
		return backendRead{
			State: StateUnsupported,
			Failure: &Failure{
				Code: FailureTextUnavailable, Message: "selected document pages have no extractable text",
			},
		}
	}
	digest := sha256.Sum256(artifact.Bytes())
	descriptor := Artifact{
		Ref:          workerArtifactRef(request.OperationID, name),
		Kind:         "extracted_text",
		ContentType:  "application/x-ndjson",
		Size:         int64(artifact.Len()),
		SHA256:       hex.EncodeToString(digest[:]),
		SourceSHA256: request.Input.SHA256,
		Pages:        pagesCovered,
		Truncated:    truncated,
	}
	return backendRead{
		State: StateSucceeded,
		Extraction: &ExtractionFacts{
			Backend:         popplerIdentity(),
			SelectedPages:   append([]int(nil), request.Read.Pages...),
			Pages:           pages,
			TotalCharacters: totalCharacters,
			Truncated:       truncated,
		},
		Artifacts: []WorkerArtifact{{Name: name, Artifact: descriptor}},
	}
}

func popplerPageText(data []byte, page, remaining int) (string, *Failure) {
	if remaining <= 0 {
		return "", nil
	}
	expectedExecutable, ok := nativeBackendExecutable(PopplerBackendName, popplerTextExecutableName)
	if !ok {
		return "", &Failure{Code: FailureBackendUnavailable, Message: "document extraction backend is unavailable"}
	}
	command, executableSnapshot, err := newVerifiedPopplerCommand(
		expectedExecutable.Path,
		expectedExecutable.SHA256,
		"-f", strconv.Itoa(page),
		"-l", strconv.Itoa(page),
		"-layout",
		"-nopgbrk",
		"-enc", "UTF-8",
		"-", "-",
	)
	if err != nil {
		return "", &Failure{Code: FailureBackendUnavailable, Message: "document extraction backend is unavailable"}
	}
	defer func() { _ = executableSnapshot.Close() }()
	command.Env = documentBackendEnvironment()
	command.Stdin = bytes.NewReader(data)
	stdout := newBoundedTextOutput((remaining+1)*utf8.UTFMax, DefaultMaxContentBytes)
	stderr := newBoundedWorkerBuffer(popplerStderrLimit)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil || stdout.exceeded || stderr.exceeded {
		return "", &Failure{
			Code:    FailureExtractionLimit,
			Message: "document text extraction failed or exceeded a limit",
		}
	}
	text, valid := completeUTF8Prefix(stdout.Bytes(), stdout.truncated)
	if !valid {
		return "", &Failure{
			Code:    FailureExtractionLimit,
			Message: "document text extraction failed or exceeded a limit",
		}
	}
	return strings.TrimRight(string(text), "\f\r\n"), nil
}

type boundedTextOutput struct {
	buffer         bytes.Buffer
	captureMaximum int
	hardMaximum    int64
	total          int64
	truncated      bool
	exceeded       bool
}

func newBoundedTextOutput(captureMaximum int, hardMaximum int64) *boundedTextOutput {
	return &boundedTextOutput{captureMaximum: captureMaximum, hardMaximum: hardMaximum}
}

func (b *boundedTextOutput) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	allowed := len(data)
	if int64(allowed) > b.hardMaximum-b.total {
		allowed = max(0, int(b.hardMaximum-b.total))
		b.exceeded = true
	}
	b.capture(data[:allowed])
	b.total += int64(allowed)
	if b.exceeded {
		return allowed, errPopplerTextOutputLimit
	}
	return len(data), nil
}

func (b *boundedTextOutput) capture(data []byte) {
	remaining := b.captureMaximum - b.buffer.Len()
	if remaining <= 0 {
		b.truncated = b.truncated || len(data) > 0
		return
	}
	if len(data) > remaining {
		_, _ = b.buffer.Write(data[:remaining])
		b.truncated = true
		return
	}
	_, _ = b.buffer.Write(data)
}

func (b *boundedTextOutput) Bytes() []byte {
	return b.buffer.Bytes()
}

func completeUTF8Prefix(data []byte, truncated bool) ([]byte, bool) {
	if utf8.Valid(data) {
		return data, true
	}
	if !truncated {
		return nil, false
	}
	minimum := max(0, len(data)-(utf8.UTFMax-1))
	for start := len(data) - 1; start >= minimum; start-- {
		if utf8.Valid(data[:start]) && !utf8.FullRune(data[start:]) {
			return data[:start], true
		}
	}
	return nil, false
}

func (popplerReadBackend) Render(data []byte, request WorkerRequest) backendRead {
	if !readBackendAvailable() {
		return backendRead{
			State: StateUnavailable,
			Failure: &Failure{
				Code: FailureBackendUnavailable, Message: "document rendering backend is unavailable",
			},
		}
	}
	renderExecutable, ok := nativeBackendExecutable(PopplerBackendName, popplerRenderExecutableName)
	if !ok {
		return backendRead{
			State: StateUnavailable,
			Failure: &Failure{
				Code: FailureBackendUnavailable, Message: "document rendering backend is unavailable",
			},
		}
	}

	artifacts := make([]WorkerArtifact, 0, len(request.Read.Pages))
	pages := make([]PageRenderFacts, 0, len(request.Read.Pages))
	var totalBytes, totalPixels int64
	for _, page := range request.Read.Pages {
		width, height, failure := popplerPageDimensions(
			data,
			page,
			request.Read.Limits.DPI,
			request.Read.Limits.MaxDimension,
		)
		if failure != nil {
			return backendRead{State: StateFailed, Failure: failure}
		}
		pixels := int64(width) * int64(height)
		if width > request.Read.Limits.MaxDimension || height > request.Read.Limits.MaxDimension ||
			pixels > request.Read.Limits.MaxPixelsPerPage || totalPixels+pixels > request.Read.Limits.MaxTotalPixels {
			return failedRead(FailureRenderLimit, "document page dimensions exceed the render limit")
		}
		name := fmt.Sprintf("page-%04d.png", page)
		prefix := strings.TrimSuffix(name, ".png")
		command, executableSnapshot, err := newVerifiedPopplerCommand(
			renderExecutable.Path,
			renderExecutable.SHA256,
			"-f", strconv.Itoa(page),
			"-l", strconv.Itoa(page),
			"-singlefile",
			"-png",
			"-cropbox",
			"-r", strconv.Itoa(request.Read.Limits.DPI),
			"-", prefix,
		)
		if err != nil {
			return backendRead{
				State: StateUnavailable,
				Failure: &Failure{
					Code: FailureBackendUnavailable, Message: "document rendering backend is unavailable",
				},
			}
		}
		command.Env = documentBackendEnvironment()
		command.Stdin = bytes.NewReader(data)
		stderr := newBoundedWorkerBuffer(popplerStderrLimit)
		command.Stdout = io.Discard
		command.Stderr = stderr
		runErr := command.Run()
		_ = executableSnapshot.Close()
		if runErr != nil || stderr.exceeded {
			return failedRead(FailureRenderLimit, "document page rendering failed or exceeded a limit")
		}
		artifact, actualWidth, actualHeight, failure := validateRenderedPNG(
			name,
			request,
			page,
			totalBytes,
			totalPixels,
		)
		if failure != nil {
			return backendRead{State: StateFailed, Failure: failure}
		}
		if actualWidth != width || actualHeight != height {
			return failedRead(FailureArtifactInvalid, "rendered page dimensions do not match the preflight")
		}
		totalBytes += artifact.Size
		totalPixels += pixels
		artifacts = append(artifacts, WorkerArtifact{Name: name, Artifact: artifact})
		pages = append(pages, PageRenderFacts{Page: page, Width: width, Height: height})
	}
	return backendRead{
		State: StateSucceeded,
		Rendering: &RenderingFacts{
			Backend:       popplerIdentity(),
			SelectedPages: append([]int(nil), request.Read.Pages...),
			Pages:         pages,
			DPI:           request.Read.Limits.DPI,
			TotalPixels:   totalPixels,
		},
		Artifacts: artifacts,
	}
}

func popplerPageDimensions(data []byte, page, dpi, maxDimension int) (int, int, *Failure) {
	infoExecutable, ok := nativeBackendExecutable(PopplerBackendName, popplerInfoExecutableName)
	if !ok {
		return 0, 0, &Failure{Code: FailureBackendUnavailable, Message: "document page preflight is unavailable"}
	}
	command, executableSnapshot, err := newVerifiedPopplerCommand(
		infoExecutable.Path,
		infoExecutable.SHA256,
		"-f", strconv.Itoa(page),
		"-l", strconv.Itoa(page),
		"-box",
		"-",
	)
	if err != nil {
		return 0, 0, &Failure{Code: FailureBackendUnavailable, Message: "document page preflight is unavailable"}
	}
	defer func() { _ = executableSnapshot.Close() }()
	command.Env = documentBackendEnvironment()
	command.Stdin = bytes.NewReader(data)
	stdout := newBoundedWorkerBuffer(popplerStderrLimit)
	stderr := newBoundedWorkerBuffer(popplerStderrLimit)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil || stdout.exceeded || stderr.exceeded {
		return 0, 0, &Failure{Code: FailureRenderLimit, Message: "document page preflight failed"}
	}
	var widthPoints, heightPoints float64
	rotation := 0
	for _, line := range strings.Split(string(stdout.Bytes()), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] != "Page" || fields[1] != strconv.Itoa(page) {
			continue
		}
		if len(fields) >= 7 && fields[2] == "size:" && fields[4] == "x" && fields[6] == "pts" {
			var widthErr, heightErr error
			widthPoints, widthErr = strconv.ParseFloat(fields[3], 64)
			heightPoints, heightErr = strconv.ParseFloat(fields[5], 64)
			if widthErr != nil || heightErr != nil {
				return 0, 0, &Failure{Code: FailureArtifactInvalid, Message: "document page dimensions are invalid"}
			}
		}
		if fields[2] == "rot:" {
			parsed, parseErr := strconv.Atoi(fields[3])
			if parseErr != nil {
				return 0, 0, &Failure{Code: FailureArtifactInvalid, Message: "document page rotation is invalid"}
			}
			rotation = parsed
		}
	}
	return boundedPageDimensions(widthPoints, heightPoints, dpi, maxDimension, rotation)
}

func validateRenderedPNG(
	name string,
	request WorkerRequest,
	page int,
	priorBytes int64,
	priorPixels int64,
) (Artifact, int, int, *Failure) {
	file, err := os.Open(name)
	if err != nil {
		return Artifact{}, 0, 0, &Failure{Code: FailureArtifactInvalid, Message: "rendered page artifact is missing"}
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 ||
		priorBytes+info.Size() > request.Read.Limits.MaxArtifactBytes {
		return Artifact{}, 0, 0, &Failure{
			Code:    FailureRenderLimit,
			Message: "rendered page exceeded the artifact byte limit",
		}
	}
	configuration, err := png.DecodeConfig(io.LimitReader(file, 1<<20))
	if err != nil || configuration.Width <= 0 || configuration.Height <= 0 ||
		configuration.Width > request.Read.Limits.MaxDimension ||
		configuration.Height > request.Read.Limits.MaxDimension {
		return Artifact{}, 0, 0, &Failure{Code: FailureArtifactInvalid, Message: "rendered page artifact is invalid"}
	}
	pixels := int64(configuration.Width) * int64(configuration.Height)
	if pixels > request.Read.Limits.MaxPixelsPerPage || priorPixels+pixels > request.Read.Limits.MaxTotalPixels {
		return Artifact{}, 0, 0, &Failure{Code: FailureRenderLimit, Message: "rendered page exceeded the pixel limit"}
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return Artifact{}, 0, 0, &Failure{Code: FailureArtifactInvalid, Message: "rendered page artifact is invalid"}
	}
	hash := sha256.New()
	read, err := io.Copy(hash, io.LimitReader(file, info.Size()+1))
	if err != nil || read != info.Size() {
		return Artifact{}, 0, 0, &Failure{Code: FailureArtifactInvalid, Message: "rendered page artifact is invalid"}
	}
	return Artifact{
		Ref:          workerArtifactRef(request.OperationID, name),
		Kind:         "page_render",
		ContentType:  "image/png",
		Size:         info.Size(),
		SHA256:       hex.EncodeToString(hash.Sum(nil)),
		SourceSHA256: request.Input.SHA256,
		Pages:        []int{page},
		Width:        configuration.Width,
		Height:       configuration.Height,
	}, configuration.Width, configuration.Height, nil
}

func documentBackendEnvironment() []string {
	return []string{"HOME=.", "TMPDIR=.", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "PATH="}
}
