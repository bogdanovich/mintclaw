package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/seahorse"
	"github.com/bogdanovich/mintclaw/pkg/skills"
)

func TestFreezeTurnEnvelopeMakesDynamicContextReplayStable(t *testing.T) {
	builder := NewContextBuilder(t.TempDir())
	discoveryCalls := 0
	builder.WithAgentDiscovery("main", func(string) []AgentDescriptor {
		discoveryCalls++
		return nil
	})
	admittedAt := time.Date(2026, time.September, 26, 10, 30, 0, 0, time.UTC)
	builder.now = func() time.Time { return admittedAt }
	req := PromptBuildRequest{
		CurrentMessage:    "deploy it",
		Channel:           "telegram",
		ChatID:            "chat-7",
		SenderID:          "owner-1",
		SenderDisplayName: "Anton",
		SelectedSkills: []skills.SelectedSkill{{
			Name: "deploy", Path: "/skills/deploy/SKILL.md", Revision: "sha256:one",
			Instructions: "Use the frozen deployment workflow.",
		}},
	}
	envelope := builder.FreezeTurnEnvelope(t.Context(), req)
	if discoveryCalls != 1 {
		t.Fatalf("agent discovery calls at admission = %d, want 1", discoveryCalls)
	}
	if envelope == nil || envelope.Version != providers.TurnEnvelopeVersion1 {
		t.Fatalf("frozen envelope = %#v", envelope)
	}
	encodedAtAdmission, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encodedAtAdmission), "2026-09-26 10:30") ||
		!strings.Contains(string(encodedAtAdmission), "Current sender: Anton (ID: owner-1)") ||
		!strings.Contains(string(encodedAtAdmission), "Use the frozen deployment workflow.") {
		t.Fatalf("frozen envelope omitted admitted context: %s", encodedAtAdmission)
	}

	builder.now = func() time.Time { return admittedAt.Add(48 * time.Hour) }
	req.CurrentTurnEnvelope = envelope
	req.SelectedSkills[0].Instructions = "Use a changed workflow."
	first := builder.BuildMessagesFromPrompt(req)
	second := builder.BuildMessagesFromPrompt(req)
	if discoveryCalls != 1 {
		t.Fatalf("frozen rebuild recomputed dynamic discovery %d times", discoveryCalls)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("rebuild drifted:\nfirst=%#v\nsecond=%#v", first, second)
	}
	root := first[len(first)-1]
	if !reflect.DeepEqual(root.TurnEnvelope, envelope) {
		t.Fatalf("root envelope = %#v, want %#v", root.TurnEnvelope, envelope)
	}
	if strings.Contains(first[0].Content, "2026-09-26") ||
		strings.Contains(first[0].Content, "frozen deployment workflow") ||
		strings.Contains(first[0].Content, "changed workflow") {
		t.Fatalf("dynamic context leaked back into system prompt: %q", first[0].Content)
	}
	projected := providers.ProjectTurnEnvelope(root)
	if !strings.Contains(projected.Content, "2026-09-26 10:30") ||
		strings.Contains(projected.Content, "2026-09-28") ||
		strings.Contains(projected.Content, "changed workflow") {
		t.Fatalf("provider projection did not preserve admission bytes: %q", projected.Content)
	}
}

func TestFrozenTurnEnvelopePreservesHistoricalMultiSenderContext(t *testing.T) {
	builder := NewContextBuilder(t.TempDir())
	firstReq := PromptBuildRequest{
		CurrentMessage: "first",
		Channel:        "telegram",
		ChatID:         "shared",
		SenderID:       "sender-a",
	}
	firstEnvelope := builder.FreezeTurnEnvelope(t.Context(), firstReq)
	firstRoot := userPromptMessage("first", nil)
	firstRoot.TurnEnvelope = firstEnvelope

	secondReq := PromptBuildRequest{
		History: []providers.Message{
			firstRoot,
			{Role: "assistant", Content: "answer"},
		},
		CurrentMessage: "second",
		Channel:        "telegram",
		ChatID:         "shared",
		SenderID:       "sender-b",
	}
	secondReq.CurrentTurnEnvelope = builder.FreezeTurnEnvelope(t.Context(), secondReq)
	messages := builder.BuildMessagesFromPrompt(secondReq)

	var roots []providers.Message
	for _, message := range messages {
		if message.Role == "user" {
			roots = append(roots, message)
		}
	}
	if len(roots) != 2 {
		t.Fatalf("user messages = %#v, want two roots", roots)
	}
	firstProjection := providers.ProjectTurnEnvelope(roots[0]).Content
	secondProjection := providers.ProjectTurnEnvelope(roots[1]).Content
	if !strings.Contains(firstProjection, "sender-a") || strings.Contains(firstProjection, "sender-b") {
		t.Fatalf("first sender projection = %q", firstProjection)
	}
	if !strings.Contains(secondProjection, "sender-b") || strings.Contains(secondProjection, "sender-a") {
		t.Fatalf("second sender projection = %q", secondProjection)
	}
}

