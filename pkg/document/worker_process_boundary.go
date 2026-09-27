//go:build (linux && amd64) || (darwin && (amd64 || arm64)) || (windows && amd64)

package document

type documentWorkerProcessBoundary interface {
	started() error
	terminate() error
	close() error
}
