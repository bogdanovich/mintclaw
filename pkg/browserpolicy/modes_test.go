package browserpolicy

import "testing"

func TestFullAccessFillIgnoresSemanticFieldIdentity(t *testing.T) {
	for _, field := range []struct {
		role string
		name string
	}{
		{role: "textbox", name: "Price"},
		{role: "spinbutton", name: "Price"},
		{role: "textbox", name: ""},
		{role: "textbox", name: "Password"},
		{role: "textbox", name: "One-time code"},
		{role: "textbox", name: "Card number"},
		{role: "combobox", name: "Account"},
	} {
		if !FillFieldAllowed(CapabilityFullAccess, field.role, field.name, []string{"account"}) {
			t.Fatalf("full_access rejected role=%q name=%q", field.role, field.name)
		}
	}
	if FillFieldAllowed(CapabilityFullAccess, "checkbox", "Price", nil) {
		t.Fatal("full_access admitted a non-fillable semantic role")
	}
	if FillFieldAllowed(CapabilityLegacyStrict, "textbox", "Price", nil) {
		t.Fatal("legacy_strict unexpectedly admitted Price")
	}
}

func TestApprovalModesSeparateEffectFromConfirmation(t *testing.T) {
	tests := []struct {
		name         string
		mode         string
		effect       string
		confirmation string
		want         bool
	}{
		{name: "none ignores commit", mode: ApprovalNone, effect: "external_commit"},
		{name: "none ignores request", mode: ApprovalNone, effect: "read", confirmation: ConfirmationRequest},
		{name: "model requested commit runs", mode: ApprovalModelRequested, effect: "external_commit"},
		{
			name:         "model requested pause",
			mode:         ApprovalModelRequested,
			effect:       "local_edit",
			confirmation: ConfirmationRequest,
			want:         true,
		},
		{name: "always commit local edit", mode: ApprovalAlwaysCommit, effect: "local_edit"},
		{name: "always commit external", mode: ApprovalAlwaysCommit, effect: "external_commit", want: true},
		{
			name:         "always commit explicit",
			mode:         ApprovalAlwaysCommit,
			effect:       "read",
			confirmation: ConfirmationRequest,
			want:         true,
		},
		{name: "legacy default", effect: "unknown", want: true},
		{name: "unimplemented policy fails closed", mode: ApprovalPolicy, effect: "read", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := RequiresApproval(test.mode, test.effect, test.confirmation); got != test.want {
				t.Fatalf("RequiresApproval() = %t, want %t", got, test.want)
			}
		})
	}
}
