package browser

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

type seamTestRuntime struct {
	profile      config.BrowserProfileConfig
	releaseCalls []bool
	releaseErr   error
}

func (*seamTestRuntime) playwrightRuntime() {}

func (runtime *seamTestRuntime) ProfileConfig() config.BrowserProfileConfig {
	return runtime.profile
}

func (*seamTestRuntime) NetworkProxy() *browserNetworkProxy { return nil }

func (*seamTestRuntime) EphemeralRuntime() *ephemeralRuntimeLease { return nil }

func (runtime *seamTestRuntime) Release(driverStopped bool) error {
	runtime.releaseCalls = append(runtime.releaseCalls, driverStopped)
	return runtime.releaseErr
}

type seamTestProvider struct {
	runtime playwrightRuntime
	err     error
	calls   int
}

func (provider *seamTestProvider) Provision(
	context.Context,
	WorkerOpenRequest,
) (playwrightRuntime, error) {
	provider.calls++
	return provider.runtime, provider.err
}

type seamTestDriver struct {
	opened playwrightDriverOpenResult
	err    error
	calls  int
	seen   playwrightRuntime
}

func (driver *seamTestDriver) Open(
	_ context.Context,
	_ WorkerOpenRequest,
	runtimeHandle playwrightRuntime,
) (playwrightDriverOpenResult, error) {
	driver.calls++
	driver.seen = runtimeHandle
	return driver.opened, driver.err
}

type seamTestWorker struct {
	closed int
}

func (*seamTestWorker) Status(context.Context) (WorkerStatus, error) {
	return WorkerReady, nil
}

func (worker *seamTestWorker) Close(context.Context) error {
	worker.closed++
	return nil
}

func seamTestFactory(t *testing.T) *PlaywrightWorkerFactory {
	t.Helper()
	factory, err := NewPlaywrightWorkerFactory(runtimeAdmittedBrowserConfig(t, false))
	if err != nil {
		t.Fatal(err)
	}
	return factory
}

func seamTestOpenRequest() WorkerOpenRequest {
	return WorkerOpenRequest{
		SessionID: "session_seam", Target: "gateway", Profile: "managed",
		ProfileRevision: "managed-v1", DryRun: true,
	}
}

func TestPlaywrightSeamsProvisionBeforeDriverAndTransferOwnership(t *testing.T) {
	factory := seamTestFactory(t)
	runtimeHandle := &seamTestRuntime{profile: factory.profileConfig}
	provider := &seamTestProvider{runtime: runtimeHandle}
	worker := &seamTestWorker{}
	driver := &seamTestDriver{opened: playwrightDriverOpenResult{Owner: worker}}
	factory.runtime = provider
	factory.driver = driver

	opened, err := factory.Open(t.Context(), seamTestOpenRequest())
	if err != nil || opened.Owner != worker {
		t.Fatalf("Open() = %#v, %v", opened, err)
	}
	if provider.calls != 1 || driver.calls != 1 || driver.seen != runtimeHandle {
		t.Fatalf(
			"seam calls provider=%d driver=%d runtime=%T",
			provider.calls,
			driver.calls,
			driver.seen,
		)
	}
	if len(runtimeHandle.releaseCalls) != 0 {
		t.Fatalf("runtime released before worker ownership: %#v", runtimeHandle.releaseCalls)
	}
}

func TestPlaywrightSeamsDoNotInvokeDriverAfterProvisionFailure(t *testing.T) {
	factory := seamTestFactory(t)
	provider := &seamTestProvider{err: ErrWorkerUnavailable}
	driver := &seamTestDriver{}
	factory.runtime = provider
	factory.driver = driver

	opened, err := factory.Open(t.Context(), seamTestOpenRequest())
	if opened.Owner != nil || !errors.Is(err, ErrWorkerUnavailable) ||
		provider.calls != 1 || driver.calls != 0 {
		t.Fatalf(
			"Open() = %#v, %v; provider=%d driver=%d",
			opened,
			err,
			provider.calls,
			driver.calls,
		)
	}
}

