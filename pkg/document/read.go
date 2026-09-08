package document

import (
	"context"
	"runtime"
	"strings"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/media"
)

const (
	operationExtract = "extract"
	operationRender  = "render"
)

type ReadOptions struct {
	Acquire AcquireOptions
	Pages   []int
	Limits  ReadLimits
}

// Extract acquires, inspects, and extracts bounded page text from one immutable local PDF.
func Extract(ctx context.Context, inputPath string, options ReadOptions) (*Snapshot, Report) {
	if report, unavailable := unavailableReadReport(operationExtract, options.Acquire); unavailable {
		return nil, report
	}
	return readWithWorkers(
		ctx,
		inputPath,
		options,
		runtime.GOOS,
		runtime.GOARCH,
		operationExtract,
		NewProcessInspector(),
		NewProcessExtractor(),
		nil,
	)
}

// Render acquires, inspects, and renders bounded pages from one immutable local PDF.
func Render(ctx context.Context, inputPath string, options ReadOptions) (*Snapshot, Report) {
	if report, unavailable := unavailableReadReport(operationRender, options.Acquire); unavailable {
		return nil, report
	}
	return readWithWorkers(
		ctx,
		inputPath,
		options,
		runtime.GOOS,
		runtime.GOARCH,
		operationRender,
		NewProcessInspector(),
		nil,
		NewProcessRenderer(),
	)
}

// ExtractMedia applies the same extraction path to one exact authority-bound media ref.
func ExtractMedia(
	ctx context.Context,
	resolver OwnedMediaResolver,
	ref string,
	owner media.MediaOwner,
	options ReadOptions,
) (*Snapshot, Report) {
	if report, unavailable := unavailableReadReport(operationExtract, options.Acquire); unavailable {
		return nil, report
	}
	return readMediaWithWorkers(
		ctx,
		resolver,
		ref,
		owner,
		options,
		runtime.GOOS,
		runtime.GOARCH,
		operationExtract,
		NewProcessInspector(),
		NewProcessExtractor(),
		nil,
	)
}

// RenderMedia applies the same rendering path to one exact authority-bound media ref.
func RenderMedia(
	ctx context.Context,
	resolver OwnedMediaResolver,
	ref string,
	owner media.MediaOwner,
	options ReadOptions,
) (*Snapshot, Report) {
	if report, unavailable := unavailableReadReport(operationRender, options.Acquire); unavailable {
		return nil, report
	}
	return readMediaWithWorkers(
		ctx,
		resolver,
		ref,
		owner,
		options,
		runtime.GOOS,
		runtime.GOARCH,
		operationRender,
		NewProcessInspector(),
		nil,
		NewProcessRenderer(),
	)
}

func unavailableReadReport(operation string, options AcquireOptions) (Report, bool) {
	capability := Capabilities().Operations[operation]
	if capability.State == CapabilitySupported {
		return Report{}, false
	}
	maximum := options.MaxBytes
	if maximum <= 0 || maximum > DefaultMaxInputBytes {
		maximum = DefaultMaxInputBytes
	}
	operationID := "document_operation_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	report := newOperationReport(operationID, operation, maximum)
	code := FailureUnsupportedPlatform
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		code = FailureBackendUnavailable
	}
	return failReadReport(report, StateUnavailable, code, capability.Reason), true
}

func readWithWorkers(
	ctx context.Context,
	inputPath string,
	options ReadOptions,
	goos string,
	goarch string,
	operation string,
	inspector InspectorWorker,
	extractor ExtractorWorker,
	renderer RendererWorker,
) (*Snapshot, Report) {
	snapshot, report := acquireOperationForPlatform(
		ctx,
		inputPath,
		options.Acquire,
		goos,
		goarch,
		operation,
	)
	return readAcquiredSnapshot(ctx, snapshot, report, options, operation, inspector, extractor, renderer)
}

