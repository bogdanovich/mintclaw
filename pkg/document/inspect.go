package document

import (
	"context"
	"runtime"

	"github.com/bogdanovich/mintclaw/pkg/media"
)

const operationInspect = "inspect"

// Inspect acquires a local operator path through PDF0A and inspects only its immutable snapshot.
func Inspect(ctx context.Context, inputPath string, options AcquireOptions) (*Snapshot, Report) {
	return inspectWithWorker(
		ctx,
		inputPath,
		options,
		runtime.GOOS,
		runtime.GOARCH,
		NewProcessInspector(),
	)
}

// InspectMedia admits one exact owner-bound media reference and inspects only its immutable snapshot.
func InspectMedia(
	ctx context.Context,
	resolver OwnedMediaResolver,
	ref string,
	owner media.MediaOwner,
	options AcquireOptions,
) (*Snapshot, Report) {
	return inspectMediaWithWorker(
		ctx,
		resolver,
		ref,
		owner,
		options,
		runtime.GOOS,
		runtime.GOARCH,
		NewProcessInspector(),
	)
}

func inspectWithWorker(
	ctx context.Context,
	inputPath string,
	options AcquireOptions,
	goos string,
	goarch string,
	worker InspectorWorker,
) (*Snapshot, Report) {
	snapshot, report := acquireOperationForPlatform(
		ctx,
		inputPath,
		options,
		goos,
		goarch,
		operationInspect,
	)
	return inspectAcquiredSnapshot(ctx, snapshot, report, worker)
}

func inspectMediaWithWorker(
	ctx context.Context,
	resolver OwnedMediaResolver,
	ref string,
	owner media.MediaOwner,
	options AcquireOptions,
	goos string,
	goarch string,
	worker InspectorWorker,
) (*Snapshot, Report) {
	snapshot, report := acquireMediaOperationForPlatform(
		ctx,
		resolver,
		ref,
		owner,
		options,
		goos,
		goarch,
		operationInspect,
	)
	return inspectAcquiredSnapshot(ctx, snapshot, report, worker)
}

func inspectAcquiredSnapshot(
	ctx context.Context,
	snapshot *Snapshot,
	report Report,
	worker InspectorWorker,
) (*Snapshot, Report) {
	if snapshot == nil || report.State != StateSucceeded || report.Input == nil {
		return snapshot, report
	}
	if worker == nil {
		return cleanupInspectionFailure(
			snapshot,
			failInspectionReport(
				report,
				StateUnavailable,
				FailureBackendUnavailable,
				"document inspection backend is unavailable",
			),
		)
	}
	result := worker.Inspect(ctx, snapshot, *report.Input, report.Limits)
	expectedInput := newWorkerOperationRequest(*report.Input, report.Limits, workerOperationInspect).Input
	if result.Input != nil && *result.Input != expectedInput {
		result = workerFailure(
			report.OperationID,
			StateFailed,
			FailureWorkerProtocol,
			"document worker returned an invalid response",
		)
	}
	if result.State == StateSucceeded && result.Input != nil && result.Inspection != nil &&
		result.Failure == nil && validInspectionFacts(*result.Inspection) {
		report.State = StateSucceeded
		report.Inspection = result.Inspection
		report.Failure = nil
		return snapshot, report
	}
	if result.State == StateSucceeded {
		result = workerFailure(
			report.OperationID,
			StateFailed,
			FailureWorkerProtocol,
			"document worker returned an invalid response",
		)
	}
	state := result.State
	if state == "" {
		state = StateFailed
	}
	failure := safeWorkerFailure(result)
	report = failInspectionReport(report, state, failure.Code, failure.Message)
	if result.Inspection != nil && validInspectionFacts(*result.Inspection) {
		report.Inspection = result.Inspection
	}
	return cleanupInspectionFailure(snapshot, report)
}

func failInspectionReport(report Report, state State, code FailureCode, message string) Report {
	report.State = state
	report.Failure = &Failure{Code: code, Message: message}
	return report
}

func cleanupInspectionFailure(snapshot *Snapshot, report Report) (*Snapshot, Report) {
	if err := snapshot.Close(); err != nil {
		return snapshot, failInspectionReport(
			report,
			StateFailed,
			FailureInternal,
			"protected scratch cleanup failed",
		)
	}
	return nil, report
}
