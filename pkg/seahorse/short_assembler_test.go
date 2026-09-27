package seahorse

import (
	"context"
	"strings"
	"testing"
	"time"
)

// --- Assembler Tests ---

func checkpointContent(checkpoint *Checkpoint) string {
	if checkpoint == nil {
		return ""
	}
	return checkpoint.Content
}

func TestCheckpointGenerationTracksActiveContentAndOrder(t *testing.T) {
	one := Summary{SummaryID: "one", Content: "one", Kind: SummaryKindLeaf}
	two := Summary{SummaryID: "two", Content: "two", Kind: SummaryKindLeaf}
	firstItems := []resolvedItem{
		{ordinal: 100, itemType: "summary", summary: &one, summaryXML: "<summary>one</summary>"},
		{ordinal: 200, itemType: "summary", summary: &two, summaryXML: "<summary>two</summary>"},
	}

	first := buildAssembleResult(firstItems, nil)
	repeated := buildAssembleResult(append([]resolvedItem(nil), firstItems...), nil)
	if first.Checkpoint == nil || repeated.Checkpoint == nil {
		t.Fatal("expected checkpoint")
	}
	if first.Checkpoint.Generation != repeated.Checkpoint.Generation {
		t.Fatalf(
			"no-op assembly changed generation: %q != %q",
			first.Checkpoint.Generation,
			repeated.Checkpoint.Generation,
		)
	}

	reordered := buildAssembleResult([]resolvedItem{firstItems[1], firstItems[0]}, nil)
	if reordered.Checkpoint == nil || reordered.Checkpoint.Generation == first.Checkpoint.Generation {
		t.Fatalf("reordered checkpoint generation = %#v, want a new generation", reordered.Checkpoint)
	}

	changedItems := append([]resolvedItem(nil), firstItems...)
	changedItems[1].summaryXML = "<summary>changed</summary>"
	changed := buildAssembleResult(changedItems, nil)
	if changed.Checkpoint == nil || changed.Checkpoint.Generation == first.Checkpoint.Generation {
		t.Fatalf("changed checkpoint generation = %#v, want a new generation", changed.Checkpoint)
	}
}

// helper: create a store with messages and summaries for assembly tests
func setupAssemblerStore(t *testing.T) (*Store, int64) {
	t.Helper()
	s := openTestStore(t)
	ctx := context.Background()

	conv, err := s.GetOrCreateConversation(ctx, "test:assemble")
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	return s, conv.ConversationID
}

func TestAssemblerAssembleEmpty(t *testing.T) {
	s, convID := setupAssemblerStore(t)
	ctx := context.Background()

	a := &Assembler{store: s, config: Config{}}
	result, err := a.Assemble(ctx, convID, AssembleInput{Budget: 1000})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(result.Messages) != 0 {
		t.Errorf("Messages = %d, want 0", len(result.Messages))
	}
	if checkpointContent(result.Checkpoint) != "" {
		t.Errorf("Summary = %q, want empty", checkpointContent(result.Checkpoint))
	}
}

func TestAssemblerAssembleMessagesOnly(t *testing.T) {
	s, convID := setupAssemblerStore(t)
	ctx := context.Background()

	// Create messages
	msg1, _ := s.AddMessage(ctx, convID, "user", "hello", 5)
	msg2, _ := s.AddMessage(ctx, convID, "assistant", "world", 5)

	// Create context items
	if err := s.UpsertContextItems(ctx, convID, []ContextItem{
		{Ordinal: 100, ItemType: "message", MessageID: msg1.ID, TokenCount: 5},
		{Ordinal: 200, ItemType: "message", MessageID: msg2.ID, TokenCount: 5},
	}); err != nil {
		t.Fatal(err)
	}

	a := &Assembler{store: s, config: Config{}}
	result, err := a.Assemble(ctx, convID, AssembleInput{Budget: 100})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if len(result.Messages) != 2 {
		t.Fatalf("Messages = %d, want 2", len(result.Messages))
	}
	if result.Messages[0].Content != "hello" {
		t.Errorf("Messages[0].Content = %q, want 'hello'", result.Messages[0].Content)
	}
	if result.Messages[1].Content != "world" {
		t.Errorf("Messages[1].Content = %q, want 'world'", result.Messages[1].Content)
	}
	// No summaries, so Summary should be empty
	if checkpointContent(result.Checkpoint) != "" {
		t.Errorf("Summary = %q, want empty", checkpointContent(result.Checkpoint))
	}
}

