//go:build linux && amd64

package document

func newFormWriteBackend() formWriteBackend {
	return pdfCPUFormWriteBackend{policy: formWritePolicy{
		writerIdentity:      pdfcpuIdentityWithIsolation(NativeBackendIsolationMode),
		standardVerifier:    verifyPDFiumFormCandidate,
		independentVerifier: verifyPopplerFormCandidate,
		hybridVerifier:      verifyPopplerFormCandidate,
		hybridFinalizer:     flattenAndVerifyPDFCPUHybridCandidate,
	}}
}
