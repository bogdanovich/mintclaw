package coding

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/reasoning"
)

type modelSessionProvider struct {
	callerMediated bool
	closeCalls     atomic.Int32
}

func (*modelSessionProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{Content: "ok"}, nil
}

func (*modelSessionProvider) GetDefaultModel() string { return "fixture-model" }

func (provider *modelSessionProvider) Capabilities() providers.ProviderCapabilities {
	return providers.ProviderCapabilities{CallerMediatedTools: provider.callerMediated}
}

func (provider *modelSessionProvider) Close() {
	provider.closeCalls.Add(1)
}

func TestCodingModelSessionPreparationFailuresPreserveCurrentSelection(t *testing.T) {
	t.Run("provider construction", func(t *testing.T) {
		oldProvider := &modelSessionProvider{}
		constructionErr := errors.New("provider construction failed")
		persistCalls := 0
		session := newCodingModelSession(codingModelSessionConfig{
			sourceConfig: modelSessionConfig(),
			createProvider: func(*config.Config) (providers.LLMProvider, string, error) {
				return nil, "", constructionErr
			},
			workspace:        t.TempDir(),
			initial:          modelSessionInitialSnapshot(),
			retainedProvider: oldProvider,
		})

		_, _, changed, err := session.selectModel(
			t.Context(),
			frontend.ModelSelection{Model: "next"},
			func(string, string, string) (thread.Metadata, error) {
				persistCalls++
				return thread.Metadata{}, nil
			},
		)
		if !errors.Is(err, constructionErr) || changed {
			t.Fatalf("selectModel() = changed %t, error %v", changed, err)
		}
		assertModelSessionInitialSnapshot(t, session.snapshot())
		if persistCalls != 0 || oldProvider.closeCalls.Load() != 0 {
			t.Fatalf(
				"failed construction side effects: persist=%d old closes=%d",
				persistCalls,
				oldProvider.closeCalls.Load(),
			)
		}
	})

	t.Run("reviewer construction", func(t *testing.T) {
		oldProvider := &modelSessionProvider{}
		candidate := &modelSessionProvider{callerMediated: true}
		persistCalls := 0
		session := newCodingModelSession(codingModelSessionConfig{
			sourceConfig: modelSessionConfig(),
			createProvider: func(*config.Config) (providers.LLMProvider, string, error) {
				return candidate, "", nil
			},
			workspace:        t.TempDir(),
			initial:          modelSessionInitialSnapshot(),
			retainedProvider: oldProvider,
		})

		_, _, changed, err := session.selectModel(
			t.Context(),
			frontend.ModelSelection{Model: "next"},
			func(string, string, string) (thread.Metadata, error) {
				persistCalls++
				return thread.Metadata{}, nil
			},
		)
		if err == nil || !strings.Contains(err.Error(), "native model ID is required") || changed {
			t.Fatalf("selectModel() = changed %t, error %v", changed, err)
		}
		assertModelSessionInitialSnapshot(t, session.snapshot())
		if persistCalls != 0 || oldProvider.closeCalls.Load() != 0 || candidate.closeCalls.Load() != 1 {
			t.Fatalf(
				"failed reviewer construction side effects: persist=%d old closes=%d candidate closes=%d",
				persistCalls,
				oldProvider.closeCalls.Load(),
				candidate.closeCalls.Load(),
			)
		}
	})
}

func TestCodingModelSessionPersistenceFailureRollsBackPreparedProvider(t *testing.T) {
	oldProvider := &modelSessionProvider{}
	candidate := &modelSessionProvider{callerMediated: true}
	persistErr := errors.New("metadata persistence failed")
	session := newCodingModelSession(codingModelSessionConfig{
		sourceConfig: modelSessionConfig(),
		createProvider: func(*config.Config) (providers.LLMProvider, string, error) {
			return candidate, "next-model", nil
		},
		workspace:        t.TempDir(),
		initial:          modelSessionInitialSnapshot(),
		retainedProvider: oldProvider,
	})

	_, _, changed, err := session.selectModel(
		t.Context(),
		frontend.ModelSelection{Model: "next", ReasoningEffort: "high"},
		func(string, string, string) (thread.Metadata, error) {
			return thread.Metadata{}, persistErr
		},
	)
	if !errors.Is(err, persistErr) || changed {
		t.Fatalf("selectModel() = changed %t, error %v", changed, err)
	}
	assertModelSessionInitialSnapshot(t, session.snapshot())
	if oldProvider.closeCalls.Load() != 0 || candidate.closeCalls.Load() != 1 {
		t.Fatalf(
			"failed persistence closes: old=%d candidate=%d",
			oldProvider.closeCalls.Load(),
			candidate.closeCalls.Load(),
		)
	}
}

