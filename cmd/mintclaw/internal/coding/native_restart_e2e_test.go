package coding

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/providers/providererrors"
	"github.com/bogdanovich/mintclaw/pkg/session"
	"github.com/bogdanovich/mintclaw/pkg/testharness/llmscenario"
)

func TestNativeCodingAttachmentsRemainLazySelectableAndDiagnosableAcrossRestart(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	now := time.Date(2026, time.August, 30, 20, 0, 0, 0, time.UTC)
	threadID := uuid.NewString()
	png, err := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=",
	)
	if err != nil {
		t.Fatal(err)
	}
	sourceDirectory := filepath.Join(t.TempDir(), "caller-private")
	if err := os.Mkdir(sourceDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	imagePath := filepath.Join(sourceDirectory, "failure.png")
	if err := os.WriteFile(imagePath, png, 0o600); err != nil {
		t.Fatal(err)
	}

	initialProvider := llmscenario.NewScriptedProvider("fixture-model-id", llmscenario.ProviderStep{
		Name: "receive current coding image with text",
		Assert: func(call llmscenario.ProviderCall) error {
			current, ok := lastProviderMessageWithRole(call.Messages, "user")
			if !ok || !strings.Contains(current.Content, "[image:") ||
				!strings.Contains(current.Content, "inspect this screenshot") {
				return fmt.Errorf("current attachment message = %+v", current)
			}
			if !messageHasMediaPrefix(current, "data:image/png;base64,") {
				return fmt.Errorf("current coding image was not attached for vision: %+v", current)
			}
			if providerMessagesContain(call.Messages, sourceDirectory) {
				return fmt.Errorf("provider messages disclosed caller path %q", sourceDirectory)
			}
			withoutMedia := current
			withoutMedia.Media = nil
			if agent.EstimateMessageTokens(current) <= agent.EstimateMessageTokens(withoutMedia) {
				return errors.New("current image was omitted from prompt-size accounting")
			}
			return nil
		},
		Response: llmscenario.TextResponse("recorded the attached screenshot"),
	})
	providersInOrder := []providers.LLMProvider{initialProvider}
	runner := nativeCodingTurnRunner{
		loadConfig: func() (*config.Config, error) { return nativeCodingFixtureConfig(), nil },
		createProvider: func(*config.Config) (providers.LLMProvider, string, error) {
			if len(providersInOrder) == 0 {
				return nil, "", errors.New("unexpected provider construction")
			}
			provider := providersInOrder[0]
			providersInOrder = providersInOrder[1:]
			return provider, "fixture-model-id", nil
		},
	}
	deps := testDependencies(home, project, &now)
	deps.newThreadID = func() string { return threadID }
	deps.turnRunner = runner
	created := string(executeCommand(
		t,
		newCodeCommand(deps),
		"inspect this screenshot",
		"--attach",
		imagePath,
	))
	if !strings.Contains(created, "recorded the attached screenshot") {
		t.Fatalf("initial attachment output = %q", created)
	}
	if err := initialProvider.AssertExhausted(); err != nil {
		t.Fatal(err)
	}

	store, err := thread.NewStore(filepath.Join(home, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	attachments, err := store.ListAttachments(threadID)
	if err != nil || len(attachments) != 1 {
		t.Fatalf("durable attachments = %+v, %v", attachments, err)
	}
	attachment := attachments[0]
	if attachment.Filename != "failure.png" || attachment.ContentType != "image/png" {
		t.Fatalf("durable attachment metadata = %+v", attachment)
	}

	selectProvider := llmscenario.NewScriptedProvider(
		"fixture-model-id",
		llmscenario.ProviderStep{
			Name: "resume without eager historical image",
			Assert: func(call llmscenario.ProviderCall) error {
				if err := llmscenario.RequireToolDefinition("coding_attachment")(call); err != nil {
					return err
				}
				if providerMessagesHaveMediaPrefix(call.Messages, "data:image/") {
					return errors.New("historical coding image was eagerly replayed after restart")
				}
				if !providerMessagesContain(call.Messages, "recorded the attached screenshot") {
					return errors.New("restart lost the canonical attachment turn")
				}
				return nil
			},
			Response: llmscenario.ToolCallResponse(
				"I will find the earlier screenshot.",
				llmscenario.ToolCall("list-image", "coding_attachment", map[string]any{
					"action": "list", "query": "failure.png",
				}),
			),
		},
		llmscenario.ProviderStep{
			Name:   "select exact historical image",
			Assert: llmscenario.RequireLastMessage("tool", attachment.Ref),
			Response: llmscenario.ToolCallResponse(
				"I found it; I will open the exact thread-owned reference.",
				llmscenario.ToolCall("open-image", "coding_attachment", map[string]any{
					"action": "open", "ref": attachment.Ref,
				}),
			),
		},
		llmscenario.ProviderStep{
			Name: "receive only the selected historical image",
			Assert: func(call llmscenario.ProviderCall) error {
				selected, ok := lastProviderMessageWithMediaPrefix(call.Messages, "data:image/png;base64,")
				if !ok {
					return fmt.Errorf(
						"selected historical image was not attached: %s",
						describeProviderMessages(call.Messages),
					)
				}
				withoutMedia := selected
				withoutMedia.Media = nil
				if agent.EstimateMessageTokens(selected) <= agent.EstimateMessageTokens(withoutMedia) {
					return errors.New("selected historical image was omitted from prompt-size accounting")
				}
				return nil
			},
			Response: llmscenario.TextResponse("reopened and inspected the historical screenshot"),
		},
	)
	providersInOrder = append(providersInOrder, selectProvider)
	now = now.Add(time.Hour)
	selected := string(executeCommand(
		t,
		newResumeCommand(deps),
		threadID,
		"--prompt",
		"please inspect the earlier screenshot again",
	))
	if !strings.Contains(selected, "reopened and inspected the historical screenshot") {
		t.Fatalf("selected attachment output = %q", selected)
	}
	if err := selectProvider.AssertExhausted(); err != nil {
		t.Fatal(err)
	}

	blobPath := filepath.Join(store.Root(), "blobs", "sha256", attachment.SHA256[:2], attachment.SHA256)
	if err := os.Remove(blobPath); err != nil {
		t.Fatal(err)
	}
	missingProvider := llmscenario.NewScriptedProvider(
		"fixture-model-id",
		llmscenario.ProviderStep{
			Name: "resume remains usable with missing historical bytes",
			Assert: func(call llmscenario.ProviderCall) error {
				if providerMessagesHaveMediaPrefix(call.Messages, "data:image/") {
					return errors.New("selected image leaked durably into later restart history")
				}
				return nil
			},
			Response: llmscenario.ToolCallResponse(
				"I will reopen the exact earlier reference.",
				llmscenario.ToolCall("open-missing-image", "coding_attachment", map[string]any{
					"action": "open", "ref": attachment.Ref,
				}),
			),
		},
		llmscenario.ProviderStep{
			Name: "receive explicit missing attachment diagnostic",
			Assert: func(call llmscenario.ProviderCall) error {
				if err := llmscenario.RequireLastMessage("tool", "unavailable")(call); err != nil {
					return err
				}
				if providerMessagesHaveMediaPrefix(call.Messages, "data:image/") {
					return errors.New("missing attachment produced image bytes")
				}
				return nil
			},
			Response: llmscenario.TextResponse("the earlier screenshot is unavailable, but the thread remains usable"),
		},
	)
	providersInOrder = append(providersInOrder, missingProvider)
	now = now.Add(time.Hour)
	missing := string(executeCommand(
		t,
		newResumeCommand(deps),
		threadID,
		"--prompt",
		"try that exact screenshot once more",
	))
	if !strings.Contains(missing, "unavailable, but the thread remains usable") {
		t.Fatalf("missing attachment output = %q", missing)
	}
	if err := missingProvider.AssertExhausted(); err != nil {
		t.Fatal(err)
	}
	if len(providersInOrder) != 0 {
		t.Fatalf("provider constructions remaining = %d", len(providersInOrder))
	}
}

func TestNativeCodingCaptionedUnsupportedImagesStayOutOfProviderVision(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		content  []byte
	}{
		{
			name:     "SVG",
			filename: "diagram.svg",
			content:  []byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect width="1" height="1"/></svg>`),
		},
		{
			name:     "corrupt PNG",
			filename: "corrupt.png",
			content:  []byte("not actually a PNG"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			project := t.TempDir()
			now := time.Date(2026, time.August, 30, 21, 0, 0, 0, time.UTC)
			sourceDirectory := filepath.Join(t.TempDir(), "caller-private")
			if err := os.Mkdir(sourceDirectory, 0o700); err != nil {
				t.Fatal(err)
			}
			imagePath := filepath.Join(sourceDirectory, test.filename)
			if err := os.WriteFile(imagePath, test.content, 0o600); err != nil {
				t.Fatal(err)
			}

			provider := llmscenario.NewScriptedProvider("fixture-model-id", llmscenario.ProviderStep{
				Name: "receive caption without unsupported vision media",
				Assert: func(call llmscenario.ProviderCall) error {
					current, ok := lastProviderMessageWithRole(call.Messages, "user")
					if !ok || !strings.Contains(current.Content, "inspect this attachment") {
						return fmt.Errorf("current attachment message = %+v", current)
					}
					if providerMessagesHaveMediaPrefix(call.Messages, "data:image/") {
						return fmt.Errorf("unsupported %s reached provider vision", test.name)
					}
					if providerMessagesContain(call.Messages, sourceDirectory) {
						return fmt.Errorf("provider messages disclosed caller path %q", sourceDirectory)
					}
					return nil
				},
				Response: llmscenario.TextResponse("handled without unsupported vision media"),
			})
			runner := nativeCodingTurnRunner{
				loadConfig: func() (*config.Config, error) { return nativeCodingFixtureConfig(), nil },
				createProvider: func(*config.Config) (providers.LLMProvider, string, error) {
					return provider, "fixture-model-id", nil
				},
			}
			deps := testDependencies(home, project, &now)
			deps.turnRunner = runner
			output := string(executeCommand(
				t,
				newCodeCommand(deps),
				"inspect this attachment",
				"--attach",
				imagePath,
			))
			if !strings.Contains(output, "handled without unsupported vision media") {
				t.Fatalf("unsupported attachment output = %q", output)
			}
			if err := provider.AssertExhausted(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeCodingCommandEditsAndResumesAcrossProcessBoundary(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is unavailable")
	}
	home := t.TempDir()
	project := nativeCodingFixtureProject(t)
	now := time.Date(2026, time.August, 12, 1, 0, 0, 0, time.UTC)
	threadID := uuid.NewString()

	initialProvider := llmscenario.NewScriptedProvider(
		"fixture-model-id",
		llmscenario.ProviderStep{
			Name: "inspect native coding context",
			Assert: func(call llmscenario.ProviderCall) error {
				for _, toolName := range []string{"read_file", "apply_patch", "exec"} {
					if err := llmscenario.RequireToolDefinition(toolName)(call); err != nil {
						return err
					}
				}
				return requireNativeCodingSystem(call, project, threadID, "Status: clean")
			},
			Response: llmscenario.ToolCallResponse(
				"I will inspect the defect.",
				llmscenario.ToolCall("read-calc", "read_file", map[string]any{"path": "calc.go"}),
			),
		},
		llmscenario.ProviderStep{
			Name:   "patch fixture",
			Assert: llmscenario.RequireLastMessage("tool", "return a - b"),
			Response: llmscenario.ToolCallResponse(
				"The operator is wrong.",
				llmscenario.ToolCall("patch-calc", "apply_patch", map[string]any{
					"input": "*** Begin Patch\n*** Update File: calc.go\n@@\n-" +
						"func Add(a, b int) int { return a - b }\n+" +
						"func Add(a, b int) int { return a + b }\n*** End Patch",
				}),
			),
		},
		llmscenario.ProviderStep{
			Name: "observe repository refresh and test",
			Assert: func(call llmscenario.ProviderCall) error {
				if err := requireNativeCodingSystem(call, project, threadID, "Status: dirty"); err != nil {
					return err
				}
				return llmscenario.RequireLastMessage("tool", "File edited")(call)
			},
			Response: llmscenario.ToolCallResponse(
				"The workspace refresh is visible; I will test it.",
				llmscenario.ToolCall("test-calc", "exec", map[string]any{
					"action": "run", "command": "go test ./...",
				}),
			),
		},
		llmscenario.ProviderStep{
			Name:     "finish initial turn",
			Assert:   llmscenario.RequireLastMessage("tool", "ok"),
			Response: llmscenario.TextResponse("fixed Add and verified the fixture"),
		},
	)
	resumeProvider := llmscenario.NewScriptedProvider(
		"fixture-model-id",
		llmscenario.ProviderStep{
			Name: "reopen durable context",
			Assert: func(call llmscenario.ProviderCall) error {
				if err := requireNativeCodingSystem(call, project, threadID, "Status: dirty"); err != nil {
					return err
				}
				if !providerMessagesContain(call.Messages, "fixed Add and verified the fixture") ||
					!providerMessagesContain(call.Messages, "test-calc") {
					return fmt.Errorf(
						"resumed provider did not receive prior decisions and tool journal: %s",
						describeProviderMessages(call.Messages),
					)
				}
				return nil
			},
			Response: llmscenario.ToolCallResponse(
				"I see the prior fix and current dirty workspace; I will add the requested note.",
				llmscenario.ToolCall("write-note", "write_file", map[string]any{
					"path": "FIXED.md", "content": "Add now returns the sum.\n",
				}),
			),
		},
		llmscenario.ProviderStep{
			Name:     "finish resumed turn",
			Assert:   llmscenario.RequireLastMessage("tool", "File written"),
			Response: llmscenario.TextResponse("recorded the follow-up in FIXED.md"),
		},
	)

	providersInOrder := []providers.LLMProvider{initialProvider, resumeProvider}
	runner := nativeCodingTurnRunner{
		loadConfig: func() (*config.Config, error) { return nativeCodingFixtureConfig(), nil },
		createProvider: func(*config.Config) (providers.LLMProvider, string, error) {
			if len(providersInOrder) == 0 {
				return nil, "", errors.New("unexpected provider construction")
			}
			provider := providersInOrder[0]
			providersInOrder = providersInOrder[1:]
			return provider, "fixture-model-id", nil
		},
	}
	deps := testDependencies(home, project, &now)
	deps.newThreadID = func() string { return threadID }
	deps.turnRunner = runner

	created := string(executeCommand(t, newCodeCommand(deps), "fix Add and run tests"))
	if !strings.Contains(created, "fixed Add and verified the fixture") {
		t.Fatalf("plain initial output = %q", created)
	}
	if err := initialProvider.AssertExhausted(); err != nil {
		t.Fatal(err)
	}
	if got := nativeCodingReadFile(t, filepath.Join(project, "calc.go")); !strings.Contains(got, "return a + b") {
		t.Fatalf("initial fixture edit = %q", got)
	}

	// A fresh command constructs and closes a fresh AgentLoop over the same
	// external thread root, matching a real process restart.
	now = now.Add(time.Hour)
	resumed := string(executeCommand(
		t,
		newResumeCommand(deps),
		threadID,
		"--prompt",
		"add a short note documenting the fix",
	))
	if !strings.Contains(resumed, "recorded the follow-up in FIXED.md") {
		t.Fatalf("plain resumed output = %q", resumed)
	}
	if err := resumeProvider.AssertExhausted(); err != nil {
		t.Fatal(err)
	}
	if got := nativeCodingReadFile(t, filepath.Join(project, "FIXED.md")); got != "Add now returns the sum.\n" {
		t.Fatalf("resumed fixture edit = %q", got)
	}
	store, err := thread.NewStore(filepath.Join(home, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := store.Load(threadID)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Model != "fixture-alias" || metadata.Provider != "fixture" {
		t.Fatalf("persisted provider selection = model %q provider %q", metadata.Model, metadata.Provider)
	}
	history := readHistory(t, filepath.Join(home, "coding", "threads", threadID), metadata.SessionKey)
	if countExact(history, "fix Add and run tests") != 1 ||
		countExact(history, "add a short note documenting the fix") != 1 ||
		countExact(history, "fixed Add and verified the fixture") != 1 ||
		countExact(history, "recorded the follow-up in FIXED.md") != 1 {
		t.Fatalf("durable user journal = %#v", history)
	}
	if len(providersInOrder) != 0 {
		t.Fatalf("provider constructions remaining = %d", len(providersInOrder))
	}
}

func TestNativeCodingFailurePreservesInspectableThread(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	now := time.Date(2026, time.August, 12, 3, 0, 0, 0, time.UTC)
	threadID := uuid.NewString()
	provider := llmscenario.NewScriptedProvider("fixture-model-id", llmscenario.ProviderStep{
		Name: "provider failure",
		Err:  errors.New("deterministic provider failure"),
	})
	deps := testDependencies(home, project, &now)
	deps.newThreadID = func() string { return threadID }
	deps.turnRunner = nativeCodingTurnRunner{
		loadConfig: func() (*config.Config, error) { return nativeCodingFixtureConfig(), nil },
		createProvider: func(*config.Config) (providers.LLMProvider, string, error) {
			return provider, "fixture-model-id", nil
		},
	}
	_, runErr := executeCommandError(newCodeCommand(deps), "/help preserve this failed request")
	if runErr == nil || !strings.Contains(runErr.Error(), "remains inspectable") ||
		!strings.Contains(runErr.Error(), "mintclaw resume "+threadID) {
		t.Fatalf("native failure = %v", runErr)
	}
	store, err := thread.NewStore(filepath.Join(home, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := store.Load(threadID)
	if err != nil {
		t.Fatalf("inspectable metadata: %v", err)
	}
	stateRoot, err := store.ThreadRoot(threadID)
	if err != nil {
		t.Fatal(err)
	}
	if history := readHistory(t, stateRoot, metadata.SessionKey); len(history) != 1 ||
		history[0] != "/help preserve this failed request" {
		t.Fatalf("inspectable failed history = %#v", history)
	}
}

func TestNativeCodingRetryableFailureDoesNotUsePersonalFallback(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	now := time.Date(2026, time.August, 12, 3, 30, 0, 0, time.UTC)
	threadID := uuid.NewString()
	var fallbackCalls atomic.Int32
	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(
			w,
			`{"choices":[{"message":{"role":"assistant","content":"personal fallback invoked"},`+
				`"finish_reason":"stop"}]}`,
		)
	}))
	t.Cleanup(fallbackServer.Close)

	rateLimitError := func() error {
		return &providererrors.ProviderError{
			Kind:        providererrors.KindRateLimit,
			HTTPStatus:  http.StatusTooManyRequests,
			SafeMessage: "fixture rate limit",
		}
	}
	provider := llmscenario.NewScriptedProvider(
		"fixture-model-id",
		llmscenario.ProviderStep{Name: "retryable primary failure", Err: rateLimitError()},
		llmscenario.ProviderStep{Name: "retryable primary failure after retry", Err: rateLimitError()},
	)
	cfg := nativeCodingFixtureConfig()
	cfg.Agents.Defaults.ModelFallbacks = []string{"personal-fallback"}
	cfg.ModelList = append(cfg.ModelList, &config.ModelConfig{
		ModelName: "personal-fallback",
		Provider:  "openai",
		Model:     "gpt-personal-fallback",
		APIBase:   fallbackServer.URL,
		APIKeys:   config.SimpleSecureStrings("test-key"),
		Enabled:   true,
	})
	deps := testDependencies(home, project, &now)
	deps.newThreadID = func() string { return threadID }
	deps.turnRunner = nativeCodingTurnRunner{
		loadConfig: func() (*config.Config, error) { return cfg, nil },
		createProvider: func(*config.Config) (providers.LLMProvider, string, error) {
			return provider, "fixture-model-id", nil
		},
	}

	_, runErr := executeCommandError(newCodeCommand(deps), "do not change models")
	if runErr == nil || !strings.Contains(runErr.Error(), "rate_limit") {
		t.Fatalf("retryable primary failure = %v", runErr)
	}
	if got := fallbackCalls.Load(); got != 0 {
		t.Fatalf("personal fallback calls = %d, want 0", got)
	}
}

func TestNativeCodingPostTurnConfirmationFailureIsIndeterminate(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	now := time.Date(2026, time.August, 12, 4, 0, 0, 0, time.UTC)
	threadID := uuid.NewString()
	provider := llmscenario.NewScriptedProvider(
		"fixture-model-id",
		llmscenario.ProviderStep{Response: llmscenario.TextResponse("completed before confirmation")},
	)
	reads := 0
	confirmationErr := errors.New("injected confirmation read failure")
	deps := testDependencies(home, project, &now)
	deps.newThreadID = func() string { return threadID }
	deps.turnRunner = nativeCodingTurnRunner{
		loadConfig: func() (*config.Config, error) { return nativeCodingFixtureConfig(), nil },
		createProvider: func(*config.Config) (providers.LLMProvider, string, error) {
			return provider, "fixture-model-id", nil
		},
		readTurnHistory: func(
			ctx context.Context,
			store session.SessionStore,
			sessionKey string,
		) ([]providers.Message, error) {
			reads++
			if reads == 2 {
				return nil, confirmationErr
			}
			return store.ReadTurnHistory(ctx, sessionKey)
		},
	}

	_, runErr := executeCommandError(newCodeCommand(deps), "perform this once")
	if !thread.IsIndeterminatePromptError(runErr) || !errors.Is(runErr, confirmationErr) ||
		!strings.Contains(runErr.Error(), "do not blindly retry") {
		t.Fatalf("confirmation failure = %v", runErr)
	}
	store, err := thread.NewStore(filepath.Join(home, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := store.Load(threadID)
	if err != nil {
		t.Fatal(err)
	}
	stateRoot, err := store.ThreadRoot(threadID)
	if err != nil {
		t.Fatal(err)
	}
	if history := readHistory(t, stateRoot, metadata.SessionKey); countExact(history, "perform this once") != 1 {
		t.Fatalf("durably admitted prompt = %#v", history)
	}
}

func nativeCodingFixtureConfig() *config.Config {
	cfg := config.DefaultConfig()
	// Native coding must override a personal stateless context mode so resume
	// still assembles this thread's canonical history through its own Seahorse.
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.Defaults.ModelName = "fixture-alias"
	cfg.Agents.Defaults.Provider = "fixture"
	cfg.Agents.Defaults.MaxLLMRetries = 1
	cfg.Agents.Defaults.LLMRetryBackoffSecs = 1
	// A threshold of one would route even simple prompts through the personal
	// light model if the isolated coding profile accidentally inherited it.
	cfg.Agents.Defaults.Routing = &config.RoutingConfig{
		Enabled:    true,
		LightModel: "personal-light",
		Threshold:  1,
	}
	cfg.ModelList = config.SecureModelList{
		&config.ModelConfig{
			ModelName: "fixture-alias",
			Provider:  "fixture",
			Model:     "fixture-model-id",
			Enabled:   true,
		},
		&config.ModelConfig{
			ModelName: "personal-light",
			Provider:  "openai",
			Model:     "gpt-light",
			APIBase:   "http://127.0.0.1:1",
			APIKeys:   config.SimpleSecureStrings("test-key"),
			Enabled:   true,
		},
	}
	return cfg
}

func nativeCodingFixtureProject(t *testing.T) string {
	t.Helper()
	project := t.TempDir()
	nativeCodingWriteFile(t, filepath.Join(project, "AGENTS.md"), "Keep changes focused and run Go tests.\n")
	nativeCodingWriteFile(t, filepath.Join(project, "go.mod"), "module example.test/nativefixture\n\ngo 1.25\n")
	nativeCodingWriteFile(t, filepath.Join(project, "calc.go"),
		"package fixture\n\nfunc Add(a, b int) int { return a - b }\n")
	nativeCodingWriteFile(t, filepath.Join(project, "calc_test.go"), `package fixture

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(2, 3); got != 5 {
		t.Fatalf("Add(2, 3) = %d, want 5", got)
	}
}
`)
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "mintclaw-tests@example.invalid"},
		{"config", "user.name", "MintClaw Tests"},
		{"add", "."},
		{"commit", "-m", "fixture"},
	} {
		command := exec.Command("git", args...)
		command.Dir = project
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	canonical, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func requireNativeCodingSystem(
	call llmscenario.ProviderCall,
	project string,
	threadID string,
	workspaceState string,
) error {
	if len(call.Messages) == 0 {
		return errors.New("provider messages are empty")
	}
	system := call.Messages[0].Content
	for _, required := range []string{
		"# MintClaw coding agent",
		"Project root: " + project,
		"Thread ID: " + threadID,
		"Session key: coding:" + threadID,
		"Working directory: " + project,
		"Trust mode: yolo",
		"Provider: fixture",
		"Keep changes focused and run Go tests.",
		"# Live workspace snapshot",
		workspaceState,
	} {
		if !strings.Contains(system, required) {
			return fmt.Errorf("coding system prompt is missing %q: %q", required, system)
		}
	}
	return nil
}

func providerMessagesContain(messages []providers.Message, value string) bool {
	for _, message := range messages {
		if strings.Contains(message.Content, value) {
			return true
		}
		for _, call := range message.ToolCalls {
			if strings.Contains(call.ID, value) || strings.Contains(call.Name, value) {
				return true
			}
		}
	}
	return false
}

func lastProviderMessageWithRole(messages []providers.Message, role string) (providers.Message, bool) {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == role {
			return messages[index], true
		}
	}
	return providers.Message{}, false
}

func messageHasMediaPrefix(message providers.Message, prefix string) bool {
	for _, value := range message.Media {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func providerMessagesHaveMediaPrefix(messages []providers.Message, prefix string) bool {
	_, ok := lastProviderMessageWithMediaPrefix(messages, prefix)
	return ok
}

func lastProviderMessageWithMediaPrefix(
	messages []providers.Message,
	prefix string,
) (providers.Message, bool) {
	for index := len(messages) - 1; index >= 0; index-- {
		if messageHasMediaPrefix(messages[index], prefix) {
			return messages[index], true
		}
	}
	return providers.Message{}, false
}

func describeProviderMessages(messages []providers.Message) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		toolIDs := make([]string, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			toolIDs = append(toolIDs, call.ID)
		}
		parts = append(parts, fmt.Sprintf(
			"role=%s content=%q tool_call_id=%q tool_ids=%v",
			message.Role,
			message.Content,
			message.ToolCallID,
			toolIDs,
		))
	}
	return strings.Join(parts, "; ")
}

func countExact(values []string, want string) int {
	count := 0
	for _, value := range values {
		if value == want {
			count++
		}
	}
	return count
}

func nativeCodingWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func nativeCodingReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

var _ codingTurnRunner = nativeCodingTurnRunner{}
