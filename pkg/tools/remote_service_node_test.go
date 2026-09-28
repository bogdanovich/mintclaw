package tools

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

func TestRemoteServiceNodeRouterProjectsAndExecutesExactServiceProfile(t *testing.T) {
	source := newRemoteServiceTestSource(t, serviceStatusTestDescriptor())
	cfg := remoteServiceTestConfig()
	router, err := NewRemoteServiceNodeRouter(cfg, source, "main")
	if err != nil {
		t.Fatal(err)
	}
	described, err := router.Describe("build", "service.status.v1")
	if err != nil {
		t.Fatal(err)
	}
	if described.Target != "build" || !described.Available || described.Risk != nodes.RiskRead ||
		described.ResultKind != "json" || described.discoveryRevision == "" {
		t.Fatalf("service description = %#v", described)
	}
	encoded := string(described.InputSchema)
	for _, forbidden := range []string{"database", "secret-services", "server-services", "private-node-id"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("service schema exposed %q: %s", forbidden, encoded)
		}
	}
	if !strings.Contains(encoded, `"vpn"`) {
		t.Fatalf("service schema omitted selected alias: %s", encoded)
	}
	source.dispatchResult = json.RawMessage(`{"service":"vpn","active":true}`)
	result := router.Execute(
		nodeInvocationTestContext("actor-1", "call-remote-service-status"),
		"build",
		"service.status.v1",
		map[string]any{"service": "vpn"},
	)
	payload := decodeNodeResult(t, result)
	if payload["state"] != string(nodes.InvocationSucceeded) || source.prepareCalls != 1 ||
		source.dispatchCalls != 1 {
		t.Fatalf(
			"service execution = %#v; prepare=%d dispatch=%d",
			payload,
			source.prepareCalls,
			source.dispatchCalls,
		)
	}
}

func TestRemoteServiceNodeRouterRequiresConfiguredBypassForAction(t *testing.T) {
	descriptor := serviceActionInvocationTestDescriptor()
	source := newRemoteServiceTestSource(t, descriptor)
	cfg := remoteServiceTestConfig()
	router, err := NewRemoteServiceNodeRouter(cfg, source, "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = router.Describe("build", descriptor.Name); !errors.Is(err, ErrRemoteServiceUnavailable) {
		t.Fatalf("service action without bypass error = %v", err)
	}
	result := router.Execute(
		nodeInvocationTestContext("actor-1", "call-service-action-denied"),
		"build",
		descriptor.Name,
		map[string]any{"service": "vpn", "action": "restart"},
	)
	if !result.IsError || source.prepareCalls != 0 || source.dispatchCalls != 0 {
		t.Fatalf("action without bypass = %#v; source = %#v", result, source)
	}

	cfg.Tools.Approval.BypassNodeTargets = []string{"build"}
	router, err = NewRemoteServiceNodeRouter(cfg, source, "main")
	if err != nil {
		t.Fatal(err)
	}
	described, err := router.Describe("build", descriptor.Name)
	if err != nil || described.Risk != nodes.RiskWrite {
		t.Fatalf("service action with bypass = %#v, %v", described, err)
	}
	result = router.Execute(
		nodeInvocationTestContext("actor-1", "call-service-action-bypassed"),
		"build",
		descriptor.Name,
		map[string]any{"service": "vpn", "action": "restart"},
	)
	if result.IsError || source.prepareCalls != 1 || source.dispatchCalls != 1 {
		t.Fatalf(
			"bypassed action = %#v; prepare=%d dispatch=%d",
			result,
			source.prepareCalls,
			source.dispatchCalls,
		)
	}
}

func TestRemoteServiceNodeRouterFailsClosedOnStaleCatalogAndGenericCommand(t *testing.T) {
	source := newRemoteServiceTestSource(t, serviceStatusTestDescriptor())
	router, err := NewRemoteServiceNodeRouter(remoteServiceTestConfig(), source, "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = router.Describe("build", "node.info.v1"); !errors.Is(err, ErrRemoteServiceUnavailable) {
		t.Fatalf("generic command error = %v", err)
	}
	snapshot := source.byRef["builder-node"]
	registration := source.registrations[snapshot.ID]
	registration.ApprovedCatalogHash = strings.Repeat("f", 64)
	source.registrations[snapshot.ID] = registration
	if _, err = router.Describe("build", "service.status.v1"); !errors.Is(err, ErrRemoteServiceUnavailable) {
		t.Fatalf("stale catalog error = %v", err)
	}
}

func newRemoteServiceTestSource(
	t *testing.T,
	descriptors ...nodes.CommandDescriptor,
) *fakeNodeInvocationSource {
	t.Helper()
	source := newFakeNodeInvocationSource(t)
	snapshot := source.byRef["builder-node"]
	snapshot.Catalog = nodes.CapabilityCatalog{Commands: descriptors}
	snapshot.CatalogHash = mustCatalogHash(t, snapshot.Catalog)
	source.byRef["builder-node"] = snapshot
	registration := source.registrations[snapshot.ID]
	registration.Snapshot = snapshot
	registration.ApprovedCatalogHash = snapshot.CatalogHash
	registration.AllowedCommands = make([]string, len(descriptors))
	for index, descriptor := range descriptors {
		registration.AllowedCommands[index] = descriptor.Name
	}
	source.registrations[snapshot.ID] = registration
	return source
}

func remoteServiceTestConfig() *config.Config {
	cfg := nodeDiscoveryTestConfig()
	binding := cfg.Execution.Targets["build"]
	binding.ServiceProfile = "server-services"
	cfg.Execution.Targets["build"] = binding
	return cfg
}
