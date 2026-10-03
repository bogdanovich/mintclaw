package coding

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	"github.com/bogdanovich/mintclaw/pkg/coding/tui"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/testharness/llmscenario"
)

func TestNativeCodingContextPrefixCorpus(t *testing.T) {
	for _, mode := range []string{"interactive", "exec", "resume"} {
		t.Run(mode, func(t *testing.T) {
			home, project := t.TempDir(), t.TempDir()
			path := filepath.Join(project, "corpus.txt")
			if err := os.WriteFile(path, []byte(llmscenario.PrefixMarker), 0o600); err != nil {
				t.Fatal(err)
			}
			corpus := llmscenario.PrefixCorpus{Path: path}
			provider := llmscenario.NewScriptedProvider("fixture-model-id", corpus.Steps()...)
			now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			deps := testDependencies(home, project, &now)
			threadID := deps.newThreadID()
			deps.newThreadID = func() string { return threadID }
			runner := nativeCodingTurnRunner{
				loadConfig: func() (*config.Config, error) { return nativeCodingFixtureConfig(), nil },
				createProvider: func(*config.Config) (providers.LLMProvider, string, error) {
					return provider, "fixture-model-id", nil
				},
			}
			deps.turnRunner = runner
			deps.newController = func(request codingTurnRequest, resumed bool) (frontend.Controller, error) {
				return newNativeCodingControllerWithDependencies(request, resumed,
					frontend.ProjectionLimits{}, runner, deps.now)
			}
			switch mode {
			case "interactive":
				deps.terminal = func(io.Reader, io.Writer, bool) tui.TerminalCapabilities {
					return tui.TerminalCapabilities{Interactive: true}
				}
				deps.runTUI = func(ctx context.Context, controller frontend.Controller, options tui.Options) error {
					defer func() {
						if err := controller.Close(ctx); err != nil {
							t.Errorf("close interactive controller: %v", err)
						}
					}()
					for _, input := range []frontend.TurnInput{options.InitialInput, {Text: llmscenario.PrefixFollowupPrompt}} {
						if err := controller.Submit(ctx, input); err != nil {
							return err
						}
						if err := controller.(frontend.TurnSettler).AwaitTurn(ctx); err != nil {
							return err
						}
						snapshot, err := controller.Snapshot(ctx)
						if err != nil {
							return err
						}
						if len(snapshot.Messages()) < 2 {
							t.Fatal("interactive transcript has no user/assistant projection")
						}
						for _, message := range snapshot.Messages() {
							requireCorpusPresentation(t, message.Text)
						}
					}
					return nil
				}
				requireCorpusPresentation(
					t,
					string(executeCommand(t, newCodeCommand(deps), llmscenario.PrefixInitialPrompt)),
				)
			case "exec":
				requireCorpusPresentation(
					t,
					string(executeCommand(t, newCodeExecCommand(deps), llmscenario.PrefixInitialPrompt)),
				)
				now = now.Add(24 * time.Hour)
				requireCorpusPresentation(t, string(executeCommand(t, newCodeExecCommand(deps), "resume", threadID,
					llmscenario.PrefixFollowupPrompt)))
			case "resume":
				requireCorpusPresentation(
					t,
					string(executeCommand(t, newCodeCommand(deps), llmscenario.PrefixInitialPrompt)),
				)
				now = now.Add(24 * time.Hour)
				requireCorpusPresentation(t, string(executeCommand(t, newResumeCommand(deps), threadID,
					"--prompt", llmscenario.PrefixFollowupPrompt)))
			}
			if err := provider.AssertExhausted(); err != nil {
				t.Fatal(err)
			}
			var requests []llmscenario.RequestSnapshot
			for _, call := range provider.Calls() {
				request, err := llmscenario.SnapshotCall(call)
				if err != nil {
					t.Fatal(err)
				}
				requests = append(requests, request)
			}
			if err := corpus.Check(requests...); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func requireCorpusPresentation(t *testing.T, text string) {
	t.Helper()
	if strings.Contains(text, "mintclaw_turn_context") || strings.Contains(text, "turn_envelope") {
		t.Fatal("presentation exposes hidden turn carrier")
	}
}
