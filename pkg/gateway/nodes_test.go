package gateway

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	nodews "github.com/bogdanovich/mintclaw/pkg/nodes/ws"
)

type fakeNodeAdmissionRoutes struct {
	handler                   http.Handler
	enrollmentHandler         http.Handler
	enrollmentRegisterErr     error
	enrollmentReplaceErr      error
	registerCount             int
	replaceCount              int
	unregisterCount           int
	enrollmentRegisterCount   int
	enrollmentReplaceCount    int
	enrollmentUnregisterCount int
}

type closeErrorNodeAdmissionHandler struct {
	*fakeNodeAdmissionHandler
	err error
}

func (handler *closeErrorNodeAdmissionHandler) Close(context.Context) error {
	return handler.err
}

func TestNodeAdmissionWorkspaceChangeFailsClosed(t *testing.T) {
	routes := &fakeNodeAdmissionRoutes{}
	runtime := &nodeAdmissionRuntime{routes: routes}
	cfg := config.DefaultConfig()
	cfg.Nodes.Enabled = true
	cfg.Agents.Defaults.Workspace = t.TempDir()
	if err := runtime.Reconcile(cfg); err != nil {
		t.Fatal(err)
	}

	badWorkspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(badWorkspace, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(badWorkspace, "state", "nodes"),
		[]byte("not a directory"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	cfg.Agents.Defaults.Workspace = badWorkspace
	if err := runtime.Reconcile(cfg); err == nil {
		t.Fatal("workspace change accepted an unreadable replacement registry")
	}
	if runtime.mounted || runtime.registry != nil || runtime.sessions != nil || routes.handler != nil {
		t.Fatal("failed workspace change retained the previous node authority domain")
	}
}

func TestNodeAdmissionWorkspaceChangeWaitsForSuccessfulDrain(t *testing.T) {
	routes := &fakeNodeAdmissionRoutes{}
	runtime := &nodeAdmissionRuntime{routes: routes}
	cfg := config.DefaultConfig()
	cfg.Nodes.Enabled = true
	cfg.Agents.Defaults.Workspace = t.TempDir()
	if err := runtime.Reconcile(cfg); err != nil {
		t.Fatal(err)
	}
	oldRegistryPath := runtime.registryPath
	oldSource := &nodeDiscoverySource{runtime: runtime, registryPath: oldRegistryPath}

	disconnectCalls := 0
	release, err := runtime.sessions.Claim(
		nodes.ID("node_test"),
		&testNodeConnection{},
		nil,
		func() error {
			disconnectCalls++
			if disconnectCalls < 3 {
				return errors.New("registry unavailable")
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := release(); err == nil {
		t.Fatal("initial disconnect unexpectedly succeeded")
	}

	cfg.Agents.Defaults.Workspace = t.TempDir()
	if err := runtime.Reconcile(cfg); err == nil {
		t.Fatal("workspace change ignored failed node drain")
	}
	if runtime.handler == nil || runtime.registryPath != oldRegistryPath || runtime.mounted || routes.handler != nil {
		t.Fatal("failed drain discarded the closing authority runtime")
	}
	if _, _, lookupErr := oldSource.Lookup("node_test"); !errors.Is(
		lookupErr,
		errNodeDiscoveryAuthorityUnavailable,
	) {
		t.Fatalf("failed drain left old discovery authority readable: %v", lookupErr)
	}
	if err := runtime.Reconcile(cfg); err != nil {
		t.Fatalf("workspace change did not recover after drain retry: %v", err)
	}
	if !runtime.mounted || runtime.registryPath == oldRegistryPath || routes.handler == nil {
		t.Fatal("successful retry did not mount the replacement authority runtime")
	}
}

type testNodeConnection struct{}

func (*testNodeConnection) Close() error { return nil }

func TestNodeDiscoverySourceBindsWorkspaceAuthority(t *testing.T) {
	oldPath := filepath.Join(t.TempDir(), "old", "registry.json")
	registry, err := nodes.NewFileRegistry(oldPath, 4)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &nodeAdmissionRuntime{
		registry:     registry,
		registryPath: oldPath,
		mounted:      true,
	}
	oldSource := &nodeDiscoverySource{runtime: runtime, registryPath: oldPath}
	if _, found, lookupErr := oldSource.Lookup("missing"); lookupErr != nil || found {
		t.Fatalf("active authority lookup = found %v, error %v", found, lookupErr)
	}

	newSource := &nodeDiscoverySource{
		runtime:      runtime,
		registryPath: filepath.Join(t.TempDir(), "new", "registry.json"),
	}
	if _, _, lookupErr := newSource.Lookup("missing"); !errors.Is(
		lookupErr,
		errNodeDiscoveryAuthorityUnavailable,
	) {
		t.Fatalf("cross-workspace lookup error = %v", lookupErr)
	}

	runtime.registryMu.Lock()
	runtime.mounted = false
	runtime.registryMu.Unlock()
	if _, _, lookupErr := oldSource.Lookup("missing"); !errors.Is(
		lookupErr,
		errNodeDiscoveryAuthorityUnavailable,
	) {
		t.Fatalf("inactive authority lookup error = %v", lookupErr)
	}
}

func TestServiceShutdownClosesNodeAdmissionOutsideReload(t *testing.T) {
	routes := &fakeNodeAdmissionRoutes{}
	runtime := &nodeAdmissionRuntime{routes: routes}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Nodes.Enabled = true
	if err := runtime.Reconcile(cfg); err != nil {
		t.Fatal(err)
	}

	if err := stopAndCleanupServices(&services{NodeAdmission: runtime}, time.Second, true); err != nil {
		t.Fatal(err)
	}
	if !runtime.mounted {
		t.Fatal("service reload closed node admission")
	}
	if err := stopAndCleanupServices(&services{NodeAdmission: runtime}, time.Second, false); err != nil {
		t.Fatal(err)
	}
	if runtime.mounted || runtime.sessions != nil || routes.handler != nil {
		t.Fatal("gateway shutdown left node admission active")
	}
}

func TestNodeAdmissionDisableReconcilesActiveTerminalStore(t *testing.T) {
	routes := &fakeNodeAdmissionRoutes{}
	runtime := &nodeAdmissionRuntime{routes: routes}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Nodes.Enabled = true
	if err := runtime.Reconcile(cfg); err != nil {
		t.Fatal(err)
	}
	store, err := runtime.gatewayTerminalStore(
		nodes.GatewayTerminalStorePath(cfg.WorkspacePath()),
		8,
		1024*1024,
	)
	if err != nil {
		t.Fatal(err)
	}
	owner := nodes.TerminalOwner{
		ActorID: "actor_disable", AgentID: "agent_disable", RouteID: "route_disable",
		SessionID: "session_disable", WorkspaceID: "workspace_disable",
		Target: "vpn", Profile: "owner",
	}
	plan, err := nodes.PrepareTerminalOpenPlan(nodes.TerminalOpenPlan{
		OpenID:          "open_disable",
		IdempotencyKey:  "idem_disable",
		NodeID:          nodes.ID("node_disable"),
		Owner:           owner,
		CatalogHash:     testDigest("a"),
		AuthorityDigest: testDigest("b"),
		WorkingScope:    "workspace",
		Columns:         80,
		Rows:            24,
		ApprovalMode:    "session_start",
	}, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Prepare(plan); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.MarkDispatched(owner, plan.OpenID, plan.PlanHash); err != nil {
		t.Fatal(err)
	}

	cfg.Nodes.Enabled = false
	if err := runtime.Reconcile(cfg); err != nil {
		t.Fatal(err)
	}
	if runtime.terminalStore != nil || runtime.terminalStorePath != "" {
		t.Fatal("disabled runtime retained a reconciled terminal store")
	}
	record, found, err := store.Lookup(owner, plan.OpenID)
	if err != nil || !found ||
		record.State != nodes.GatewayTerminalUnknown ||
		record.Reason != "gateway_shutdown" {
		t.Fatalf("disabled terminal = (%#v, %v, %v)", record, found, err)
	}
}

func TestNodeAdmissionRuntimeOwnsOneInvocationStoreAndClosesIt(t *testing.T) {
	runtime := &nodeAdmissionRuntime{}
	path := nodes.GatewayInvocationStorePath(t.TempDir())
	first, err := runtime.gatewayInvocationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtime.gatewayInvocationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("same invocation ledger path created multiple retained stores")
	}
	if _, err = runtime.gatewayInvocationStore(
		nodes.GatewayInvocationStorePath(t.TempDir()),
	); err == nil {
		t.Fatal("invocation store path changed before runtime reconciliation")
	}
	if err = runtime.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if runtime.invocationStore != nil || runtime.invocationStorePath != "" {
		t.Fatal("node runtime retained a closed invocation store")
	}
	if _, _, err = first.Lookup(nodes.GatewayInvocationPrincipal{}, "inv_closed"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed runtime invocation store lookup error = %v", err)
	}
	if err = runtime.Close(t.Context()); err != nil {
		t.Fatalf("second runtime Close() error = %v", err)
	}
}

func TestNodeAdmissionRuntimeClosesInvocationStoreWhenSessionDrainIsIncomplete(t *testing.T) {
	runtime := &nodeAdmissionRuntime{
		handler: &closeErrorNodeAdmissionHandler{
			fakeNodeAdmissionHandler: &fakeNodeAdmissionHandler{},
			err:                      nodews.ErrSessionDrainIncomplete,
		},
	}
	path := nodes.GatewayInvocationStorePath(t.TempDir())
	store, err := runtime.gatewayInvocationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = runtime.Close(t.Context()); !errors.Is(err, nodews.ErrSessionDrainIncomplete) {
		t.Fatalf("Close() error = %v", err)
	}
	if runtime.invocationStore != store || runtime.invocationStorePath != path {
		t.Fatal("incomplete drain discarded invocation store reconciliation authority")
	}
	if _, _, err = store.Lookup(nodes.GatewayInvocationPrincipal{}, "inv_closed"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("drain-timeout invocation store lookup error = %v", err)
	}
}

func TestNodeAdmissionCloseReconcilesAfterPostDrainDeactivationError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node_terminals.json")
	store, err := nodes.NewGatewayTerminalStore(path, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	owner := nodes.TerminalOwner{
		ActorID: "actor_deactivate", AgentID: "agent_deactivate", RouteID: "route_deactivate",
		SessionID: "session_deactivate", WorkspaceID: "workspace_deactivate",
		Target: "vpn", Profile: "owner",
	}
	plan, err := nodes.PrepareTerminalOpenPlan(nodes.TerminalOpenPlan{
		OpenID:          "open_deactivate",
		IdempotencyKey:  "idem_deactivate",
		NodeID:          nodes.ID("node_deactivate"),
		Owner:           owner,
		CatalogHash:     testDigest("a"),
		AuthorityDigest: testDigest("b"),
		WorkingScope:    "workspace",
		Columns:         80,
		Rows:            24,
		ApprovalMode:    "session_start",
	}, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Prepare(plan); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.MarkDispatched(owner, plan.OpenID, plan.PlanHash); err != nil {
		t.Fatal(err)
	}
	deactivationErr := errors.New("registry deactivation failed")
	runtime := &nodeAdmissionRuntime{
		handler: &closeErrorNodeAdmissionHandler{
			fakeNodeAdmissionHandler: &fakeNodeAdmissionHandler{},
			err:                      deactivationErr,
		},
		terminalStore:     store,
		terminalStorePath: path,
	}
	if err := runtime.Close(t.Context()); !errors.Is(err, deactivationErr) {
		t.Fatalf("Close() error = %v", err)
	}
	if runtime.terminalStore != store || runtime.handler == nil {
		t.Fatal("post-drain deactivation error discarded retry authority")
	}
	record, found, err := store.Lookup(owner, plan.OpenID)
	if err != nil || !found ||
		record.State != nodes.GatewayTerminalUnknown ||
		record.Reason != "gateway_shutdown" {
		t.Fatalf("post-drain terminal = (%#v, %v, %v)", record, found, err)
	}
}

func TestNodeAdmissionCloseRetainsTerminalStoreWhenReconciliationFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node_terminals.json")
	initial, err := nodes.NewGatewayTerminalStore(path, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	owner := nodes.TerminalOwner{
		ActorID: "actor_retry", AgentID: "agent_retry", RouteID: "route_retry",
		SessionID: "session_retry", WorkspaceID: "workspace_retry",
		Target: "vpn", Profile: "owner",
	}
	plan, err := nodes.PrepareTerminalOpenPlan(nodes.TerminalOpenPlan{
		OpenID:          "open_retry",
		IdempotencyKey:  "idem_retry",
		NodeID:          nodes.ID("node_retry"),
		Owner:           owner,
		CatalogHash:     testDigest("a"),
		AuthorityDigest: testDigest("b"),
		WorkingScope:    "workspace",
		Columns:         80,
		Rows:            24,
		ApprovalMode:    "session_start",
	}, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := initial.Prepare(plan); err != nil {
		t.Fatal(err)
	}
	if _, _, err := initial.MarkDispatched(owner, plan.OpenID, plan.PlanHash); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	deactivationErr := errors.New("registry deactivation failed")
	runtime := &nodeAdmissionRuntime{
		handler: &closeErrorNodeAdmissionHandler{
			fakeNodeAdmissionHandler: &fakeNodeAdmissionHandler{},
			err:                      deactivationErr,
		},
		terminalStore:     initial,
		terminalStorePath: path,
	}
	if err := runtime.Close(t.Context()); !errors.Is(err, deactivationErr) ||
		!strings.Contains(err.Error(), "reconcile gateway terminals") {
		t.Fatalf("Close() error = %v", err)
	}
	if runtime.terminalStore != initial ||
		runtime.terminalStorePath != path ||
		runtime.handler == nil {
		t.Fatal("failed terminal reconciliation discarded retry authority")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime.handler = &fakeNodeAdmissionHandler{}
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatalf("retry Close() error = %v", err)
	}
	if runtime.terminalStore != nil || runtime.handler != nil {
		t.Fatal("successful retry retained terminal shutdown authority")
	}
	record, found, err := initial.Lookup(owner, plan.OpenID)
	if err != nil || !found ||
		record.State != nodes.GatewayTerminalUnknown ||
		record.Reason != "gateway_shutdown" {
		t.Fatalf("retried reconciliation state = (%#v, %v, %v)", record, found, err)
	}
}

func (routes *fakeNodeAdmissionRoutes) RegisterHTTPHandler(path string, handler http.Handler) error {
	if path == nodeEnrollmentOperatorPath {
		if routes.enrollmentRegisterErr != nil {
			return routes.enrollmentRegisterErr
		}
		if routes.enrollmentHandler != nil {
			return errors.New("enrollment route already registered")
		}
		routes.enrollmentHandler = handler
		routes.enrollmentRegisterCount++
		return nil
	}
	if routes.handler != nil {
		return errors.New("route already registered")
	}
	routes.handler = handler
	routes.registerCount++
	return nil
}

func (routes *fakeNodeAdmissionRoutes) ReplaceHTTPHandler(path string, handler http.Handler) error {
	if path == nodeEnrollmentOperatorPath {
		if routes.enrollmentReplaceErr != nil {
			return routes.enrollmentReplaceErr
		}
		if routes.enrollmentHandler == nil {
			return errors.New("enrollment route not registered")
		}
		routes.enrollmentHandler = handler
		routes.enrollmentReplaceCount++
		return nil
	}
	if routes.handler == nil {
		return errors.New("route not registered")
	}
	routes.handler = handler
	routes.replaceCount++
	return nil
}

func (routes *fakeNodeAdmissionRoutes) UnregisterHTTPHandler(path string) {
	if path == nodeEnrollmentOperatorPath {
		routes.enrollmentHandler = nil
		routes.enrollmentUnregisterCount++
		return
	}
	routes.handler = nil
	routes.unregisterCount++
}

func TestNodeAdmissionRuntimeReconcilesConfigLifecycle(t *testing.T) {
	routes := &fakeNodeAdmissionRoutes{}
	runtime := &nodeAdmissionRuntime{routes: routes}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(t.TempDir(), "first")

	if err := runtime.Reconcile(cfg); err != nil {
		t.Fatal(err)
	}
	if runtime.mounted || routes.handler != nil {
		t.Fatal("disabled node admission mounted a route")
	}

	cfg.Nodes.Enabled = true
	if err := runtime.Reconcile(cfg); err != nil {
		t.Fatal(err)
	}
	firstRegistry := runtime.registry
	firstSessions := runtime.sessions
	firstOffers := runtime.enrollmentOffers
	if !runtime.mounted || firstRegistry == nil || firstOffers == nil || routes.registerCount != 1 ||
		routes.enrollmentHandler == nil || routes.enrollmentRegisterCount != 1 {
		t.Fatalf("enabled runtime = %#v, routes = %#v", runtime, routes)
	}

	cfg.Nodes.AllowLoopbackPlaintext = true
	if err := runtime.Reconcile(cfg); err != nil {
		t.Fatal(err)
	}
	if runtime.registry == firstRegistry || runtime.enrollmentOffers == firstOffers || routes.replaceCount != 1 ||
		routes.enrollmentReplaceCount != 1 {
		t.Fatalf("reloaded runtime = %#v, routes = %#v", runtime, routes)
	}
	if runtime.sessions != firstSessions {
		t.Fatal("config reload replaced shared node session ownership")
	}

	cfg.Agents.Defaults.Workspace = filepath.Join(t.TempDir(), "second")
	if err := runtime.Reconcile(cfg); err != nil {
		t.Fatal(err)
	}
	if runtime.sessions == firstSessions {
		t.Fatal("workspace change retained node session ownership across registries")
	}
	if routes.replaceCount != 1 || routes.enrollmentReplaceCount != 1 {
		t.Fatalf("route replacement counts = %#v", routes)
	}
	if routes.registerCount != 2 || routes.unregisterCount != 1 || routes.enrollmentRegisterCount != 2 ||
		routes.enrollmentUnregisterCount != 1 {
		t.Fatalf("workspace rotation route counts = %#v", routes)
	}

	cfg.Nodes.Enabled = false
	if err := runtime.Reconcile(cfg); err != nil {
		t.Fatal(err)
	}
	if runtime.mounted || runtime.registry != nil || runtime.registryPath != "" || runtime.sessions != nil ||
		routes.handler != nil || routes.enrollmentHandler != nil || routes.unregisterCount != 2 ||
		routes.enrollmentUnregisterCount != 2 {
		t.Fatalf("disabled runtime = %#v, routes = %#v", runtime, routes)
	}
}

func TestNodeAdmissionRuntimeRollsBackEnrollmentRouteFailures(t *testing.T) {
	t.Run("initial mount", func(t *testing.T) {
		routes := &fakeNodeAdmissionRoutes{enrollmentRegisterErr: errors.New("enrollment unavailable")}
		runtime := &nodeAdmissionRuntime{routes: routes}
		cfg := config.DefaultConfig()
		cfg.Nodes.Enabled = true
		cfg.Agents.Defaults.Workspace = t.TempDir()
		if err := runtime.Reconcile(cfg); err == nil || !strings.Contains(err.Error(), "enrollment unavailable") {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if runtime.mounted || runtime.handler != nil || routes.handler != nil || routes.enrollmentHandler != nil ||
			routes.unregisterCount != 1 {
			t.Fatalf("failed initial runtime = %#v, routes = %#v", runtime, routes)
		}
	})

	t.Run("replacement", func(t *testing.T) {
		routes := &fakeNodeAdmissionRoutes{}
		runtime := &nodeAdmissionRuntime{routes: routes}
		cfg := config.DefaultConfig()
		cfg.Nodes.Enabled = true
		cfg.Agents.Defaults.Workspace = t.TempDir()
		if err := runtime.Reconcile(cfg); err != nil {
			t.Fatal(err)
		}
		oldHandler := runtime.handler
		oldEnrollmentHandler := runtime.enrollmentHandler
		oldOffers := runtime.enrollmentOffers
		routes.enrollmentReplaceErr = errors.New("enrollment unavailable")
		cfg.Nodes.AllowLoopbackPlaintext = true
		if err := runtime.Reconcile(cfg); err == nil || !strings.Contains(err.Error(), "enrollment unavailable") {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if runtime.handler != oldHandler || runtime.enrollmentHandler != oldEnrollmentHandler ||
			runtime.enrollmentOffers != oldOffers || routes.handler != oldHandler ||
			routes.enrollmentHandler != oldEnrollmentHandler {
			t.Fatalf("failed replacement runtime = %#v, routes = %#v", runtime, routes)
		}
	})
}
