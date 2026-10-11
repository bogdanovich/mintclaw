package browser

import (
	"context"
	"errors"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func proxyRecoveryFixture(t *testing.T) (*Broker, *navigationRecoveryFactory) {
	t.Helper()
	store := NewMemoryStore()
	initial, factory, root := recoveryFixture(t, store, "datadome", 403)
	cloud := root.Tools.Browser.Targets["cloud"]
	cloud.Steel.ProxyOnBlock, cloud.Steel.ProxyCountry = true, "US"
	root.Tools.Browser.Targets["cloud"] = cloud
	factory.proxy = &actionTestWorker{
		observation: DriverObservation{URL: initialBlankOrigin, Origin: initialBlankOrigin},
	}
	factory.proxy.onExecute = func(action DriverAction) {
		factory.proxy.observation = driverObservationFixture()
		factory.proxy.observation.URL, factory.proxy.observation.HTTPStatus = action.URL, 200
	}
	broker, err := NewBroker(root, store, factory)
	if err != nil {
		t.Fatal(err)
	}
	broker.lookupIP = initial.lookupIP
	return broker, factory
}

func TestNavigationProxyUsesFreshBlockAndStopsAfterOneProxy(t *testing.T) {
	for _, test := range []struct {
		name, block, redirect string
		status                int
		wantOpens             int
	}{
		{name: "confirmed_block", block: "datadome", status: 403, wantOpens: 3},
		{name: "direct_success", status: 200, wantOpens: 2},
		{name: "plain_403", status: 403, wantOpens: 2},
		{name: "plain_429", status: 429, wantOpens: 2},
		{name: "missing_evidence", wantOpens: 2},
		{name: "redirected_block", block: "datadome", status: 403, redirect: "https://different.test/", wantOpens: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			broker, factory := proxyRecoveryFixture(t)
			factory.cloud.onExecute = func(action DriverAction) {
				factory.cloud.observation = driverObservationFixture()
				factory.cloud.observation.URL = action.URL
				if test.redirect != "" {
					factory.cloud.observation.URL = test.redirect
				}
				factory.cloud.observation.HTTPStatus = test.status
				factory.cloud.observation.AntiBotBlock = test.block
			}
			recovery, source, invocation, observation := recoveryNavigate(t, broker)
			if len(factory.requests) != test.wantOpens || factory.requests[1].SteelProxy {
				t.Fatalf("unexpected direct/proxy sequence: %+v", factory.requests)
			}
			if test.wantOpens == 3 {
				if !factory.requests[2].SteelProxy || !recovery.UseProxy || recovery.Status != "recovered" ||
					recovery.FromSessionID != source.ID || factory.cloud.closed != 1 || recovery.Observation == nil {
					t.Fatalf("proxy recovery = %+v; direct closes = %d", recovery, factory.cloud.closed)
				}
				proxied, err := broker.store.GetSession(t.Context(), recovery.SessionID)
				if err != nil || !proxied.SteelProxy || proxied.Profile != "personal" {
					t.Fatalf("proxy session = %+v; err = %v", proxied, err)
				}
				_, _ = broker.RecoverNavigation(t.Context(), testOwner(), invocation.ID, observation.SnapshotID)
				if len(factory.requests) != 3 {
					t.Fatal("duplicate source recovery created another Steel session")
				}
			}
		})
	}
}

func TestNavigationProxyBlockedAgainDoesNotLoop(t *testing.T) {
	broker, factory := proxyRecoveryFixture(t)
	for _, worker := range []*actionTestWorker{factory.cloud, factory.proxy} {
		worker.onExecute = func(action DriverAction) {
			worker.observation = driverObservationFixture()
			worker.observation.URL, worker.observation.HTTPStatus = action.URL, 403
			worker.observation.AntiBotBlock = "datadome"
		}
	}
	recovery, _, _, _ := recoveryNavigate(t, broker)
	if recovery.Status != "blocked" || !recovery.UseProxy || len(factory.requests) != 3 {
		t.Fatalf("recovery = %+v; requests = %+v", recovery, factory.requests)
	}
	_, err := broker.RecoverNavigation(t.Context(), testOwner(), recovery.InvocationID, recovery.Observation.SnapshotID)
	if err != nil || len(factory.requests) != 3 {
		t.Fatalf("proxied challenge retried: requests=%d err=%v", len(factory.requests), err)
	}
}

