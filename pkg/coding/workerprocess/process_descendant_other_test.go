//go:build !windows && !linux && !darwin

package workerprocess

func maybeStartProcessTestDescendant() error { return nil }
