package companion

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
)

func computerPolicyFixture(t *testing.T) ComputerProfilePolicy {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("enabled native computer profiles support Linux/macOS")
	}
	profile := ComputerProfilePolicy{
		Enabled: true, Executable: "pinned-driver", SHA256: strings.Repeat("A", 64), PID: 42, WindowID: 99,
		AllowedActors: []string{"actor-b", "actor-a"}, AllowedAgents: []string{"agent"},
		AllowedRuntimes: []runtimecap.Kind{runtimecap.KindGateway},
	}
	if runtime.GOOS == "linux" {
		profile.Environment = map[string]string{"DISPLAY": ":1", "XAUTHORITY": "/explicit/auth"}
	}
	return profile
}

func TestComputerProfileNormalizationIsInertAndAuthorityBound(t *testing.T) {
	profile := computerPolicyFixture(t)
	base := t.TempDir()
	input := map[string]ComputerProfilePolicy{"fixture": profile, "same-window": profile}
	normalized, err := NormalizeComputerProfiles(input, base)
	if err != nil {
		t.Fatal(err)
	}
	first := normalized["fixture"]
	if first.Executable != filepath.Join(base, "pinned-driver") || first.SHA256 != strings.ToLower(profile.SHA256) ||
		len(first.Revision) != 64 || first.Revision == normalized["same-window"].Revision ||
		first.LeaseSeconds != 300 || first.ObservationSeconds != 15 {
		t.Fatal("policy normalization omitted authority or defaults")
	}
	if profile.AllowedActors[0] != "actor-b" || profile.Executable != "pinned-driver" {
		t.Fatal("normalization mutated input")
	}
	first.AllowedActors[0] = "mutated"
	if input["fixture"].AllowedActors[0] != "actor-b" {
		t.Fatal("owner slice aliased")
	}
	for name, mutate := range map[string]func(*ComputerProfilePolicy){
		"process": func(p *ComputerProfilePolicy) { p.PID++ },
		"window":  func(p *ComputerProfilePolicy) { p.WindowID++ },
		"driver":  func(p *ComputerProfilePolicy) { p.SHA256 = strings.Repeat("b", 64) },
		"actor":   func(p *ComputerProfilePolicy) { p.AllowedActors = []string{"other"} },
		"runtime": func(p *ComputerProfilePolicy) { p.AllowedRuntimes = []runtimecap.Kind{runtimecap.KindCoding} },
		"lease":   func(p *ComputerProfilePolicy) { p.LeaseSeconds = 60 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := profile
			mutate(&changed)
			result, err := NormalizeComputerProfiles(map[string]ComputerProfilePolicy{"fixture": changed}, base)
			if err != nil || result["fixture"].Revision == normalized["fixture"].Revision {
				t.Fatal("policy change retained old authority revision", err)
			}
		})
	}
}

func TestComputerProfilesRejectUnboundedOrAmbientAuthority(t *testing.T) {
	for name, mutate := range map[string]func(*ComputerProfilePolicy){
		"no PID":             func(p *ComputerProfilePolicy) { p.PID = 0 },
		"no window":          func(p *ComputerProfilePolicy) { p.WindowID = 0 },
		"no digest":          func(p *ComputerProfilePolicy) { p.SHA256 = "" },
		"no actor":           func(p *ComputerProfilePolicy) { p.AllowedActors = nil },
		"bad actor":          func(p *ComputerProfilePolicy) { p.AllowedActors = []string{"actor\n"} },
		"bad UTF8":           func(p *ComputerProfilePolicy) { p.AllowedActors = []string{string([]byte{255})} },
		"no agent":           func(p *ComputerProfilePolicy) { p.AllowedAgents = nil },
		"no runtime":         func(p *ComputerProfilePolicy) { p.AllowedRuntimes = nil },
		"unknown runtime":    func(p *ComputerProfilePolicy) { p.AllowedRuntimes = []runtimecap.Kind{"shell"} },
		"unbounded lease":    func(p *ComputerProfilePolicy) { p.LeaseSeconds = 1801 },
		"unbounded evidence": func(p *ComputerProfilePolicy) { p.ObservationSeconds = 61 },
		"credentials env":    func(p *ComputerProfilePolicy) { p.Environment = map[string]string{"OPENAI_API_KEY": "private"} },
		"driver control env": func(p *ComputerProfilePolicy) {
			p.Environment = map[string]string{"CUA_DRIVER_PERMISSION_MODE": "unrestricted"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			profile := computerPolicyFixture(t)
			mutate(&profile)
			if _, err := NormalizeComputerProfiles(
				map[string]ComputerProfilePolicy{"fixture": profile},
				t.TempDir(),
			); err == nil {
				t.Fatal("unsafe native authority accepted")
			}
		})
	}
	if runtime.GOOS == "linux" {
		profile := computerPolicyFixture(t)
		profile.Environment = nil
		if _, err := NormalizeComputerProfiles(
			map[string]ComputerProfilePolicy{"fixture": profile},
			t.TempDir(),
		); err == nil {
			t.Fatal("ambient GUI selection accepted")
		}
	}
}

func TestComputerProfileDefaultsDoNotEnableOrGrantAnything(t *testing.T) {
	for _, profiles := range []map[string]ComputerProfilePolicy{nil, {"disabled": {Enabled: false}}} {
		cfg, err := (Config{GatewayURL: "wss://gateway.example", ComputerProfiles: profiles}).Normalize(t.TempDir())
		if err != nil || HasEnabledComputerProfile(cfg.ComputerProfiles) || len(cfg.Policy.AllowedCommands) != 0 {
			t.Fatal("default configuration acquired desktop authority", err)
		}
	}
	profile := computerPolicyFixture(t)
	normalized, err := NormalizeComputerProfiles(map[string]ComputerProfilePolicy{"fixture": profile}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	principal := runtimecap.Principal{
		Runtime:     runtimecap.KindGateway,
		ActorID:     "actor-a",
		AgentID:     "agent",
		SessionID:   "s",
		ExecutionID: "e",
	}
	if !normalized["fixture"].Admits(principal) {
		t.Fatal("configured principal rejected")
	}
	principal.ActorID = "different"
	if normalized["fixture"].Admits(principal) {
		t.Fatal("actor constraint ignored")
	}
	if profile.Environment != nil {
		profile.Environment["DISPLAY"] = "changed"
		if normalized["fixture"].Environment["DISPLAY"] == "changed" {
			t.Fatal("environment aliased")
		}
	}
}
