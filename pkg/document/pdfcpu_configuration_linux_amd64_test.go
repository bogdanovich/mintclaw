//go:build linux && amd64

package document

import (
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func TestNewPDFCPUConfigurationPreservesCommandAndResourcePolicy(t *testing.T) {
	limits := Limits{
		MaxInputBytes:     101,
		MaxPages:          202,
		MaxContentBytes:   404,
		MaxObjects:        303,
		MaxRecursionDepth: 17,
	}
	commands := []struct {
		name    string
		command model.CommandMode
	}{
		{name: "inspection", command: model.VALIDATE},
		{name: "form fields", command: model.LISTFORMFIELDS},
	}
	for _, test := range commands {
		t.Run(test.name, func(t *testing.T) {
			configuration := newPDFCPUConfiguration(test.command, limits)
			if configuration.Cmd != test.command {
				t.Errorf("command = %v, want %v", configuration.Cmd, test.command)
			}
			if configuration.ValidationMode != model.ValidationRelaxed {
				t.Errorf("validation mode = %v, want relaxed", configuration.ValidationMode)
			}
			configuredLimits := []struct {
				name string
				got  int64
				want int64
			}{
				{name: "stream bytes", got: configuration.Limits.MaxStreamBytes, want: limits.MaxInputBytes},
				{name: "decode bytes", got: configuration.Limits.MaxDecodeBytes, want: limits.MaxContentBytes},
				{name: "image bytes", got: configuration.Limits.MaxImageBytes, want: limits.MaxContentBytes},
				{name: "image pixels", got: configuration.Limits.MaxImagePixels, want: limits.MaxContentBytes / 4},
				{name: "objects", got: int64(configuration.Limits.MaxObjectCount), want: int64(limits.MaxObjects)},
				{
					name: "object streams", got: int64(configuration.Limits.MaxObjectStreamCount),
					want: int64(limits.MaxObjects),
				},
				{
					name: "object stream first", got: configuration.Limits.MaxObjectStreamFirst,
					want: limits.MaxContentBytes,
				},
				{name: "xref entries", got: int64(configuration.Limits.MaxXRefEntries), want: int64(limits.MaxObjects)},
				{
					name: "recursion depth", got: int64(configuration.Limits.MaxRecursionDepth),
					want: int64(limits.MaxRecursionDepth),
				},
			}
			for _, limit := range configuredLimits {
				if limit.got != limit.want {
					t.Errorf("%s = %d, want %d", limit.name, limit.got, limit.want)
				}
			}
		})
	}
}