func TestCodingModelSessionSuccessfulReplacementOwnsProviderUntilClose(t *testing.T) {
	oldProvider := &modelSessionProvider{}
	candidate := &modelSessionProvider{callerMediated: true}
	session := newCodingModelSession(codingModelSessionConfig{
		sourceConfig: modelSessionConfig(),
		createProvider: func(*config.Config) (providers.LLMProvider, string, error) {
			return candidate, "next-model", nil
		},
		workspace:        t.TempDir(),
		initial:          modelSessionInitialSnapshot(),
		retainedProvider: oldProvider,
	})

	metadata, status, changed, err := session.selectModel(
		t.Context(),
		frontend.ModelSelection{Model: "next", ReasoningEffort: "high"},
		func(model string, provider string, effort string) (thread.Metadata, error) {
			return thread.Metadata{Model: model, Provider: provider, ReasoningEffort: effort}, nil
		},
	)
	if err != nil || !changed {
		t.Fatalf("selectModel() = changed %t, error %v", changed, err)
	}
	if metadata.Model != "next" || metadata.Provider != "fixture" || metadata.ReasoningEffort != "high" {
		t.Fatalf("persisted metadata = %+v", metadata)
	}
	if status.ReasoningEffort != "high" || !status.ReasoningConfigured {
		t.Fatalf("selected status = %+v", status)
	}
	snapshot := session.snapshot()
	if snapshot.model != "next" || snapshot.provider != "fixture" || snapshot.reasoningEffort != "high" ||
		!snapshot.reasoningPinned || !snapshot.modelPinned || snapshot.reviewer == nil {
		t.Fatalf("selected snapshot = %+v", snapshot)
	}
	if oldProvider.closeCalls.Load() != 1 || candidate.closeCalls.Load() != 0 {
		t.Fatalf(
			"replacement closes: old=%d candidate=%d",
			oldProvider.closeCalls.Load(),
			candidate.closeCalls.Load(),
		)
	}

	session.close()
	session.close()
	if oldProvider.closeCalls.Load() != 1 || candidate.closeCalls.Load() != 1 {
		t.Fatalf(
			"final closes: old=%d candidate=%d",
			oldProvider.closeCalls.Load(),
			candidate.closeCalls.Load(),
		)
	}
	if _, _, _, err := session.selectModel(
		t.Context(),
		frontend.ModelSelection{Model: "next"},
		func(string, string, string) (thread.Metadata, error) { return thread.Metadata{}, nil },
	); !errors.Is(err, errCodingModelSessionClosed) {
		t.Fatalf("selection after close error = %v", err)
	}
}

func TestCodingModelSessionCloseWaitsForAtomicSelection(t *testing.T) {
	oldProvider := &modelSessionProvider{}
	candidate := &modelSessionProvider{callerMediated: true}
	persistStarted := make(chan struct{})
	releasePersist := make(chan struct{})
	session := newCodingModelSession(codingModelSessionConfig{
		sourceConfig: modelSessionConfig(),
		createProvider: func(*config.Config) (providers.LLMProvider, string, error) {
			return candidate, "next-model", nil
		},
		workspace:        t.TempDir(),
		initial:          modelSessionInitialSnapshot(),
		retainedProvider: oldProvider,
	})
	selectionDone := make(chan error, 1)
	go func() {
		_, _, _, err := session.selectModel(
			context.Background(),
			frontend.ModelSelection{Model: "next", ReasoningEffort: "high"},
			func(model string, provider string, effort string) (thread.Metadata, error) {
				close(persistStarted)
				<-releasePersist
				return thread.Metadata{Model: model, Provider: provider, ReasoningEffort: effort}, nil
			},
		)
		selectionDone <- err
	}()
	<-persistStarted

	snapshotDone := make(chan codingModelSessionSnapshot, 1)
	go func() { snapshotDone <- session.snapshot() }()
	closeDone := make(chan struct{})
	go func() {
		session.close()
		close(closeDone)
	}()
	assertBlockedModelSessionOperation(t, snapshotDone, "snapshot")
	assertBlockedModelSessionOperation(t, closeDone, "close")

	close(releasePersist)
	if err := <-selectionDone; err != nil {
		t.Fatal(err)
	}
	snapshot := <-snapshotDone
	<-closeDone
	if snapshot.model != "next" || snapshot.provider != "fixture" || snapshot.reasoningEffort != "high" {
		t.Fatalf("snapshot observed a partial replacement: %+v", snapshot)
	}
	if oldProvider.closeCalls.Load() != 1 || candidate.closeCalls.Load() != 1 {
		t.Fatalf(
			"racing close counts: old=%d candidate=%d",
			oldProvider.closeCalls.Load(),
			candidate.closeCalls.Load(),
		)
	}
}

