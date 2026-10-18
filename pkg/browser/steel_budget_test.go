package browser

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	steelapi "github.com/steel-dev/steel-go"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func trialSteelBudget() config.BrowserSteelBudgetConfig {
	return config.BrowserSteelBudgetConfig{
		MaxSessionsPerDay: 3, MaxBillableSecondsPerDay: 900,
		MaxSessionCostUSD: 0.25, MaxDailyCostUSD: 0.75,
	}
}

func privateBudgetTestPath(t *testing.T, name string) string {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, name)
}

func TestSteelBudgetReservationsSurviveRestartAndFailClosed(t *testing.T) {
	path := privateBudgetTestPath(t, "usage.json")
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := reserveSteelBudget(path, trialSteelBudget(), 180, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := reserveSteelBudget(path, trialSteelBudget(), 180, now); !errors.Is(err, ErrProviderQuota) {
		t.Fatalf("fourth restart admission = %v", err)
	}
	if err := reserveSteelBudget(path, trialSteelBudget(), 180, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := reserveSteelBudget(path, trialSteelBudget(), 180, now); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("clock rollback admission = %v", err)
	}
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reserveSteelBudget(path, trialSteelBudget(), 180, now); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("corrupt allowance admission = %v", err)
	}
}

func TestSteelBudgetSecondsAndUSDAdmission(t *testing.T) {
	for _, kind := range []string{"seconds", "usd"} {
		t.Run(kind, func(t *testing.T) {
			budget := trialSteelBudget()
			if kind == "seconds" {
				budget.MaxBillableSecondsPerDay = 180
			} else {
				budget.MaxDailyCostUSD = 0.25
			}
			path := privateBudgetTestPath(t, "usage.json")
			if err := reserveSteelBudget(path, budget, 180, time.Now()); err != nil {
				t.Fatal(err)
			}
			if err := reserveSteelBudget(path, budget, 180, time.Now()); !errors.Is(err, ErrProviderQuota) {
				t.Fatalf("second admission = %v", err)
			}
		})
	}
}

func TestSteelCreateForwardsPaidSettingsWithoutPOSTRetries(t *testing.T) {
	var payload map[string]any
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"ambiguous create"}`))
	}))
	defer server.Close()
	client := &steelSDKRuntimeClient{client: steelapi.NewClientWithBaseURL("test", server.URL)}
	_, err := client.CreateSession(context.Background(), steelProviderSessionRequest{
		Timeout: 180 * time.Second, Inactivity: 60 * time.Second, PersistProfile: true,
		UseProxy: true, ProxyCountry: "US", SolveCaptcha: true,
	})
	if err == nil || calls != 1 || payload["solveCaptcha"] != true || payload["persistProfile"] != true ||
		payload["timeout"] != float64(180000) {
		t.Fatalf("create requests=%d payload=%+v err=%v", calls, payload, err)
	}
	proxy := payload["useProxy"].(map[string]any)
	if proxy["geolocation"].(map[string]any)["country"] != "US" {
		t.Fatalf("proxy = %+v", proxy)
	}
}

func TestSteelCostUsesExactUSDAndRejectsInvalidCurrency(t *testing.T) {
	for _, currency := range []string{"usd", "eur"} {
		t.Run(currency, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id": "session_1", "currency": currency, "unit": "dollar", "totalCost": 0.251234,
				})
			}))
			defer server.Close()
			client := &steelSDKRuntimeClient{client: steelapi.NewClientWithBaseURL("test", server.URL)}
			cost, err := client.SessionCost(context.Background(), "session_1")
			if currency == "usd" && (err != nil || cost != 0.251234) {
				t.Fatalf("cost=%v err=%v", cost, err)
			}
			if currency != "usd" && !errors.Is(err, ErrProviderUnavailable) {
				t.Fatalf("invalid currency error=%v", err)
			}
		})
	}
}

func TestSteelCostDistinguishesPendingFinalUsageWithoutRetries(t *testing.T) {
	for _, status := range []int{http.StatusConflict, http.StatusUnauthorized, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"message":"Session cost is available after the session has been released."}`))
			}))
			defer server.Close()
			client := &steelSDKRuntimeClient{client: steelapi.NewClientWithBaseURL(
				"test", server.URL, steelapi.WithMaxRetries(2),
			)}
			_, err := client.SessionCost(t.Context(), "session_1")
			if err == nil || errors.Is(err, errSteelCostPending) != (status == http.StatusConflict) || calls != 1 {
				t.Fatalf("status=%d error=%v requests=%d", status, err, calls)
			}
		})
	}
}

func TestSteelBudgetStopRetainsCleanupOwnershipAndRetriesRelease(t *testing.T) {
	client := &fakeSteelRuntimeClient{releaseErrs: []error{ErrProviderUnavailable, nil}}
	runtime := &steelPlaywrightRuntime{client: client, sessionID: "session_1", baselineCaptured: true}
	if runtime.stopForBudget() || runtime.sessionReleased || runtime.closed {
		t.Fatal("failed remote release discarded budget cleanup ownership")
	}
	if !runtime.stopForBudget() || !runtime.sessionReleased || runtime.closed || len(client.releaseCalls) != 2 {
		t.Fatal("budget stop failed to retry remote release or discarded normal profile cleanup")
	}
}

