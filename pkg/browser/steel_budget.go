package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"time"

	steelapi "github.com/steel-dev/steel-go"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

// Reserve the maximum exposure before a billable create. Reservations are never
// refunded: an interrupted create or delayed provider bill cannot reopen budget.
type steelBudgetLedger struct {
	Version          int    `json:"version"`
	Day              string `json:"day"`
	Sessions         int    `json:"sessions"`
	Seconds          int    `json:"seconds"`
	ReservedMicroUSD int64  `json:"reserved_micro_usd"`
}

func reserveSteelBudget(path string, budget config.BrowserSteelBudgetConfig, seconds int, now time.Time) error {
	if budget == (config.BrowserSteelBudgetConfig{}) {
		return nil
	}
	if err := prepareStorePath(path); err != nil {
		return ErrProviderUnavailable
	}
	ledger := steelBudgetLedger{Version: 1, Day: now.UTC().Format("2006-01-02")}
	var raw []byte
	file, err := os.Open(path)
	if err == nil {
		raw, err = io.ReadAll(io.LimitReader(file, steelProviderStateBytes+1))
		_ = file.Close()
	}
	if err == nil {
		if len(raw) > steelProviderStateBytes || rejectDuplicateJSONMembers(raw) != nil {
			return ErrProviderUnavailable
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		var previous steelBudgetLedger
		if decoder.Decode(&previous) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
			previous.Version != 1 || previous.Sessions < 0 || previous.Seconds < 0 || previous.ReservedMicroUSD < 0 {
			return ErrProviderUnavailable
		}
		day, parseErr := time.Parse("2006-01-02", previous.Day)
		if parseErr != nil || previous.Day != day.Format("2006-01-02") || previous.Day > ledger.Day {
			return ErrProviderUnavailable
		}
		if previous.Day == ledger.Day {
			ledger = previous
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrProviderUnavailable
	}
	reservation := int64(math.Ceil(budget.MaxSessionCostUSD * 1_000_000))
	daily := int64(math.Floor(budget.MaxDailyCostUSD * 1_000_000))
	if seconds <= 0 || ledger.Sessions >= budget.MaxSessionsPerDay ||
		ledger.Seconds > budget.MaxBillableSecondsPerDay-seconds ||
		ledger.ReservedMicroUSD > daily-reservation {
		return ErrProviderQuota
	}
	ledger.Sessions++
	ledger.Seconds += seconds
	ledger.ReservedMicroUSD += reservation
	raw, err = json.Marshal(ledger)
	if err != nil || writeSecureStoreFile(path, append(raw, '\n'), 0o600) != nil {
		return ErrProviderUnavailable
	}
	return nil
}

type steelCostClient interface {
	SessionCost(context.Context, string) (float64, error)
}

func (client *steelSDKRuntimeClient) SessionCost(ctx context.Context, id string) (float64, error) {
	cost, err := client.client.Sessions.Cost(ctx, id)
	if err != nil {
		return 0, classifySteelAPIError(ctx, err)
	}
	if cost.ID != id || cost.Currency != steelapi.SessionCostResponseCurrencyUsd ||
		cost.Unit != steelapi.SessionCostResponseUnitDollar ||
		cost.JSON.TotalCost.IsNull() || cost.JSON.TotalCost.IsInvalid() ||
		math.IsNaN(cost.TotalCost) || math.IsInf(cost.TotalCost, 0) || cost.TotalCost < 0 {
		return 0, ErrProviderUnavailable
	}
	return cost.TotalCost, nil
}

func (runtimeHandle *steelPlaywrightRuntime) startBudgetMonitor(limit float64) {
	if limit <= 0 {
		return
	}
	client, ok := runtimeHandle.client.(steelCostClient)
	if !ok {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	runtimeHandle.cancelBudget = cancel
	sessionID := runtimeHandle.sessionID
	go func() {
		defer cancel()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		stopping := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !stopping {
					costCtx, cancelCost := context.WithTimeout(ctx, 5*time.Second)
					cost, err := client.SessionCost(costCtx, sessionID)
					cancelCost()
					if ctx.Err() != nil {
						return
					}
					if err == nil && cost < limit {
						continue
					}
					stopping = true
				}
				// Missing cost evidence fails closed. Release remotely even while
				// the driver remains alive; normal broker cleanup still owns the
				// profile flush and exclusive lease. Retry the latched stop decision
				// regardless of any later, cheaper provider cost report.
				if runtimeHandle.stopForBudget() {
					return
				}
			}
		}
	}()
}

func (runtimeHandle *steelPlaywrightRuntime) stopForBudget() bool {
	runtimeHandle.mu.Lock()
	defer runtimeHandle.mu.Unlock()
	if runtimeHandle.closed || runtimeHandle.sessionReleased {
		return true
	}
	if !runtimeHandle.baselineCaptured {
		baselineCtx, cancelBaseline := context.WithTimeout(context.Background(), steelProviderCloseTimeout)
		baseline, err := runtimeHandle.client.GetProfile(baselineCtx, runtimeHandle.profileID)
		cancelBaseline()
		if err == nil &&
			(baseline.Status == string(steelapi.ProfileStatusReady) || baseline.Status == string(steelapi.ProfileStatusUploading)) {
			runtimeHandle.profileBaseline = baseline
			runtimeHandle.baselineCaptured = true
		}
	}
	// A slow profile lookup must not exhaust the remote release deadline.
	ctx, cancel := context.WithTimeout(context.Background(), steelProviderCloseTimeout)
	defer cancel()
	if runtimeHandle.client.ReleaseSession(ctx, runtimeHandle.sessionID) == nil {
		runtimeHandle.sessionReleased = true
		return true
	}
	return false
}
