package agent

import "testing"

func TestNormalizeTurnSpecUsesCanonicalDispatchDefaults(t *testing.T) {
	opts := normalizeTurnSpec(turnSpec{
		ModelBinding: effectiveModelBinding{RouteSessionKey: " route-1 "},
		Dispatch: DispatchRequest{
			SessionKey:  " session-1 ",
			UserMessage: "hello",
			Media:       []string{"media://one"},
		},
	})

	if opts.Dispatch.RouteSessionKey != "route-1" {
		t.Fatalf("RouteSessionKey = %q, want route-1", opts.Dispatch.RouteSessionKey)
	}
	if opts.Dispatch.SessionKey != "session-1" {
		t.Fatalf("SessionKey = %q, want session-1", opts.Dispatch.SessionKey)
	}
	if opts.Dispatch.BaseSessionKey != "session-1" {
		t.Fatalf("BaseSessionKey = %q, want session-1", opts.Dispatch.BaseSessionKey)
	}
	if opts.Dispatch.UserMessage != "hello" {
		t.Fatalf("UserMessage = %q, want hello", opts.Dispatch.UserMessage)
	}
	if len(opts.Dispatch.Media) != 1 || opts.Dispatch.Media[0] != "media://one" {
		t.Fatalf("Media = %v, want [media://one]", opts.Dispatch.Media)
	}
}

func TestNormalizeTurnSpecPreservesExplicitRouteAndBaseSession(t *testing.T) {
	opts := normalizeTurnSpec(turnSpec{
		ModelBinding: effectiveModelBinding{RouteSessionKey: "binding-route"},
		Dispatch: DispatchRequest{
			RouteSessionKey: "explicit-route",
			BaseSessionKey:  "base-session",
			SessionKey:      "current-session",
		},
	})

	if opts.Dispatch.RouteSessionKey != "explicit-route" {
		t.Fatalf("RouteSessionKey = %q, want explicit-route", opts.Dispatch.RouteSessionKey)
	}
	if opts.Dispatch.BaseSessionKey != "base-session" {
		t.Fatalf("BaseSessionKey = %q, want base-session", opts.Dispatch.BaseSessionKey)
	}
}
