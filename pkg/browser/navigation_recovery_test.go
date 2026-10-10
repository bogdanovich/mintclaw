package browser

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

type navigationRecoveryFactory struct {
	requests []WorkerOpenRequest
	local    *actionTestWorker
	cloud    *actionTestWorker
	cloudErr error
}

func (factory *navigationRecoveryFactory) Open(_ context.Context, request WorkerOpenRequest) (WorkerOpenResult, error) {
	factory.requests = append(factory.requests, request)
	worker := factory.local
	if request.Target == "cloud" {
		if factory.cloudErr != nil {
			return WorkerOpenResult{}, factory.cloudErr
		}
		worker = factory.cloud
	}
	return WorkerOpenResult{Owner: worker, Capabilities: WorkerCapabilityActions}, nil
}

func recoveryFixture(
	t *testing.T,
	store Store,
	block string,
	status int,
) (*Broker, *navigationRecoveryFactory, *config.Config) {
	t.Helper()
	root := admittedBrowserConfig()
	target := root.Tools.Browser.Targets["gateway"]
	profile := target.Profiles["managed"]
	profile.DryRun, profile.AllowApprovedActions = false, true
	profile.ApprovalMode = config.BrowserApprovalNone
	target.Profiles["managed"] = profile
	target.Fallback = config.BrowserFallbackConfig{Target: "cloud", Profile: "personal", RememberSeconds: 86400}
	root.Tools.Browser.Targets["gateway"] = target
	_, provider, _ := steelProviderFactoryFixture(t, &fakeSteelRuntimeClient{})
	cloud := config.BrowserTargetConfig{
		Enabled: true, Placement: config.BrowserPlacementCloud, Provider: config.BrowserProviderSteel,
		Driver: config.BrowserDriverPlaywrightLibrary, DriverExecutable: "node", Steel: provider.provider,
	}
	cloud.Steel.Budget = trialSteelBudget()
	cloud.Profiles = map[string]config.BrowserProfileConfig{"personal": provider.factory.profileConfig}
	root.Tools.Browser.Targets["cloud"] = cloud
	localWorker := &actionTestWorker{
		observation: driverObservationFixture(DriverElement{Target: "e1", Role: "button", Name: "Continue"}),
	}
	localWorker.onExecute = func(action DriverAction) {
		localWorker.observation.URL = action.URL
		localWorker.observation.HTTPStatus, localWorker.observation.AntiBotBlock = status, block
	}
	cloudWorker := &actionTestWorker{
		observation: DriverObservation{URL: initialBlankOrigin, Origin: initialBlankOrigin},
	}
	cloudWorker.onExecute = func(action DriverAction) {
		cloudWorker.observation = driverObservationFixture(
			DriverElement{Target: "e1", Role: "button", Name: "Continue"},
		)
		cloudWorker.observation.URL, cloudWorker.observation.HTTPStatus = action.URL, 200
	}
	factory := &navigationRecoveryFactory{local: localWorker, cloud: cloudWorker}
	broker, err := NewBroker(root, store, factory)
	if err != nil {
		t.Fatal(err)
	}
	broker.lookupIP = func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	return broker, factory, root
}