func TestPlaywrightSeamsReleaseRuntimeWhenDriverDoesNotOwnCleanup(t *testing.T) {
	for _, test := range []struct {
		name       string
		driverErr  error
		releaseErr error
		wantErr    error
	}{
		{name: "attach failure", driverErr: ErrWorkerUnavailable, wantErr: ErrWorkerUnavailable},
		{name: "missing owner", wantErr: ErrWorkerUnavailable},
		{
			name: "cleanup failure", driverErr: ErrWorkerUnavailable,
			releaseErr: ErrCleanupRequired, wantErr: ErrCleanupRequired,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			factory := seamTestFactory(t)
			runtimeHandle := &seamTestRuntime{
				profile: factory.profileConfig, releaseErr: test.releaseErr,
			}
			factory.runtime = &seamTestProvider{runtime: runtimeHandle}
			factory.driver = &seamTestDriver{
				err: test.driverErr, opened: playwrightDriverOpenResult{RuntimeUntouched: true},
			}

			opened, err := factory.Open(t.Context(), seamTestOpenRequest())
			if opened.Owner != nil || !errors.Is(err, test.wantErr) {
				t.Fatalf("Open() = %#v, %v; want %v", opened, err, test.wantErr)
			}
			if len(runtimeHandle.releaseCalls) != 1 || !runtimeHandle.releaseCalls[0] {
				t.Fatalf("runtime release calls = %#v, want [true]", runtimeHandle.releaseCalls)
			}
		})
	}
}

func TestPlaywrightSeamsFailClosedWhenDriverCannotProveRuntimeUntouched(t *testing.T) {
	factory := seamTestFactory(t)
	runtimeHandle := &seamTestRuntime{
		profile: factory.profileConfig, releaseErr: ErrCleanupRequired,
	}
	factory.runtime = &seamTestProvider{runtime: runtimeHandle}
	factory.driver = &seamTestDriver{err: ErrWorkerUnavailable}

	opened, err := factory.Open(t.Context(), seamTestOpenRequest())
	if opened.Owner != nil || !errors.Is(err, ErrCleanupRequired) {
		t.Fatalf("Open() = %#v, %v; want cleanup-required", opened, err)
	}
	if len(runtimeHandle.releaseCalls) != 1 || runtimeHandle.releaseCalls[0] {
		t.Fatalf("runtime release calls = %#v, want [false]", runtimeHandle.releaseCalls)
	}
}

func TestPlaywrightSeamsLeaveFailedDriverCleanupWithItsOwner(t *testing.T) {
	factory := seamTestFactory(t)
	runtimeHandle := &seamTestRuntime{profile: factory.profileConfig}
	worker := &seamTestWorker{}
	factory.runtime = &seamTestProvider{runtime: runtimeHandle}
	factory.driver = &seamTestDriver{
		opened: playwrightDriverOpenResult{Owner: worker},
		err:    ErrWorkerUnavailable,
	}

	opened, err := factory.Open(t.Context(), seamTestOpenRequest())
	if opened.Owner != worker || !errors.Is(err, ErrWorkerUnavailable) {
		t.Fatalf("Open() = %#v, %v", opened, err)
	}
	if len(runtimeHandle.releaseCalls) != 0 {
		t.Fatalf("factory stole partial-startup cleanup: %#v", runtimeHandle.releaseCalls)
	}
}

