package gateway

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	unregisterHook            func(string)
}

type closeErrorNodeAdmissionHandler struct {
	*fakeNodeAdmissionHandler
	err error
}

func (handler *closeErrorNodeAdmissionHandler) Close(context.Context) error {
	return handler.err
}

type leaseReleasingNodeAdmissionHandler struct {
	*fakeNodeAdmissionHandler
	closeOnce sync.Once
	closed    chan struct{}
}

func newMountedTestNodeAdmissionRuntime() *nodeAdmissionRuntime {
	return &nodeAdmissionRuntime{
		routes:           &fakeNodeAdmissionRoutes{},
		handler:          &fakeNodeAdmissionHandler{},
		enrollmentOffers: nodes.NewEnrollmentOfferManager(nodes.EnrollmentOfferConfig{}),
		generation:       1,
		mounted:          true,
	}
}

func (handler *leaseReleasingNodeAdmissionHandler) Close(context.Context) error {
	handler.closeOnce.Do(func() {
		close(handler.closed)
	})
	return nil
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

func TestNodeAdmissionCloseEndsSessionsBeforeWaitingForGenerationLease(t *testing.T) {
	routes := &fakeNodeAdmissionRoutes{}
	handler := &leaseReleasingNodeAdmissionHandler{
		fakeNodeAdmissionHandler: &fakeNodeAdmissionHandler{},
		closed:                   make(chan struct{}),
	}
	runtime := &nodeAdmissionRuntime{
		routes:           routes,
		handler:          handler,
		registryPath:     "registry.json",
		enrollmentOffers: nodes.NewEnrollmentOfferManager(nodes.EnrollmentOfferConfig{}),
		generation:       1,
		mounted:          true,
	}

	leaseStarted := make(chan struct{})
	releaseLease := make(chan struct{})
	leaseDone := make(chan error, 1)
	go func() {
		leaseDone <- runtime.withInvocationHandler(
			runtime.registryPath,
			runtime.generation,
			func(nodeAdmissionHandler) error {
				close(leaseStarted)
				select {
				case <-handler.closed:
				case <-releaseLease:
				}
				return nil
			},
		)
	}()
	<-leaseStarted

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- runtime.Close(ctx)
	}()

	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		close(releaseLease)
		<-leaseDone
		<-closeDone
		t.Fatal("Close() waited for a generation lease before ending its node sessions")
	}
	close(releaseLease)
	if err := <-leaseDone; err != nil {
		t.Fatalf("leased operation error = %v", err)
	}
	if runtime.mounted || runtime.handler != nil || routes.unregisterCount != 1 ||
		routes.enrollmentUnregisterCount != 1 {
		t.Fatalf("closed runtime = %#v, routes = %#v", runtime, routes)
	}
}

func TestNodeAdmissionCloseEndsSessionsBeforeQueuedStateWriter(t *testing.T) {
	routes := &fakeNodeAdmissionRoutes{}
	handler := &leaseReleasingNodeAdmissionHandler{
		fakeNodeAdmissionHandler: &fakeNodeAdmissionHandler{},
		closed:                   make(chan struct{}),
	}
	runtime := &nodeAdmissionRuntime{
		routes:           routes,
		handler:          handler,
		registryPath:     "registry.json",
		enrollmentOffers: nodes.NewEnrollmentOfferManager(nodes.EnrollmentOfferConfig{}),
		generation:       1,
		mounted:          true,
	}

	leaseStarted := make(chan struct{})
	releaseLease := make(chan struct{})
	leaseDone := make(chan error, 1)
	go func() {
		leaseDone <- runtime.withInvocationHandler(
			runtime.registryPath,
			runtime.generation,
			func(nodeAdmissionHandler) error {
				close(leaseStarted)
				select {
				case <-handler.closed:
				case <-releaseLease:
				}
				return nil
			},
		)
	}()
	<-leaseStarted

	writerStarted := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		close(writerStarted)
		runtime.registryMu.Lock()
		runtime.registryMu.Unlock() //nolint:staticcheck // empty critical section proves queued writer ordering
		close(writerDone)
	}()
	<-writerStarted
	writerQueued := false
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if runtime.registryMu.TryRLock() {
			runtime.registryMu.RUnlock()
			time.Sleep(time.Millisecond)
			continue
		}
		writerQueued = true
		break
	}
	if !writerQueued {
		close(releaseLease)
		<-leaseDone
		<-writerDone
		t.Fatal("state writer did not queue behind the generation lease")
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := runtime.Close(ctx); err != nil {
		close(releaseLease)
		<-leaseDone
		<-writerDone
		t.Fatalf("Close() error = %v", err)
	}
	close(releaseLease)
	if err := <-leaseDone; err != nil {
		t.Fatalf("leased operation error = %v", err)
	}
	<-writerDone
}