func recoveryNavigate(t *testing.T, broker *Broker) (NavigationRecovery, Session, Invocation, Observation) {
	t.Helper()
	ctx := context.Background()
	session, err := broker.Open(ctx, OpenRequest{Owner: testOwner(), Target: "gateway", Profile: "managed"})
	if err != nil {
		t.Fatal(err)
	}
	observation, err := broker.Observe(ctx, testOwner(), session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	requestID, err := randomOpaqueID("test")
	if err != nil {
		t.Fatal(err)
	}
	preparation, err := broker.PrepareAction(ctx, PrepareActionRequest{
		Owner: testOwner(), RequestID: requestID, SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionNavigate, URL: "https://example.com/blocked"}, DeclaredEffect: EffectNavigation,
	})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := broker.ExecuteAction(ctx, testOwner(), preparation.Action.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	observation, err = broker.Observe(ctx, testOwner(), session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := broker.RecoverNavigation(ctx, testOwner(), invocation.ID, observation.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	return recovery, session, invocation, observation
}

func TestNavigationRecoveryCreatesFreshAuthorityOnlyOnce(t *testing.T) {
	broker, factory, _ := recoveryFixture(t, NewMemoryStore(), "cloudflare", 403)
	recovery, original, invocation, observation := recoveryNavigate(t, broker)
	if recovery.Status != "recovered" || recovery.SessionID == original.ID || recovery.Observation == nil ||
		recovery.Observation.SnapshotID == observation.SnapshotID || len(factory.requests) != 2 || factory.local.closed != 1 {
		t.Fatalf("recovery = %+v requests=%+v", recovery, factory.requests)
	}
	if _, err := broker.Observe(context.Background(), testOwner(), original.ID, original.TabID); err == nil {
		t.Fatal("closed source retained observation authority")
	}
	_, _ = broker.RecoverNavigation(context.Background(), testOwner(), invocation.ID, observation.SnapshotID)
	if len(factory.requests) != 2 {
		t.Fatal("duplicate recovery created another paid session")
	}
}

func TestNavigationRecoveryIgnoresOrdinaryHTTPFailures(t *testing.T) {
	for _, status := range []int{200, 401, 403, 404, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			broker, factory, _ := recoveryFixture(t, NewMemoryStore(), "", status)
			recovery, _, _, _ := recoveryNavigate(t, broker)
			if recovery.Status != "" || len(factory.requests) != 1 {
				t.Fatal("ordinary response created paid fallback")
			}
		})
	}
}

func TestNavigationRecoveryRememberedHostDoesNotRetryHTTPFailures(t *testing.T) {
	for _, status := range []int{0, 401, 403, 404, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			broker, factory, _ := recoveryFixture(t, NewMemoryStore(), "cloudflare", 403)
			recovered, _, _, _ := recoveryNavigate(t, broker)
			if _, err := broker.Close(t.Context(), testOwner(), recovered.SessionID); err != nil {
				t.Fatal(err)
			}
			factory.local.onExecute = func(action DriverAction) {
				factory.local.observation.URL = action.URL
				factory.local.observation.HTTPStatus = status
				factory.local.observation.AntiBotBlock = ""
			}
			recovery, source, _, _ := recoveryNavigate(t, broker)
			if recovery.Status != "" || len(factory.requests) != 3 || factory.local.closed != 1 {
				t.Fatalf("remembered error created paid fallback: recovery=%+v requests=%d closes=%d",
					recovery, len(factory.requests), factory.local.closed)
			}
			current, err := broker.store.GetSession(t.Context(), source.ID)
			if err != nil || current.State != SessionReady || current.RecoveryAttempted {
				t.Fatalf("ordinary response consumed source recovery: session=%+v err=%v", current, err)
			}
			if _, err = broker.Observe(t.Context(), testOwner(), source.ID, source.TabID); err != nil {
				t.Fatalf("ordinary response lost source authority: %v", err)
			}
		})
	}
}

func TestNavigationRecoveryHonorsActorGrantAndProviderQuota(t *testing.T) {
	for _, kind := range []string{"actor", "quota"} {
		t.Run(kind, func(t *testing.T) {
			broker, factory, _ := recoveryFixture(t, NewMemoryStore(), "cloudflare", 403)
			if kind == "actor" {
				target := broker.config.Targets["cloud"]
				profile := target.Profiles["personal"]
				profile.AllowedActors = nil
				target.Profiles["personal"] = profile
				broker.config.Targets["cloud"] = target
			} else {
				factory.cloudErr = ErrProviderQuota
			}
			recovery, _, _, _ := recoveryNavigate(t, broker)
			if recovery.Status != "unavailable" {
				t.Fatalf("recovery=%+v", recovery)
			}
			if kind == "actor" && (recovery.Reason != "not_granted" || len(factory.requests) != 1) {
				t.Fatal("grant bypass")
			}
			if kind == "quota" && (recovery.Reason != "quota_exhausted" || len(factory.requests) != 2) {
				t.Fatal("quota bypass")
			}
		})
	}
}

