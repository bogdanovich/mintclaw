//go:build !((linux && amd64) || (darwin && (amd64 || arm64)) || (windows && amd64))

package document

func newPortableReadBackend(portablePDFiumFactory) readBackend { return nil }