func TestAssemblerAssembleWithSummary(t *testing.T) {
	s, convID := setupAssemblerStore(t)
	ctx := context.Background()

	// Create a summary
	summary, _ := s.CreateSummary(ctx, CreateSummaryInput{
		ConversationID: convID,
		Kind:           SummaryKindLeaf,
		Depth:          0,
		Content:        "summary of early messages",
		TokenCount:     50,
	})

	// Create recent messages
	msg1, _ := s.AddMessage(ctx, convID, "user", "recent", 5)
	msg2, _ := s.AddMessage(ctx, convID, "assistant", "reply", 5)

	// Context: summary + recent messages
	if err := s.UpsertContextItems(ctx, convID, []ContextItem{
		{Ordinal: 100, ItemType: "summary", SummaryID: summary.SummaryID, TokenCount: 50},
		{Ordinal: 200, ItemType: "message", MessageID: msg1.ID, TokenCount: 5},
		{Ordinal: 300, ItemType: "message", MessageID: msg2.ID, TokenCount: 5},
	}); err != nil {
		t.Fatal(err)
	}

	a := &Assembler{store: s, config: Config{}}
	result, err := a.Assemble(ctx, convID, AssembleInput{Budget: 1000})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// Messages = 2 raw messages (summaries are in the checkpoint, not Messages)
	if len(result.Messages) != 2 {
		t.Errorf("Messages = %d, want 2 (raw messages only)", len(result.Messages))
	}
	// Summary should contain XML with summary content
	if checkpointContent(result.Checkpoint) == "" {
		t.Error("Summary should not be empty when summary exists")
	}
	if !strings.Contains(checkpointContent(result.Checkpoint), summary.Content) {
		t.Errorf("Summary should contain summary content %q", summary.Content)
	}
	if !strings.Contains(checkpointContent(result.Checkpoint), "<summary") {
		t.Error("Summary should contain <summary XML tag")
	}
}

func TestAssemblerSummaryBudgetUsesFormattedXML(t *testing.T) {
	s, convID := setupAssemblerStore(t)
	ctx := context.Background()

	summary, err := s.CreateSummary(ctx, CreateSummaryInput{
		ConversationID: convID,
		Kind:           SummaryKindLeaf,
		Depth:          0,
		Content:        strings.Repeat(`quoted "summary" <with> xml overhead `, 20),
		TokenCount:     1, // Deliberately stale/under-counted stored value.
	})
	if err != nil {
		t.Fatalf("CreateSummary: %v", err)
	}

	a := &Assembler{store: s, config: Config{}}
	resolved, err := a.resolveItem(ctx, ContextItem{
		Ordinal:    100,
		ItemType:   "summary",
		SummaryID:  summary.SummaryID,
		TokenCount: 1,
	})
	if err != nil {
		t.Fatalf("resolveItem: %v", err)
	}

	if resolved.summaryXML == "" {
		t.Fatal("expected formatted summary XML")
	}
	if resolved.tokenCount <= 1 {
		t.Fatalf("formatted summary token count = %d, want greater than stored count", resolved.tokenCount)
	}
}

