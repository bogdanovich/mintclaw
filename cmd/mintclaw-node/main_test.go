package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	codingscope "github.com/bogdanovich/mintclaw/pkg/coding/scope"
	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/companion"
)

func TestCodingScopesPrintsSafeStableDescriptors(t *testing.T) {
	tempDir := t.TempDir()
	sourceParent := filepath.Join(tempDir, "sources")
	projectRoot := filepath.Join(sourceParent, "project")
	mintclawHome := filepath.Join(tempDir, "mintclaw-home")
	for _, path := range []string{projectRoot, mintclawHome} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if output, initErr := exec.Command("git", "-C", projectRoot, "init").CombinedOutput(); initErr != nil {
		t.Fatalf("initialize project fixture: %v: %s", initErr, output)
	}
	workerExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := companion.Config{
		GatewayURL: "wss://gateway.example/nodes/v1/ws",
		StateDir:   filepath.Join(tempDir, "node-state"),
		CodingScopes: map[string]companion.CodingScopePolicy{
			"mintclaw": {
				Revision: "operator-v1", Kind: codingscope.KindGitProject,
				SourceParent: sourceParent, Root: projectRoot,
				AllowedProfiles:  []codingtask.TaskMode{codingtask.TaskModeInvestigate},
				WorkerExecutable: workerExecutable, WorkerProtocolVersion: companion.CodingWorkerProtocolV2,
				MintClawHome: mintclawHome, CredentialSource: companion.CodingCredentialSourceNative,
				ProviderProfile: companion.CodingProviderProfileDefault, Provider: "openai", Model: "test-model",
			},
		},
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tempDir, "config.json")
	if err = os.WriteFile(configPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	var first bytes.Buffer
	if err = codingScopes([]string{"--config", configPath}, &first); err != nil {
		t.Fatal(err)
	}
	var descriptors []companion.CodingScopeDescriptor
	if err = json.Unmarshal(first.Bytes(), &descriptors); err != nil {
		t.Fatalf("decode descriptors: %v: %s", err, first.String())
	}
	if len(descriptors) != 1 || descriptors[0].Alias != "mintclaw" ||
		len(descriptors[0].Revision) != 64 ||
		descriptors[0].Kind != codingscope.KindGitProject ||
		len(descriptors[0].AllowedProfiles) != 1 ||
		descriptors[0].AllowedProfiles[0] != codingtask.TaskModeInvestigate {
		t.Fatalf("descriptors = %#v", descriptors)
	}
	for _, private := range []string{projectRoot, mintclawHome, workerExecutable, "test-model", "openai"} {
		if strings.Contains(first.String(), private) {
			t.Fatalf("descriptor output contains private value %q: %s", private, first.String())
		}
	}
	var second bytes.Buffer
	if err = codingScopes([]string{"--config", configPath}, &second); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatalf("descriptor output changed:\nfirst: %s\nsecond: %s", first.String(), second.String())
	}
}

func TestManagedNodeHealthUsesCompanionProtocolCatalog(t *testing.T) {
	catalog := nodes.CapabilityCatalog{Commands: []nodes.CommandDescriptor{{
		Name:         "test.info.v1",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer","maximum":60}}}`),
		OutputSchema: json.RawMessage(`{"type":"object"}`),
		Risk:         nodes.RiskRead,
	}}}
	health, err := managedNodeHealth("node_test", "v2.0.0", catalog)
	if err != nil {
		t.Fatal(err)
	}
	want, err := catalog.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if health.CatalogHash != want {
		t.Fatalf("managed health catalog hash = %q; want %q", health.CatalogHash, want)
	}
}
