//go:build linux && amd64

package document

import "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"

func newPDFCPUConfiguration(command model.CommandMode, limits Limits) *model.Configuration {
	configuration := model.NewDefaultConfiguration()
	configuration.Cmd = command
	configuration.ValidationMode = model.ValidationRelaxed
	configuration.Limits.MaxStreamBytes = limits.MaxInputBytes
	configuration.Limits.MaxDecodeBytes = limits.MaxContentBytes
	configuration.Limits.MaxImageBytes = limits.MaxContentBytes
	configuration.Limits.MaxImagePixels = limits.MaxContentBytes / 4
	configuration.Limits.MaxObjectCount = limits.MaxObjects
	configuration.Limits.MaxObjectStreamCount = limits.MaxObjects
	configuration.Limits.MaxObjectStreamFirst = limits.MaxContentBytes
	configuration.Limits.MaxXRefEntries = limits.MaxObjects
	configuration.Limits.MaxRecursionDepth = limits.MaxRecursionDepth
	return configuration
}