func TestAssemblerDropsCoveredLeafWhenCondensedSelected(t *testing.T) {
	s, convID := setupAssemblerStore(t)
	ctx := context.Background()

	leaf, err := s.CreateSummary(ctx, CreateSummaryInput{
		ConversationID: convID,
		Kind:           SummaryKindLeaf,
		Depth:          0,
		Content:        "leaf content should be covered",
		TokenCount:     10,
	})
	if err != nil {
		t.Fatalf("CreateSummary leaf: %v", err)
	}
	condensed, err := s.CreateSummary(ctx, CreateSummaryInput{
		ConversationID: convID,
		Kind:           SummaryKindCondensed,
		Depth:          1,
		Content:        "condensed content should remain",
		TokenCount:     10,
		ParentIDs:      []string{leaf.SummaryID},
	})
	if err != nil {
		t.Fatalf("CreateSummary condensed: %v", err)
	}
	msg, err := s.AddMessage(ctx, convID, "user", "fresh", 1)
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	if upsertErr := s.UpsertContextItems(ctx, convID, []ContextItem{
		{Ordinal: 100, ItemType: "summary", SummaryID: leaf.SummaryID, TokenCount: 10},
		{Ordinal: 200, ItemType: "summary", SummaryID: condensed.SummaryID, TokenCount: 10},
		{Ordinal: 300, ItemType: "message", MessageID: msg.ID, TokenCount: 1},
	}); upsertErr != nil {
		t.Fatalf("UpsertContextItems: %v", upsertErr)
	}

	a := &Assembler{store: s, config: Config{}}
	result, err := a.Assemble(ctx, convID, AssembleInput{Budget: 1000})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if strings.Contains(checkpointContent(result.Checkpoint), leaf.Content) {
		t.Fatalf("covered leaf summary was assembled: %s", checkpointContent(result.Checkpoint))
	}
	if !strings.Contains(checkpointContent(result.Checkpoint), condensed.Content) {
		t.Fatalf("condensed summary missing: %s", checkpointContent(result.Checkpoint))
	}
}

func TestAssemblerBudgetEvictsOldest(t *testing.T) {
	s, convID := setupAssemblerStore(t)
	ctx := context.Background()

	// Create 40 messages, each with 10 tokens = 400 total
	msgs := make([]*Message, 40)
	for i := 0; i < 40; i++ {
		m, _ := s.AddMessage(ctx, convID, "user", "msg", 10)
		msgs[i] = m
	}

	// Context items for all messages
	items := make([]ContextItem, 40)
	for i := 0; i < 40; i++ {
		items[i] = ContextItem{
			Ordinal:    (i + 1) * 100,
			ItemType:   "message",
			MessageID:  msgs[i].ID,
			TokenCount: 10,
		}
	}
	if err := s.UpsertContextItems(ctx, convID, items); err != nil {
		t.Fatal(err)
	}

	// Budget of 200 tokens with FreshTailCount=32
	// Fresh tail = last 32 messages (320 tokens, over budget)
	// Evictable = first 8 messages (80 tokens)
	// The oldest messages from the fresh tail should be dropped so only the
	// newest 20 messages remain within the 200-token budget.
	a := &Assembler{store: s, config: Config{}}
	result, err := a.Assemble(ctx, convID, AssembleInput{Budget: 200})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if len(result.Messages) != 20 {
		t.Errorf("Messages = %d, want 20", len(result.Messages))
	}
	if result.Messages[0].ID != msgs[20].ID {
		t.Errorf("first message ID = %d, want %d (msgs[20])", result.Messages[0].ID, msgs[20].ID)
	}

	totalTokens := 0
	for _, msg := range result.Messages {
		totalTokens += msg.TokenCount
	}
	if totalTokens > 200 {
		t.Errorf("assembled tokens = %d, want <= 200", totalTokens)
	}
}

func TestAssemblerFreshTailMaxTokensCapsProtectedTail(t *testing.T) {
	s, convID := setupAssemblerStore(t)
	ctx := context.Background()

	var items []ContextItem
	for i := 0; i < FreshTailCount; i++ {
		msg, err := s.AddMessage(ctx, convID, "user", "fresh message", 10)
		if err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
		items = append(items, ContextItem{
			Ordinal:    (i + 1) * OrdinalStep,
			ItemType:   "message",
			MessageID:  msg.ID,
			TokenCount: 10,
		})
	}
	if err := s.UpsertContextItems(ctx, convID, items); err != nil {
		t.Fatalf("UpsertContextItems: %v", err)
	}

	a := &Assembler{store: s, config: Config{FreshTailMaxTokens: 50}}
	result, err := a.Assemble(ctx, convID, AssembleInput{Budget: 1000})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(result.Messages) != 5 {
		t.Fatalf("messages = %d, want 5 capped by freshTailMaxTokens", len(result.Messages))
	}
}

