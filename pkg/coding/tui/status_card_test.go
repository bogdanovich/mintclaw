package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

type statusCardScenario struct {
	name     string
	width    int
	home     string
	snapshot frontend.ThreadSnapshot
}

func TestStatusCardGoldenScenarios(t *testing.T) {
	for _, scenario := range statusCardScenarios() {
		t.Run(scenario.name, func(t *testing.T) {
			lines := renderStatusCard(scenario.snapshot, scenario.width, scenario.home)
			rendered := strings.Join(lines, "\n")
			if repeated := strings.Join(
				renderStatusCard(scenario.snapshot, scenario.width, scenario.home),
				"\n",
			); repeated != rendered {
				t.Fatal("status card rendering is not deterministic")
			}
			goldenPath := filepath.Join("testdata", "status_cards", scenario.name+".golden")
			golden, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Errorf("read %s: %v\nrendered:\n%s", goldenPath, err, rendered)
			} else if want := strings.TrimRight(string(golden), "\r\n"); rendered != want {
				t.Errorf("status card mismatch:\nwant:\n%s\n\ngot:\n%s", want, rendered)
			}
			for _, line := range lines {
				if width := ansi.StringWidth(line); width > scenario.width {
					t.Fatalf("line width %d > %d: %q", width, scenario.width, line)
				}
			}
			lower := strings.ToLower(rendered)
			for _, forbidden := range []string{
				"/home/alice", "alice@example.com", "account-secret", "access-token",
			} {
				if strings.Contains(lower, forbidden) {
					t.Fatalf("status card leaked %q: %s", forbidden, rendered)
				}
			}
		})
	}
}

func TestStatusPlainOutputIsBorderlessCopySafeAndOmitsUnknownAccount(t *testing.T) {
	scenario := statusCardScenarios()[4]
	plain := RenderStatusPlain(scenario.snapshot, scenario.home)
	if strings.ContainsAny(plain, "╭╮╰╯│") || strings.Contains(plain, "Account:") ||
		!strings.Contains(plain, "Provider: limited-provider") ||
		!strings.Contains(plain, "Permissions: read only") {
		t.Fatalf("plain limited-provider status = %q", plain)
	}
	for _, line := range strings.Split(plain, "\n") {
		if strings.ContainsRune(line, '\t') {
			t.Fatalf("plain status retained a terminal-dependent tab: %q", line)
		}
	}
}

func TestStatusPathDisplayOnlyAbbreviatesPathsWithinConfiguredHome(t *testing.T) {
	home := filepath.Clean("/home/alice")
	if got := statusHomeDirectory([]string{"HOME=/ignored", "HOME=" + home}); got != home {
		t.Fatalf("status home = %q, want %q", got, home)
	}
	if got := statusPathDisplay(filepath.Join(home, "src", "mintclaw"), home); got != "~/src/mintclaw" {
		t.Fatalf("path within home = %q", got)
	}
	if got := statusPathDisplay("/home/alice-other/repo", home); got != "/home/alice-other/repo" {
		t.Fatalf("path outside home = %q", got)
	}
	if got := statusPathDisplay("", home); got != "unavailable" {
		t.Fatalf("empty path = %q", got)
	}
}