func TestNodeAdmissionCloseBoundsGenerationLeaseWait(t *testing.T) {
	routes := &fakeNodeAdmissionRoutes{}
	runtime := &nodeAdmissionRuntime{
		routes:           routes,
		handler:          &fakeNodeAdmissionHandler{},
		registryPath:     "registry.json",
		enrollmentOffers: nodes.NewEnrollmentOfferManager(nodes.EnrollmentOfferConfig{}),
		generation:       1,
		mounted:          true,
	}

	leaseStarted := make(chan struct{})
	releaseLease := make(chan struct{})
	leaseDone := make(chan error, 1)
	go func() {
		leaseDone <- runtime.withInvocationHandler(
			runtime.registryPath,
			runtime.generation,
			func(nodeAdmissionHandler) error {
				close(leaseStarted)
				<-releaseLease
				return nil
			},
		)
	}()
	<-leaseStarted

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	startedAt := time.Now()
	err := runtime.Close(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close() error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		t.Fatalf("Close() exceeded its bounded wait: %s", elapsed)
	}
	if !runtime.mounted || runtime.handler == nil {
		t.Fatal("bounded close discarded state required for a retry")
	}

	close(releaseLease)
	if err := <-leaseDone; err != nil {
		t.Fatalf("leased operation error = %v", err)
	}
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatalf("retry Close() error = %v", err)
	}
	if runtime.mounted || runtime.handler != nil {
		t.Fatal("successful close retry retained node admission state")
	}
}

func TestNodeAdmissionCloseBoundsInitialStateWait(t *testing.T) {
	runtime := &nodeAdmissionRuntime{}
	runtime.registryMu.Lock()

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	startedAt := time.Now()
	err := runtime.Close(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close() error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		t.Fatalf("Close() exceeded its bounded initial state wait: %s", elapsed)
	}

	runtime.registryMu.Unlock()
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatalf("retry Close() error = %v", err)
	}
}

func TestNodeAdmissionCloseBoundsFinalStateRelease(t *testing.T) {
	writerAcquired := make(chan struct{})
	releaseWriter := make(chan struct{})
	var queueWriter sync.Once
	runtime := &nodeAdmissionRuntime{
		handler:          &fakeNodeAdmissionHandler{},
		registryPath:     "registry.json",
		enrollmentOffers: nodes.NewEnrollmentOfferManager(nodes.EnrollmentOfferConfig{}),
		generation:       1,
		mounted:          true,
	}
	routes := &fakeNodeAdmissionRoutes{unregisterHook: func(path string) {
		if path != nodews.Path {
			return
		}
		queueWriter.Do(func() {
			go func() {
				runtime.registryMu.Lock()
				close(writerAcquired)
				<-releaseWriter
				runtime.registryMu.Unlock()
			}()
			<-writerAcquired
		})
	}}
	runtime.routes = routes

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	startedAt := time.Now()
	err := runtime.Close(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		close(releaseWriter)
		t.Fatalf("Close() error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		close(releaseWriter)
		t.Fatalf("Close() exceeded its bounded final state release: %s", elapsed)
	}
	if runtime.handler == nil || runtime.registryPath == "" {
		close(releaseWriter)
		t.Fatal("bounded final state release discarded references required for a retry")
	}

	close(releaseWriter)
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatalf("retry Close() error = %v", err)
	}
	if runtime.handler != nil || runtime.registryPath != "" {
		t.Fatal("successful close retry retained node admission references")
	}
}