func TestLocalPlaywrightRuntimeIsOpaqueAndConfigurationBound(t *testing.T) {
	firstRoot := runtimeAdmittedBrowserConfig(t, false)
	firstFactory, err := NewPlaywrightWorkerFactory(firstRoot)
	if err != nil {
		t.Fatal(err)
	}
	firstTarget := firstRoot.Tools.Browser.Targets[config.BrowserDefaultTarget]
	firstProfile := firstTarget.Profiles[config.BrowserDefaultProfile]
	firstProfile.AllowedAgents[0] = "mutated"
	firstProfile.AllowedActors[0] = "mutated"
	firstTarget.Profiles[config.BrowserDefaultProfile] = firstProfile
	firstRoot.Tools.Browser.Targets[config.BrowserDefaultTarget] = firstTarget
	first, err := firstFactory.runtime.Provision(t.Context(), seamTestOpenRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if releaseErr := first.Release(true); releaseErr != nil {
			t.Errorf("Release(first) error = %v", releaseErr)
		}
	}()
	if encoded, marshalErr := json.Marshal(first); marshalErr == nil || encoded != nil {
		t.Fatalf("json.Marshal(runtime) = %q, %v; want rejected", encoded, marshalErr)
	}

	secondFactory := seamTestFactory(t)
	secondFactory.profileConfig.Revision = "managed-v2"
	second, err := secondFactory.runtime.Provision(t.Context(), WorkerOpenRequest{
		SessionID: "session_replacement", Target: "gateway", Profile: "managed",
		ProfileRevision: "managed-v2", DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if releaseErr := second.Release(true); releaseErr != nil {
			t.Errorf("Release(second) error = %v", releaseErr)
		}
	}()
	if first.ProfileConfig().Revision != "managed-v1" ||
		first.ProfileConfig().AllowedAgents[0] != "browser" ||
		first.ProfileConfig().AllowedActors[0] != "telegram:owner" ||
		second.ProfileConfig().Revision != "managed-v2" ||
		first.ProfileConfig().Runtime.ProfileDirectory == second.ProfileConfig().Runtime.ProfileDirectory {
		t.Fatalf(
			"runtime snapshots first=%#v second=%#v",
			first.ProfileConfig(),
			second.ProfileConfig(),
		)
	}
}

func TestLocalPlaywrightRuntimeDefersEphemeralRemovalUntilDriverStops(t *testing.T) {
	root, runtimeConfig := ephemeralPlaywrightConfig(t, false)
	factory, err := NewPlaywrightProfileWorkerFactory(root, "gateway", "ephemeral")
	if err != nil {
		t.Fatal(err)
	}
	runtimeHandle, err := factory.runtime.Provision(t.Context(), WorkerOpenRequest{
		SessionID: "session_ephemeral_seam", Target: "gateway", Profile: "ephemeral",
		ProfileRevision: "ephemeral-v1", DryRun: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	lease := runtimeHandle.EphemeralRuntime()
	if lease == nil {
		t.Fatal("ephemeral runtime lease = nil")
	}
	path := lease.Path()
	if err = runtimeHandle.Release(false); !errors.Is(err, ErrCleanupRequired) {
		t.Fatalf("Release(false) error = %v, want ErrCleanupRequired", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("ephemeral identity removed before driver stop: %v", statErr)
	}
	if err = runtimeHandle.Release(true); err != nil {
		t.Fatalf("Release(true) error = %v", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("ephemeral identity still exists after release: %v", statErr)
	}
	entries, readErr := os.ReadDir(runtimeConfig.EphemeralRoot)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("ephemeral root entries = %#v, %v", entries, readErr)
	}
}

func TestPlaywrightDriverCloseRetriesBeforeRuntimeRelease(t *testing.T) {
	runtimeHandle := &seamTestRuntime{}
	client := &fakePlaywrightClient{closeErr: errors.New("driver still running")}
	worker := &playwrightWorker{client: client, runtime: runtimeHandle}

	if err := worker.Close(t.Context()); !errors.Is(err, ErrWorkerUnavailable) {
		t.Fatalf("first Close() error = %v", err)
	}
	if len(runtimeHandle.releaseCalls) != 1 || runtimeHandle.releaseCalls[0] {
		t.Fatalf("first runtime release = %#v, want [false]", runtimeHandle.releaseCalls)
	}
	client.closeErr = nil
	if err := worker.Close(t.Context()); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if len(runtimeHandle.releaseCalls) != 2 || !runtimeHandle.releaseCalls[1] {
		t.Fatalf("runtime release retries = %#v, want [false true]", runtimeHandle.releaseCalls)
	}
}

func TestPlaywrightMCPControlDriverConformance(t *testing.T) {
	for _, test := range []struct {
		name          string
		catalog       bool
		pingErr       error
		wantOpenError error
		wantStatus    WorkerStatus
	}{
		{name: "attach and status", catalog: true, wantStatus: WorkerReady},
		{
			name: "incompatible partial startup", wantOpenError: ErrDriverIncompatible,
		},
		{
			name: "driver crash", catalog: true, pingErr: errors.New("driver exited"),
			wantStatus: WorkerLost,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			factory := seamTestFactory(t)
			client := &fakePlaywrightClient{pingErr: test.pingErr}
			if test.catalog {
				client.catalog = playwrightCatalogFixture()
			}
			factory.clientFactory = func() playwrightMCPClient { return client }
			runtimeHandle, err := factory.runtime.Provision(t.Context(), seamTestOpenRequest())
			if err != nil {
				t.Fatal(err)
			}
			opened, openErr := factory.driver.Open(
				t.Context(), seamTestOpenRequest(), runtimeHandle,
			)
			if !errors.Is(openErr, test.wantOpenError) {
				t.Fatalf("driver Open() error = %v, want %v", openErr, test.wantOpenError)
			}
			worker, ok := opened.Owner.(*playwrightWorker)
			if !ok || worker.runtime != runtimeHandle {
				t.Fatalf("driver owner = %T; runtime ownership was not transferred", opened.Owner)
			}
			if test.wantOpenError == nil {
				status, statusErr := worker.Status(t.Context())
				if statusErr != nil || status != test.wantStatus {
					t.Fatalf("Status() = %q, %v; want %q", status, statusErr, test.wantStatus)
				}
			}
			if err = worker.Close(t.Context()); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			local, ok := runtimeHandle.(*localPlaywrightRuntime)
			if !ok || !local.closed || client.closeCalls != 1 {
				t.Fatalf(
					"cleanup local=%T closed=%v client_calls=%d",
					runtimeHandle,
					ok && local.closed,
					client.closeCalls,
				)
			}
		})
	}
}
