package nodes

import "errors"

const (
	InvocationDispatchCommandDenied           = "COMMAND_DENIED"
	InvocationDispatchCommandUnavailable      = "COMMAND_UNAVAILABLE"
	InvocationDispatchExecutionFailed         = "EXECUTION_FAILED"
	InvocationDispatchFileNotFound            = "FILE_NOT_FOUND"
	InvocationDispatchBrowserSessionNotFound  = "SESSION_NOT_FOUND"
	InvocationDispatchBrowserNavigationFailed = "NAVIGATION_FAILED"
	InvocationDispatchBrowserCleanupRequired  = "CLEANUP_REQUIRED"
	InvocationDispatchIdempotencyConflict     = "IDEMPOTENCY_CONFLICT"
	InvocationDispatchInvalidPlan             = "INVALID_PLAN"
	InvocationDispatchNodeBusy                = "NODE_BUSY"
	InvocationDispatchCanceled                = "INVOCATION_CANCELED"
	InvocationDispatchUnknown                 = "INVOCATION_UNKNOWN"
	InvocationDispatchRejected                = "DISPATCH_REJECTED"
	InvocationDispatchCodingProjectNotFound   = "PROJECT_NOT_FOUND"
	InvocationDispatchCodingProjectStale      = "PROJECT_STALE"
	InvocationDispatchCodingModeDenied        = "MODE_DENIED"
	InvocationDispatchCodingTaskNotFound      = "TASK_NOT_FOUND"
	InvocationDispatchCodingProjectBusy       = "PROJECT_BUSY"
	InvocationDispatchCodingTaskConflict      = "TASK_CONFLICT"
	InvocationDispatchCodingTaskNotResumable  = "TASK_NOT_RESUMABLE"
	InvocationDispatchCodingTaskNotRunning    = "TASK_NOT_RUNNING"
	InvocationDispatchCodingHostUnavailable   = "HOST_UNAVAILABLE"
	InvocationDispatchCodingCommandTimeout    = "COMMAND_TIMEOUT"
	InvocationDispatchCodingOperationFailed   = "CODING_OPERATION_FAILED"
	InvocationDispatchCodingOutputLimit       = "OUTPUT_LIMIT_TOO_SMALL"
)

// InvocationDispatchError carries a bounded companion response classification
// across the WebSocket, gateway, and model-facing invocation boundary. Its
// Error output intentionally excludes the remote message and transport detail.
type InvocationDispatchError struct {
	code  string
	cause error
}

func NewInvocationDispatchError(code string, cause error) error {
	return &InvocationDispatchError{code: normalizeInvocationDispatchErrorCode(code), cause: cause}
}

func (err *InvocationDispatchError) Error() string {
	return "node invocation dispatch failed (" + err.code + ")"
}

func (err *InvocationDispatchError) Unwrap() error {
	return err.cause
}

func InvocationDispatchErrorCode(err error) (string, bool) {
	var dispatchErr *InvocationDispatchError
	if !errors.As(err, &dispatchErr) {
		return "", false
	}
	return dispatchErr.code, true
}

func normalizeInvocationDispatchErrorCode(code string) string {
	switch code {
	case InvocationDispatchCommandDenied,
		InvocationDispatchCommandUnavailable,
		InvocationDispatchExecutionFailed,
		InvocationDispatchFileNotFound,
		InvocationDispatchBrowserSessionNotFound,
		InvocationDispatchBrowserNavigationFailed,
		InvocationDispatchBrowserCleanupRequired,
		InvocationDispatchIdempotencyConflict,
		InvocationDispatchInvalidPlan,
		InvocationDispatchNodeBusy,
		InvocationDispatchCanceled,
		InvocationDispatchUnknown,
		InvocationDispatchCodingProjectNotFound,
		InvocationDispatchCodingProjectStale,
		InvocationDispatchCodingModeDenied,
		InvocationDispatchCodingTaskNotFound,
		InvocationDispatchCodingProjectBusy,
		InvocationDispatchCodingTaskConflict,
		InvocationDispatchCodingTaskNotResumable,
		InvocationDispatchCodingTaskNotRunning,
		InvocationDispatchCodingHostUnavailable,
		InvocationDispatchCodingCommandTimeout,
		InvocationDispatchCodingOperationFailed,
		InvocationDispatchCodingOutputLimit:
		return code
	default:
		return InvocationDispatchRejected
	}
}
