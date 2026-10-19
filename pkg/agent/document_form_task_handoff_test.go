package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestDocumentFormTaskHandoffReadmitsOnlyNewPDFSelectorsAndMedia(t *testing.T) {
	workspace := t.TempDir()
	writePDFSkillForTest(t, workspace)
	registry := tools.NewToolRegistry()
	registry.RegisterHidden(tools.NewDocumentTool())
	registry.Register(tools.NewBM25SearchTool(registry, 5, 5))
	agent := &AgentInstance{
		ID:             "main",
		Workspace:      workspace,
		Tools:          registry,
		ContextBuilder: NewContextBuilder(workspace),
	}
	store := media.NewFileMediaStore()
	t.Cleanup(store.Stop)
	path := filepath.Join(t.TempDir(), "new.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7\nNEW_PRIVATE_SOURCE\n%%EOF\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(path, media.MediaMeta{
		Filename: "new.pdf", ContentType: "application/pdf",
		CleanupPolicy: media.CleanupPolicyForgetOnly,
	}, "new-input")
	if err != nil {
		t.Fatal(err)
	}
	ts := documentTestTurnState(agent, "")
	ts.media = nil
	ts.userMessage = "Protected form input is ready."
	ts.documentLocalPaths = []string{"/srv/OLD_PRIVATE_FORM.pdf"}
	owner, err := nodeFileMediaOwnerForTurn(ts)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.BindOwner(ref, owner); err != nil {
		t.Fatal(err)
	}
	input := &toolshared.ToolContextInput{
		Text:  `Read the new "/srv/NEW_PRIVATE_FORM.pdf" instead.`,
		Media: []string{ref},
	}
	pipeline := &Pipeline{Context: PipelineContextServices{MediaResolver: store}}
	pipeline.prepareDocumentHandoff(ts, input)
	if ts.userMessage != "Protected form input is ready." || len(ts.media) != 0 ||
		len(ts.documentProjections) != 1 || ts.documentProjections[0].Ref != ref ||
		len(ts.documentLocalPaths) != 1 || ts.documentLocalPaths[0] != "/srv/NEW_PRIVATE_FORM.pdf" {
		t.Fatal("handoff changed canonical root input, inherited an old selector, or failed new PDF admission")
	}
	ctx := toolExecutionContextForTurn(t.Context(), ts)
	if !toolshared.ToolDocumentRefAllowed(ctx, ref) ||
		!toolshared.ToolDocumentLocalPathAllowed(ctx, "/srv/NEW_PRIVATE_FORM.pdf") ||
		toolshared.ToolDocumentLocalPathAllowed(ctx, "/srv/OLD_PRIVATE_FORM.pdf") {
		t.Fatal("new document selectors were not exact live tool authority")
	}
	messages := pipeline.resolveFollowupMedia([]providers.Message{
		{Role: "user", Content: ts.userMessage},
		{Role: "tool", ToolCallID: "native-task-handoff", Content: "new task", Media: []string{ref}},
	}, ts, config.DefaultMaxMediaSize)
	if len(messages[1].Media) != 0 || len(messages[1].Attachments) != 1 ||
		messages[1].Attachments[0].Type != "document" ||
		!strings.Contains(messages[1].Content, ref) ||
		strings.Contains(messages[1].Content, path) ||
		strings.Contains(messages[1].Content, "NEW_PRIVATE_SOURCE") {
		t.Fatal("live handoff PDF was omitted or treated as an image rather than a document")
	}
	encoded, err := json.Marshal(&toolshared.ToolResult{ForLLM: "safe metadata", ContextInput: input})
	if err != nil || strings.Contains(string(encoded), "PRIVATE_") || strings.Contains(string(encoded), ref) {
		t.Fatal("new input handoff escaped ordinary result JSON")
	}
	messages[1].LiveToolContextID = "native-live-input"
	consumeLiveToolContextMessages(messages, []liveToolContextProjection{{
		toolCallID: "native-task-handoff", messageID: "native-live-input", durableContent: "safe native metadata",
	}})
	if len(messages[1].Attachments) != 0 || len(messages[1].Media) != 0 ||
		messages[1].Content != "safe native metadata" {
		t.Fatal("consumed live PDF descriptor survived in the model transcript")
	}
}