func readMediaWithWorkers(
	ctx context.Context,
	resolver OwnedMediaResolver,
	ref string,
	owner media.MediaOwner,
	options ReadOptions,
	goos string,
	goarch string,
	operation string,
	inspector InspectorWorker,
	extractor ExtractorWorker,
	renderer RendererWorker,
) (*Snapshot, Report) {
	snapshot, report := acquireMediaOperationForPlatform(
		ctx,
		resolver,
		ref,
		owner,
		options.Acquire,
		goos,
		goarch,
		operation,
	)
	return readAcquiredSnapshot(ctx, snapshot, report, options, operation, inspector, extractor, renderer)
}

func readAcquiredSnapshot(
	ctx context.Context,
	snapshot *Snapshot,
	report Report,
	options ReadOptions,
	operation string,
	inspector InspectorWorker,
	extractor ExtractorWorker,
	renderer RendererWorker,
) (*Snapshot, Report) {
	if snapshot == nil || report.State != StateSucceeded || report.Input == nil {
		return snapshot, report
	}
	if inspector == nil || (operation == operationExtract && extractor == nil) ||
		(operation == operationRender && renderer == nil) {
		return cleanupReadFailure(snapshot, failReadReport(
			report,
			StateUnavailable,
			FailureBackendUnavailable,
			"document read backend is unavailable",
		))
	}
	inspection := inspector.Inspect(ctx, snapshot, *report.Input, report.Limits)
	expectedInspectionInput := newWorkerOperationRequest(*report.Input, report.Limits, workerOperationInspect).Input
	if inspection.Input != nil && *inspection.Input != expectedInspectionInput {
		inspection = workerFailure(
			report.OperationID,
			StateFailed,
			FailureWorkerProtocol,
			"document worker returned an invalid response",
		)
	}
	if inspection.State != StateSucceeded || inspection.Input == nil || inspection.Inspection == nil ||
		inspection.Failure != nil || !validInspectionFacts(*inspection.Inspection) {
		if inspection.State == StateSucceeded {
			inspection = workerFailure(
				report.OperationID,
				StateFailed,
				FailureWorkerProtocol,
				"document worker returned an invalid response",
			)
		}
		if inspection.Inspection != nil && validInspectionFacts(*inspection.Inspection) {
			report.Inspection = inspection.Inspection
		}
		failure := safeWorkerFailure(inspection)
		state := inspection.State
		if state == "" {
			state = StateFailed
		}
		return cleanupReadFailure(snapshot, failReadReport(report, state, failure.Code, failure.Message))
	}
	report.Inspection = inspection.Inspection
	if operation == operationExtract && inspection.Inspection.ExtractableText.State == FactAbsent {
		return cleanupReadFailure(snapshot, failReadReport(
			report,
			StateUnsupported,
			FailureTextUnavailable,
			"selected document pages have no extractable text",
		))
	}
	if operation == operationRender && inspection.Inspection.XFA.State == FactPresent {
		return cleanupReadFailure(snapshot, failReadReport(
			report,
			StateUnsupported,
			FailureUnsupportedFeature,
			"XFA rendering is not supported in PDF1A",
		))
	}
	limits, ok := normalizeReadLimits(operation, options.Limits)
	if !ok {
		return cleanupReadFailure(snapshot, failReadReport(
			report,
			StateFailed,
			FailureInvalidInput,
			"document read limits are invalid",
		))
	}
	pageCount := 0
	if inspection.Inspection.PageCount.Value != nil {
		pageCount = *inspection.Inspection.PageCount.Value
	}
	pages, ok := normalizePageSelection(options.Pages, pageCount, limits.MaxPages)
	if !ok {
		return cleanupReadFailure(snapshot, failReadReport(
			report,
			StateFailed,
			FailureInvalidPageSelection,
			"document page selection is invalid",
		))
	}
	read := WorkerReadRequest{Pages: pages, Limits: limits}
	report.ReadLimits = &limits
	var result WorkerResult
	if operation == operationExtract {
		result = extractor.Extract(ctx, snapshot, *report.Input, report.Limits, read)
	} else {
		result = renderer.Render(ctx, snapshot, *report.Input, report.Limits, read)
	}
	if result.State != StateSucceeded || result.Input == nil || result.Failure != nil ||
		*result.Input != newReadWorkerRequest(*report.Input, report.Limits, operation, read).Input ||
		!validWorkerSuccessPayload(newReadWorkerRequest(*report.Input, report.Limits, operation, read), result) {
		if result.State == StateSucceeded {
			result = workerFailure(
				report.OperationID,
				StateFailed,
				FailureWorkerProtocol,
				"document worker returned an invalid response",
			)
		}
		failure := safeWorkerFailure(result)
		state := result.State
		if state == "" {
			state = StateFailed
		}
		return cleanupReadFailure(snapshot, failReadReport(report, state, failure.Code, failure.Message))
	}
	report.State = StateSucceeded
	report.Extraction = result.Extraction
	report.Rendering = result.Rendering
	report.Artifacts = make([]Artifact, 0, len(result.Artifacts))
	for _, artifact := range result.Artifacts {
		report.Artifacts = append(report.Artifacts, artifact.Artifact)
	}
	report.Failure = nil
	return snapshot, report
}

