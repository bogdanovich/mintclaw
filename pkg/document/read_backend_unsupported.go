//go:build !linux || !amd64

package document

func newReadBackend() readBackend { return nil }