type budgetMonitorTestClient struct {
	*fakeSteelRuntimeClient
	costCalls  int
	costError  error
	costErrors []error
	cost       float64
	released   chan struct{}
}

func (client *budgetMonitorTestClient) SessionCost(context.Context, string) (float64, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.costCalls++
	if client.costCalls <= len(client.costErrors) {
		return client.cost, client.costErrors[client.costCalls-1]
	}
	if client.costCalls == 1 {
		return client.cost, client.costError
	}
	return 0, nil
}

func TestSteelBudgetMonitorKeepsPendingSessionAndStopsOnLaterCostEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "threshold"},
		{name: "unexpected_error", err: ErrProviderUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := &budgetMonitorTestClient{
				fakeSteelRuntimeClient: &fakeSteelRuntimeClient{},
				cost:                   0.25, costErrors: []error{errSteelCostPending, errSteelCostPending, tc.err},
				released: make(chan struct{}, 1),
			}
			runtime := &steelPlaywrightRuntime{client: client, sessionID: "session_current", baselineCaptured: true}
			runtime.startBudgetMonitor(0.25)
			t.Cleanup(runtime.cancelBudget)
			select {
			case <-client.released:
			case <-time.After(25 * time.Second):
				t.Fatal("budget monitor did not stop on later threshold or unexpected error")
			}
			client.mu.Lock()
			defer client.mu.Unlock()
			if client.costCalls != 3 || len(client.releaseCalls) != 1 {
				t.Fatalf("pending session was stopped early: cost calls=%d release calls=%d",
					client.costCalls, len(client.releaseCalls))
			}
		})
	}
}

func (client *budgetMonitorTestClient) ReleaseSession(ctx context.Context, sessionID string) error {
	err := client.fakeSteelRuntimeClient.ReleaseSession(ctx, sessionID)
	if err == nil {
		select {
		case client.released <- struct{}{}:
		default:
		}
	}
	return err
}

func TestSteelBudgetMonitorRetriesStopDespiteLaterCheapCost(t *testing.T) {
	for _, reason := range []string{"missing_cost", "threshold"} {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()
			client := &budgetMonitorTestClient{
				fakeSteelRuntimeClient: &fakeSteelRuntimeClient{releaseErrs: []error{ErrProviderUnavailable, nil}},
				cost:                   0.25, released: make(chan struct{}, 1),
			}
			if reason == "missing_cost" {
				client.costError = ErrProviderUnavailable
			}
			runtime := &steelPlaywrightRuntime{client: client, sessionID: "session_current", baselineCaptured: true}
			runtime.startBudgetMonitor(0.25)
			t.Cleanup(runtime.cancelBudget)
			select {
			case <-client.released:
			case <-time.After(20 * time.Second):
				t.Fatal("budget stopping was abandoned after failed remote release")
			}
			client.mu.Lock()
			defer client.mu.Unlock()
			if client.costCalls != 1 || len(client.releaseCalls) != 2 {
				t.Fatalf("stop decision was not latched: cost calls=%d release calls=%d",
					client.costCalls, len(client.releaseCalls))
			}
		})
	}
}

func TestSteelBudgetStopWithMissingBaselineKeepsProfileFenced(t *testing.T) {
	for _, reason := range []string{"lookup_failure", "invalid_status"} {
		t.Run(reason, func(t *testing.T) {
			client := &fakeSteelRuntimeClient{
				createResults: []steelProviderSession{fakeSteelSession("session_current", "profile_personal")},
				profileResults: []steelProviderProfile{
					{Status: "READY", SourceSessionID: "session_previous", UpdatedAt: time.Unix(1, 0)},
					{Status: "INVALID", SourceSessionID: "session_previous", UpdatedAt: time.Unix(1, 0)},
					{Status: "READY", SourceSessionID: "session_current", UpdatedAt: time.Unix(1, 0)},
				},
			}
			if reason == "lookup_failure" {
				client.profileErrs = []error{nil, ErrProviderUnavailable}
			}
			_, provider, stateFile := steelProviderFactoryFixture(t, client)
			if err := writeSteelProviderState(stateFile, "profile_personal"); err != nil {
				t.Fatal(err)
			}
			handle, err := provider.Provision(t.Context(), WorkerOpenRequest{SessionID: "browser_session_1"})
			if err != nil {
				t.Fatal(err)
			}
			runtime := handle.(*steelPlaywrightRuntime)
			t.Cleanup(func() { _ = runtime.lease.Close() })
			if !runtime.stopForBudget() || !runtime.sessionReleased {
				t.Fatal("missing profile evidence must not keep billing alive")
			}
			if err = runtime.Release(
				true,
			); !errors.Is(err, ErrCleanupRequired) || runtime.closed ||
				runtime.profileReady {
				t.Fatalf("stale persisted profile accepted: closed=%v ready=%v err=%v",
					runtime.closed, runtime.profileReady, err)
			}
			if runtime.baselineCaptured || runtime.lease.Validate() != nil {
				t.Fatal("failed baseline capture relinquished profile fencing")
			}
		})
	}
}
