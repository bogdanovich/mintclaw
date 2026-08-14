package browserpolicy

import "testing"

func TestOrdinaryFillFieldFailsClosed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		role string
		want bool
	}{
		{name: "Display name", role: "textbox", want: true},
		{name: "Search", role: "searchbox", want: true},
		{name: "Email address", role: "textbox", want: true},
		{name: "Input", role: "textbox"},
		{name: "2FA code", role: "textbox"},
		{name: "PIN", role: "textbox"},
		{name: "Card #", role: "textbox"},
		{name: "Nombre", role: "textbox"},
		{name: "Display name", role: "combobox"},
	}
	for _, test := range tests {
		t.Run(test.name+"/"+test.role, func(t *testing.T) {
			t.Parallel()
			if got := OrdinaryFillField(test.role, test.name, nil); got != test.want {
				t.Fatalf("OrdinaryFillField(%q, %q) = %v, want %v", test.role, test.name, got, test.want)
			}
		})
	}
	if OrdinaryFillField("textbox", "Display name", []string{"display name"}) {
		t.Fatal("operator-designated sensitive field was admitted")
	}
}

func TestNormalizeSensitiveFieldTerms(t *testing.T) {
	t.Parallel()
	got, err := NormalizeSensitiveFieldTerms([]string{" Employee ID ", "Internal Note"})
	if err != nil || len(got) != 2 || got[0] != "employee id" || got[1] != "internal note" {
		t.Fatalf("NormalizeSensitiveFieldTerms() = %#v, %v", got, err)
	}
	if _, err = NormalizeSensitiveFieldTerms([]string{"PIN", " pin "}); err == nil {
		t.Fatal("duplicate normalized terms were accepted")
	}
}
