package browserpolicy

import "testing"

func TestFillRoleAllowedUsesOnlyMechanicalRoles(t *testing.T) {
	for _, role := range []string{"textbox", "searchbox", "combobox", "spinbutton"} {
		if !FillRoleAllowed(role) {
			t.Fatalf("fillable role %q was rejected", role)
		}
	}
	for _, role := range []string{"", "button", "checkbox", "radio"} {
		if FillRoleAllowed(role) {
			t.Fatalf("non-fillable role %q was admitted", role)
		}
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
		{name: "invalid mode fails closed", effect: "unknown", want: true},
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

func TestRestrictedApprovalKeepsExplicitConfirmationAdditive(t *testing.T) {
	tests := []struct {
		name         string
		decision     string
		confirmation string
		want         bool
	}{
		{name: "allow", decision: DecisionAllow},
		{name: "allow with request", decision: DecisionAllow, confirmation: ConfirmationRequest, want: true},
		{name: "ask", decision: DecisionAsk, want: true},
		{name: "ask with request", decision: DecisionAsk, confirmation: ConfirmationRequest, want: true},
		{name: "deny fails closed", decision: DecisionDeny, want: true},
		{name: "invalid fails closed", decision: "invalid", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := RestrictedRequiresApproval(test.decision, test.confirmation); got != test.want {
				t.Fatalf("RestrictedRequiresApproval() = %t, want %t", got, test.want)
			}
		})
	}
}
