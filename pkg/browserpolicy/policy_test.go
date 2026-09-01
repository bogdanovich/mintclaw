package browserpolicy

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPolicyOrderedRulesAndExactMatching(t *testing.T) {
	policy := Policy{
		DefaultDecision: DecisionDeny,
		Rules: []Rule{
			{
				ID: "ask-ticket-purchase",
				Match: RuleMatch{
					Actions: []string{"click"}, Effects: []string{"external_commit"},
					Origins: []string{"https://tickets.example"}, Roles: []string{"button"},
					NamePatterns: []string{"buy *"},
				},
				Decision: DecisionAsk,
			},
			{
				ID:       "allow-ticket-clicks",
				Match:    RuleMatch{Actions: []string{"click"}, Origins: []string{"https://tickets.example"}},
				Decision: DecisionAllow,
			},
		},
	}
	revision, err := PolicyRevision(policy)
	if err != nil {
		t.Fatal(err)
	}
	base := ActionMetadata{
		Action: "click", Effect: "external_commit", Origin: "https://tickets.example",
		Role: "Button", Name: "  Buy   two tickets ", ProfileRevision: "managed-v1",
		PolicyRevision: revision,
	}
	result, err := Evaluate(t.Context(), policy, base)
	if err != nil || result.Decision != DecisionAsk {
		t.Fatalf("Evaluate(ordered ask) = %#v, %v", result, err)
	}

	tests := []struct {
		name     string
		mutate   func(*ActionMetadata)
		decision string
	}{
		{name: "later allow", mutate: func(input *ActionMetadata) { input.Name = "Details" }, decision: DecisionAllow},
		{
			name:     "origin is exact",
			mutate:   func(input *ActionMetadata) { input.Origin = "https://shop.example" },
			decision: DecisionDeny,
		},
		{
			name:     "action is exact",
			mutate:   func(input *ActionMetadata) { input.Action = "fill" },
			decision: DecisionDeny,
		},
		{
			name:     "effect is exact",
			mutate:   func(input *ActionMetadata) { input.Effect = "local_edit" },
			decision: DecisionAllow,
		},
		{name: "role is exact", mutate: func(input *ActionMetadata) { input.Role = "link" }, decision: DecisionAllow},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.mutate(&input)
			result, evaluateErr := Evaluate(t.Context(), policy, input)
			if evaluateErr != nil || result.Decision != test.decision {
				t.Fatalf("Evaluate() = %#v, %v, want %s", result, evaluateErr, test.decision)
			}
		})
	}
}

func TestPolicyNormalizationAndRevision(t *testing.T) {
	policy := Policy{
		DefaultDecision: DecisionAllow,
		Rules: []Rule{{
			ID: "allow-edit",
			Match: RuleMatch{
				Actions: []string{"select", "fill"},
				Origins: []string{"https://EXAMPLE.com:443"},
				Roles:   []string{"TextBox"}, NamePatterns: []string{"  Price  * "},
			},
			Decision: DecisionAsk,
		}},
	}
	normalized, err := NormalizePolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	match := normalized.Rules[0].Match
	if strings.Join(match.Actions, ",") != "fill,select" ||
		strings.Join(match.Origins, ",") != "https://example.com" ||
		strings.Join(match.Roles, ",") != "textbox" ||
		strings.Join(match.NamePatterns, ",") != "price *" {
		t.Fatalf("NormalizePolicy() = %#v", match)
	}
	left, err := PolicyRevision(policy)
	if err != nil {
		t.Fatal(err)
	}
	right, err := PolicyRevision(normalized)
	if err != nil || left != right {
		t.Fatalf("normalized revision = %q, %v, want %q", right, err, left)
	}
}

func TestPolicyRejectsInvalidConfiguration(t *testing.T) {
	valid := Policy{DefaultDecision: DecisionDeny}
	tests := []struct {
		name   string
		mutate func(*Policy)
	}{
		{name: "decision", mutate: func(policy *Policy) { policy.DefaultDecision = "maybe" }},
		{name: "duplicate id", mutate: func(policy *Policy) {
			policy.Rules = []Rule{{ID: "same", Decision: DecisionAllow}, {ID: "same", Decision: DecisionDeny}}
		}},
		{name: "invalid action", mutate: func(policy *Policy) {
			policy.Rules = []Rule{{ID: "bad", Match: RuleMatch{Actions: []string{"script"}}, Decision: DecisionAllow}}
		}},
		{name: "relative hook", mutate: func(policy *Policy) {
			policy.Hook = &Hook{Command: []string{"browser-policy"}, TimeoutMS: 1000}
		}},
		{name: "unbounded timeout", mutate: func(policy *Policy) {
			policy.Hook = &Hook{Command: []string{"/bin/true"}, TimeoutMS: MaxHookTimeoutMS + 1}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := valid
			test.mutate(&policy)
			if _, err := NormalizePolicy(policy); !errors.Is(err, ErrInvalidPolicy) {
				t.Fatalf("NormalizePolicy() error = %v", err)
			}
		})
	}
}

