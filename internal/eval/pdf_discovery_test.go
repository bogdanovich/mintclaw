package eval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

type pdfDiscoveryProvider struct{ calls int }

func (p *pdfDiscoveryProvider) GetDefaultModel() string { return "eval-model" }

func (p *pdfDiscoveryProvider) Chat(
	context.Context, []providers.Message, []providers.ToolDefinition, string, map[string]any,
) (*providers.LLMResponse, error) {
	p.calls++
	return nil, errors.New("model calls forbidden in native composition control")
}

func TestPDFAdmissionMatchesNativeDocumentDiscovery(t *testing.T) {
	for _, discovery := range []bool{false, true} {
		name := "missing_discovery"
		if discovery {
			name = "native_discovery"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newAdmissionFixture(t)
			fixture.cfg.Agents.Defaults.ContextManager = "none"
			fixture.cfg.Tools.Document.Enabled = true
			fixture.cfg.Tools.Document.AuditModel = "eval-model"
			fixture.preset.Capabilities = append(fixture.preset.Capabilities, "pdf")
			fixture.cfg.Agents.List[0].ToolPolicy.Allow = []string{"document", "request_user_input"}
			if discovery {
				fixture.cfg.Agents.List[0].ToolPolicy.Allow = append(
					fixture.cfg.Agents.List[0].ToolPolicy.Allow, tools.BM25SearchToolName,
				)
			}
			provider := &pdfDiscoveryProvider{}
			messages := bus.NewMessageBus()
			t.Cleanup(messages.Close)
			loop := agent.NewAgentLoop(fixture.cfg, messages, provider, agent.WithIsolatedSkillBootstrap())
			t.Cleanup(loop.Close)
			registry := loop.GetRegistry().GetDefaultAgent().Tools
			if !registry.HasRegistered("document") ||
				registry.HasRegistered(tools.BM25SearchToolName) != discovery {
				t.Fatal("native registry did not preserve the configured capability policy")
			}
			if _, visible := registry.Get("document"); visible {
				t.Fatal("document was exposed before native deferred discovery")
			}
			if discovery {
				result := registry.Execute(t.Context(), tools.BM25SearchToolName, map[string]any{
					"query": "PDF document inspect form fields",
				})
				if result == nil || result.IsError {
					t.Fatal("native discovery failed")
				}
				if _, visible := registry.Get("document"); !visible {
					t.Fatal("native discovery did not promote document")
				}
			}
			for _, forbidden := range []string{"read_file", "write_file", "exec", tools.RegexSearchToolName} {
				if registry.HasRegistered(forbidden) {
					t.Fatal("PDF discovery widened unrelated tool authority", forbidden)
				}
			}
			admitted := validateConversationPolicy(fixture.cfg, fixture.preset, fixture.workspace) == nil
			if admitted != discovery || provider.calls != 0 || fixture.tasks.Load() != 0 {
				t.Fatal("eval admission disagreed with usable native document discovery", admitted, discovery)
			}
		})
	}
}

func TestPDFDiscoveryAdmissionRefusesUnusableOrBroaderProfilesBeforeEffects(t *testing.T) {
	for _, mode := range []string{"missing", "regex", "without_pdf"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newAdmissionFixture(t)
			if mode == "without_pdf" {
				fixture.cfg.Agents.List[0].ToolPolicy.Allow = append(
					fixture.cfg.Agents.List[0].ToolPolicy.Allow, tools.BM25SearchToolName,
				)
			} else {
				enableAdmissionPDF(t, fixture)
				fixture.cfg.Agents.List[0].ToolPolicy.Allow = []string{"document", "request_user_input"}
				if mode == "regex" {
					fixture.cfg.Agents.List[0].ToolPolicy.Allow = append(
						fixture.cfg.Agents.List[0].ToolPolicy.Allow, tools.RegexSearchToolName,
					)
				}
			}
			fixture.writeConfig(t)
			if _, err := admitPreparedTarget(
				t.Context(),
				fixture.preset,
				fixture.home,
			); !errors.Is(err, ErrUnsafeTarget) ||
				fixture.probes.Load() != 0 ||
				fixture.tasks.Load() != 0 {
				t.Fatal("unusable/broader discovery policy reached gateway admission", mode, err)
			}
			lock := filepath.Join(fixture.home, config.SecurityConfigFile+".lock")
			if err := os.Remove(lock); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			parent := t.TempDir()
			result, err := PrepareProfile(t.Context(), fixture.preset, fixture.home, parent)
			entries, readErr := os.ReadDir(parent)
			_, lockErr := os.Lstat(lock)
			if !errors.Is(err, ErrPreparation) || result.Home != "" || readErr != nil || len(entries) != 0 ||
				!errors.Is(lockErr, os.ErrNotExist) || fixture.probes.Load() != 0 || fixture.tasks.Load() != 0 {
				t.Fatal("unusable/broader discovery policy reached repository/allocation", mode, err, readErr)
			}
		})
	}
}
