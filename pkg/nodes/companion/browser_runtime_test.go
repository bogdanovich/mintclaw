package companion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

type fakeBrowserCommandHost struct {
	profiles      []nodes.BrowserProfileDescriptor
	opened        int
	observed      int
	navigated     int
	closed        int
	navigateError error
	invalidAction bool
}

func (host *fakeBrowserCommandHost) BrowserProfiles() []nodes.BrowserProfileDescriptor {
	return nodes.CloneBrowserProfileDescriptors(host.profiles)
}

func (host *fakeBrowserCommandHost) Open(
	_ context.Context,
	request nodes.BrowserHostOpenRequest,
) (nodes.BrowserSessionResult, error) {
	host.opened++
	return nodes.BrowserSessionResult{
		SessionID: request.SessionID, State: "ready", TabID: "tab_primary", Controller: "agent",
		Features:  nodes.BrowserHostFeatures{Observe: true, Navigate: true},
		ExpiresAt: 200, IdleExpiresAt: 150,
	}, nil
}

func (*fakeBrowserCommandHost) Status(
	_ context.Context,
	request nodes.BrowserHostStatusRequest,
) (nodes.BrowserSessionResult, error) {
	return nodes.BrowserSessionResult{
		SessionID: request.SessionID, State: "ready", TabID: "tab_primary", Controller: "agent",
		Features:  nodes.BrowserHostFeatures{Observe: true, Navigate: true},
		ExpiresAt: 200, IdleExpiresAt: 150,
	}, nil
}

func (host *fakeBrowserCommandHost) Observe(
	_ context.Context,
	request nodes.BrowserHostObserveRequest,
) (nodes.BrowserObservationResult, error) {
	host.observed++
	return browserRuntimeObservation(request.SessionID, request.TabID, request.SnapshotGeneration), nil
}

func (host *fakeBrowserCommandHost) Navigate(
	_ context.Context,
	request nodes.BrowserHostActRequest,
) (nodes.BrowserObservationResult, error) {
	host.navigated++
	if host.navigateError != nil {
		return nodes.BrowserObservationResult{}, host.navigateError
	}
	result := browserRuntimeObservation(request.SessionID, request.TabID, request.SnapshotGeneration+1)
	if host.invalidAction {
		result.SnapshotGeneration = 0
	}
	return result, nil
}

func (host *fakeBrowserCommandHost) Close(
	_ context.Context,
	request nodes.BrowserHostStatusRequest,
) (nodes.BrowserSessionResult, error) {
	host.closed++
	return nodes.BrowserSessionResult{
		SessionID: request.SessionID, State: "closed", TabID: "tab_primary", Controller: "agent",
		Features:  nodes.BrowserHostFeatures{Observe: true, Navigate: true},
		ExpiresAt: 200, IdleExpiresAt: 150,
	}, nil
}

func browserRuntimeObservation(sessionID, tabID string, generation uint64) nodes.BrowserObservationResult {
	return nodes.BrowserObservationResult{
		SessionID: sessionID, TabID: tabID, SnapshotGeneration: generation,
		URL: "about:blank", Origin: "about:blank", Snapshot: "", Elements: []nodes.BrowserElement{},
	}
}

func TestRuntimeRegistersTypedBrowserCommandsWithoutModelContract(t *testing.T) {
	host := browserRuntimeHostFixture()
	policy := testRuntimePolicy(browserRuntimeCommands())
	policy.MaximumRisk = nodes.RiskWrite
	policy.MaxOutputBytes = nodes.MaxBrowserToolResultBytes
	runtime, err := NewRuntime(
		nodes.ID("node_test"), "test", policy, newMemoryInvocationLedger(), WithBrowserHost(host),
	)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, descriptor := range runtime.Catalog().Commands {
		if !nodes.IsBrowserCommand(descriptor.Name) {
			continue
		}
		seen[descriptor.Name] = true
		if descriptor.ModelContract != nil {
			t.Fatalf("browser command %q has model contract %#v", descriptor.Name, descriptor.ModelContract)
		}
	}
	for _, command := range browserRuntimeCommands() {
		if !seen[command] {
			t.Fatalf("browser command %q was not registered", command)
		}
	}
}

