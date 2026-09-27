//go:build (darwin && (amd64 || arm64)) || (windows && amd64)

package document

import (
	"errors"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func newFormWriteBackend() formWriteBackend {
	return pdfCPUFormWriteBackend{policy: formWritePolicy{
		writerIdentity:   pdfcpuIdentity(),
		standardVerifier: verifyPDFiumFormCandidate,
	}}
}

func normalizePDFCPUHybridContext(*model.Context) error {
	return errors.New("hybrid form normalization is unavailable on this platform")
}
