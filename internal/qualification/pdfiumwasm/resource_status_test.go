//go:build linux || darwin

package pdfiumwasm_test

import (
	"strings"
	"testing"
)

func TestPeakResidentBytesFromStatus(t *testing.T) {
	peak, err := peakResidentBytesFromStatus([]byte("Name:\tcandidate.test\nVmHWM:\t  1536 kB\nVmRSS:\t100 kB\n"))
	if err != nil || peak != 1536*1024 {
		t.Fatalf("peak = %d, err = %v", peak, err)
	}
	for name, status := range map[string]string{
		"missing":   "VmRSS: 123 kB\n",
		"duplicate": "VmHWM: 123 kB\nVmHWM: 124 kB\n",
		"unit":      "VmHWM: 123 MB\n",
		"extra":     "VmHWM: 123 kB extra\n",
		"negative":  "VmHWM: -1 kB\n",
		"zero":      "VmHWM: 0 kB\n",
		"syntax":    "VmHWM: unknown kB\n",
		"overflow":  "VmHWM: 9223372036854775807 kB\n",
		"bound":     strings.Repeat("x", 64*1024+1),
	} {
		t.Run(name, func(t *testing.T) {
			if actual, parseErr := peakResidentBytesFromStatus([]byte(status)); parseErr == nil || actual != 0 {
				t.Fatalf("invalid status accepted: peak=%d err=%v", actual, parseErr)
			}
		})
	}
}