func TestAssemblerFreshTailMaxTokensPreservesNewestMessageOverCap(t *testing.T) {
	s, convID := setupAssemblerStore(t)
	ctx := context.Background()

	msg1, err := s.AddMessage(ctx, convID, "user", "small", 10)
	if err != nil {
		t.Fatalf("AddMessage small: %v", err)
	}
	msg2, err := s.AddMessage(ctx, convID, "user", strings.Repeat("huge ", 100), 100)
	if err != nil {
		t.Fatalf("AddMessage huge: %v", err)
	}
	if upsertErr := s.UpsertContextItems(ctx, convID, []ContextItem{
		{Ordinal: 100, ItemType: "message", MessageID: msg1.ID, TokenCount: 10},
		{Ordinal: 200, ItemType: "message", MessageID: msg2.ID, TokenCount: 100},
	}); upsertErr != nil {
		t.Fatalf("UpsertContextItems: %v", upsertErr)
	}

	a := &Assembler{store: s, config: Config{FreshTailMaxTokens: 50}}
	result, err := a.Assemble(ctx, convID, AssembleInput{Budget: 1000})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("messages = %d, want newest oversized message preserved", len(result.Messages))
	}
	if result.Messages[0].ID != msg2.ID {
		t.Fatalf("preserved message id = %d, want %d", result.Messages[0].ID, msg2.ID)
	}
}

func TestAssemblerBudgetPreservesLatestToolTurnWhenItExceedsBudget(t *testing.T) {
	s, convID := setupAssemblerStore(t)
	ctx := context.Background()

	oldMsg, _ := s.AddMessage(ctx, convID, "assistant", "older context", 20)
	userMsg, _ := s.AddMessage(ctx, convID, "user", "inspect the file", 5)
	assistantToolMsg, _ := s.AddMessageWithParts(ctx, convID, "assistant", []MessagePart{
		{
			Type:       "tool_use",
			Name:       "read_file",
			Arguments:  `{"path":"/tmp/test.txt"}`,
			ToolCallID: "tc_1",
		},
	}, 5)
	toolResultMsg, _ := s.AddMessageWithParts(ctx, convID, "tool", []MessagePart{
		{
			Type:       "tool_result",
			ToolCallID: "tc_1",
			Text:       "very large tool output",
		},
	}, 200)
	finalAssistantMsg, _ := s.AddMessage(ctx, convID, "assistant", "done", 5)

	if err := s.UpsertContextItems(ctx, convID, []ContextItem{
		{Ordinal: 100, ItemType: "message", MessageID: oldMsg.ID, TokenCount: 20},
		{Ordinal: 200, ItemType: "message", MessageID: userMsg.ID, TokenCount: 5},
		{Ordinal: 300, ItemType: "message", MessageID: assistantToolMsg.ID, TokenCount: 5},
		{Ordinal: 400, ItemType: "message", MessageID: toolResultMsg.ID, TokenCount: 200},
		{Ordinal: 500, ItemType: "message", MessageID: finalAssistantMsg.ID, TokenCount: 5},
	}); err != nil {
		t.Fatal(err)
	}

	a := &Assembler{store: s, config: Config{}}
	result, err := a.Assemble(ctx, convID, AssembleInput{Budget: 210})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if len(result.Messages) != 4 {
		t.Fatalf("Messages = %d, want 4 protected-turn messages", len(result.Messages))
	}
	if result.Messages[0].ID != userMsg.ID {
		t.Fatalf("first message ID = %d, want current user message %d", result.Messages[0].ID, userMsg.ID)
	}
	if result.Messages[1].ID != assistantToolMsg.ID {
		t.Fatalf("second message ID = %d, want assistant tool-call %d", result.Messages[1].ID, assistantToolMsg.ID)
	}
	if result.Messages[2].ID != toolResultMsg.ID {
		t.Fatalf("third message ID = %d, want tool result %d", result.Messages[2].ID, toolResultMsg.ID)
	}
	if result.Messages[3].ID != finalAssistantMsg.ID {
		t.Fatalf("fourth message ID = %d, want final assistant %d", result.Messages[3].ID, finalAssistantMsg.ID)
	}

	totalTokens := 0
	for _, msg := range result.Messages {
		totalTokens += msg.TokenCount
	}
	if totalTokens <= 210 {
		t.Fatalf("assembled tokens = %d, want protected turn to remain over budget", totalTokens)
	}
}

