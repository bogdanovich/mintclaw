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
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

var renderedArtifactName = regexp.MustCompile(`^page-[0-9]{4}\.png$`)

type extractedPageArtifact struct {
	Page      int    `json:"page"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

func adoptWorkerArtifacts(
	snapshot *Snapshot,
	workerScratch string,
	request WorkerRequest,
	result *WorkerResult,
) error {
	if snapshot == nil || result == nil || request.Read == nil || len(result.Artifacts) == 0 {
		return errors.New("document worker artifact set is invalid")
	}
	entries, err := os.ReadDir(workerScratch)
	if err != nil || len(entries) != len(result.Artifacts) {
		return errors.New("document worker artifact set is incomplete or undeclared")
	}
	declared := make(map[string]WorkerArtifact, len(result.Artifacts))
	for _, artifact := range result.Artifacts {
		if _, exists := declared[artifact.Name]; exists || !validWorkerArtifactName(request.Operation, artifact.Name) {
			return errors.New("document worker artifact name is invalid")
		}
		declared[artifact.Name] = artifact
	}
	for _, entry := range entries {
		artifact, ok := declared[entry.Name()]
		if !ok || entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return errors.New("document worker artifact is undeclared or not regular")
		}
		if err = validateWorkerArtifactFile(workerScratch, request, result, artifact); err != nil {
			return err
		}
	}

	destination := filepath.Join(snapshot.dir, "artifacts")
	if err = os.Mkdir(destination, 0o700); err != nil {
		return fmt.Errorf("create document artifact directory: %w", err)
	}
	adopted := false
	defer func() {
		if !adopted {
			_ = os.RemoveAll(destination)
		}
	}()
	paths := make(map[string]string, len(result.Artifacts))
	for _, artifact := range result.Artifacts {
		source := filepath.Join(workerScratch, artifact.Name)
		target := filepath.Join(destination, artifact.Name)
		if err = os.Rename(source, target); err != nil {
			return fmt.Errorf("adopt document artifact: %w", err)
		}
		if err = os.Chmod(target, 0o400); err != nil {
			return fmt.Errorf("protect document artifact: %w", err)
		}
		paths[artifact.Artifact.Ref] = target
	}
	snapshot.artifactPaths = paths
	adopted = true
	return nil
}

func validWorkerArtifactName(operation, name string) bool {
	switch operation {
	case workerOperationExtract:
		return name == "extracted-text.jsonl"
	case workerOperationRender:
		return renderedArtifactName.MatchString(name)
	default:
		return false
	}
}

func validateWorkerArtifactFile(
	root string,
	request WorkerRequest,
	result *WorkerResult,
	worker WorkerArtifact,
) error {
	path := filepath.Join(root, worker.Name)
	file, err := openSourceNoFollow(path)
	if err != nil {
		return errors.New("document worker artifact cannot be opened safely")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() != worker.Artifact.Size {
		return errors.New("document worker artifact size is invalid")
	}
	data, err := io.ReadAll(io.LimitReader(file, request.Read.Limits.MaxArtifactBytes+1))
	if err != nil || int64(len(data)) != info.Size() || int64(len(data)) > request.Read.Limits.MaxArtifactBytes {
		return errors.New("document worker artifact exceeds its byte limit")
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != worker.Artifact.SHA256 {
		return errors.New("document worker artifact digest is invalid")
	}
	switch request.Operation {
	case workerOperationExtract:
		return validateExtractedArtifact(data, result, worker.Artifact)
	case workerOperationRender:
		return validateRenderedArtifact(data, result, worker.Artifact, request.Read.Limits)
	default:
		return errors.New("document worker artifact operation is invalid")
	}
}

func validateExtractedArtifact(data []byte, result *WorkerResult, artifact Artifact) error {
	if result.Extraction == nil || !utf8.Valid(data) {
		return errors.New("document extracted text artifact is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	for index, facts := range result.Extraction.Pages {
		var page extractedPageArtifact
		if err := decoder.Decode(&page); err != nil || page.Page != facts.Page ||
			utf8.RuneCountInString(page.Text) != facts.Characters || page.Truncated != facts.Truncated ||
			artifact.Pages[index] != page.Page {
			return errors.New("document extracted text artifact does not match its descriptor")
		}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("document extracted text artifact contains unexpected records")
	}
	return nil
}

func validateRenderedArtifact(data []byte, result *WorkerResult, artifact Artifact, limits ReadLimits) error {
	if result.Rendering == nil || len(artifact.Pages) != 1 || len(data) < 8 ||
		!bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")) {
		return errors.New("document rendered page artifact is invalid")
	}
	configuration, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || configuration.Width != artifact.Width || configuration.Height != artifact.Height ||
		configuration.Width <= 0 || configuration.Height <= 0 ||
		configuration.Width > limits.MaxDimension || configuration.Height > limits.MaxDimension ||
		int64(configuration.Width)*int64(configuration.Height) > limits.MaxPixelsPerPage {
		return errors.New("document rendered page dimensions are invalid")
	}
	reader := bytes.NewReader(data)
	page, err := png.Decode(reader)
	if err != nil || reader.Len() != 0 || page.Bounds().Dx() != artifact.Width ||
		page.Bounds().Dy() != artifact.Height {
		return errors.New("document rendered page dimensions are invalid")
	}
	return nil
}

func validExtractionFacts(request WorkerRequest, facts ExtractionFacts, artifacts []WorkerArtifact) bool {
	if request.Read == nil || !validPopplerIdentity(facts.Backend) ||
		!equalPages(facts.SelectedPages, request.Read.Pages) || len(facts.Pages) == 0 || len(artifacts) != 1 {
		return false
	}
	characters := 0
	pages := make([]int, 0, len(facts.Pages))
	previous := 0
	truncated := false
	for index, page := range facts.Pages {
		if index >= len(request.Read.Pages) || page.Page != request.Read.Pages[index] || page.Page <= previous ||
			page.Characters < 0 || page.Characters > request.Read.Limits.MaxCharacters || truncated {
			return false
		}
		previous = page.Page
		characters += page.Characters
		pages = append(pages, page.Page)
		truncated = page.Truncated
	}
	if characters != facts.TotalCharacters || characters > request.Read.Limits.MaxCharacters ||
		facts.Truncated != truncated || facts.Truncated != artifacts[0].Artifact.Truncated ||
		(!facts.Truncated && len(facts.Pages) != len(request.Read.Pages)) {
		return false
	}
	return validArtifactDescriptor(
		request,
		artifacts[0],
		"extracted-text.jsonl",
		"extracted_text",
		"application/x-ndjson",
		pages,
	)
}

func validRenderingFacts(request WorkerRequest, facts RenderingFacts, artifacts []WorkerArtifact) bool {
	if request.Read == nil || !validPopplerIdentity(facts.Backend) ||
		!equalPages(facts.SelectedPages, request.Read.Pages) || len(facts.Pages) != len(request.Read.Pages) ||
		len(artifacts) != len(request.Read.Pages) || facts.DPI != request.Read.Limits.DPI {
		return false
	}
	var totalPixels, totalBytes int64
	for index, page := range facts.Pages {
		if page.Page != request.Read.Pages[index] || page.Width <= 0 || page.Height <= 0 ||
			page.Width > request.Read.Limits.MaxDimension || page.Height > request.Read.Limits.MaxDimension {
			return false
		}
		pixels := int64(page.Width) * int64(page.Height)
		if pixels > request.Read.Limits.MaxPixelsPerPage {
			return false
		}
		totalPixels += pixels
		totalBytes += artifacts[index].Artifact.Size
		name := fmt.Sprintf("page-%04d.png", page.Page)
		if !validArtifactDescriptor(
			request,
			artifacts[index],
			name,
			"page_render",
			"image/png",
			[]int{page.Page},
		) || artifacts[index].Artifact.Width != page.Width || artifacts[index].Artifact.Height != page.Height {
			return false
		}
	}
	return totalPixels == facts.TotalPixels && totalPixels <= request.Read.Limits.MaxTotalPixels &&
		totalBytes <= request.Read.Limits.MaxArtifactBytes
}

func validArtifactDescriptor(
	request WorkerRequest,
	worker WorkerArtifact,
	name string,
	kind string,
	contentType string,
	pages []int,
) bool {
	digest, err := hex.DecodeString(worker.Artifact.SHA256)
	return err == nil && len(digest) == sha256.Size &&
		worker.Artifact.SHA256 == strings.ToLower(worker.Artifact.SHA256) &&
		worker.Name == name &&
		worker.Artifact.Ref == workerArtifactRef(request.OperationID, name) &&
		worker.Artifact.Kind == kind &&
		worker.Artifact.ContentType == contentType &&
		worker.Artifact.Size > 0 &&
		worker.Artifact.Size <= request.Read.Limits.MaxArtifactBytes &&
		worker.Artifact.SourceSHA256 == request.Input.SHA256 &&
		equalPages(worker.Artifact.Pages, pages) &&
		((kind == "extracted_text" && worker.Artifact.Width == 0 && worker.Artifact.Height == 0) ||
			(kind == "page_render" && worker.Artifact.Width > 0 && worker.Artifact.Height > 0 && !worker.Artifact.Truncated))
}

func validPopplerIdentity(identity BackendIdentity) bool {
	return identity.Name == PopplerBackendName && identity.Version == PopplerBackendVersion &&
		identity.Role == "production" && identity.IsolationMode == "one_shot_child"
}

func equalPages(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
