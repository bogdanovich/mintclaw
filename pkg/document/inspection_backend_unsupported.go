//go:build !linux || !amd64

package document

import "io"

func newInspectionBackend() inspectionBackend {
	return unsupportedInspectionBackend{}
}

type unsupportedInspectionBackend struct{}

func (unsupportedInspectionBackend) Inspect(_ io.ReadSeeker, _ Limits) backendInspection {
	return backendInspection{
		State: StateUnavailable,
		Failure: &Failure{
			Code:    FailureBackendUnavailable,
			Message: "document inspection is admitted only on linux/amd64",
		},
	}
}