func TestRuntimeExecutesTypedBrowserLifecycle(t *testing.T) {
	host := browserRuntimeHostFixture()
	runtime := newBrowserRuntimeFixture(t, host)
	limits := nodes.BrowserLimits{}.Effective()
	invokeBrowserRuntime(t, runtime, nodes.BrowserCommandSessionOpen, nodes.BrowserSessionOpenInput{
		SessionID: "browser_session_1", Profile: "managed", ProfileRevision: "managed-v1",
		BrowserPolicyRevision: strings.Repeat("a", 64), DryRun: true, Limits: limits,
	})
	invokeBrowserRuntime(t, runtime, nodes.BrowserCommandSessionStatus, nodes.BrowserSessionStatusInput{
		SessionID: "browser_session_1", ProfileRevision: "managed-v1",
	})
	invokeBrowserRuntime(t, runtime, nodes.BrowserCommandObserve, nodes.BrowserObserveInput{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
	})
	invokeBrowserRuntime(t, runtime, nodes.BrowserCommandAct, nodes.BrowserActInput{
		SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
		ActionInvocationID: "browser_action_1",
		Action:             nodes.BrowserAction{Kind: "navigate", URL: "https://example.com/"},
		Effect:             "navigation", CurrentOrigin: "about:blank",
		PreparedActionHash:    strings.Repeat("b", 64),
		BrowserPolicyRevision: strings.Repeat("a", 64), ProfileRevision: "managed-v1",
	})
	invokeBrowserRuntime(t, runtime, nodes.BrowserCommandSessionClose, nodes.BrowserSessionStatusInput{
		SessionID: "browser_session_1", ProfileRevision: "managed-v1",
	})
	if host.opened != 1 || host.observed != 1 || host.navigated != 1 || host.closed != 1 {
		t.Fatalf(
			"browser host calls = open %d observe %d navigate %d close %d",
			host.opened, host.observed, host.navigated, host.closed,
		)
	}
}

func TestRuntimeMarksAmbiguousOrInvalidBrowserActionUnknownWithoutReplay(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*fakeBrowserCommandHost)
	}{
		{name: "host lost", configure: func(host *fakeBrowserCommandHost) {
			host.navigateError = nodes.ErrBrowserHostLost
		}},
		{name: "invalid terminal output", configure: func(host *fakeBrowserCommandHost) {
			host.invalidAction = true
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := browserRuntimeHostFixture()
			test.configure(host)
			runtime := newBrowserRuntimeFixture(t, host)
			input, err := json.Marshal(nodes.BrowserActInput{
				SessionID: "browser_session_1", TabID: "tab_primary", SnapshotGeneration: 1,
				ActionInvocationID: "browser_action_1",
				Action:             nodes.BrowserAction{Kind: "navigate", URL: "https://example.com/"},
				Effect:             "navigation", CurrentOrigin: "about:blank",
				PreparedActionHash:    strings.Repeat("b", 64),
				BrowserPolicyRevision: strings.Repeat("a", 64), ProfileRevision: "managed-v1",
			})
			if err != nil {
				t.Fatal(err)
			}
			plan := testRuntimePlan(t, runtime, nodes.BrowserCommandAct, input)
			if _, err = runtime.Invoke(t.Context(), plan); !errors.Is(err, ErrInvocationOutcomeUnknown) {
				t.Fatalf("Invoke() error = %v, want unknown", err)
			}
			record, found := runtime.ledger.(*InvocationLedger).Get(plan.InvocationID)
			if !found || record.State != nodes.InvocationUnknown || host.navigated != 1 {
				t.Fatalf("record = %#v, found %v, navigate calls %d", record, found, host.navigated)
			}
			if _, err = runtime.Invoke(t.Context(), plan); !errors.Is(err, ErrInvocationOutcomeUnknown) ||
				host.navigated != 1 {
				t.Fatalf("replay error = %v, navigate calls = %d", err, host.navigated)
			}
		})
	}
}

func browserRuntimeHostFixture() *fakeBrowserCommandHost {
	return &fakeBrowserCommandHost{profiles: []nodes.BrowserProfileDescriptor{{
		Alias: "managed", Revision: "managed-v1", Driver: nodes.BrowserDriverPlaywrightMCP,
		Mode: nodes.BrowserProfileManaged, NetworkMode: nodes.BrowserNetworkAnyHTTP,
		DryRun: true, Actions: []string{"navigate"}, Limits: nodes.BrowserLimits{}.Effective(),
	}}}
}

func newBrowserRuntimeFixture(t *testing.T, host *fakeBrowserCommandHost) *Runtime {
	t.Helper()
	policy := testRuntimePolicy(browserRuntimeCommands())
	policy.MaximumRisk = nodes.RiskWrite
	policy.MaxOutputBytes = nodes.MaxBrowserToolResultBytes
	runtime, err := NewRuntime(
		nodes.ID("node_test"), "test", policy, newMemoryInvocationLedger(), WithBrowserHost(host),
	)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func invokeBrowserRuntime(t *testing.T, runtime *Runtime, command string, input any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	plan := testRuntimePlan(t, runtime, command, raw)
	result, err := runtime.Invoke(t.Context(), plan)
	if err != nil {
		t.Fatalf("Invoke(%s) error = %v", command, err)
	}
	return result
}

func browserRuntimeCommands() []string {
	return []string{
		nodes.BrowserCommandSessionOpen,
		nodes.BrowserCommandSessionStatus,
		nodes.BrowserCommandObserve,
		nodes.BrowserCommandAct,
		nodes.BrowserCommandSessionClose,
	}
}
