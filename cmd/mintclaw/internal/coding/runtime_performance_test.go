package coding

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

type codingPerformanceMetrics struct {
	requestNanos    atomic.Int64
	firstTokenNanos atomic.Int64
}

type codingPerformanceProvider struct {
	started time.Time
	metrics *codingPerformanceMetrics
}

func (provider *codingPerformanceProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	elapsed := time.Since(provider.started)
	provider.metrics.requestNanos.Add(elapsed.Nanoseconds())
	provider.metrics.firstTokenNanos.Add(elapsed.Nanoseconds())
	return &providers.LLMResponse{Content: "ready", FinishReason: "stop"}, nil
}

func (provider *codingPerformanceProvider) ChatStreamEvents(
	_ context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
	onChunk func(providers.StreamChunk),
) (*providers.LLMResponse, error) {
	provider.metrics.requestNanos.Add(time.Since(provider.started).Nanoseconds())
	onChunk(providers.StreamChunk{Content: "ready"})
	provider.metrics.firstTokenNanos.Add(time.Since(provider.started).Nanoseconds())
	return &providers.LLMResponse{Content: "ready", FinishReason: "stop"}, nil
}

func (*codingPerformanceProvider) GetDefaultModel() string { return "fixture-model-id" }

func (*codingPerformanceProvider) Capabilities() providers.ProviderCapabilities {
	return providers.ProviderCapabilities{Streaming: true}
}

func BenchmarkNativeCodingRuntimeOpenWarm(b *testing.B) {
	suppressCodingBenchmarkLogs(b)
	home := b.TempDir()
	project := benchmarkCodingProject(b)
	_, store, metadata, lease := benchmarkCodingThread(b, home, project)
	b.Cleanup(func() {
		if err := lease.Release(); err != nil {
			b.Errorf("release lease: %v", err)
		}
	})
	runner := benchmarkCodingRunner(&codingPerformanceMetrics{}, nil)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		runtime, err := openNativeCodingRuntime(
			runner,
			codingTurnRequest{Store: store, Lease: lease, Metadata: metadata},
			nil,
			nil,
			nil,
		)
		if err != nil {
			b.Fatal(err)
		}
		if err := runtime.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNativeCodingFirstStreamingTurn(b *testing.B) {
	suppressCodingBenchmarkLogs(b)
	root := b.TempDir()
	project := benchmarkCodingProject(b)
	metrics := &codingPerformanceMetrics{}
	var provider *codingPerformanceProvider
	runner := benchmarkCodingRunner(metrics, func(created *codingPerformanceProvider) {
		provider = created
	})

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		home := filepath.Join(root, "home-"+thread.NewThreadID())
		if err := os.MkdirAll(home, 0o700); err != nil {
			b.Fatal(err)
		}
		_, store, metadata, lease := benchmarkCodingThread(b, home, project)
		projector, err := frontend.NewProjector(metadata.ThreadID, frontend.ProjectionLimits{})
		if err != nil {
			_ = lease.Release()
			b.Fatal(err)
		}
		runtime, err := openNativeCodingRuntime(
			runner,
			codingTurnRequest{Store: store, Lease: lease, Metadata: metadata},
			projector,
			nil,
			nil,
		)
		if err != nil {
			_ = lease.Release()
			b.Fatal(err)
		}
		provider.started = time.Now()
		b.StartTimer()
		_, err = runtime.runTurn(
			b.Context(),
			frontend.TurnInput{Text: "inspect the bounded fixture"},
			nil,
		)
		b.StopTimer()
		if err != nil {
			_ = runtime.Close()
			_ = lease.Release()
			b.Fatal(err)
		}
		if err := runtime.Close(); err != nil {
			_ = lease.Release()
			b.Fatal(err)
		}
		if err := lease.Release(); err != nil {
			b.Fatal(err)
		}
	}
	if b.N > 0 {
		b.ReportMetric(float64(metrics.requestNanos.Load())/float64(b.N)/1_000, "request-us/op")
		b.ReportMetric(float64(metrics.firstTokenNanos.Load())/float64(b.N)/1_000, "first-token-us/op")
	}
}

func suppressCodingBenchmarkLogs(b *testing.B) {
	b.Helper()
	level := logger.GetLevel()
	logger.SetLevel(logger.ERROR)
	b.Cleanup(func() { logger.SetLevel(level) })
}

func benchmarkCodingRunner(
	metrics *codingPerformanceMetrics,
	observe func(*codingPerformanceProvider),
) nativeCodingTurnRunner {
	return nativeCodingTurnRunner{
		loadConfig: func() (*config.Config, error) { return nativeCodingFixtureConfig(), nil },
		createProvider: func(*config.Config) (providers.LLMProvider, string, error) {
			provider := &codingPerformanceProvider{metrics: metrics}
			if observe != nil {
				observe(provider)
			}
			return provider, "fixture-model-id", nil
		},
	}
}

func benchmarkCodingProject(b *testing.B) string {
	b.Helper()
	project := b.TempDir()
	if err := os.WriteFile(
		filepath.Join(project, "AGENTS.md"),
		[]byte("Keep benchmark work bounded.\n"),
		0o600,
	); err != nil {
		b.Fatal(err)
	}
	return project
}

func benchmarkCodingThread(
	b *testing.B,
	home string,
	project string,
) (thread.ProjectIdentity, *thread.Store, thread.Metadata, *thread.Lease) {
	b.Helper()
	now := time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)
	projectIdentity, store, metadata, lease, err := prepareNewThread(
		b.Context(),
		testDependencies(home, project, &now),
		"",
		"fixture-alias",
		true,
	)
	if err != nil {
		b.Fatal(err)
	}
	return projectIdentity, store, metadata, lease
}
