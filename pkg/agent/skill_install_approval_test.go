package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/skills"
	integrationtools "github.com/bogdanovich/mintclaw/pkg/tools/integration"
)

type approvalInstallRegistry struct {
	downloads int
}

func (*approvalInstallRegistry) Name() string { return "approval-test" }

func (*approvalInstallRegistry) ResolveInstallDirName(target string) (string, error) {
	return target, nil
}

func (*approvalInstallRegistry) SkillURL(slug, _ string) string { return slug }

func (*approvalInstallRegistry) Search(context.Context, string, int) ([]skills.SearchResult, error) {
	return nil, nil
}

func (*approvalInstallRegistry) GetSkillMeta(context.Context, string) (*skills.SkillMeta, error) {
	return nil, nil
}

func (registry *approvalInstallRegistry) DownloadAndInstall(
	_ context.Context,
	_ string,
	_ string,
	targetDir string,
) (*skills.InstallResult, error) {
	registry.downloads++
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(
		filepath.Join(targetDir, "SKILL.md"),
		[]byte("---\nname: personal-skill\ndescription: approval test\n---\n"),
		0o600,
	); err != nil {
		return nil, err
	}
	return &skills.InstallResult{Version: "test"}, nil
}

func TestApprovedSkillInstallPreservesOriginatingOwnershipIntent(t *testing.T) {
	provider := &sequenceProvider{responses: []*providers.LLMResponse{
		{ToolCalls: []providers.ToolCall{{
			ID: "call-install-skill", Name: "install_skill", Arguments: map[string]any{
				"slug": "personal-skill", "scope": "workspace", "registry": "approval-test",
			},
		}}},
		{Content: "install remained blocked", FinishReason: "stop"},
	}}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	installInteractionChannelManager(t, al, newInteractionChannelManager())

	home := t.TempDir()
	workspace := t.TempDir()
	registry := &approvalInstallRegistry{}
	registries := skills.NewRegistryManager()
	registries.AddRegistry(registry)
	agent.Tools.Register(integrationtools.NewInstallSkillTool(
		registries,
		skills.SkillInstallContext{UserHome: home, Workspace: workspace},
		nil,
	))
	if err := al.MountHook(NamedHook("approve-install", &selectiveDurableApprovalHook{
		durableApprovalHook: durableApprovalHook{actionSummary: "Install the selected skill"},
		tool:                "install_skill",
	})); err != nil {
		t.Fatal(err)
	}

	inbound := &bus.InboundContext{
		Channel: "telegram", ChatID: "chat-1", SenderID: "user-1", MessageID: "origin-message",
	}
	const sessionKey = "approved-skill-install"
	response, status, err := runAgentLoopWithStatusForTest(t.Context(), al, agent, turnSpec{
		Dispatch: DispatchRequest{
			RouteSessionKey: sessionKey,
			SessionKey:      sessionKey,
			UserMessage:     "install this skill for yourself",
			InboundContext:  inbound,
		},
		DefaultResponse: defaultResponse,
		EnableSummary:   true,
		SendResponse:    false,
	})
	if err != nil || response != "" || status != TurnEndStatusSuspended {
		t.Fatalf("initial install turn = (%q, %q, %v)", response, status, err)
	}

	interactionRegistry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, ok := activeInteractionForSession(interactionRegistry, sessionKey)
	if !ok || record.Kind != interactions.KindApproval {
		t.Fatalf("approval interaction = %#v, found=%t", record, ok)
	}
	record, err = interactionRegistry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "allow_once", MessageID: "approval-answer", ReceivedAt: time.Now().UnixMilli(),
	}, interactions.OutcomeAllowed)
	if err != nil {
		t.Fatal(err)
	}
	resumeCommand, err := newResumeInteractionCommand(
		interactionRegistry,
		agent.Workspace,
		agent,
		nil,
		*inbound,
		record,
	)
	if err != nil {
		t.Fatal(err)
	}
	resumeResult, err := newInteractionService(al).Resume(t.Context(), resumeCommand)
	if err != nil {
		t.Fatal(err)
	}
	if resumeResult.Record.Status != interactions.StatusResolved {
		t.Fatalf("approval status = %q, want resolved", resumeResult.Record.Status)
	}

	history := agent.Sessions.GetHistory(sessionKey)
	_, resultIndex := interactionToolPairIndexes(history, record.Origin.ToolCallID)
	if resultIndex < 0 || !strings.Contains(history[resultIndex].Content, "retry with scope=user") {
		t.Fatalf("approved install result did not retain originating ownership intent: %#v", history)
	}
	if registry.downloads != 0 {
		t.Fatalf("registry downloads = %d, want 0", registry.downloads)
	}
	if _, statErr := os.Stat(filepath.Join(workspace, "skills")); !os.IsNotExist(statErr) {
		t.Fatalf("workspace skill root was created: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".agents", "skills")); !os.IsNotExist(statErr) {
		t.Fatalf("user skill root was created: %v", statErr)
	}
}