func TestNavigationProxyFailureDoesNotRetry(t *testing.T) {
	for _, failure := range []error{ErrProviderQuota, ErrProviderAuthentication, ErrProviderUnavailable} {
		t.Run(failure.Error(), func(t *testing.T) {
			broker, factory := proxyRecoveryFixture(t)
			factory.cloud.onExecute = func(action DriverAction) {
				factory.cloud.observation = driverObservationFixture()
				factory.cloud.observation.URL, factory.cloud.observation.HTTPStatus = action.URL, 403
				factory.cloud.observation.AntiBotBlock = "datadome"
			}
			factory.proxyErr = failure
			recovery, _, invocation, observation := recoveryNavigate(t, broker)
			if recovery.Status != "unavailable" || len(factory.requests) != 3 || factory.cloud.closed != 1 {
				t.Fatalf("recovery = %+v; requests = %d", recovery, len(factory.requests))
			}
			if errors.Is(failure, ErrProviderQuota) && recovery.Reason != "quota_exhausted" {
				t.Fatal("proxy ignored shared budget denial")
			}
			_, _ = broker.RecoverNavigation(t.Context(), testOwner(), invocation.ID, observation.SnapshotID)
			if len(factory.requests) != 3 {
				t.Fatal("failed proxy creation retried")
			}
		})
	}
}

func TestNavigationProxyCannotBeRequestedWithoutConfiguration(t *testing.T) {
	broker, factory, _ := recoveryFixture(t, NewMemoryStore(), "datadome", 403)
	_, err := broker.Open(context.Background(), OpenRequest{
		Owner: testOwner(), Target: "cloud", Profile: "personal", steelProxy: true,
	})
	if !errors.Is(err, ErrDenied) || len(factory.requests) != 0 {
		t.Fatalf("unconfigured proxy admitted: err=%v requests=%d", err, len(factory.requests))
	}
}

func TestNavigationProxyCapabilityRemainsFullAccessOnly(t *testing.T) {
	broker, factory := proxyRecoveryFixture(t)
	target := broker.config.Targets["cloud"]
	profile := target.Profiles["personal"]
	profile.CapabilityMode = config.BrowserCapabilityRestricted
	target.Profiles["personal"] = profile
	broker.config.Targets["cloud"] = target
	recovery, _, _, _ := recoveryNavigate(t, broker)
	if recovery.Reason != "not_granted" || len(factory.requests) != 1 {
		t.Fatalf("restricted proxy fallback admitted: %+v requests=%d", recovery, len(factory.requests))
	}
}

func TestNavigationProxyRememberedSuccessStillTriesDirectFirst(t *testing.T) {
	broker, factory := proxyRecoveryFixture(t)
	factory.cloud.onExecute = func(action DriverAction) {
		factory.cloud.observation = driverObservationFixture()
		factory.cloud.observation.URL, factory.cloud.observation.HTTPStatus = action.URL, 403
		factory.cloud.observation.AntiBotBlock = "datadome"
	}
	first, _, _, _ := recoveryNavigate(t, broker)
	if !first.UseProxy {
		t.Fatal("fixture did not recover through a proxy")
	}
	if _, err := broker.Close(t.Context(), testOwner(), first.SessionID); err != nil {
		t.Fatal(err)
	}
	factory.local.onExecute = func(action DriverAction) {
		factory.local.observation.URL, factory.local.observation.HTTPStatus = action.URL, 200
		factory.local.observation.AntiBotBlock = ""
	}
	factory.cloud.onExecute = func(action DriverAction) {
		factory.cloud.observation = driverObservationFixture()
		factory.cloud.observation.URL, factory.cloud.observation.HTTPStatus = action.URL, 200
	}
	second, _, _, _ := recoveryNavigate(t, broker)
	if second.Reason != "remembered_domain" || second.UseProxy || len(factory.requests) != 5 ||
		factory.requests[4].SteelProxy {
		t.Fatalf("remembered proxy skipped direct mode: recovery=%+v requests=%+v", second, factory.requests)
	}
}

func TestNavigationProxySupportsExplicitDirectSteelSession(t *testing.T) {
	broker, factory := proxyRecoveryFixture(t)
	factory.cloud.onExecute = func(action DriverAction) {
		factory.cloud.observation = driverObservationFixture()
		factory.cloud.observation.URL, factory.cloud.observation.HTTPStatus = action.URL, 403
		factory.cloud.observation.AntiBotBlock = "datadome"
	}
	session, err := broker.Open(t.Context(), OpenRequest{Owner: testOwner(), Target: "cloud", Profile: "personal"})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := broker.Observe(t.Context(), testOwner(), session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	preparation, err := broker.PrepareAction(t.Context(), PrepareActionRequest{
		Owner: testOwner(), RequestID: "direct_steel_navigation", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observed.SnapshotID, SnapshotGeneration: observed.SnapshotGeneration,
		Action: Action{Kind: ActionNavigate, URL: "https://example.com/blocked"}, DeclaredEffect: EffectNavigation,
	})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := broker.ExecuteAction(t.Context(), testOwner(), preparation.Action.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	observed, err = broker.Observe(t.Context(), testOwner(), session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := broker.RecoverNavigation(t.Context(), testOwner(), invocation.ID, observed.SnapshotID)
	if err != nil || recovery.Status != "recovered" || !recovery.UseProxy || len(factory.requests) != 2 ||
		factory.requests[0].SteelProxy || !factory.requests[1].SteelProxy || recovery.FromSessionID != session.ID {
		t.Fatalf("direct Steel recovery=%+v requests=%+v err=%v", recovery, factory.requests, err)
	}
}