func TestPolicyHookDecisionsAndFailures(t *testing.T) {
	statePath := t.TempDir() + "/hook-state.json"
	policy := policyWithTestHook(t, 1000, statePath)
	revision, err := PolicyRevision(policy)
	if err != nil {
		t.Fatal(err)
	}
	metadata := ActionMetadata{
		Action: "click", Effect: "external_commit", Origin: "https://tickets.example",
		Role: "button", Name: "Buy", ProfileRevision: "managed-v1", PolicyRevision: revision,
	}
	tests := []struct {
		name      string
		output    string
		exit      int
		sleepMS   int
		want      string
		wantError bool
	}{
		{name: "allow", output: `{"decision":"allow"}`, want: DecisionAllow},
		{name: "deny", output: `{"decision":"deny","reason":"blocked"}`, want: DecisionDeny},
		{name: "ask", output: `{"decision":"ask","summary":"Confirm purchase"}`, want: DecisionAsk},
		{name: "crash", output: `{"decision":"allow"}`, exit: 2, want: DecisionDeny, wantError: true},
		{name: "malformed", output: `{`, want: DecisionDeny, wantError: true},
		{name: "unknown decision", output: `{"decision":"later"}`, want: DecisionDeny, wantError: true},
		{name: "unknown field", output: `{"decision":"allow","extra":true}`, want: DecisionDeny, wantError: true},
		{name: "oversized", output: strings.Repeat("x", MaxHookOutputBytes+1), want: DecisionDeny, wantError: true},
		{name: "timeout", output: `{"decision":"allow"}`, sleepMS: 80, want: DecisionDeny, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writePolicyHookTestState(t, statePath, policyHookTestState{
				Output: test.output, Exit: test.exit, SleepMS: test.sleepMS,
			})
			candidate := policy
			if test.name == "timeout" {
				candidate.Hook.TimeoutMS = 10
				metadata.PolicyRevision, err = PolicyRevision(candidate)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				metadata.PolicyRevision = revision
			}
			result, evaluateErr := Evaluate(t.Context(), candidate, metadata)
			if (evaluateErr != nil) != test.wantError || result.Decision != test.want {
				t.Fatalf("Evaluate() = %#v, %v, want %s/error=%t", result, evaluateErr, test.want, test.wantError)
			}
		})
	}
}

func TestPolicyHookRefinesAskButCannotOverrideDeny(t *testing.T) {
	statePath := t.TempDir() + "/hook-state.json"
	writePolicyHookTestState(t, statePath, policyHookTestState{Output: `{"decision":"allow"}`})
	policy := policyWithTestHook(t, 1000, statePath)
	policy.DefaultDecision = DecisionAsk
	revision, err := PolicyRevision(policy)
	if err != nil {
		t.Fatal(err)
	}
	metadata := ActionMetadata{
		Action: "click", Effect: "read", Origin: "https://example.com",
		ProfileRevision: "managed-v1", PolicyRevision: revision,
	}
	result, err := Evaluate(t.Context(), policy, metadata)
	if err != nil || result.Decision != DecisionAllow {
		t.Fatalf("Evaluate(ask refined to allow) = %#v, %v", result, err)
	}
	policy.DefaultDecision = DecisionDeny
	policy.Hook.Command[0] = "/definitely/missing/browser-policy"
	metadata.PolicyRevision, err = PolicyRevision(policy)
	if err != nil {
		t.Fatal(err)
	}
	result, err = Evaluate(t.Context(), policy, metadata)
	if err != nil || result.Decision != DecisionDeny {
		t.Fatalf("Evaluate(declarative deny) = %#v, %v", result, err)
	}
}

