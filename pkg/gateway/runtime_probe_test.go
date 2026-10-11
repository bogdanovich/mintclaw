package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/health"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

func TestRuntimeProbeObservesActiveConfigNotChangedFile(t *testing.T) {
	harness := newGatewayReloadHarness(t)
	home := t.TempDir()
	configPath := filepath.Join(home, "profile.json")
	if _, err := config.NewRepository(configPath).Save(harness.config); err != nil {
		t.Fatal(err)
	}
	expected, err := config.PublicFingerprint(harness.config)
	if err != nil {
		t.Fatal(err)
	}
	first, err := snapshotRuntimeProbe(home, configPath, harness.loop, harness.services)
	if err != nil || first.ConfigSHA256 != expected || first.PID != os.Getpid() || first.Version != 1 {
		t.Fatal("active runtime identity unavailable", err)
	}
	changed := config.DefaultConfig()
	changed.Agents.Defaults.Workspace = "/not-the-running-workspace"
	if _, err = config.NewRepository(configPath).Save(changed); err != nil {
		t.Fatal(err)
	}
	second, err := snapshotRuntimeProbe(home, configPath, harness.loop, harness.services)
	if err != nil || first.ConfigSHA256 != second.ConfigSHA256 || first.InstanceSHA256 != second.InstanceSHA256 {
		t.Fatal("on-disk edit was confused with active runtime config", err)
	}
	data, err := json.Marshal(second)
	if err != nil || strings.Contains(string(data), home) ||
		strings.Contains(
			string(data),
			"reload-test-token",
		) || strings.Contains(string(data), "/not-the-running-workspace") {
		t.Fatal("probe exposed runtime values")
	}
	harness.services.reloading.Store(true)
	if _, err = snapshotRuntimeProbe(home, configPath, harness.loop, harness.services); err == nil {
		t.Fatal("reload-in-progress observation admitted")
	}
	harness.services.reloading.Store(false)
	if err = os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	if _, err = snapshotRuntimeProbe(home, configPath, harness.loop, harness.services); err == nil ||
		strings.Contains(err.Error(), home) {
		t.Fatal("missing path admitted or echoed")
	}
}

func TestRuntimeProbePhysicalPathIdentity(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Skip("platform does not permit a symlink fixture")
	}
	first, err := physicalPathFingerprint(target)
	if err != nil {
		t.Fatal(err)
	}
	second, err := physicalPathFingerprint(alias)
	if err != nil || first != second {
		t.Fatal("physical path identity was lexical", err)
	}
}

func TestRuntimeProbeAndCompletedReloadCannotMixGenerations(t *testing.T) {
	level := logger.GetLevel()
	defer logger.SetLevel(level)
	logger.SetLevel(logger.WARN)
	harness := newGatewayReloadHarness(t)
	home := t.TempDir()
	configPath := filepath.Join(home, "config.json")
	if _, err := config.NewRepository(configPath).Save(harness.config); err != nil {
		t.Fatal(err)
	}
	oldHash, err := config.PublicFingerprint(harness.config)
	if err != nil {
		t.Fatal(err)
	}
	paused, resume := make(chan struct{}), make(chan struct{})
	var resumeOnce sync.Once
	release := func() { resumeOnce.Do(func() { close(resume) }) }
	ctx, cancel := context.WithCancel(t.Context())
	var workers sync.WaitGroup
	defer func() {
		cancel()
		release()
		workers.Wait()
	}()
	type observation struct {
		probe health.RuntimeProbe
		err   error
	}
	observed := make(chan observation, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		probe, probeErr := snapshotRuntimeProbeWithPaths(home, configPath, harness.loop, harness.services,
			func(name string) (string, error) {
				if name == home {
					close(paused)
					<-resume
				}
				return physicalPathFingerprint(name)
			})
		observed <- observation{probe: probe, err: probeErr}
	}()
	select {
	case <-paused:
	case <-time.After(5 * time.Second):
		t.Fatal("probe did not pause after capturing the config fingerprint")
	}
	if harness.services.runtimeSnapshotMu.TryLock() {
		harness.services.runtimeSnapshotMu.Unlock()
		t.Fatal("probe released reload exclusion before capturing all fields")
	}
	next := *harness.config
	next.Gateway.LogLevel = "error"
	reloaded := make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		reloaded <- handleConfigReloadWithHooks(ctx, harness.loop, &next, &harness.provider,
			harness.services, harness.bus, true, false, serviceShutdownTimeout, gatewayReloadHooks{
				afterStage: func(stage gatewayReloadStage) error {
					if stage == gatewayReloadAgentPrepared {
						if _, probeErr := snapshotRuntimeProbe(home, configPath, harness.loop, harness.services); probeErr == nil {
							return errors.New("probe admitted a reload transition with reloading=false")
						}
					}
					return nil
				},
			})
	}()
	release()
	first := <-observed
	if first.err != nil || first.probe.ConfigSHA256 != oldHash || first.probe.LogLevel != "warn" {
		t.Fatal("in-flight probe mixed reload generations", first.err)
	}
	if err = <-reloaded; err != nil {
		t.Fatal(err)
	}
	second, err := snapshotRuntimeProbe(home, configPath, harness.loop, harness.services)
	newHash, hashErr := config.PublicFingerprint(harness.loop.GetConfig())
	if err != nil || hashErr != nil || second.ConfigSHA256 != newHash || newHash == oldHash ||
		second.LogLevel != "error" {
		t.Fatal("completed reload was not observed as one consistent generation", err, hashErr)
	}
}
