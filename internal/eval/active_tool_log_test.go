package eval

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	"github.com/bogdanovich/mintclaw/pkg/events"
)

func TestActiveToolEnqueueLogRequiresUniqueNativeMetadata(t *testing.T) {
	for _, mode := range []string{
		"observed", "nullable", "missing", "duplicate", "duplicate_key", "wrong_agent", "zero_depth",
		"partial", "private", "nullable_private",
	} {
		t.Run(mode, func(t *testing.T) {
			fixture := newAdmissionFixture(t)
			enableTestActiveTool(fixture)
			host := newTestActiveToolFixture(t)
			endpoint, err := url.Parse(host.url)
			if err != nil {
				t.Fatal(err)
			}
			fixture.preset.ActiveTool.Address = endpoint.Host
			fixture.preset.ActiveTool.Namespace = strings.Split(endpoint.Path, "/")[2]
			path := filepath.Join(fixture.home, "eval-gateway-"+uuid.NewString()+".jsonl")
			target := admittedTarget{
				preset: fixture.preset, config: fixture.cfg, activeTool: host, ownedLog: path,
			}
			args := activeToolArgs{TrialID: uuid.NewString(), Slot: 1}
			if host.register(args.TrialID, "session-a", args.Slot) != nil {
				t.Fatal("register active tool")
			}
			activeCtx, activeCancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer activeCancel()
			go func() { _, _, _ = host.call(activeCtx, nil, args) }()
			if err := host.waitEntered(activeCtx, args); err != nil {
				t.Fatal(err)
			}
			input := Event{
				Kind:      "input",
				Turn:      1,
				RequestID: uuid.NewString(),
				Content:   "synthetic-steer-" + args.TrialID,
			}
			record := activeToolLogEvent{
				ID: uuid.NewString(), Kind: events.KindAgentInterruptReceived.String(), Time: time.Now().UTC(),
				AgentID: "main", SessionKey: "eval-session", TurnID: uuid.NewString(),
				MessageHash: diagnostictrace.FilteredHash(input.Content, nil), InterruptKind: "steering",
				Role: "user", QueueDepth: 1,
			}
			switch mode {
			case "wrong_agent":
				record.AgentID = "other-agent"
			case "zero_depth":
				record.QueueDepth = 0
			case "missing":
				record.MessageHash = diagnostictrace.FilteredHash("other input", nil)
			}
			data, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			data = append(data, '\n')
			switch mode {
			case "nullable":
				data = append([]byte("{\"unrelated\":{\"optional\":null}}\n"), data...)
			case "duplicate":
				data = append(data, data...)
			case "duplicate_key":
				data = append(data, []byte("{\"unrelated\":null,\"unrelated\":1}\n")...)
			case "partial":
				data = append(data, []byte(`{"event_kind":`)...)
			case "private":
				data = append(data, []byte("{\"ignored_field\":\"private-log-canary\"}\n")...)
			case "nullable_private":
				data = append(data, []byte("{\"optional\":null,\"ignored_field\":\"private-log-canary\"}\n")...)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			limit := 2 * time.Second
			if mode == "missing" || mode == "partial" || mode == "duplicate_key" {
				limit = 40 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(t.Context(), limit)
			defer cancel()
			observed, err := waitActiveToolEnqueue(ctx, target, args, input,
				strings.NewReplacer("private-log-canary", "[REDACTED]"))
			if mode == "observed" || mode == "nullable" {
				if err != nil || observed.event.ID != record.ID || observed.raw == "" ||
					observed.barrier.State != "active" || observed.observedAt.IsZero() {
					t.Fatal("native observation missing", observed, err)
				}
			} else if err == nil {
				t.Fatal("missing/invalid native evidence became passing")
			} else if (mode == "private" || mode == "nullable_private") && !errors.Is(err, ErrPrivateEvidence) {
				t.Fatal("unrelated raw private field was projected away", err)
			}
		})
	}
}
