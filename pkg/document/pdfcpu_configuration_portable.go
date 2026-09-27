package document

import (
	"sync"

	pdfcpuapi "github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

var disablePDFCPUConfigDirectory sync.Once

func newPDFCPUConfiguration(command model.CommandMode, limits Limits) *model.Configuration {
	disablePDFCPUConfigDirectory.Do(pdfcpuapi.DisableConfigDir)
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
