//go:build linux && integration

package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/fileutil"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/companion"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
)

// The controller must start the owned Writer windows and the private input
// host. This test only talks to that companion through signed local WSS.
func TestGatewayComputerAgentWriterWithOwnedCompanion(t *testing.T) {
	dir, node := os.Getenv("MINTCLAW_TEST_WRITER_RUN"), nodes.ID(os.Getenv("MINTCLAW_TEST_WRITER_NODE"))
	if dir == "" && node == "" {
		t.Skip("explicit owned Writer fixture and companion identity required")
	}
	info, err := os.Lstat(dir)
	if !filepath.IsAbs(dir) || err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || node.Validate() != nil {
		t.Fatal("invalid private Writer fixture directory or companion identity")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var marker struct {
		TaskID string `json:"task_id"`
	}
	readComputerWriterFixture(t, root, "fixture/.mintclaw-writer-fixture.json", &marker)
	if marker.TaskID != "CU-T09" {
		t.Fatal("expected the owned CU-T09 Writer fixture")
	}
	var recipe struct {
		ProtectedTexts []string `json:"protected_texts"`
		OriginalText   string   `json:"original_text"`
		Batches        []struct {
			ExpectedText string `json:"expected_text"`
		} `json:"batches"`
	}
	readComputerWriterFixture(t, root, "actual-go-config.json", &recipe)
	last := len(recipe.Batches) - 1
	if last < 2 || last > 15 || len(recipe.ProtectedTexts) != len(recipe.Batches) || recipe.OriginalText == "" ||
		recipe.ProtectedTexts[0] != "" || recipe.ProtectedTexts[last] != "" {
		t.Fatal("invalid private Writer fixture recipe")
	}
	actions := make([]computerAgentFixtureAction, len(recipe.Batches))
	for index, step := range recipe.Batches {
		args := map[string]any{}
		var action map[string]any
		switch index {
		case 0, last:
			key := "end"
			if index == last {
				key = "s"
			}
			action = map[string]any{
				"kind": "key", "key": map[string]any{"name": key, "modifiers": []any{"control"}},
			}
		default:
			if recipe.ProtectedTexts[index] == "" {
				t.Fatal("missing protected text in Writer fixture")
			}
			action = map[string]any{"kind": "insert_text", "text": map[string]any{"element_ref": "fixture_caret"}}
			args["text"] = recipe.ProtectedTexts[index]
		}
		args["action"] = action
		actions[index] = computerAgentFixtureAction{arguments: args, observation: step.ExpectedText}
	}

	workspace := t.TempDir()
	registry, admission, runtime := newVerticalSliceNodeRuntime(t, workspace)
	server := httptest.NewTLSServer(admission)
	defer server.Close()
	defer closeVerticalSliceAdmission(t, admission)
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Nodes.Enabled = true
	cfg.Execution.Targets = map[string]config.ExecutionTarget{"native": {Type: "node", Node: "writer-fixture"}}
	cfg.Tools.Computer = config.ComputerToolsConfig{Enabled: true, Targets: map[string]config.ComputerTargetConfig{
		"desktop": {
			AllowInput: true, NodeTarget: "native", Profile: "owned-fixture", Revision: strings.Repeat("a", 64),
			Actors: []string{computerAgentFixtureActor}, Agents: []string{computerAgentFixtureAgent},
			Runtimes: []runtimecap.Kind{runtimecap.KindGateway}, LeaseSeconds: 120,
		},
	}}
	fingerprint := sha256.Sum256(server.Certificate().Raw)
	endpoint, err := json.Marshal(map[string]string{
		"url":         strings.Replace(server.URL, "https://", "wss://", 1) + companion.GatewayPath,
		"fingerprint": hex.EncodeToString(fingerprint[:]),
		"workspace":   computerNodeID("workspace", cfg.WorkspacePath()),
	})
	path := filepath.Join(dir, "gateway-endpoint.json")
	if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) || err != nil ||
		fileutil.WriteFileAtomic(path, endpoint, 0o600) != nil {
		t.Fatal("failed to publish private Writer fixture endpoint")
	}
	pending := waitForVerticalSliceNodeState(t, registry, nodes.StatePendingPairing)
	if pending.ID != node {
		t.Fatal("unexpected companion pairing")
	}
	if _, err := registry.Approve(node, nodes.PairingApproval{
		Aliases: []nodes.Alias{"writer-fixture"}, At: time.Now().Unix(),
		AllowedCommands: []string{
			nodes.ComputerCommandTargets, nodes.ComputerCommandOpen, nodes.ComputerCommandStatus,
			nodes.ComputerCommandObserve, nodes.ComputerCommandHandoff, nodes.ComputerCommandRequestResume,
			nodes.ComputerCommandResume, nodes.ComputerCommandRelease, nodes.ComputerCommandAct,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if waitForVerticalSliceNodeState(t, registry, nodes.StateConnected).ID != node {
		t.Fatal("unexpected connected companion")
	}
	runComputerAgentFixture(t, cfg, runtime, recipe.OriginalText, actions)
	t.Log("AgentLoop received only computer tools, verified fresh Writer results and released the original session")
}

func readComputerWriterFixture(t *testing.T, root *os.Root, name string, output any) {
	t.Helper()
	file, err := root.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 32*1024 {
		t.Fatal("invalid Writer fixture file")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 32*1024))
	if decoder.Decode(output) != nil || decoder.Decode(new(any)) != io.EOF {
		t.Fatal("invalid Writer fixture document")
	}
}
