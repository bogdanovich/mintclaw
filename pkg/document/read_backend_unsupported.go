//go:build !linux || !amd64

package document

func newNativeReadBackend() readBackend { return nil }
