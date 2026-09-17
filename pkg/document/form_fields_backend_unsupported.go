//go:build !linux || !amd64

package document

func newFormFieldsBackend() formFieldsBackend { return nil }
