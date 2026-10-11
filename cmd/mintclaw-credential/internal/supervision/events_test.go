package supervision

import (
	"strings"
	"testing"
)

func TestPopulatedRequiresUnambiguousKernelEvidence(t *testing.T) {
	for _, test := range []struct {
		data string
		live bool
	}{
		{"populated 0\nfrozen 0\n", false},
		{"frozen 1\npopulated 1\n", true},
		{"populated 0\nfuture_counter 42\n", false},
	} {
		live, err := populated(test.data)
		if err != nil || live != test.live {
			t.Fatalf("kernel evidence denied or misclassified")
		}
	}
	for _, data := range []string{
		"", "populated 0", "frozen 0\n", "populated 2\n", "populated 00\n",
		"populated 0\npopulated 1\n", "populated 0\nfrozen 0\nfrozen 1\n",
		"populated 0\n\n", "populated\t0\n", "populated  0\n", "populated 0 \n",
		"populated 0\nfuture_counter -1\n", "populated 0\nfuture_counter NaN\n",
		"Populated 0\n", "populated 0\x00\n", strings.Repeat("a", 4097),
	} {
		if _, err := populated(data); err == nil {
			t.Fatal("ambiguous kernel evidence accepted")
		}
	}
}
