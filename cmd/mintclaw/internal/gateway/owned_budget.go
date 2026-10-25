package gateway

import (
	"errors"

	"github.com/bogdanovich/mintclaw/internal/evaltarget"
	"github.com/bogdanovich/mintclaw/pkg/gateway"
)

func runOwnedBudgetGateway(home, configPath, encoded string) (runErr error) {
	spec, budget, err := evaltarget.ParseSpec(encoded)
	if err != nil {
		return err
	}
	recorder, err := evaltarget.NewRecorder(home, spec)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, recorder.Close()) }()
	runErr = gateway.RunWithProviderCallBudget(false, home, configPath, false, budget)
	return errors.Join(runErr, recorder.Record(budget.Seal(), runErr == nil))
}
