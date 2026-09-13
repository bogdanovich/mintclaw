package prompt

import (
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
		wantErr bool
	}{
		{name: "text", content: "Inspect the repository."},
		{name: "whitespace", content: " \t\n", wantErr: true},
		{name: "invalid UTF-8", content: string([]byte{0xff}), wantErr: true},
		{name: "oversized", content: strings.Repeat("x", MaxBytes+1), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := Validate(test.content); (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, want error %v", err, test.wantErr)
			}
		})
	}
}
