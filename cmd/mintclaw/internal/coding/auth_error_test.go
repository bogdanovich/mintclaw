package coding

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

type authFailureCodingProvider struct {
	streamCalls int
	chatCalls   int
}

func (provider *authFailureCodingProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	provider.chatCalls++
	return nil, fmt.Errorf("unexpected non-streaming fallback")
}

func (provider *authFailureCodingProvider) ChatStreamEvents(
	_ context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
	_ func(providers.StreamChunk),
) (*providers.LLMResponse, error) {
	provider.streamCalls++
	if provider.streamCalls == 1 {
		return nil, &providers.ProviderError{
			Kind:        providers.ProviderErrorAuthentication,
			SafeMessage: "provider credential refresh failed", RequestID: "private-request-marker",
		}
	}
	return &providers.LLMResponse{Content: "Recovered in the same session."}, nil
}

func (*authFailureCodingProvider) GetDefaultModel() string { return "fixture-model-id" }

func (*authFailureCodingProvider) Capabilities() providers.ProviderCapabilities {
	return providers.ProviderCapabilities{Streaming: true}
}

func TestNativeControllerAuthenticationFailureHasOneSafeRecoveryMessage(t *testing.T) {
	for _, method := range []string{"oauth", ""} {
		t.Run("auth_method="+method, func(t *testing.T) {
			t.Setenv("MINTCLAW_HOME", t.TempDir())
			t.Setenv("MINTCLAW_AUTH_FILE", filepath.Join(t.TempDir(), "auth.json"))
			store, lease, metadata := newRuntimeAttachmentThread(t)
			metadata.Model, metadata.Provider = "fixture-alias", "openai"
			if err := store.Save(metadata); err != nil {
				t.Fatal(err)
			}
			baseline, err := codingworkspace.NewRepository(
				metadata.Project.ProjectRoot, metadata.Project.InvocationCWD, codingworkspace.Limits{},
			).CaptureBaseline(t.Context(), codingworkspace.BaselineRequest{
				ProjectKey: metadata.Project.ProjectKey, CapturedAt: time.Now().UTC(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if err = store.PublishRepositoryBaseline(t.Context(), lease, metadata, baseline); err != nil {
				t.Fatal(err)
			}
			cfg := nativeCodingFixtureConfig()
			cfg.ModelList = config.SecureModelList{&config.ModelConfig{
				ModelName: metadata.Model, Provider: metadata.Provider,
				Model: "fixture-model-id", Enabled: true, AuthMethod: method,
				Streaming: config.ModelStreamingConfig{Enabled: true},
			}}
			provider := &authFailureCodingProvider{}
			controller, err := newNativeCodingControllerWithDependencies(
				codingTurnRequest{Store: store, Lease: lease, Metadata: metadata},
				false,
				frontend.ProjectionLimits{},
				nativeCodingTurnRunner{
					loadConfig: func() (*config.Config, error) { return cfg, nil },
					createProvider: func(*config.Config) (providers.LLMProvider, string, error) {
						return provider, "fixture-model-id", nil
					},
				},
				time.Now,
			)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = controller.Close(context.Background()) })
			if err = controller.Submit(t.Context(), frontend.TurnInput{Text: "Describe the repository."}); err != nil {
				t.Fatal(err)
			}
			settler := controller.(frontend.TurnSettler)
			if err = settler.AwaitTurn(t.Context()); err == nil {
				t.Fatal("authentication failure settled successfully")
			}
			snapshot, err := controller.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var failures []string
			for _, item := range snapshot.Items {
				if item.Kind == frontend.PresentationError && item.Message != nil {
					failures = append(failures, item.Message.Text)
				}
			}
			if len(failures) != 1 || !strings.Contains(failures[0], "OpenAI authentication failed") {
				t.Fatalf("user-visible errors = %q, want one actionable authentication error", failures)
			}
			command := "mintclaw auth login --provider openai --device-code"
			if strings.Contains(failures[0], command) != (method == "oauth") {
				t.Fatalf("wrong recovery for auth method %q: %q", method, failures[0])
			}
			if strings.Contains(fmt.Sprintf("%+v", snapshot), "private-request-marker") {
				t.Fatal("provider diagnostic data reached presentation state")
			}
			if provider.streamCalls != 1 || provider.chatCalls != 0 {
				t.Fatalf("auth request replayed: streaming=%d chat=%d", provider.streamCalls, provider.chatCalls)
			}
			if err = controller.Submit(t.Context(), frontend.TurnInput{Text: "Try again."}); err != nil {
				t.Fatal(err)
			}
			if err = settler.AwaitTurn(t.Context()); err != nil {
				t.Fatal(err)
			}
			snapshot, err = controller.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.LastTurn == nil || snapshot.LastTurn.Outcome != frontend.TurnOutcomeCompleted ||
				provider.streamCalls != 2 || provider.chatCalls != 0 {
				t.Fatalf("same-session recovery = %+v, streaming=%d chat=%d",
					snapshot.LastTurn, provider.streamCalls, provider.chatCalls)
			}
		})
	}
}
