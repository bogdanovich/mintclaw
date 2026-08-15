package nodes

import (
	"errors"
	"testing"
)

func TestInvocationDispatchErrorBoundsRemoteClassification(t *testing.T) {
	cause := errors.New("private remote rejection detail")
	err := NewInvocationDispatchError(InvocationDispatchCommandDenied, cause)
	code, classified := InvocationDispatchErrorCode(err)
	if !classified || code != InvocationDispatchCommandDenied {
		t.Fatalf("InvocationDispatchErrorCode() = %q, %v", code, classified)
	}
	if err.Error() != "node invocation dispatch failed (COMMAND_DENIED)" {
		t.Fatalf("Error() = %q", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatal("dispatch error did not retain its internal cause")
	}

	notFound := NewInvocationDispatchError(InvocationDispatchFileNotFound, cause)
	code, classified = InvocationDispatchErrorCode(notFound)
	if !classified || code != InvocationDispatchFileNotFound {
		t.Fatalf("file-not-found dispatch code = %q, %v", code, classified)
	}
	if notFound.Error() != "node invocation dispatch failed (FILE_NOT_FOUND)" {
		t.Fatalf("file-not-found Error() = %q", notFound.Error())
	}

	browserSessionNotFound := NewInvocationDispatchError(
		InvocationDispatchBrowserSessionNotFound,
		cause,
	)
	code, classified = InvocationDispatchErrorCode(browserSessionNotFound)
	if !classified || code != InvocationDispatchBrowserSessionNotFound {
		t.Fatalf("browser-session-not-found dispatch code = %q, %v", code, classified)
	}

	unknown := NewInvocationDispatchError("PRIVATE_REMOTE_CODE", cause)
	code, classified = InvocationDispatchErrorCode(unknown)
	if !classified || code != InvocationDispatchRejected {
		t.Fatalf("unknown dispatch code = %q, %v", code, classified)
	}
}