func newReadWorkerRequest(input DocumentRef, limits Limits, operation string, read WorkerReadRequest) WorkerRequest {
	request := newWorkerOperationRequest(input, limits, operation)
	request.Read = &read
	return request
}

func normalizeReadLimits(operation string, supplied ReadLimits) (ReadLimits, bool) {
	limits := defaultReadLimits(operation)
	if supplied == (ReadLimits{}) {
		return limits, true
	}
	if supplied.MaxPages != 0 {
		limits.MaxPages = supplied.MaxPages
	}
	if supplied.MaxCharacters != 0 {
		limits.MaxCharacters = supplied.MaxCharacters
	}
	if supplied.DPI != 0 {
		limits.DPI = supplied.DPI
	}
	if supplied.MaxDimension != 0 {
		limits.MaxDimension = supplied.MaxDimension
	}
	if supplied.MaxPixelsPerPage != 0 {
		limits.MaxPixelsPerPage = supplied.MaxPixelsPerPage
	}
	if supplied.MaxTotalPixels != 0 {
		limits.MaxTotalPixels = supplied.MaxTotalPixels
	}
	if supplied.MaxArtifactBytes != 0 {
		limits.MaxArtifactBytes = supplied.MaxArtifactBytes
	}
	return limits, validWorkerReadRequest(operation, WorkerReadRequest{Pages: []int{1}, Limits: limits})
}

func normalizePageSelection(pages []int, pageCount, maximum int) ([]int, bool) {
	if pageCount <= 0 || maximum <= 0 {
		return nil, false
	}
	if len(pages) == 0 {
		if pageCount > maximum {
			return nil, false
		}
		pages = make([]int, pageCount)
		for index := range pages {
			pages[index] = index + 1
		}
	}
	if len(pages) > maximum {
		return nil, false
	}
	normalized := append([]int(nil), pages...)
	previous := 0
	for _, page := range normalized {
		if page <= previous || page > pageCount {
			return nil, false
		}
		previous = page
	}
	return normalized, true
}

func failReadReport(report Report, state State, code FailureCode, message string) Report {
	report.State = state
	report.Extraction = nil
	report.Rendering = nil
	report.Artifacts = nil
	report.Failure = &Failure{Code: code, Message: message}
	return report
}

func cleanupReadFailure(snapshot *Snapshot, report Report) (*Snapshot, Report) {
	if err := snapshot.Close(); err != nil {
		return snapshot, failReadReport(report, StateFailed, FailureInternal, "protected scratch cleanup failed")
	}
	return nil, report
}
