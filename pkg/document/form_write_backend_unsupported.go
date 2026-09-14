//go:build !linux || !amd64

package document

type unavailableFormWriteBackend struct{}

func newFormWriteBackend() formWriteBackend { return unavailableFormWriteBackend{} }

func (unavailableFormWriteBackend) Fill(_ []byte, _ WorkerRequest) backendFormWrite {
	return failedFormWrite(
		StateUnavailable,
		FailureUnsupportedPlatform,
		"document form writing is initially admitted only on linux/amd64",
	)
}