func TestFrozenTurnEnvelopeCarriesMediaOnlyRelation(t *testing.T) {
	builder := NewContextBuilder(t.TempDir())
	req := PromptBuildRequest{
		Media: []string{"image.png"},
		CurrentMessageRelation: InboundMessageRelation{
			Kind:      bus.InboundRelationAdjacentFollowupMedia,
			MediaOnly: true,
		},
	}
	req.CurrentTurnEnvelope = builder.FreezeTurnEnvelope(t.Context(), req)
	messages := builder.BuildMessagesFromPrompt(req)
	root := messages[len(messages)-1]
	if root.Content != "" {
		t.Fatalf("canonical current content = %q, want empty", root.Content)
	}
	projected := providers.ProjectTurnEnvelope(root)
	if !strings.Contains(projected.Content, "likely adds context") {
		t.Fatalf("projected relation context = %q", projected.Content)
	}
}

func TestRestoreFrozenTurnEnvelopesRequiresUnchangedOwnerMessage(t *testing.T) {
	before := []providers.Message{{
		Role: "user", Content: "hello",
		TurnEnvelope: &providers.TurnEnvelope{
			Version: providers.TurnEnvelopeVersion1,
			Parts:   []providers.TurnEnvelopePart{{ID: "context.runtime", Content: "frozen"}},
		},
	}}
	after := stripCanonicalMessageStateFromAll(cloneProviderMessages(before))
	if err := restoreFrozenTurnEnvelopes(before, after); err != nil {
		t.Fatalf("restore unchanged envelope: %v", err)
	}
	if !reflect.DeepEqual(after[0].TurnEnvelope, before[0].TurnEnvelope) {
		t.Fatalf("restored envelope = %#v, want %#v", after[0].TurnEnvelope, before[0].TurnEnvelope)
	}

	modified := stripCanonicalMessageStateFromAll(cloneProviderMessages(before))
	modified[0].Content = "hook rewrite"
	if err := restoreFrozenTurnEnvelopes(before, modified); err == nil {
		t.Fatal("restore accepted a modified envelope-owning message")
	}
}

func TestEstimateMessageTokensIncludesFrozenTurnEnvelope(t *testing.T) {
	plain := providers.Message{Role: "user", Content: "hello"}
	withEnvelope := plain
	withEnvelope.TurnEnvelope = &providers.TurnEnvelope{
		Version: providers.TurnEnvelopeVersion1,
		Parts: []providers.TurnEnvelopePart{{
			ID: "context.runtime", Content: strings.Repeat("frozen context ", 100),
		}},
	}
	if EstimateMessageTokens(withEnvelope) <= EstimateMessageTokens(plain) {
		t.Fatalf(
			"envelope tokens = %d, plain tokens = %d",
			EstimateMessageTokens(withEnvelope),
			EstimateMessageTokens(plain),
		)
	}
}

func TestSetupTurnPersistsAndReusesFrozenTurnEnvelope(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()

	admittedAt := time.Date(2026, time.September, 26, 14, 15, 0, 0, time.UTC)
	agent.ContextBuilder.now = func() time.Time { return admittedAt }
	const sessionKey = "frozen-turn-envelope"
	opts := makeTestTurnSpec(sessionKey)
	opts.Dispatch.UserMessage = "preserve this context"
	opts.Dispatch.InboundContext.SenderID = "sender-at-admission"
	ts := newTurnState(agent, opts, turnEventScope{
		turnID: "turn-envelope", context: newTurnContext(nil, nil, nil),
	})

	exec, err := newTestPipeline(al).SetupTurn(t.Context(), ts)
	if err != nil {
		t.Fatalf("SetupTurn() error = %v", err)
	}
	if ts.turnEnvelope == nil {
		t.Fatal("SetupTurn() did not freeze a turn envelope")
	}
	root := exec.messages[len(exec.messages)-1]
	if !reflect.DeepEqual(root.TurnEnvelope, ts.turnEnvelope) {
		t.Fatalf("live root envelope = %#v, want %#v", root.TurnEnvelope, ts.turnEnvelope)
	}
	history := agent.Sessions.GetHistory(sessionKey)
	if len(history) != 1 || !history[0].RootTurnStart ||
		!reflect.DeepEqual(history[0].TurnEnvelope, ts.turnEnvelope) {
		t.Fatalf("durable root = %#v, want frozen root envelope", history)
	}
	projected := providers.ProjectTurnEnvelope(history[0])
	if !strings.Contains(projected.Content, "2026-09-26 14:15") ||
		!strings.Contains(projected.Content, "sender-at-admission") {
		t.Fatalf("durable root projection = %q", projected.Content)
	}
}

func TestFrozenTurnEnvelopeSeahorseRestartKeepsProviderBytes(t *testing.T) {
	canonical := providers.Message{
		Role: "user", Content: "resume",
		TurnEnvelope: &providers.TurnEnvelope{
			Version: providers.TurnEnvelopeVersion1,
			Parts: []providers.TurnEnvelopePart{
				{ID: "context.runtime", Content: "frozen runtime"},
				{ID: "context.active_skill.deploy", Content: "frozen skill"},
			},
		},
	}
	want, err := json.Marshal(providers.ProjectTurnEnvelope(canonical))
	if err != nil {
		t.Fatal(err)
	}
	derived := providerToSeahorseMessage(canonical)
	restarted := seahorseToProviderMessages(&seahorse.AssembleResult{Messages: []seahorse.Message{derived}})
	if len(restarted) != 1 {
		t.Fatalf("restarted messages = %#v", restarted)
	}
	got, err := json.Marshal(providers.ProjectTurnEnvelope(restarted[0]))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("provider bytes after restart = %s, want %s", got, want)
	}
}