func TestAssemblerBudgetFitsAll(t *testing.T) {
	s, convID := setupAssemblerStore(t)
	ctx := context.Background()

	msgs := make([]*Message, 5)
	for i := 0; i < 5; i++ {
		m, _ := s.AddMessage(ctx, convID, "user", "msg", 10)
		msgs[i] = m
	}

	items := make([]ContextItem, 5)
	for i := 0; i < 5; i++ {
		items[i] = ContextItem{
			Ordinal:    (i + 1) * 100,
			ItemType:   "message",
			MessageID:  msgs[i].ID,
			TokenCount: 10,
		}
	}
	if err := s.UpsertContextItems(ctx, convID, items); err != nil {
		t.Fatal(err)
	}

	// Budget = 100, total = 50, FreshTailCount=32 → all items in tail
	a := &Assembler{store: s, config: Config{}}
	result, err := a.Assemble(ctx, convID, AssembleInput{Budget: 100})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if len(result.Messages) != 5 {
		t.Errorf("Messages = %d, want 5", len(result.Messages))
	}
}

func TestAssemblerSummaryXMLFormat(t *testing.T) {
	s, convID := setupAssemblerStore(t)
	ctx := context.Background()

	summary, _ := s.CreateSummary(ctx, CreateSummaryInput{
		ConversationID: convID,
		Kind:           SummaryKindLeaf,
		Depth:          0,
		Content:        "test summary content",
		TokenCount:     20,
	})

	msg, _ := s.AddMessage(ctx, convID, "user", "hello", 5)

	if err := s.UpsertContextItems(ctx, convID, []ContextItem{
		{Ordinal: 100, ItemType: "summary", SummaryID: summary.SummaryID, TokenCount: 20},
		{Ordinal: 200, ItemType: "message", MessageID: msg.ID, TokenCount: 5},
	}); err != nil {
		t.Fatal(err)
	}

	a := &Assembler{store: s, config: Config{}}
	result, err := a.Assemble(ctx, convID, AssembleInput{Budget: 1000})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// Messages should only contain raw messages (no XML summary in Messages)
	if len(result.Messages) != 1 {
		t.Errorf("Messages = %d, want 1 (raw message only)", len(result.Messages))
	}
	// Summary should contain XML with summary content
	if checkpointContent(result.Checkpoint) == "" {
		t.Fatal("Summary should not be empty")
	}
	if !contains(checkpointContent(result.Checkpoint), "<summary") {
		t.Errorf("Summary missing <summary tag: %q", checkpointContent(result.Checkpoint))
	}
	if !contains(checkpointContent(result.Checkpoint), summary.SummaryID) {
		t.Errorf("Summary missing summary ID: %q", checkpointContent(result.Checkpoint))
	}
}

