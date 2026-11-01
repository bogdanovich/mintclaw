package gateway

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/internal/evaltarget"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestOwnedBudgetFlagIsHiddenAndFailsBeforeStartup(t *testing.T) {
	spec := evaltarget.Spec{
		Version: evaltarget.Version, Nonce: "11111111-1111-4111-8111-111111111111",
		Limits: providers.CallBudgetLimits{
			MaxCalls: 1, MaxInputBytes: 1024, MaxOutputTokens: 128,
			MaxReportedTokens: 1000, MaxInFlight: 1,
		},
	}
	encoded, err := spec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--" + evaltarget.FlagName, "private-secret-value"},
		{"--" + evaltarget.FlagName, ""},
		{"--" + evaltarget.FlagName, encoded, "--debug"},
		{"--" + evaltarget.FlagName, encoded, "--allow-empty"},
	} {
		command := NewGatewayCommand()
		command.SetOut(io.Discard)
		command.SetErr(io.Discard)
		flag := command.Flags().Lookup(evaltarget.FlagName)
		if flag == nil || !flag.Hidden || strings.Contains(command.UsageString(), evaltarget.FlagName) {
			t.Fatal("private child protocol advertised as an ordinary operator flag")
		}
		command.SetArgs(args)
		if err := command.Execute(); !errors.Is(err, evaltarget.ErrBudgetProtocol) ||
			strings.Contains(err.Error(), "private-secret-value") {
			t.Fatalf("invalid owned mode = %v", err)
		}
	}
}
