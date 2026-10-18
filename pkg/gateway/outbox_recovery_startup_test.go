package gateway

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/netbind"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/state"
)

func TestGatewayStartupDistinguishesIsolatedAndFatalOutboxRecords(t *testing.T) {
	for _, tc := range []struct {
		name         string
		damaged      []byte
		wantRecovery bool
	}{
		{name: "malformed JSON", damaged: []byte("{private-payload"), wantRecovery: true},
		{name: "unsupported format", damaged: []byte(`{"version":0}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Agents.Defaults.Workspace = t.TempDir()
			cfg.Agents.Defaults.ContextManager = "none"
			cfg.Agents.Defaults.ContextManagerConfig = nil
			store, err := outbox.Open(cfg.WorkspacePath())
			if err != nil {
				t.Fatal(err)
			}
			var damagedID string
			for _, source := range []string{"damaged", "pending"} {
				intent, createErr := outbox.NewMessageIntent(cfg.WorkspacePath(), outbox.Identity{
					SourceID: source, Channel: "telegram", ChatID: "test-chat",
				}, bus.OutboundMessage{Content: "synthetic recovery fixture"}, time.Now())
				if createErr != nil {
					t.Fatal(createErr)
				}
				if _, createErr = store.Create(intent); createErr != nil {
					t.Fatal(createErr)
				}
				if source == "damaged" {
					damagedID = intent.ID
				}
			}
			path := filepath.Join(outbox.Path(cfg.WorkspacePath()), damagedID+".json")
			if err := os.WriteFile(path, tc.damaged, 0o600); err != nil {
				t.Fatal(err)
			}

			messageBus := bus.NewMessageBus()
			t.Cleanup(messageBus.Close)
			runtimeState := state.NewManager(cfg.WorkspacePath())
			loop := agent.NewAgentLoop(cfg, messageBus, &startupBlockedProvider{reason: "not used"},
				agent.WithStateManager(runtimeState))
			t.Cleanup(loop.Close)
			checkpointErr := errors.New("recovered outbox checkpoint reached")
			reached := false
			_, err = setupAndStartServicesWithHooks(context.Background(), cfg, loop, messageBus,
				runtimeState, "test-token", netbind.OpenResult{}, gatewayStartupHooks{
					afterStage: func(stage gatewayStartupStage, _ *services) error {
						if stage == gatewayStartupOutboundOutboxRecovered {
							reached = true
							return checkpointErr
						}
						return nil
					},
				})
			if reached != tc.wantRecovery {
				t.Fatalf("recovery checkpoint reached = %v, want %v; error = %v", reached, tc.wantRecovery, err)
			}
			if tc.wantRecovery && !errors.Is(err, checkpointErr) {
				t.Fatalf("startup error = %v, want checkpoint stop", err)
			}
			if !tc.wantRecovery && (err == nil || !strings.Contains(err.Error(), "recovering outbound outbox")) {
				t.Fatalf("startup error = %v, want fatal outbox recovery", err)
			}
		})
	}
}