func TestAssemblerSummaryXMLEscaping(t *testing.T) {
	// Summary content with special XML characters should be properly escaped
	s, convID := setupAssemblerStore(t)
	ctx := context.Background()

	// Create summary with content containing XML special characters
	summary, _ := s.CreateSummary(ctx, CreateSummaryInput{
		ConversationID: convID,
		Kind:           SummaryKindLeaf,
		Depth:          0,
		Content:        `User said: "hello" & asked about <tags>`,
		TokenCount:     20,
	})

	if err := s.UpsertContextItems(ctx, convID, []ContextItem{
		{Ordinal: 100, ItemType: "summary", SummaryID: summary.SummaryID, TokenCount: 20},
	}); err != nil {
		t.Fatal(err)
	}

	a := &Assembler{store: s, config: Config{}}
	result, err := a.Assemble(ctx, convID, AssembleInput{Budget: 1000})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// checkpoint content should contain XML with escaped delimiters while keeping
	// text quotes readable. Quotes only need escaping in attributes.
	if checkpointContent(result.Checkpoint) == "" {
		t.Fatal("Summary should not be empty")
	}

	// Check that special characters are escaped
	if strings.Contains(checkpointContent(result.Checkpoint), "<tags>") {
		t.Errorf("BUG: unescaped < in summary content: %q", checkpointContent(result.Checkpoint))
	}
	if !strings.Contains(checkpointContent(result.Checkpoint), `"hello"`) {
		t.Errorf("summary content quotes should remain readable: %q", checkpointContent(result.Checkpoint))
	}
	// & should be escaped as &amp;
	if strings.Contains(checkpointContent(result.Checkpoint), " & ") {
		t.Errorf("BUG: unescaped & in summary content: %q", checkpointContent(result.Checkpoint))
	}
}

func TestAssemblerSummaryXMLWithParents(t *testing.T) {
	s, convID := setupAssemblerStore(t)
	ctx := context.Background()

	// Create a leaf and a condensed summary (condensed has parent)
	leaf, _ := s.CreateSummary(ctx, CreateSummaryInput{
		ConversationID: convID,
		Kind:           SummaryKindLeaf,
		Depth:          0,
		Content:        "leaf content",
		TokenCount:     20,
	})
	condensed, _ := s.CreateSummary(ctx, CreateSummaryInput{
		ConversationID: convID,
		Kind:           SummaryKindCondensed,
		Depth:          1,
		Content:        "condensed content",
		TokenCount:     15,
		ParentIDs:      []string{leaf.SummaryID},
	})

	msg, _ := s.AddMessage(ctx, convID, "user", "fresh", 5)

	if err := s.UpsertContextItems(ctx, convID, []ContextItem{
		{Ordinal: 100, ItemType: "summary", SummaryID: condensed.SummaryID, TokenCount: 15},
		{Ordinal: 200, ItemType: "message", MessageID: msg.ID, TokenCount: 5},
	}); err != nil {
		t.Fatal(err)
	}

	a := &Assembler{store: s, config: Config{}}
	result, err := a.Assemble(ctx, convID, AssembleInput{Budget: 1000})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// checkpoint content should contain XML with parent information
	if checkpointContent(result.Checkpoint) == "" {
		t.Fatal("Summary should not be empty")
	}
	xmlContent := checkpointContent(result.Checkpoint)

	// Should contain <parents> section with parent ID
	if !contains(xmlContent, "<parents>") {
		t.Errorf("condensed summary XML missing <parents> section: %q", xmlContent)
	}
	if !contains(xmlContent, leaf.SummaryID) {
		t.Errorf("condensed summary XML missing parent ID %q: %q", leaf.SummaryID, xmlContent)
	}

	// Should contain kind="condensed"
	if !contains(xmlContent, `kind="condensed"`) {
		t.Errorf("condensed summary XML missing kind attribute: %q", xmlContent)
	}
}

