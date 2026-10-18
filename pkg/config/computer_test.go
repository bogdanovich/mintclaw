package config

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
)

func computerConfigFixture() *Config {
	cfg := DefaultConfig()
	cfg.Nodes.Enabled = true
	cfg.Execution.Targets = map[string]ExecutionTarget{"native": {Type: "node", Node: "paired-companion"}}
	cfg.Tools.Computer = ComputerToolsConfig{Enabled: true, Targets: map[string]ComputerTargetConfig{
		"selected": {
			NodeTarget: "native",
			Profile:    "selected",
			Revision:   strings.Repeat("a", 64),
			Actors: []string{
				"telegram:owner",
			},
			Agents:   []string{"browser"},
			Runtimes: []runtimecap.Kind{runtimecap.KindGateway},
		},
	}}
	return cfg
}

func TestComputerConfigurationIsOptInAndIndependent(t *testing.T) {
	if cfg := DefaultConfig(); cfg.Tools.Computer.Enabled || len(cfg.Tools.Computer.Targets) != 0 ||
		cfg.ValidateComputerConfig() != nil {
		t.Fatal("default configuration enabled native access")
	}
	cfg := computerConfigFixture()
	if err := cfg.ValidateComputerConfig(); err != nil {
		t.Fatal(err)
	}
	policy := cfg.Tools.Computer.Targets["selected"]
	principal := runtimecap.Principal{
		Runtime: runtimecap.KindGateway, ActorID: "telegram:owner", AgentID: "browser",
		SessionID: "thread", ExecutionID: "turn",
	}
	if !policy.Admits(principal) || policy.LeaseTTL() != 300*time.Second || policy.ObservationTTL() != 15*time.Second {
		t.Fatal("explicit policy was not admitted with bounded defaults")
	}
	for _, change := range []func(*runtimecap.Principal){
		func(p *runtimecap.Principal) { p.ActorID = "someone-else" },
		func(p *runtimecap.Principal) { p.AgentID = "other-agent" },
		func(p *runtimecap.Principal) { p.Runtime = runtimecap.KindCoding },
	} {
		forged := principal
		change(&forged)
		if policy.Admits(forged) {
			t.Fatal("computer grant widened implicitly")
		}
	}
	before := cfg.Tools.Computer.PolicyRevision(cfg.Execution.Targets)
	policy.Actors = []string{"telegram:someone-else"}
	cfg.Tools.Computer.Targets["selected"] = policy
	if before == cfg.Tools.Computer.PolicyRevision(cfg.Execution.Targets) {
		t.Fatal("owner policy change retained revision")
	}
	encoded, err := json.Marshal(cfg.Tools.Computer)
	if err != nil || strings.Contains(string(encoded), "DISPLAY") || strings.Contains(string(encoded), "window_id") {
		t.Fatal("gateway policy acquired native selector authority")
	}
}

func TestComputerPolicyRevisionPinsSelectedExecutionTargets(t *testing.T) {
	cfg := computerConfigFixture()
	revision := cfg.Tools.Computer.PolicyRevision(cfg.Execution.Targets)
	cfg.Execution.Targets["unrelated"] = ExecutionTarget{Type: "node", Node: "unrelated-node"}
	if revision != cfg.Tools.Computer.PolicyRevision(cfg.Execution.Targets) {
		t.Fatal("unrelated execution target invalidated the computer grant")
	}
	for _, change := range []func(ExecutionTarget) ExecutionTarget{
		func(target ExecutionTarget) ExecutionTarget { target.Node = "replacement-node"; return target },
		func(target ExecutionTarget) ExecutionTarget { target.Executor = "local"; return target },
	} {
		original := cfg.Execution.Targets["native"]
		cfg.Execution.Targets["native"] = change(original)
		if revision == cfg.Tools.Computer.PolicyRevision(cfg.Execution.Targets) {
			t.Fatal("selected execution transport changed without retiring tool authority")
		}
		cfg.Execution.Targets["native"] = original
	}
}

func TestComputerConfigurationRejectsAmbientOrUnboundedGrants(t *testing.T) {
	for name, change := range map[string]func(*Config){
		"nodes-disabled":    func(c *Config) { c.Nodes.Enabled = false },
		"no-targets":        func(c *Config) { c.Tools.Computer.Targets = nil },
		"missing-execution": func(c *Config) { c.Execution.Targets = nil },
		"bad-target": func(c *Config) {
			p := c.Tools.Computer.Targets["selected"]
			delete(c.Tools.Computer.Targets, "selected")
			c.Tools.Computer.Targets["../ambient"] = p
		},
		"no-revision": func(c *Config) {
			p := c.Tools.Computer.Targets["selected"]
			p.Revision = ""
			c.Tools.Computer.Targets["selected"] = p
		},
		"no-actors": func(c *Config) {
			p := c.Tools.Computer.Targets["selected"]
			p.Actors = nil
			c.Tools.Computer.Targets["selected"] = p
		},
		"no-agents": func(c *Config) {
			p := c.Tools.Computer.Targets["selected"]
			p.Agents = nil
			c.Tools.Computer.Targets["selected"] = p
		},
		"no-runtimes": func(c *Config) {
			p := c.Tools.Computer.Targets["selected"]
			p.Runtimes = nil
			c.Tools.Computer.Targets["selected"] = p
		},
		"shell-runtime": func(c *Config) {
			p := c.Tools.Computer.Targets["selected"]
			p.Runtimes = []runtimecap.Kind{"shell"}
			c.Tools.Computer.Targets["selected"] = p
		},
		"long-lease": func(c *Config) {
			p := c.Tools.Computer.Targets["selected"]
			p.LeaseSeconds = 1801
			c.Tools.Computer.Targets["selected"] = p
		},
		"long-observation": func(c *Config) {
			p := c.Tools.Computer.Targets["selected"]
			p.ObservationSeconds = 61
			c.Tools.Computer.Targets["selected"] = p
		},
		"observation-outlives-lease": func(c *Config) {
			p := c.Tools.Computer.Targets["selected"]
			p.LeaseSeconds = 1
			c.Tools.Computer.Targets["selected"] = p
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := computerConfigFixture()
			change(cfg)
			if cfg.ValidateComputerConfig() == nil {
				t.Fatal("invalid native grant admitted")
			}
		})
	}
}
