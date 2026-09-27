//go:build (linux && amd64) || (darwin && (amd64 || arm64)) || (windows && amd64)

package document

import (
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func TestFormVisualExpectationRequiresExplicitButtonState(t *testing.T) {
	checkbox := pdfCPUFormBinding{
		field: FormField{Kind: FormFieldCheckbox},
		expected: pdfCPUFormValue{
			kind: FormFieldCheckbox,
		},
	}
	if _, assertion := formVisualExpectation(checkbox, nil); assertion != formVisualAssertionUnselectedButton {
		t.Fatalf("unchecked checkbox assertion = %v", assertion)
	}
	checkbox.expected.checked = true
	if _, assertion := formVisualExpectation(checkbox, nil); assertion != formVisualAssertionSelectedButton {
		t.Fatalf("checked checkbox assertion = %v", assertion)
	}

	radio := pdfCPUFormBinding{field: FormField{Kind: FormFieldRadio}}
	if _, assertion := formVisualExpectation(
		radio,
		types.Dict{"AS": types.Name("Off")},
	); assertion != formVisualAssertionUnselectedButton {
		t.Fatalf("unselected radio assertion = %v", assertion)
	}
	if _, assertion := formVisualExpectation(
		radio,
		types.Dict{"AS": types.Name("selected")},
	); assertion != formVisualAssertionSelectedButton {
		t.Fatalf("selected radio assertion = %v", assertion)
	}
}

func TestFormVisualListSelectionRequiresExactSelectedRows(t *testing.T) {
	tests := []struct {
		name string
		rows []formVisualSelectionRow
		want bool
	}{
		{
			name: "exact",
			rows: []formVisualSelectionRow{
				{text: "one", selected: true},
				{text: "someone"},
			},
			want: true,
		},
		{
			name: "superstring",
			rows: []formVisualSelectionRow{
				{text: "one"},
				{text: "someone", selected: true},
			},
		},
		{
			name: "extra highlight",
			rows: []formVisualSelectionRow{
				{text: "one", selected: true},
				{text: "someone", selected: true},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := formVisualListSelectionMatches([]string{"one"}, test.rows); got != test.want {
				t.Fatalf("selection match = %v, want %v", got, test.want)
			}
		})
	}
}