func TestAssemblerSummaryXMLIncludesDescendantCount(t *testing.T) {
	s, convID := setupAssemblerStore(t)
	ctx := context.Background()

	// Create a leaf summary with specific descendant count
	leaf, _ := s.CreateSummary(ctx, CreateSummaryInput{
		ConversationID:       convID,
		Kind:                 SummaryKindLeaf,
		Depth:                0,
		Content:              "leaf content",
		TokenCount:           20,
		DescendantCount:      8,
		DescendantTokenCount: 1200,
	})

	msg, _ := s.AddMessage(ctx, convID, "user", "fresh", 5)

	if err := s.UpsertContextItems(ctx, convID, []ContextItem{
		{Ordinal: 100, ItemType: "summary", SummaryID: leaf.SummaryID, TokenCount: 20},
		{Ordinal: 200, ItemType: "message", MessageID: msg.ID, TokenCount: 5},
	}); err != nil {
		t.Fatal(err)
	}

	a := &Assembler{store: s, config: Config{}}
	result, err := a.Assemble(ctx, convID, AssembleInput{Budget: 1000})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if checkpointContent(result.Checkpoint) == "" {
		t.Fatal("Summary should not be empty")
	}
	xmlContent := checkpointContent(result.Checkpoint)

	// Should contain descendant_count="8"
	if !contains(xmlContent, `descendant_count="8"`) {
		t.Errorf("summary XML missing descendant_count attribute: %q", xmlContent)
	}
}

func TestAssemblerLeafSummaryNoParents(t *testing.T) {
	s, convID := setupAssemblerStore(t)
	ctx := context.Background()

	// Leaf summary has no parents
	leaf, _ := s.CreateSummary(ctx, CreateSummaryInput{
		ConversationID: convID,
		Kind:           SummaryKindLeaf,
		Depth:          0,
		Content:        "leaf content",
		TokenCount:     20,
	})

	msg, _ := s.AddMessage(ctx, convID, "user", "fresh", 5)

	if err := s.UpsertContextItems(ctx, convID, []ContextItem{
		{Ordinal: 100, ItemType: "summary", SummaryID: leaf.SummaryID, TokenCount: 20},
		{Ordinal: 200, ItemType: "message", MessageID: msg.ID, TokenCount: 5},
	}); err != nil {
		t.Fatal(err)
	}

	a := &Assembler{store: s, config: Config{}}
	result, err := a.Assemble(ctx, convID, AssembleInput{Budget: 1000})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if checkpointContent(result.Checkpoint) == "" {
		t.Fatal("Summary should not be empty")
	}
	xmlContent := checkpointContent(result.Checkpoint)

	// Leaf summary should NOT have <parents> section
	if contains(xmlContent, "<parents>") {
		t.Errorf("leaf summary XML should not have <parents> section: %q", xmlContent)
	}
}

func TestAssemblerDepthAwarePrompt(t *testing.T) {
	s, convID := setupAssemblerStore(t)
	ctx := context.Background()

	// Create a condensed summary (depth >= 2) to trigger full guidance
	now := time.Now().UTC()
	leaf, _ := s.CreateSummary(ctx, CreateSummaryInput{
		ConversationID: convID,
		Kind:           SummaryKindLeaf,
		Depth:          0,
		Content:        "leaf summary",
		TokenCount:     20,
		EarliestAt:     &now,
		LatestAt:       &now,
	})
	condensed, _ := s.CreateSummary(ctx, CreateSummaryInput{
		ConversationID:       convID,
		Kind:                 SummaryKindCondensed,
		Depth:                2,
		Content:              "condensed summary",
		TokenCount:           15,
		ParentIDs:            []string{leaf.SummaryID},
		DescendantCount:      1,
		DescendantTokenCount: 20,
	})

	msg, _ := s.AddMessage(ctx, convID, "user", "fresh", 5)

	if err := s.UpsertContextItems(ctx, convID, []ContextItem{
		{Ordinal: 100, ItemType: "summary", SummaryID: condensed.SummaryID, TokenCount: 15},
		{Ordinal: 200, ItemType: "message", MessageID: msg.ID, TokenCount: 5},
	}); err != nil {
		t.Fatal(err)
	}

	a := &Assembler{store: s, config: Config{}}
	result, err := a.Assemble(ctx, convID, AssembleInput{Budget: 1000})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// Should have a depth-aware prompt in checkpoint content
	if checkpointContent(result.Checkpoint) == "" {
		t.Error("expected non-empty Summary when depth >= 2")
	}
	// systemPromptAddition is embedded in checkpoint content
	if !strings.Contains(checkpointContent(result.Checkpoint), "multi-level summarization") {
		t.Error("Summary should contain system prompt addition about multi-level summarization")
	}
}