func TestNodeAdmissionCloseRejectsStoreInstalledAfterShutdownSnapshot(t *testing.T) {
	storePath := nodes.GatewayInvocationStorePath(t.TempDir())
	installErrors := make(chan error, 1)
	installDone := make(chan struct{})
	var installOnce sync.Once
	runtime := &nodeAdmissionRuntime{
		handler:          &fakeNodeAdmissionHandler{},
		registryPath:     "registry.json",
		enrollmentOffers: nodes.NewEnrollmentOfferManager(nodes.EnrollmentOfferConfig{}),
		generation:       1,
		mounted:          true,
	}
	routes := &fakeNodeAdmissionRoutes{unregisterHook: func(path string) {
		if path != nodews.Path {
			return
		}
		installOnce.Do(func() {
			go func() {
				defer close(installDone)
				_, err := runtime.gatewayInvocationStore(storePath)
				installErrors <- err
			}()
		})
		<-installDone
	}}
	runtime.routes = routes

	if err := runtime.Close(t.Context()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if installErr := <-installErrors; !errors.Is(installErr, errNodeDiscoveryAuthorityUnavailable) {
		t.Fatalf("post-snapshot store install error = %v, want unavailable authority", installErr)
	}
	if runtime.invocationStore != nil || runtime.invocationStorePath != "" {
		t.Fatal("shutdown retained a rejected post-snapshot store")
	}
	if _, statErr := os.Stat(storePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("rejected post-snapshot store stat error = %v, want not exist", statErr)
	}
}

func TestNodeAdmissionCloseRejectsStoreInstallerQueuedBehindFinalCleanup(t *testing.T) {
	storePath := nodes.GatewayInvocationStorePath(t.TempDir())
	unregistered := make(chan struct{})
	var unregisterOnce sync.Once
	runtime := &nodeAdmissionRuntime{
		handler:          &fakeNodeAdmissionHandler{},
		registryPath:     "registry.json",
		enrollmentOffers: nodes.NewEnrollmentOfferManager(nodes.EnrollmentOfferConfig{}),
		generation:       1,
		mounted:          true,
	}
	runtime.routes = &fakeNodeAdmissionRoutes{unregisterHook: func(path string) {
		if path == nodews.Path {
			unregisterOnce.Do(func() { close(unregistered) })
		}
	}}
	oldGeneration := runtime.invocationGeneration()

	runtime.handlerMu.RLock()
	handlerLeaseHeld := true
	defer func() {
		if handlerLeaseHeld {
			runtime.handlerMu.RUnlock()
		}
	}()

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- runtime.Close(t.Context())
	}()
	<-unregistered

	deadline := time.Now().Add(time.Second)
	for runtime.registryMu.TryRLock() {
		runtime.registryMu.RUnlock()
		if time.Now().After(deadline) {
			t.Fatal("final cleanup did not queue for node admission state")
		}
		time.Sleep(time.Millisecond)
	}

	installStarted := make(chan struct{})
	installDone := make(chan error, 1)
	go func() {
		close(installStarted)
		_, err := runtime.gatewayInvocationStore(storePath)
		installDone <- err
	}()
	<-installStarted

	runtime.handlerMu.RUnlock()
	handlerLeaseHeld = false
	if err := <-closeDone; err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if installErr := <-installDone; !errors.Is(installErr, errNodeDiscoveryAuthorityUnavailable) {
		t.Fatalf("post-cleanup store install error = %v, want unavailable authority", installErr)
	}
	if runtime.generation == oldGeneration {
		t.Fatal("shutdown did not advance the admission generation")
	}
	if runtime.invocationStore != nil || runtime.invocationStorePath != "" {
		t.Fatal("final cleanup admitted a queued store installer")
	}
	if _, statErr := os.Stat(storePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("queued store stat error = %v, want not exist", statErr)
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
	runtime := newMountedTestNodeAdmissionRuntime()
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
	runtime := newMountedTestNodeAdmissionRuntime()
	runtime.handler = &closeErrorNodeAdmissionHandler{
		fakeNodeAdmissionHandler: &fakeNodeAdmissionHandler{},
		err:                      nodews.ErrSessionDrainIncomplete,
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
		if routes.unregisterHook != nil {
			routes.unregisterHook(path)
		}
		return
	}
	routes.handler = nil
	routes.unregisterCount++
	if routes.unregisterHook != nil {
		routes.unregisterHook(path)
	}
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
	if _, err := firstOffers.Issue(
		"wss://gateway.example/nodes/v1/ws",
		"",
		time.Minute,
	); !errors.Is(err, nodes.ErrEnrollmentInvalidated) {
		t.Fatalf("reconciled offer manager Issue() error = %v", err)
	}

	secondOffers := runtime.enrollmentOffers
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
	if _, err := secondOffers.Issue(
		"wss://gateway.example/nodes/v1/ws",
		"",
		time.Minute,
	); !errors.Is(err, nodes.ErrEnrollmentInvalidated) {
		t.Fatalf("workspace-rotated offer manager Issue() error = %v", err)
	}

	thirdOffers := runtime.enrollmentOffers
	cfg.Nodes.Enabled = false
	if err := runtime.Reconcile(cfg); err != nil {
		t.Fatal(err)
	}
	if runtime.mounted || runtime.registry != nil || runtime.registryPath != "" || runtime.sessions != nil ||
		routes.handler != nil || routes.enrollmentHandler != nil || routes.unregisterCount != 2 ||
		routes.enrollmentUnregisterCount != 2 {
		t.Fatalf("disabled runtime = %#v, routes = %#v", runtime, routes)
	}
	if _, err := thirdOffers.Issue(
		"wss://gateway.example/nodes/v1/ws",
		"",
		time.Minute,
	); !errors.Is(err, nodes.ErrEnrollmentInvalidated) {
		t.Fatalf("closed offer manager Issue() error = %v", err)
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
		if _, err := oldOffers.Issue("wss://gateway.example/nodes/v1/ws", "", time.Minute); err != nil {
			t.Fatalf("failed replacement invalidated active manager: %v", err)
		}
	})
}