func TestCodingModelSessionSnapshotDetachesRuntimeStatus(t *testing.T) {
	initial := modelSessionInitialSnapshot()
	initial.status.InstructionSources = []frontend.InstructionSource{{Path: "AGENTS.md"}}
	initial.status.Skills = []frontend.SkillSummary{{Name: "fixture"}}
	initial.status.Account = &frontend.ProviderAccount{Provider: "fixture"}
	initial.status.Models = []frontend.ModelOption{{
		Name:      "old",
		Providers: []string{"fixture"},
		ReasoningProfile: reasoning.Profile{
			Options: []reasoning.Option{{ID: reasoning.EffortLow}},
		},
	}}
	session := newCodingModelSession(codingModelSessionConfig{initial: initial})

	snapshot := session.snapshot()
	snapshot.status.InstructionSources[0].Path = "mutated"
	snapshot.status.Skills[0].Name = "mutated"
	snapshot.status.Account.Provider = "mutated"
	snapshot.status.Models[0].Providers[0] = "mutated"
	snapshot.status.Models[0].ReasoningProfile.Options[0].ID = reasoning.EffortHigh

	detached := session.snapshot().status
	if detached.InstructionSources[0].Path != "AGENTS.md" || detached.Skills[0].Name != "fixture" ||
		detached.Account.Provider != "fixture" || detached.Models[0].Providers[0] != "fixture" ||
		detached.Models[0].ReasoningProfile.Options[0].ID != reasoning.EffortLow {
		t.Fatalf("snapshot mutation escaped into session status: %+v", detached)
	}
}

func modelSessionConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = "old"
	cfg.Agents.Defaults.Provider = "fixture"
	cfg.ModelList = config.SecureModelList{
		&config.ModelConfig{
			ModelName: "old",
			Provider:  "fixture",
			Model:     "old-model",
			Enabled:   true,
		},
		&config.ModelConfig{
			ModelName:     "next",
			Provider:      "fixture",
			Model:         "next-model",
			Enabled:       true,
			ThinkingLevel: "high",
			Reasoning: &config.ModelReasoningConfig{
				SupportedEfforts: []reasoning.Effort{reasoning.EffortHigh},
				DefaultEffort:    reasoning.EffortHigh,
			},
		},
	}
	return cfg
}

func modelSessionInitialSnapshot() codingModelSessionSnapshot {
	return codingModelSessionSnapshot{
		model:           "old",
		provider:        "fixture",
		reasoningEffort: string(reasoning.EffortOff),
		status: frontend.RuntimeStatus{
			ReasoningEffort: string(reasoning.EffortOff),
		},
	}
}

func assertModelSessionInitialSnapshot(t *testing.T, snapshot codingModelSessionSnapshot) {
	t.Helper()
	if snapshot.model != "old" || snapshot.provider != "fixture" ||
		snapshot.reasoningEffort != string(reasoning.EffortOff) || snapshot.modelPinned || snapshot.reviewer != nil {
		t.Fatalf("model session changed after failed selection: %+v", snapshot)
	}
}

func assertBlockedModelSessionOperation[T any](t *testing.T, result <-chan T, operation string) {
	t.Helper()
	select {
	case <-result:
		t.Fatalf("%s completed while model selection persistence was in progress", operation)
	case <-time.After(50 * time.Millisecond):
	}
}