func statusCardScenarios() []statusCardScenario {
	const home = "/home/alice"
	baseRuntime := frontend.RuntimeStatus{
		Version:             "mintclaw v0.1.0-test",
		ReasoningEffort:     "medium",
		ReasoningConfigured: true,
		Permission:          frontend.PermissionFullAccess,
		Autonomy:            frontend.AutonomyYolo,
	}
	gitNew := frontend.ThreadSnapshot{
		ThreadID: "thread-new",
		Metadata: frontend.ThreadMetadata{
			Title:       "Parser cleanup",
			ProjectRoot: home + "/src/mintclaw",
			CWD:         home + "/src/mintclaw",
			Model:       "gpt-5.6-sol",
			Provider:    "openai",
		},
		Runtime:  cloneStatusRuntime(baseRuntime),
		Activity: frontend.ActivityIdle,
		ContextUsage: frontend.ContextUsage{
			UsedTokens: 20_000, LimitTokens: 131_072,
		},
		Workspace: &codingworkspace.Snapshot{
			ProjectRoot: home + "/src/mintclaw",
			CWD:         home + "/src/mintclaw",
			Git: codingworkspace.GitState{
				Available: true, StatusAvailable: true, Branch: "main", Head: "1234567890",
			},
		},
	}
	gitNew.Runtime.InstructionSources = []frontend.InstructionSource{{
		Path: home + "/src/mintclaw/AGENTS.md", Scope: home + "/src/mintclaw", Label: "AGENTS.md",
	}}
	gitNew.Runtime.Account = &frontend.ProviderAccount{
		Provider: "openai", AuthMethod: "oauth", State: frontend.ProviderAccountAuthenticated,
	}

	nonGitRuntime := baseRuntime
	nonGitRuntime.ReasoningEffort = "off"
	nonGitRuntime.ReasoningConfigured = false
	nonGit := frontend.ThreadSnapshot{
		ThreadID: "thread-scratch",
		Metadata: frontend.ThreadMetadata{
			ProjectRoot: "/work/scratch", CWD: "/work/scratch", Model: "local", Provider: "ollama",
		},
		Runtime: &nonGitRuntime,
		Workspace: &codingworkspace.Snapshot{
			ProjectRoot: "/work/scratch", CWD: "/work/scratch",
			Git: codingworkspace.GitState{UnavailableReason: "not a repository"},
		},
		Activity: frontend.ActivityIdle,
	}

	resumedRuntime := baseRuntime
	resumedRuntime.Resumed = true
	resumedRuntime.Permission = frontend.PermissionReadOnly
	resumedRuntime.InstructionWarningCount = 1
	resumedRuntime.InstructionSources = []frontend.InstructionSource{
		{Path: home + "/.mintclaw/AGENTS.md", Label: "AGENTS.md", Global: true},
		{Path: home + "/src/mintclaw/CLAUDE.md", Scope: home + "/src/mintclaw", Label: "CLAUDE.md"},
	}
	resumedRuntime.Account = &frontend.ProviderAccount{
		Provider: "openai", AuthMethod: "oauth", State: frontend.ProviderAccountNeedsRefresh,
	}
	resumed := frontend.ThreadSnapshot{
		ThreadID: "thread-resumed",
		Metadata: frontend.ThreadMetadata{
			Title: "Resume parser work", ProjectRoot: home + "/src/mintclaw", CWD: home + "/src/mintclaw/pkg",
			Model: "gpt-5.6-sol", Provider: "openai",
		},
		Runtime:  &resumedRuntime,
		Activity: frontend.ActivityRunning,
		Status:   "editing parser state",
		Workspace: &codingworkspace.Snapshot{
			ProjectRoot: home + "/src/mintclaw", CWD: home + "/src/mintclaw/pkg",
			Git: codingworkspace.GitState{
				Available: true, StatusAvailable: true, Branch: "feat/parser", Dirty: true,
			},
		},
		Items: []frontend.PresentationItem{{
			ID: "plan", TurnID: "turn", Sequence: 1, Revision: 1,
			Kind: frontend.PresentationPlanUpdate, Lifecycle: frontend.PresentationCompleted,
			Plan: &frontend.PlanState{Steps: []frontend.PlanStepState{
				{Step: "Inspect parser", Status: frontend.PlanStepCompleted},
				{Step: "Fix resume", Status: frontend.PlanStepInProgress},
			}},
		}},
	}

	compactedRuntime := baseRuntime
	compacted := gitNew.Clone()
	compacted.ThreadID = "thread-compacted"
	compacted.Runtime = &compactedRuntime
	compacted.LastCompaction = &frontend.CompactionState{
		Status:              frontend.CompactionCompleted,
		Background:          true,
		TokensBefore:        98_000,
		TokensAfter:         31_000,
		TokensSaved:         67_000,
		TokenCountsObserved: true,
	}

	limitedRuntime := frontend.RuntimeStatus{
		Version:    "mintclaw v0.1.0-test",
		Permission: frontend.PermissionReadOnly,
		Autonomy:   frontend.AutonomyYolo,
	}
	limited := frontend.ThreadSnapshot{
		ThreadID: "thread-limited",
		Metadata: frontend.ThreadMetadata{
			ProjectRoot: "/srv/project", CWD: "/srv/project", Model: "provider-model", Provider: "limited-provider",
		},
		Runtime:  &limitedRuntime,
		Activity: frontend.ActivityIdle,
	}

	return []statusCardScenario{
		{name: "git-new-80", width: 80, home: home, snapshot: gitNew},
		{name: "non-git-40", width: 40, home: home, snapshot: nonGit},
		{name: "resumed-80", width: 80, home: home, snapshot: resumed},
		{name: "compacted-120", width: 120, home: home, snapshot: compacted},
		{name: "limited-provider-40", width: 40, home: home, snapshot: limited},
	}
}

func cloneStatusRuntime(status frontend.RuntimeStatus) *frontend.RuntimeStatus {
	clone := status
	return &clone
}
