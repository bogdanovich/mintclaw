//go:build (linux && amd64) || (darwin && (amd64 || arm64))

package document

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"os"
	"strings"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	pdfcpuapi "github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

type pdfiumReadBackend struct {
	factory portablePDFiumFactory
}

func newPortableReadBackend(factory portablePDFiumFactory) readBackend {
	if factory == nil {
		return nil
	}
	return pdfiumReadBackend{factory: factory}
}

func (backend pdfiumReadBackend) Extract(data []byte, request WorkerRequest) backendRead {
	return backend.withDocument(data, func(instance pdfium.Pdfium, document references.FPDF_DOCUMENT) backendRead {
		var artifact bytes.Buffer
		encoder := json.NewEncoder(&artifact)
		pages := make([]PageTextFacts, 0, len(request.Read.Pages))
		remaining := request.Read.Limits.MaxCharacters
		truncated := false
		for index, page := range request.Read.Pages {
			response, err := instance.GetPageText(&requests.GetPageText{Page: requests.Page{
				ByIndex: &requests.PageByIndex{Document: document, Index: page - 1},
			}})
			if err != nil || response == nil {
				return failedRead(FailureExtractionLimit, "portable document text extraction failed")
			}
			text, characters, pageTruncated := boundExtractedPageText(
				strings.TrimRight(response.Text, "\f\r\n"),
				remaining,
				index < len(request.Read.Pages)-1,
			)
			remaining -= characters
			if err = encoder.Encode(struct {
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

		const name = "extracted-text.jsonl"
		if err := writePrivateArtifact(name, artifact.Bytes()); err != nil {
			return failedRead(FailureInternal, "document extraction artifact could not be written")
		}
		digest := sha256.Sum256(artifact.Bytes())
		return backendRead{
			State: StateSucceeded,
			Extraction: &ExtractionFacts{
				Backend:         pdfiumWASMIdentity(),
				SelectedPages:   append([]int(nil), request.Read.Pages...),
				Pages:           pages,
				TotalCharacters: totalCharacters,
				Truncated:       truncated,
			},
			Artifacts: []WorkerArtifact{{
				Name: name,
				Artifact: Artifact{
					Ref: workerArtifactRef(request.OperationID, name), Kind: "extracted_text",
					ContentType: "application/x-ndjson", Size: int64(artifact.Len()),
					SHA256: hex.EncodeToString(digest[:]), SourceSHA256: request.Input.SHA256,
					Pages: pagesCovered, Truncated: truncated,
				},
			}},
		}
	})
}

func (backend pdfiumReadBackend) Render(data []byte, request WorkerRequest) backendRead {
	dimensions, failure := portableRenderDimensions(data, request)
	if failure != nil {
		return backendRead{State: StateFailed, Failure: failure}
	}
	return backend.withDocument(data, func(instance pdfium.Pdfium, document references.FPDF_DOCUMENT) backendRead {
		artifacts := make([]WorkerArtifact, 0, len(request.Read.Pages))
		pages := make([]PageRenderFacts, 0, len(request.Read.Pages))
		var totalBytes, totalPixels int64
		for _, page := range request.Read.Pages {
			dimension, found := dimensions[page]
			if !found {
				return failedRead(FailureArtifactInvalid, "portable document page dimensions are unavailable")
			}
			width, height := dimension.width, dimension.height
			pixels := int64(width) * int64(height)
			if pixels > request.Read.Limits.MaxPixelsPerPage ||
				totalPixels+pixels > request.Read.Limits.MaxTotalPixels {
				return failedRead(FailureRenderLimit, "document page dimensions exceed the render limit")
			}

			response, err := instance.RenderPageInDPI(&requests.RenderPageInDPI{
				Page: requests.Page{ByIndex: &requests.PageByIndex{Document: document, Index: page - 1}},
				DPI:  request.Read.Limits.DPI, RenderForm: true, Document: &document,
			})
			if err != nil || response == nil {
				return failedRead(FailureRenderLimit, "portable document page rendering failed")
			}
			result := response.Result
			if result.RenderedImage == nil || result.Width != width || result.Height != height {
				response.Cleanup()
				return failedRead(FailureArtifactInvalid, "rendered page dimensions do not match the preflight")
			}
			encoded := newBoundedArtifactBuffer(request.Read.Limits.MaxArtifactBytes - totalBytes)
			encodeErr := png.Encode(encoded, result.RenderedImage)
			response.Cleanup()
			if encodeErr != nil || encoded.exceeded || encoded.Len() == 0 {
				return failedRead(FailureRenderLimit, "rendered page exceeded the artifact byte limit")
			}

			name := fmt.Sprintf("page-%04d.png", page)
			if err = writePrivateArtifact(name, encoded.Bytes()); err != nil {
				return failedRead(FailureInternal, "document rendering artifact could not be written")
			}
			digest := sha256.Sum256(encoded.Bytes())
			artifact := Artifact{
				Ref: workerArtifactRef(request.OperationID, name), Kind: "page_render", ContentType: "image/png",
				Size: int64(encoded.Len()), SHA256: hex.EncodeToString(digest[:]),
				SourceSHA256: request.Input.SHA256, Pages: []int{page}, Width: width, Height: height,
			}
			totalBytes += artifact.Size
			totalPixels += pixels
			artifacts = append(artifacts, WorkerArtifact{Name: name, Artifact: artifact})
			pages = append(pages, PageRenderFacts{Page: page, Width: width, Height: height})
		}
		return backendRead{
			State: StateSucceeded,
			Rendering: &RenderingFacts{
				Backend: pdfiumWASMIdentity(), SelectedPages: append([]int(nil), request.Read.Pages...),
				Pages: pages, DPI: request.Read.Limits.DPI, TotalPixels: totalPixels,
			},
			Artifacts: artifacts,
		}
	})
}

type portableRenderDimension struct {
	width  int
	height int
}

func portableRenderDimensions(data []byte, request WorkerRequest) (map[int]portableRenderDimension, *Failure) {
	configuration := newPDFCPUConfiguration(model.VALIDATE, defaultInspectionLimits())
	context, err := pdfcpuapi.ReadContext(bytes.NewReader(data), configuration)
	if err != nil || context == nil || context.XRefTable == nil {
		return nil, &Failure{Code: FailureMalformedPDF, Message: "portable document render preflight failed"}
	}
	root, err := context.Catalog()
	if err != nil || root == nil || boundCatalogMetadata(context, root, DefaultMaxContentBytes) != nil ||
		pdfcpuapi.ValidateContext(context) != nil {
		return nil, &Failure{Code: FailureMalformedPDF, Message: "portable document render preflight failed"}
	}
	boundaries, err := context.PageBoundaries(nil)
	if err != nil || len(boundaries) != context.PageCount {
		return nil, &Failure{
			Code:    FailureRenderLimit,
			Message: "portable document page dimensions exceed the render limit",
		}
	}
	dimensions := make(map[int]portableRenderDimension, len(request.Read.Pages))
	for _, page := range request.Read.Pages {
		if page < 1 || page > len(boundaries) {
			return nil, &Failure{
				Code:    FailureRenderLimit,
				Message: "portable document page dimensions exceed the render limit",
			}
		}
		crop := boundaries[page-1].CropBox()
		if crop == nil {
			return nil, &Failure{
				Code:    FailureRenderLimit,
				Message: "portable document page dimensions exceed the render limit",
			}
		}
		width, height, boundsFailure := boundedPageDimensions(
			crop.Width(),
			crop.Height(),
			request.Read.Limits.DPI,
			request.Read.Limits.MaxDimension,
			boundaries[page-1].Rot,
		)
		if boundsFailure != nil {
			if boundsFailure.Code == FailureArtifactInvalid {
				return nil, &Failure{
					Code: FailureRenderLimit, Message: "portable document page dimensions exceed the render limit",
				}
			}
			return nil, boundsFailure
		}
		dimensions[page] = portableRenderDimension{width: width, height: height}
	}
	return dimensions, nil
}

func (backend pdfiumReadBackend) withDocument(
	data []byte,
	operation func(pdfium.Pdfium, references.FPDF_DOCUMENT) backendRead,
) (result backendRead) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultReadWorkerTimeout)
	defer cancel()
	pool, err := backend.factory(ctx)
	if err != nil || pool == nil {
		return unavailablePortableReadBackend()
	}
	defer func() {
		if closeErr := pool.Close(); closeErr != nil {
			result = failedRead(FailureInternal, "portable document backend cleanup failed")
		}
	}()
	instance, err := pool.GetInstanceWithContext(ctx)
	if err != nil || instance == nil {
		return unavailablePortableReadBackend()
	}
	defer func() {
		if closeErr := instance.Close(); closeErr != nil {
			result = failedRead(FailureInternal, "portable document backend cleanup failed")
		}
	}()
	document, err := instance.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil || document == nil {
		return failedRead(FailureMalformedPDF, "portable document backend rejected the inspected input")
	}
	defer func() {
		if _, closeErr := instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{
			Document: document.Document,
		}); closeErr != nil {
			result = failedRead(FailureInternal, "portable document backend cleanup failed")
		}
	}()
	pageCount, err := instance.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: document.Document})
	if err != nil || pageCount == nil || pageCount.PageCount < 1 || pageCount.PageCount > DefaultMaxPages {
		return failedRead(FailureMalformedPDF, "portable document page count is invalid")
	}
	return operation(instance, document.Document)
}

func unavailablePortableReadBackend() backendRead {
	return backendRead{
		State: StateUnavailable,
		Failure: &Failure{
			Code: FailureBackendUnavailable, Message: "portable document read backend is unavailable",
		},
	}
}

func writePrivateArtifact(name string, data []byte) error {
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	written, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written != len(data) {
		return errors.New("short artifact write")
	}
	return nil
}

type boundedArtifactBuffer struct {
	bytes.Buffer
	maximum  int64
	exceeded bool
}

func newBoundedArtifactBuffer(maximum int64) *boundedArtifactBuffer {
	return &boundedArtifactBuffer{maximum: maximum}
}

func (buffer *boundedArtifactBuffer) Write(data []byte) (int, error) {
	if buffer.maximum <= int64(buffer.Len()) || int64(len(data)) > buffer.maximum-int64(buffer.Len()) {
		buffer.exceeded = true
		return 0, errors.New("document artifact byte limit exceeded")
	}
	return buffer.Buffer.Write(data)
}