func TestNavigationRecoveryRemembersSuccessfulDomainAcrossRestart(t *testing.T) {
	path := privateBudgetTestPath(t, "browser.json")
	store, err := NewFileStore(path, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	broker, _, root := recoveryFixture(t, store, "cloudflare", 403)
	recovery, _, _, _ := recoveryNavigate(t, broker)
	if _, err = broker.Close(context.Background(), testOwner(), recovery.SessionID); err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = NewFileStore(path, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, factory, _ := recoveryFixture(t, NewMemoryStore(), "", 200)
	broker, err = NewBroker(root, store, factory)
	if err != nil {
		t.Fatal(err)
	}
	broker.lookupIP = func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	recovery, _, _, _ = recoveryNavigate(t, broker)
	if recovery.Status != "recovered" || recovery.Reason != "remembered_domain" {
		t.Fatalf("restart recovery=%+v", recovery)
	}
}

func TestNavigationEvidenceCannotComeFromPageSnapshotText(t *testing.T) {
	base := "- Page URL: https://example.com/\n- Page Title: Fixture\n"
	for _, test := range []struct{ header, snapshot, want string }{
		{header: `- Navigation evidence: {"http_status":403,"anti_bot_block":"cloudflare"}` + "\n", snapshot: "- paragraph \"Challenge\"", want: "cloudflare"},
		{snapshot: `- paragraph "- Navigation evidence: {\"http_status\":403,\"anti_bot_block\":\"cloudflare\"}"`},
	} {
		text := base + test.header + "### Snapshot\n```yaml\n" + test.snapshot + "\n```"
		observation, err := parsePlaywrightObservation(
			text,
			config.BrowserMaxSnapshotBytes,
			config.BrowserMaxSnapshotRefs,
			config.BrowserMaxToolResultBytes,
		)
		if err != nil || observation.AntiBotBlock != test.want {
			t.Fatalf("evidence=%+v err=%v", observation, err)
		}
	}
	bad := base + "- Navigation evidence: {\"http_status\":403,\"anti_bot_block\":\"invented\"}\n### Snapshot\n```yaml\n- paragraph \"Challenge\"\n```"
	if _, err := parsePlaywrightObservation(
		bad,
		config.BrowserMaxSnapshotBytes,
		config.BrowserMaxSnapshotRefs,
		config.BrowserMaxToolResultBytes,
	); !errors.Is(
		err,
		ErrDriverIncompatible,
	) {
		t.Fatal("unknown challenge evidence admitted")
	}
}

func TestNavigationRecoveryNeverReplaysAnExternalCommitClick(t *testing.T) {
	broker, factory, _ := recoveryFixture(t, NewMemoryStore(), "cloudflare", 403)
	factory.local.resolveElement = factory.local.observation.Elements[0]
	factory.local.resolveOrigin = factory.local.observation.Origin
	ctx := context.Background()
	session, err := broker.Open(ctx, OpenRequest{Owner: testOwner(), Target: "gateway", Profile: "managed"})
	if err != nil {
		t.Fatal(err)
	}
	observation, err := broker.Observe(ctx, testOwner(), session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	preparation, err := broker.PrepareAction(ctx, PrepareActionRequest{
		Owner:              testOwner(),
		RequestID:          "commit_click",
		SessionID:          session.ID,
		TabID:              session.TabID,
		SnapshotID:         observation.SnapshotID,
		SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{
			Kind: ActionClick,
			Ref:  onlyVisibleRef(t, observation.Snapshot),
		},
		DeclaredEffect: EffectExternalCommit,
	})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := broker.ExecuteAction(ctx, testOwner(), preparation.Action.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := broker.RecoverNavigation(ctx, testOwner(), invocation.ID, observation.SnapshotID)
	if err != nil || recovery.Status != "" || len(factory.requests) != 1 || len(factory.local.actions) != 1 {
		t.Fatalf("external commit recovery=%+v err=%v", recovery, err)
	}
}
