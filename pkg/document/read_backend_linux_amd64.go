//go:build linux && amd64

package document

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	popplerTextExecutable   = "/usr/bin/pdftotext"
	popplerRenderExecutable = "/usr/bin/pdftoppm"
	popplerInfoExecutable   = "/usr/bin/pdfinfo"
	popplerTextSHA256       = "0fb98ea179e19154a90202608c164f2a319b79f16576fa6534b2d601033565e7"
	popplerRenderSHA256     = "207dcabcaeea0ce572aefc498d07d44d56a9ca06a85b3ae1fecd050476a34bf8"
	popplerInfoSHA256       = "3293dda06d80e1e38dab859aa47368c2876aedc41cbc2e24e8fb9a4e66392078"
	popplerProbeTimeout     = 2 * time.Second
	popplerStderrLimit      = 8 * 1024
)

type popplerReadBackend struct{}

func failedRead(code FailureCode, message string) backendRead {
	return backendRead{
		State:   StateFailed,
		Failure: &Failure{Code: code, Message: message},
	}
}

func newReadBackend() readBackend {
	return popplerReadBackend{}
}

func readBackendAvailable() bool {
	return popplerVersion(popplerTextExecutable) == PopplerBackendVersion &&
		popplerVersion(popplerRenderExecutable) == PopplerBackendVersion &&
		popplerVersion(popplerInfoExecutable) == PopplerBackendVersion &&
		executableSHA256(popplerTextExecutable) == popplerTextSHA256 &&
		executableSHA256(popplerRenderExecutable) == popplerRenderSHA256 &&
		executableSHA256(popplerInfoExecutable) == popplerInfoSHA256
}

func executableSHA256(path string) string {
	file, err := openSourceNoFollow(path)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err = io.Copy(hash, io.LimitReader(file, 64*1024*1024)); err != nil {
		return ""
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func popplerVersion(executable string) string {
	ctx, cancel := context.WithTimeout(context.Background(), popplerProbeTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-v")
	command.Env = documentBackendEnvironment()
	output, err := command.CombinedOutput()
	if err != nil || ctx.Err() != nil || len(output) > popplerStderrLimit {
		return ""
	}
	fields := strings.Fields(string(output))
	if len(fields) < 3 || fields[1] != "version" {
		return ""
	}
	return fields[2]
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

func boundExtractedPageText(text string, remaining int, hasLaterPage bool) (string, int, bool) {
	characters := utf8.RuneCountInString(text)
	truncated := characters > remaining || (characters == remaining && hasLaterPage)
	if characters > remaining {
		return truncateRunes(text, remaining), remaining, true
	}
	return text, characters, truncated
}

func popplerPageText(data []byte, page, remaining int) (string, *Failure) {
	if remaining <= 0 {
		return "", nil
	}
	command := exec.Command(
		popplerTextExecutable,
		"-f", strconv.Itoa(page),
		"-l", strconv.Itoa(page),
		"-layout",
		"-nopgbrk",
		"-enc", "UTF-8",
		"-", "-",
	)
	command.Env = documentBackendEnvironment()
	command.Stdin = bytes.NewReader(data)
	stdout := newBoundedWorkerBuffer(DefaultMaxExtractChars*utf8.UTFMax + 1)
	stderr := newBoundedWorkerBuffer(popplerStderrLimit)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil || stdout.exceeded || stderr.exceeded || !utf8.Valid(stdout.Bytes()) {
		return "", &Failure{
			Code:    FailureExtractionLimit,
			Message: "document text extraction failed or exceeded a limit",
		}
	}
	return strings.TrimRight(string(stdout.Bytes()), "\f\r\n"), nil
}

func truncateRunes(value string, maximum int) string {
	if maximum <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum])
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

	artifacts := make([]WorkerArtifact, 0, len(request.Read.Pages))
	pages := make([]PageRenderFacts, 0, len(request.Read.Pages))
	var totalBytes, totalPixels int64
	for _, page := range request.Read.Pages {
		width, height, failure := popplerPageDimensions(data, page, request.Read.Limits.DPI)
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
		command := exec.Command(
			popplerRenderExecutable,
			"-f", strconv.Itoa(page),
			"-l", strconv.Itoa(page),
			"-singlefile",
			"-png",
			"-cropbox",
			"-r", strconv.Itoa(request.Read.Limits.DPI),
			"-", prefix,
		)
		command.Env = documentBackendEnvironment()
		command.Stdin = bytes.NewReader(data)
		stderr := newBoundedWorkerBuffer(popplerStderrLimit)
		command.Stdout = io.Discard
		command.Stderr = stderr
		if err := command.Run(); err != nil || stderr.exceeded {
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

func popplerPageDimensions(data []byte, page, dpi int) (int, int, *Failure) {
	command := exec.Command(
		popplerInfoExecutable,
		"-f", strconv.Itoa(page),
		"-l", strconv.Itoa(page),
		"-box",
		"-",
	)
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
	if widthPoints <= 0 || heightPoints <= 0 ||
		(rotation != 0 && rotation != 90 && rotation != 180 && rotation != 270) {
		return 0, 0, &Failure{Code: FailureArtifactInvalid, Message: "document page dimensions are unavailable"}
	}
	width := int(math.Ceil(widthPoints * float64(dpi) / 72))
	height := int(math.Ceil(heightPoints * float64(dpi) / 72))
	if rotation == 90 || rotation == 270 {
		width, height = height, width
	}
	return width, height, nil
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
