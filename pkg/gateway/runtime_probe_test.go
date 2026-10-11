package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
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
