package computer

import (
	"errors"
	"testing"
)

func TestReadOnlyHostTrustedLossSpansControlNotResourceIdentity(t *testing.T) {
	host, request := readOnlyFixture(t, nil, nil)
	original, err := host.Open(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	human, err := host.Handoff(t.Context(), original.Binding)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Binding){
		"session": func(b *Binding) { b.SessionID = "other" },
		"worker":  func(b *Binding) { b.WorkerID = "other" },
		"scope":   func(b *Binding) { b.Target.Scope.WindowGeneration++ },
		"policy":  func(b *Binding) { b.PolicyRevision = "other" },
		"owner":   func(b *Binding) { b.Principal.ExecutionID = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			forged := original.Binding
			mutate(&forged)
			if _, err := host.Lose(t.Context(), forged); err == nil {
				t.Fatal("forged native loss accepted")
			}
		})
	}
	status, err := host.Status(t.Context(), request.Principal, original.Binding.SessionID)
	if err != nil || status != human {
		t.Fatal("forged loss changed live authority", err)
	}
	lost, err := host.Lose(t.Context(), original.Binding)
	if err != nil || lost.Control != ControlLost || lost.Binding.Generation != human.Binding.Generation+1 {
		t.Fatal("native loss did not cross human-control generation", err)
	}
	replacement, err := host.Open(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.Lose(t.Context(), original.Binding); err != nil {
		t.Fatal(err)
	}
	if _, err = host.Observe(t.Context(), replacement); err != nil {
		t.Fatal("old loss revoked replacement", err)
	}
	if _, err = host.Observe(t.Context(), lost); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
}