func TestPolicyHookInputContainsOnlyBoundedMetadata(t *testing.T) {
	capture := t.TempDir() + "/input.json"
	statePath := t.TempDir() + "/hook-state.json"
	writePolicyHookTestState(t, statePath, policyHookTestState{
		Output: `{"decision":"allow"}`, Capture: capture,
	})
	policy := policyWithTestHook(t, 1000, statePath)
	revision, err := PolicyRevision(policy)
	if err != nil {
		t.Fatal(err)
	}
	metadata := ActionMetadata{
		Action: "fill", Effect: "local_edit", Origin: "https://example.com",
		Role: "textbox", Name: "Password", ProfileRevision: "managed-v1", PolicyRevision: revision,
	}
	result, err := Evaluate(t.Context(), policy, metadata)
	if err != nil || result.Decision != DecisionAllow {
		t.Fatalf("Evaluate() = %#v, %v", result, err)
	}
	encoded, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var input map[string]any
	if err = json.Unmarshal(encoded, &input); err != nil {
		t.Fatal(err)
	}
	if len(input) != 4 || input["schema_version"] != "mintclaw.browser.policy.input.v1" ||
		strings.Contains(string(encoded), "secret-value") || strings.Contains(string(encoded), "cookie") {
		t.Fatalf("hook input = %s", encoded)
	}
	action, ok := input["action"].(map[string]any)
	if !ok || len(action) != 5 || action["kind"] != "fill" || action["name"] != "Password" {
		t.Fatalf("hook action input = %#v", input["action"])
	}
}

func TestPolicyHookDoesNotInheritParentSecrets(t *testing.T) {
	const secretName = "MINTCLAW_BROWSER_POLICY_SECRET_CANARY"
	t.Setenv(secretName, "must-not-reach-hook")
	statePath := t.TempDir() + "/hook-state.json"
	writePolicyHookTestState(t, statePath, policyHookTestState{
		Output: `{"decision":"allow"}`, RejectEnv: secretName,
	})
	policy := policyWithTestHook(t, 1000, statePath)
	revision, err := PolicyRevision(policy)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Evaluate(t.Context(), policy, ActionMetadata{
		Action: "click", Effect: "read", Origin: "https://example.com",
		ProfileRevision: "managed-v1", PolicyRevision: revision,
	})
	if err != nil || result.Decision != DecisionAllow {
		t.Fatalf("Evaluate() = %#v, %v", result, err)
	}
}

func TestPolicyRevisionMismatchFailsClosed(t *testing.T) {
	policy := Policy{DefaultDecision: DecisionAllow}
	result, err := Evaluate(t.Context(), policy, ActionMetadata{
		Action: "click", Effect: "read", Origin: "https://example.com",
		ProfileRevision: "managed-v1", PolicyRevision: strings.Repeat("a", 64),
	})
	if !errors.Is(err, ErrInvalidPolicy) || result.Decision != DecisionDeny {
		t.Fatalf("Evaluate() = %#v, %v", result, err)
	}
}

func TestCombineDecisionsIsRestrictive(t *testing.T) {
	tests := []struct{ left, right, want string }{
		{DecisionAllow, DecisionAllow, DecisionAllow},
		{DecisionAllow, DecisionAsk, DecisionAsk},
		{DecisionAsk, DecisionAllow, DecisionAsk},
		{DecisionAsk, DecisionDeny, DecisionDeny},
		{DecisionDeny, DecisionAllow, DecisionDeny},
	}
	for _, test := range tests {
		got, err := CombineDecisions(test.left, test.right)
		if err != nil || got != test.want {
			t.Fatalf("CombineDecisions(%s, %s) = %s, %v", test.left, test.right, got, err)
		}
	}
}

func policyWithTestHook(t *testing.T, timeoutMS int, statePath string) Policy {
	t.Helper()
	return Policy{
		DefaultDecision: DecisionAllow,
		Hook: &Hook{
			Command:   []string{os.Args[0], "-test.run=TestPolicyHookProcess", "--", statePath},
			TimeoutMS: timeoutMS,
		},
	}
}

type policyHookTestState struct {
	Output    string `json:"output"`
	Exit      int    `json:"exit"`
	SleepMS   int    `json:"sleep_ms"`
	Capture   string `json:"capture,omitempty"`
	RejectEnv string `json:"reject_env,omitempty"`
}

func writePolicyHookTestState(t *testing.T, path string, state policyHookTestState) {
	t.Helper()
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPolicyHookProcess(t *testing.T) {
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	encodedState, err := os.ReadFile(os.Args[separator+1])
	if err != nil {
		os.Exit(3)
	}
	var state policyHookTestState
	if json.Unmarshal(encodedState, &state) != nil {
		os.Exit(3)
	}
	if state.RejectEnv != "" && os.Getenv(state.RejectEnv) != "" {
		_, _ = io.WriteString(os.Stdout, `{"decision":"deny","reason":"environment leaked"}`)
		os.Exit(0)
	}
	input, _ := io.ReadAll(os.Stdin)
	if state.Capture != "" {
		_ = os.WriteFile(state.Capture, input, 0o600)
	}
	if state.SleepMS > 0 {
		time.Sleep(time.Duration(state.SleepMS) * time.Millisecond)
	}
	_, _ = io.WriteString(os.Stdout, state.Output)
	if state.Exit != 0 {
		os.Exit(state.Exit)
	}
	os.Exit(0)
}