func TestFormatSummaryXMLUsesSummaryRef(t *testing.T) {
	// Spec: condensed summaries use <summary_ref id="parentId" /> not <parent>parentId</parent>
	now := time.Now().UTC()
	s := Summary{
		SummaryID:       "sum_condensed1",
		Kind:            SummaryKindCondensed,
		Depth:           1,
		Content:         "condensed content",
		TokenCount:      50,
		DescendantCount: 2,
		EarliestAt:      &now,
		LatestAt:        &now,
	}
	parentIDs := []string{"sum_leaf1", "sum_leaf2"}

	xml := FormatSummaryXML(&s, parentIDs)

	// Must use <summary_ref id="..." /> per spec
	if !contains(xml, `<summary_ref id="sum_leaf1" />`) {
		t.Errorf("expected <summary_ref id=\"sum_leaf1\" />, got: %s", xml)
	}
	if !contains(xml, `<summary_ref id="sum_leaf2" />`) {
		t.Errorf("expected <summary_ref id=\"sum_leaf2\" />, got: %s", xml)
	}
	// Must NOT use old <parent> tag
	if contains(xml, "<parent>") {
		t.Errorf("should not use <parent> tag, got: %s", xml)
	}
}

func TestFormatSummaryXMLIncludesTimestamps(t *testing.T) {
	// Spec: summary XML includes earliest_at and latest_at attributes
	earliest := time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
	latest := time.Date(2026, 3, 15, 14, 30, 0, 0, time.UTC)
	s := Summary{
		SummaryID:       "sum_leaf1",
		Kind:            SummaryKindLeaf,
		Depth:           0,
		Content:         "leaf content",
		TokenCount:      30,
		DescendantCount: 0,
		EarliestAt:      &earliest,
		LatestAt:        &latest,
	}

	xml := FormatSummaryXML(&s, nil)

	if !contains(xml, `earliest_at="2026-03-15T10:00:00Z"`) {
		t.Errorf("missing earliest_at attribute, got: %s", xml)
	}
	if !contains(xml, `latest_at="2026-03-15T14:30:00Z"`) {
		t.Errorf("missing latest_at attribute, got: %s", xml)
	}
}

func TestFormatSummaryXMLNoTimestampsWhenNil(t *testing.T) {
	// When EarliestAt/LatestAt are nil, attributes should be omitted
	s := Summary{
		SummaryID:       "sum_leaf1",
		Kind:            SummaryKindLeaf,
		Depth:           0,
		Content:         "leaf content",
		TokenCount:      30,
		DescendantCount: 0,
	}

	xml := FormatSummaryXML(&s, nil)

	if contains(xml, "earliest_at=") {
		t.Errorf("should not have earliest_at when nil, got: %s", xml)
	}
	if contains(xml, "latest_at=") {
		t.Errorf("should not have latest_at when nil, got: %s", xml)
	}
}

func TestFormatSummaryXMLEscapesAttributesButNotTextQuotes(t *testing.T) {
	s := Summary{
		SummaryID:       `sum_"quoted"`,
		Kind:            SummaryKindLeaf,
		Depth:           0,
		Content:         `text with "quotes" and <tag>`,
		TokenCount:      30,
		DescendantCount: 0,
	}

	xml := FormatSummaryXML(&s, nil)

	if !contains(xml, `id="sum_&quot;quoted&quot;"`) {
		t.Fatalf("summary id attribute was not escaped: %s", xml)
	}
	if contains(xml, `&quot;quotes&quot;`) {
		t.Fatalf("text quotes should remain readable in content: %s", xml)
	}
	if !contains(xml, `"quotes"`) {
		t.Fatalf("text quotes missing from content: %s", xml)
	}
	if !contains(xml, `&lt;tag&gt;`) {
		t.Fatalf("text XML delimiters should be escaped: %s", xml)
	}
}
