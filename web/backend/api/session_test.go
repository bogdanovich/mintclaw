package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

func sessionsTestDir(t *testing.T, configPath string) string {
	t.Helper()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	dir := filepath.Join(cfg.Agents.Defaults.Workspace, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	return dir
}

func mintClawTestScope(t *testing.T, sessionID string) json.RawMessage {
	t.Helper()
	scope, err := json.Marshal(session.SessionScope{
		Version:    session.ScopeVersion,
		AgentID:    "main",
		Channel:    "mintclaw",
		Account:    "default",
		Dimensions: []string{"sender"},
		Values: map[string]string{
			"sender": "mintclaw-user",
		},
		ClientSessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("Marshal(session scope) error = %v", err)
	}
	return scope
}

func newMintClawTestSession(t *testing.T, store *memory.JSONLStore, sessionID string) string {
	t.Helper()
	key := session.BuildOpaqueSessionKey("mintclaw|session=" + sessionID)
	if err := store.UpsertSessionMeta(t.Context(), key, mintClawTestScope(t, sessionID), sessionID); err != nil {
		t.Fatalf("UpsertSessionMeta() error = %v", err)
	}
	return key
}

func setMintClawTestSessionUpdatedAt(
	t *testing.T,
	store *memory.JSONLStore,
	dir string,
	sessionKey string,
	updatedAt time.Time,
) {
	t.Helper()
	meta, err := store.GetSessionMeta(t.Context(), sessionKey)
	if err != nil {
		t.Fatalf("GetSessionMeta() error = %v", err)
	}
	meta.UpdatedAt = updatedAt
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("Marshal(meta) error = %v", err)
	}
	path := filepath.Join(dir, sanitizeSessionKey(sessionKey)+".meta.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile(meta) error = %v", err)
	}
}

func assertVisibleToolCallMessage(
	t *testing.T,
	msg sessionChatMessage,
	toolName string,
) bus.OutboundToolCall {
	t.Helper()

	if msg.Role != "assistant" || msg.Kind != "tool_calls" {
		t.Fatalf("message = %#v, want assistant/tool_calls", msg)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("len(message.ToolCalls) = %d, want 1", len(msg.ToolCalls))
	}
	if got := msg.ToolCalls[0].Function; got == nil || got.Name != toolName {
		t.Fatalf("tool call = %#v, want function %q", msg.ToolCalls[0], toolName)
	}
	return msg.ToolCalls[0]
}

func testProviderToolCall(t *testing.T, id, name, arguments, explanation string) providers.ToolCall {
	t.Helper()
	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(arguments), &decoded); err != nil {
		t.Fatalf("Unmarshal(tool call arguments) error = %v", err)
	}
	return providers.ToolCall{
		ID:                      id,
		Type:                    "function",
		Name:                    name,
		Arguments:               decoded,
		ToolFeedbackExplanation: explanation,
	}
}

func TestHandleListSessions_JSONLStorage(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, storeErr := memory.NewJSONLStore(dir)
	if storeErr != nil {
		t.Fatalf("NewJSONLStore() error = %v", storeErr)
	}

	sessionKey := newMintClawTestSession(t, store, "history-jsonl")
	if err := store.AddFullMessage(context.Background(), sessionKey, providers.Message{
		Role:    "user",
		Content: "Explain why the history API is empty after migration.",
	}); err != nil {
		t.Fatalf("AddFullMessage(user) error = %v", err)
	}
	if err := store.AddFullMessage(context.Background(), sessionKey, providers.Message{
		Role:    "assistant",
		Content: "Because the API still reads only legacy JSON session files.",
	}); err != nil {
		t.Fatalf("AddFullMessage(assistant) error = %v", err)
	}
	if err := store.AddFullMessage(context.Background(), sessionKey, providers.Message{
		Role:    "tool",
		Content: "ignored",
	}); err != nil {
		t.Fatalf("AddFullMessage(tool) error = %v", err)
	}
	if err := store.SetSummary(context.Background(), sessionKey, "JSONL-backed session"); err != nil {
		t.Fatalf("SetSummary() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var items []sessionListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].ID != "history-jsonl" {
		t.Fatalf("items[0].ID = %q, want %q", items[0].ID, "history-jsonl")
	}
	if items[0].MessageCount != 2 {
		t.Fatalf("items[0].MessageCount = %d, want 2", items[0].MessageCount)
	}
	if items[0].Title != "Explain why the history API is empty after migration." {
		t.Fatalf(
			"items[0].Title = %q, want %q",
			items[0].Title,
			"Explain why the history API is empty after migration.",
		)
	}
	if items[0].Preview != "Explain why the history API is empty after migration." {
		t.Fatalf("items[0].Preview = %q", items[0].Preview)
	}
}

func TestHandleListSessions_TransientThoughtDoesNotInflateMessageCount(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	const sessionID = "history-jsonl-transient"
	sessionKey := session.BuildOpaqueSessionKey("mintclaw|session=" + sessionID)
	base := filepath.Join(dir, sanitizeSessionKey(sessionKey))
	now := time.Now().UTC()

	rawJSONL := strings.Join([]string{
		`{"role":"user","content":"keep me"}`,
		`{"role":"assistant","content":"","reasoning_content":"dangling thought"}`,
		`{"role":"assistant","content":"and me"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(base+".jsonl", []byte(rawJSONL), 0o644); err != nil {
		t.Fatalf("WriteFile(jsonl) error = %v", err)
	}
	metaData, err := json.Marshal(memory.SessionMeta{
		Key:       sessionKey,
		Count:     3,
		Skip:      0,
		CreatedAt: now,
		UpdatedAt: now,
		Scope:     mintClawTestScope(t, sessionID),
		ClientSessionIDs: []string{
			sessionID,
		},
	})
	if err != nil {
		t.Fatalf("Marshal(meta) error = %v", err)
	}
	if err := os.WriteFile(base+".meta.json", metaData, 0o644); err != nil {
		t.Fatalf("WriteFile(meta) error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var items []sessionListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].ID != "history-jsonl-transient" {
		t.Fatalf("items[0].ID = %q, want %q", items[0].ID, "history-jsonl-transient")
	}
	if items[0].MessageCount != 2 {
		t.Fatalf("items[0].MessageCount = %d, want 2 after dropping transient thought", items[0].MessageCount)
	}
}

func TestHandleListSessions_TitleUsesFirstUserMessage(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, storeErr := memory.NewJSONLStore(dir)
	if storeErr != nil {
		t.Fatalf("NewJSONLStore() error = %v", storeErr)
	}

	sessionKey := newMintClawTestSession(t, store, "summary-title")
	if err := store.AddFullMessage(context.Background(), sessionKey, providers.Message{
		Role:    "user",
		Content: "fallback preview",
	}); err != nil {
		t.Fatalf("AddFullMessage() error = %v", err)
	}
	if err := store.SetSummary(
		context.Background(),
		sessionKey,
		"  This summary is intentionally longer than sixty characters so it must be truncated in the history menu.  ",
	); err != nil {
		t.Fatalf("SetSummary() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var items []sessionListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	expectedTitle := truncateRunes("fallback preview", maxSessionTitleRunes)
	if items[0].Title != expectedTitle {
		t.Fatalf("items[0].Title = %q", items[0].Title)
	}
	if items[0].Preview != "fallback preview" {
		t.Fatalf("items[0].Preview = %q, want %q", items[0].Preview, "fallback preview")
	}
}

func TestHandleGetSession_JSONLStorage(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := newMintClawTestSession(t, store, "detail-jsonl")
	for _, msg := range []providers.Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "second"},
		{Role: "tool", Content: "ignored"},
	} {
		if err := store.AddFullMessage(context.Background(), sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}
	if err := store.SetSummary(context.Background(), sessionKey, "detail summary"); err != nil {
		t.Fatalf("SetSummary() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/detail-jsonl", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		ID       string `json:"id"`
		Summary  string `json:"summary"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if resp.ID != "detail-jsonl" {
		t.Fatalf("resp.ID = %q, want %q", resp.ID, "detail-jsonl")
	}
	if resp.Summary != "detail summary" {
		t.Fatalf("resp.Summary = %q, want %q", resp.Summary, "detail summary")
	}
	if len(resp.Messages) != 2 {
		t.Fatalf("len(resp.Messages) = %d, want 2", len(resp.Messages))
	}
	if resp.Messages[0].Role != "user" || resp.Messages[0].Content != "first" {
		t.Fatalf("first message = %#v, want user/first", resp.Messages[0])
	}
	if resp.Messages[1].Role != "assistant" || resp.Messages[1].Content != "second" {
		t.Fatalf("second message = %#v, want assistant/second", resp.Messages[1])
	}
}

func TestHandleGetSession_ExposesHandledToolAttachmentsThroughMediaProxy(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := newMintClawTestSession(t, store, "attachment-history")
	for _, msg := range []providers.Message{
		{Role: "user", Content: "send me the report"},
		{
			Role:    "assistant",
			Content: handledToolResponseSummaryText,
			Attachments: []providers.Attachment{{
				Type:        "file",
				Ref:         "media://attachment-1",
				Filename:    "report.txt",
				ContentType: "text/plain",
			}},
		},
	} {
		if err := store.AddFullMessage(context.Background(), sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/attachment-history", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []sessionChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if len(resp.Messages) != 2 {
		t.Fatalf("len(resp.Messages) = %d, want 2", len(resp.Messages))
	}
	if resp.Messages[0].Role != "user" || resp.Messages[0].Content != "send me the report" {
		t.Fatalf("message = %#v, want user request", resp.Messages[0])
	}
	attachmentMessage := resp.Messages[1]
	if attachmentMessage.Role != "assistant" || attachmentMessage.Content != "" ||
		len(attachmentMessage.Attachments) != 1 {
		t.Fatalf("attachment message = %#v, want assistant attachment", attachmentMessage)
	}
	attachment := attachmentMessage.Attachments[0]
	if attachment.URL != "/mintclaw/media/attachment-1" || attachment.Filename != "report.txt" ||
		attachment.ContentType != "text/plain" {
		t.Fatalf("attachment = %#v, want media proxy URL and metadata", attachment)
	}
}

func TestHandleGetSession_ExposesHandledToolAttachmentsWithDurableURL(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := newMintClawTestSession(t, store, "attachment-history-durable")
	for _, msg := range []providers.Message{
		{Role: "user", Content: "send me the report"},
		{
			Role:    "assistant",
			Content: handledToolResponseSummaryText,
			Attachments: []providers.Attachment{{
				Type:        "file",
				URL:         "https://example.com/report.txt",
				Filename:    "report.txt",
				ContentType: "text/plain",
			}},
		},
	} {
		if err := store.AddFullMessage(context.Background(), sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/attachment-history-durable", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []sessionChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if len(resp.Messages) != 2 {
		t.Fatalf("len(resp.Messages) = %d, want 2", len(resp.Messages))
	}

	assistant := resp.Messages[1]
	if assistant.Role != "assistant" {
		t.Fatalf("assistant role = %q, want assistant", assistant.Role)
	}
	if assistant.Content != "" {
		t.Fatalf("assistant content = %q, want empty string", assistant.Content)
	}
	if len(assistant.Attachments) != 1 {
		t.Fatalf("len(assistant.Attachments) = %d, want 1", len(assistant.Attachments))
	}
	if assistant.Attachments[0].URL != "https://example.com/report.txt" {
		t.Fatalf(
			"attachment url = %q, want %q",
			assistant.Attachments[0].URL,
			"https://example.com/report.txt",
		)
	}
	if assistant.Attachments[0].Filename != "report.txt" {
		t.Fatalf("attachment filename = %q, want %q", assistant.Attachments[0].Filename, "report.txt")
	}
}

func TestHandleSessionsRejectsPreviousReleaseScopeOnlyMetadata(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, storeErr := memory.NewJSONLStore(dir)
	if storeErr != nil {
		t.Fatalf("NewJSONLStore() error = %v", storeErr)
	}

	sessionKey := session.BuildOpaqueSessionKey("mintclaw|session=scope-jsonl")
	if err := store.AddFullMessage(context.Background(), sessionKey, providers.Message{
		Role:    "user",
		Content: "scope discovered session",
	}); err != nil {
		t.Fatalf("AddFullMessage() error = %v", err)
	}
	if err := store.SetSummary(context.Background(), sessionKey, "scope summary"); err != nil {
		t.Fatalf("SetSummary() error = %v", err)
	}

	scopeData, err := json.Marshal(session.SessionScope{
		Version:    session.ScopeVersion,
		AgentID:    "main",
		Channel:    "mintclaw",
		Account:    "default",
		Dimensions: []string{"chat"},
		Values: map[string]string{
			"chat": "direct:mintclaw:scope-jsonl",
		},
	})
	if err != nil {
		t.Fatalf("Marshal(scope) error = %v", err)
	}
	if err := store.UpsertSessionMeta(context.Background(), sessionKey, scopeData, ""); err != nil {
		t.Fatalf("UpsertSessionMeta() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	listRec := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d, body=%s", listRec.Code, http.StatusOK, listRec.Body.String())
	}

	var items []sessionListItem
	if err := json.Unmarshal(listRec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal(list) error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("len(items) = %d, want 0", len(items))
	}

	detailRec := httptest.NewRecorder()
	detailReq := httptest.NewRequest(http.MethodGet, "/api/sessions/scope-jsonl", nil)
	mux.ServeHTTP(detailRec, detailReq)
	if detailRec.Code != http.StatusNotFound {
		t.Fatalf(
			"detail status = %d, want %d, body=%s",
			detailRec.Code,
			http.StatusNotFound,
			detailRec.Body.String(),
		)
	}

	deleteRec := httptest.NewRecorder()
	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/sessions/scope-jsonl", nil)
	mux.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusNotFound {
		t.Fatalf(
			"delete status = %d, want %d, body=%s",
			deleteRec.Code,
			http.StatusNotFound,
			deleteRec.Body.String(),
		)
	}
}

func TestHandleSessions_SharedHistoryResolvesAllCurrentClientIDs(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, storeErr := memory.NewJSONLStore(dir)
	if storeErr != nil {
		t.Fatalf("NewJSONLStore() error = %v", storeErr)
	}
	backend := session.NewJSONLBackend(store)
	sessionKey := session.BuildOpaqueSessionKey("mintclaw|sender=mintclaw-user")
	baseScope := session.SessionScope{
		Version:    session.ScopeVersion,
		AgentID:    "main",
		Channel:    "mintclaw",
		Dimensions: []string{"sender"},
		Values:     map[string]string{"sender": "mintclaw-user"},
	}
	for _, clientID := range []string{"browser-1", "browser-2"} {
		scope := session.CloneScope(&baseScope)
		scope.ClientSessionID = clientID
		backend.EnsureSessionMetadata(sessionKey, scope)
	}
	meta, err := store.GetSessionMeta(context.Background(), sessionKey)
	if err != nil {
		t.Fatalf("GetSessionMeta() error = %v", err)
	}
	if len(meta.ClientSessionIDs) != 2 ||
		meta.ClientSessionIDs[0] != "browser-1" || meta.ClientSessionIDs[1] != "browser-2" {
		t.Fatalf("ClientSessionIDs = %v, want [browser-1 browser-2]", meta.ClientSessionIDs)
	}
	if err := store.AddFullMessage(context.Background(), sessionKey, providers.Message{
		Role:    "user",
		Content: "shared browser history",
	}); err != nil {
		t.Fatalf("AddFullMessage() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	listRec := httptest.NewRecorder()
	mux.ServeHTTP(listRec, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d, body=%s", listRec.Code, http.StatusOK, listRec.Body.String())
	}
	var items []sessionListItem
	if err := json.Unmarshal(listRec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal(list) error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want one shared history", len(items))
	}

	for _, clientID := range []string{"browser-1", "browser-2"} {
		detailRec := httptest.NewRecorder()
		detailReq := httptest.NewRequest(http.MethodGet, "/api/sessions/"+clientID, nil)
		mux.ServeHTTP(detailRec, detailReq)
		if detailRec.Code != http.StatusOK {
			t.Fatalf(
				"detail %s status = %d, want %d, body=%s",
				clientID,
				detailRec.Code,
				http.StatusOK,
				detailRec.Body.String(),
			)
		}
	}
}

func TestHandleSessions_RepeatedClientIDSelectsNewestUsableHistory(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, storeErr := memory.NewJSONLStore(dir)
	if storeErr != nil {
		t.Fatalf("NewJSONLStore() error = %v", storeErr)
	}
	const clientID = "reused-browser"
	writeHistory := func(identity, content string, updatedAt time.Time) string {
		key := session.BuildOpaqueSessionKey(identity)
		if err := store.UpsertSessionMeta(t.Context(), key, mintClawTestScope(t, clientID), clientID); err != nil {
			t.Fatalf("UpsertSessionMeta() error = %v", err)
		}
		if err := store.AddFullMessage(
			t.Context(),
			key,
			providers.Message{Role: "user", Content: content},
		); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
		setMintClawTestSessionUpdatedAt(t, store, dir, key, updatedAt)
		return key
	}

	oldKey := writeHistory("mintclaw|history=old", "old history", time.Unix(1, 0).UTC())
	newKey := writeHistory("mintclaw|history=new", "new history", time.Unix(2, 0).UTC())
	emptyKey := session.BuildOpaqueSessionKey("mintclaw|history=metadata-only")
	if err := store.UpsertSessionMeta(t.Context(), emptyKey, mintClawTestScope(t, clientID), clientID); err != nil {
		t.Fatalf("UpsertSessionMeta(metadata-only) error = %v", err)
	}
	setMintClawTestSessionUpdatedAt(t, store, dir, emptyKey, time.Unix(3, 0).UTC())

	writeDirtyHistory := func(identity, content string, updatedAt time.Time, terminated bool) string {
		key := session.BuildOpaqueSessionKey(identity)
		if err := store.UpsertSessionMeta(t.Context(), key, mintClawTestScope(t, clientID), clientID); err != nil {
			t.Fatalf("UpsertSessionMeta(dirty) error = %v", err)
		}
		line, err := json.Marshal(providers.Message{Role: "user", Content: content})
		if err != nil {
			t.Fatalf("Marshal(dirty message) error = %v", err)
		}
		if terminated {
			line = append(line, '\n')
		}
		base := filepath.Join(dir, sanitizeSessionKey(key))
		if err := os.WriteFile(base+".jsonl", line, 0o644); err != nil {
			t.Fatalf("WriteFile(dirty jsonl) error = %v", err)
		}
		meta, err := store.GetSessionMeta(t.Context(), key)
		if err != nil {
			t.Fatalf("GetSessionMeta(dirty) error = %v", err)
		}
		meta.HistoryDirty = true
		meta.Count = 0
		meta.Skip = 0
		meta.UpdatedAt = updatedAt
		metaData, err := json.Marshal(meta)
		if err != nil {
			t.Fatalf("Marshal(dirty meta) error = %v", err)
		}
		if err := os.WriteFile(base+".meta.json", metaData, 0o644); err != nil {
			t.Fatalf("WriteFile(dirty meta) error = %v", err)
		}
		return key
	}

	// Model a gateway writer paused after its append became durable but before
	// its final metadata commit. The web API is a separate reader and must not
	// take ownership of journal recovery.
	dirtyKey := writeDirtyHistory(
		"mintclaw|history=dirty-first-append",
		"paused writer history",
		time.Unix(4, 0).UTC(),
		true,
	)
	unterminatedKey := writeDirtyHistory(
		"mintclaw|history=unterminated-first-append",
		"unterminated writer history",
		time.Unix(5, 0).UTC(),
		false,
	)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	listRec := httptest.NewRecorder()
	mux.ServeHTTP(listRec, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d, body=%s", listRec.Code, http.StatusOK, listRec.Body.String())
	}
	var items []sessionListItem
	if err := json.Unmarshal(listRec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal(list) error = %v", err)
	}
	if len(items) != 1 || items[0].ID != clientID || items[0].Preview != "paused writer history" {
		t.Fatalf("items = %#v, want one newest usable history", items)
	}
	for _, key := range []string{dirtyKey, unterminatedKey} {
		observedMeta, err := store.GetSessionMeta(t.Context(), key)
		if err != nil {
			t.Fatalf("GetSessionMeta(paused writer) error = %v", err)
		}
		if !observedMeta.HistoryDirty || observedMeta.Count != 0 {
			t.Fatalf("paused writer metadata was mutated by discovery: %#v", observedMeta)
		}
	}

	detailRec := httptest.NewRecorder()
	mux.ServeHTTP(
		detailRec,
		httptest.NewRequest(http.MethodGet, "/api/sessions/"+clientID, nil),
	)
	if detailRec.Code != http.StatusOK || !strings.Contains(detailRec.Body.String(), "paused writer history") {
		t.Fatalf("detail status = %d, body=%s", detailRec.Code, detailRec.Body.String())
	}

	deleteRec := httptest.NewRecorder()
	mux.ServeHTTP(
		deleteRec,
		httptest.NewRequest(http.MethodDelete, "/api/sessions/"+clientID, nil),
	)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want %d, body=%s", deleteRec.Code, http.StatusNoContent, deleteRec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, sanitizeSessionKey(dirtyKey)+".meta.json")); !os.IsNotExist(err) {
		t.Fatalf("paused-writer active metadata still exists: %v", err)
	}
	for _, key := range []string{oldKey, newKey, emptyKey, unterminatedKey} {
		if _, err := os.Stat(filepath.Join(dir, sanitizeSessionKey(key)+".meta.json")); err != nil {
			t.Fatalf("non-active metadata %q missing: %v", key, err)
		}
	}
}

func TestHandleGetSession_SkipsTransientThoughtMessages(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := newMintClawTestSession(t, store, "detail-transient-thought")
	for _, msg := range []providers.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", ReasoningContent: "internal chain of thought"},
		{Role: "assistant", Content: "final visible answer"},
	} {
		if err := store.AddFullMessage(context.Background(), sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/detail-transient-thought", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []sessionChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Messages) != 2 {
		t.Fatalf("len(resp.Messages) = %d, want 2", len(resp.Messages))
	}
	if resp.Messages[0].Role != "user" || resp.Messages[0].Content != "hello" {
		t.Fatalf("first message = %#v, want user/hello", resp.Messages[0])
	}
	if resp.Messages[1].Role != "assistant" || resp.Messages[1].Content != "final visible answer" {
		t.Fatalf("second message = %#v, want assistant/final visible answer", resp.Messages[1])
	}
}

func TestHandleGetSession_ReconstructsThoughtFromAssistantReasoningContent(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := newMintClawTestSession(t, store, "detail-reasoning-content")
	for _, msg := range []providers.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "final visible answer", ModelName: "gpt-5.4", ReasoningContent: "internal chain of thought"},
	} {
		if err := store.AddFullMessage(context.Background(), sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/detail-reasoning-content", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []sessionChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Messages) != 3 {
		t.Fatalf("len(resp.Messages) = %d, want 3", len(resp.Messages))
	}
	if resp.Messages[1].Role != "assistant" ||
		resp.Messages[1].Content != "internal chain of thought" ||
		resp.Messages[1].Kind != "thought" {
		t.Fatalf("thought message = %#v, want assistant thought/internal chain of thought", resp.Messages[1])
	}
	if resp.Messages[1].ModelName != "gpt-5.4" {
		t.Fatalf("thought model_name = %q, want %q", resp.Messages[1].ModelName, "gpt-5.4")
	}
	if resp.Messages[2].Role != "assistant" || resp.Messages[2].Content != "final visible answer" {
		t.Fatalf("final message = %#v, want assistant/final visible answer", resp.Messages[2])
	}
	if resp.Messages[2].ModelName != "gpt-5.4" {
		t.Fatalf("final model_name = %q, want %q", resp.Messages[2].ModelName, "gpt-5.4")
	}
}

func TestHandleGetSession_ReconstructsRefreshMatrixForThoughtAndToolSummary(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := newMintClawTestSession(t, store, "detail-refresh-matrix")
	for _, msg := range []providers.Message{
		{Role: "user", Content: "turn1"},
		{Role: "assistant", Content: "plain visible", ReasoningContent: "plain thought"},
		{Role: "user", Content: "turn2"},
		{
			Role:             "assistant",
			ReasoningContent: "tool thought",
			ToolCalls: []providers.ToolCall{
				testProviderToolCall(t, "call_read_file", "read_file", `{"path":"README.md"}`, ""),
			},
		},
		{Role: "tool", ToolCallID: "call_read_file", Content: "file result"},
		{Role: "user", Content: "turn3"},
		{
			Role:    "assistant",
			Content: "tool visible only",
			ToolCalls: []providers.ToolCall{
				testProviderToolCall(t, "call_list_dir", "list_dir", `{"path":"."}`, ""),
			},
		},
		{Role: "tool", ToolCallID: "call_list_dir", Content: "dir result"},
		{Role: "user", Content: "turn4"},
		{
			Role:             "assistant",
			Content:          "tool visible and thought",
			ReasoningContent: "tool mixed thought",
			ToolCalls: []providers.ToolCall{
				testProviderToolCall(t, "call_exec", "exec", `{"command":"pwd"}`, ""),
			},
		},
		{Role: "tool", ToolCallID: "call_exec", Content: "pwd result"},
	} {
		if err := store.AddFullMessage(context.Background(), sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/detail-refresh-matrix", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []sessionChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if len(resp.Messages) != 13 {
		t.Fatalf("len(resp.Messages) = %d, want 13", len(resp.Messages))
	}

	assertMessage := func(index int, role, kind, content string) {
		t.Helper()
		msg := resp.Messages[index]
		if msg.Role != role || msg.Kind != kind || msg.Content != content {
			t.Fatalf("messages[%d] = %#v, want role=%q kind=%q content=%q", index, msg, role, kind, content)
		}
	}

	assertMessage(0, "user", "", "turn1")
	assertMessage(1, "assistant", "thought", "plain thought")
	assertMessage(2, "assistant", "", "plain visible")
	assertMessage(3, "user", "", "turn2")
	assertMessage(4, "assistant", "thought", "tool thought")
	assertVisibleToolCallMessage(t, resp.Messages[5], "read_file")
	assertMessage(6, "user", "", "turn3")
	assertMessage(7, "assistant", "", "tool visible only")
	assertVisibleToolCallMessage(t, resp.Messages[8], "list_dir")
	assertMessage(9, "user", "", "turn4")
	assertMessage(10, "assistant", "thought", "tool mixed thought")
	assertMessage(11, "assistant", "", "tool visible and thought")
	assertVisibleToolCallMessage(t, resp.Messages[12], "exec")
}

func TestHandleGetSession_ReconstructsVisibleMessageToolOutputWithoutDuplicateSummary(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := newMintClawTestSession(t, store, "detail-message-tool")
	for _, msg := range []providers.Message{
		{Role: "user", Content: "test"},
		{
			Role:      "assistant",
			Content:   "",
			ModelName: "gpt-5.4-mini",
			ToolCalls: []providers.ToolCall{
				testProviderToolCall(t, "call_1", "message", `{"content":"visible tool output"}`, ""),
			},
		},
		{Role: "tool", Content: "Message sent to mintclaw:mintclaw:detail-message-tool", ToolCallID: "call_1"},
		{Role: "assistant", Content: handledToolResponseSummaryText},
	} {
		if err := store.AddFullMessage(context.Background(), sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/detail-message-tool", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []sessionChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Messages) != 3 {
		t.Fatalf("len(resp.Messages) = %d, want 3", len(resp.Messages))
	}
	if resp.Messages[0].Role != "user" || resp.Messages[0].Content != "test" {
		t.Fatalf("first message = %#v, want user/test", resp.Messages[0])
	}
	assertVisibleToolCallMessage(t, resp.Messages[1], "message")
	if resp.Messages[1].ModelName != "gpt-5.4-mini" {
		t.Fatalf("tool_calls model_name = %q, want %q", resp.Messages[1].ModelName, "gpt-5.4-mini")
	}
	if resp.Messages[2].Role != "assistant" || resp.Messages[2].Content != "visible tool output" {
		t.Fatalf("assistant message = %#v, want visible tool output", resp.Messages[2])
	}
	if resp.Messages[2].ModelName != "gpt-5.4-mini" {
		t.Fatalf("visible tool output model_name = %q, want %q", resp.Messages[2].ModelName, "gpt-5.4-mini")
	}
}

func TestHandleGetSession_PreservesFinalAssistantReplyAfterMessageToolOutput(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := newMintClawTestSession(t, store, "detail-message-tool-final-reply")
	for _, msg := range []providers.Message{
		{Role: "user", Content: "test"},
		{
			Role: "assistant",
			ToolCalls: []providers.ToolCall{
				testProviderToolCall(t, "call_1", "message", `{"content":"visible tool output"}`, ""),
			},
		},
		{Role: "tool", Content: "Message sent to mintclaw:mintclaw:detail-message-tool-final-reply", ToolCallID: "call_1"},
		{Role: "assistant", Content: "final assistant reply"},
	} {
		if err := store.AddFullMessage(context.Background(), sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/detail-message-tool-final-reply", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []sessionChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Messages) != 4 {
		t.Fatalf("len(resp.Messages) = %d, want 4", len(resp.Messages))
	}
	if resp.Messages[0].Role != "user" || resp.Messages[0].Content != "test" {
		t.Fatalf("first message = %#v, want user/test", resp.Messages[0])
	}
	assertVisibleToolCallMessage(t, resp.Messages[1], "message")
	if resp.Messages[2].Role != "assistant" || resp.Messages[2].Content != "visible tool output" {
		t.Fatalf("interim assistant message = %#v, want visible tool output", resp.Messages[2])
	}
	if resp.Messages[3].Role != "assistant" || resp.Messages[3].Content != "final assistant reply" {
		t.Fatalf("final assistant message = %#v, want final assistant reply", resp.Messages[3])
	}
}

func TestHandleListSessions_MessageCountUsesVisibleTranscript(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := newMintClawTestSession(t, store, "list-visible-count")
	for _, msg := range []providers.Message{
		{Role: "user", Content: "test"},
		{
			Role: "assistant",
			ToolCalls: []providers.ToolCall{
				testProviderToolCall(t, "call_1", "message", `{"content":"visible tool output"}`, ""),
			},
		},
		{Role: "tool", Content: "Message sent to mintclaw:mintclaw:list-visible-count", ToolCallID: "call_1"},
		{Role: "assistant", Content: handledToolResponseSummaryText},
	} {
		if err := store.AddFullMessage(context.Background(), sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var items []sessionListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].MessageCount != 3 {
		t.Fatalf("items[0].MessageCount = %d, want 3", items[0].MessageCount)
	}
}

func TestHandleListSessions_DeduplicatesAssistantToolCallContentFromVisibleTranscript(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := newMintClawTestSession(t, store, "list-deduped-tool-content")
	for _, msg := range []providers.Message{
		{Role: "user", Content: "check file"},
		{
			Role:    "assistant",
			Content: "Read the file before replying.",
			ToolCalls: []providers.ToolCall{
				testProviderToolCall(
					t, "call_1", "read_file", `{"path":"README.md"}`, "Read the file before replying.",
				),
			},
		},
		{Role: "tool", Content: "raw read_file result", ToolCallID: "call_1"},
	} {
		if err := store.AddFullMessage(context.Background(), sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var items []sessionListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].MessageCount != 2 {
		t.Fatalf("items[0].MessageCount = %d, want 2", items[0].MessageCount)
	}
}

func TestHandleGetSession_DoesNotDuplicateAssistantToolCallContent(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := newMintClawTestSession(t, store, "detail-tool-summary-and-content")
	for _, msg := range []providers.Message{
		{Role: "user", Content: "check file"},
		{
			Role:    "assistant",
			Content: "Read the file before replying.",
			ToolCalls: []providers.ToolCall{
				testProviderToolCall(
					t,
					"call_1",
					"read_file",
					`{"path":"README.md","start_line":1,"end_line":10}`,
					"Read the file before replying.",
				),
			},
		},
		{Role: "tool", Content: "raw read_file result", ToolCallID: "call_1"},
	} {
		if err := store.AddFullMessage(context.Background(), sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/detail-tool-summary-and-content", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []sessionChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Messages) != 2 {
		t.Fatalf("len(resp.Messages) = %d, want 2", len(resp.Messages))
	}
	if resp.Messages[0].Role != "user" || resp.Messages[0].Content != "check file" {
		t.Fatalf("first message = %#v, want user/check file", resp.Messages[0])
	}
	toolCall := assertVisibleToolCallMessage(t, resp.Messages[1], "read_file")
	if toolCall.ExtraContent == nil ||
		toolCall.ExtraContent.ToolFeedbackExplanation != "Read the file before replying." {
		t.Fatalf("tool call = %#v, want explanation", toolCall)
	}
}

func TestHandleGetSession_PreservesDistinctAssistantToolCallContent(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := newMintClawTestSession(t, store, "detail-tool-summary-distinct-content")
	for _, msg := range []providers.Message{
		{Role: "user", Content: "check file"},
		{
			Role:    "assistant",
			Content: "I will summarize the findings after reading the file.",
			ToolCalls: []providers.ToolCall{
				testProviderToolCall(
					t,
					"call_1",
					"read_file",
					`{"path":"README.md","start_line":1,"end_line":10}`,
					"Read the file before replying.",
				),
			},
		},
	} {
		if err := store.AddFullMessage(context.Background(), sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/detail-tool-summary-distinct-content", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []sessionChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Messages) != 3 {
		t.Fatalf("len(resp.Messages) = %d, want 3", len(resp.Messages))
	}
	if resp.Messages[1].Role != "assistant" ||
		resp.Messages[1].Content != "I will summarize the findings after reading the file." {
		t.Fatalf("assistant content = %#v, want preserved distinct content", resp.Messages[1])
	}
	assertVisibleToolCallMessage(t, resp.Messages[2], "read_file")
}

func TestHandleGetSession_ExposesMediaAsAttachmentWhenAssistantToolCallContentDuplicatesSummary(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := newMintClawTestSession(t, store, "detail-tool-summary-duplicate-content-with-media")
	for _, msg := range []providers.Message{
		{Role: "user", Content: "check screenshot"},
		{
			Role:    "assistant",
			Content: "Reviewing the generated screenshot.",
			Media:   []string{"data:image/png;base64,abc123"},
			ToolCalls: []providers.ToolCall{
				testProviderToolCall(
					t, "call_1", "view_image", `{"path":"artifact.png"}`, "Reviewing the generated screenshot.",
				),
			},
		},
	} {
		if err := store.AddFullMessage(context.Background(), sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/detail-tool-summary-duplicate-content-with-media", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []sessionChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Messages) != 3 {
		t.Fatalf("len(resp.Messages) = %d, want 3", len(resp.Messages))
	}
	if resp.Messages[1].Role != "assistant" {
		t.Fatalf("assistant message role = %q, want assistant", resp.Messages[1].Role)
	}
	if resp.Messages[1].Content != "" {
		t.Fatalf("assistant content = %q, want duplicate content suppressed", resp.Messages[1].Content)
	}
	if len(resp.Messages[1].Attachments) != 1 ||
		resp.Messages[1].Attachments[0].URL != "data:image/png;base64,abc123" ||
		resp.Messages[1].Attachments[0].Type != "image" {
		t.Fatalf("assistant attachments = %#v, want image attachment", resp.Messages[1].Attachments)
	}
	assertVisibleToolCallMessage(t, resp.Messages[2], "view_image")
}

func TestHandleGetSession_PreservesAttachmentsWhenAssistantToolCallContentDuplicatesSummary(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := newMintClawTestSession(t, store, "detail-tool-summary-duplicate-content-with-attachments")
	for _, msg := range []providers.Message{
		{Role: "user", Content: "check report"},
		{
			Role:    "assistant",
			Content: "Reviewing the generated report.",
			Attachments: []providers.Attachment{{
				Type:        "file",
				URL:         "https://example.com/report.txt",
				Filename:    "report.txt",
				ContentType: "text/plain",
			}},
			ToolCalls: []providers.ToolCall{
				testProviderToolCall(
					t, "call_1", "read_file", `{"path":"report.txt"}`, "Reviewing the generated report.",
				),
			},
		},
	} {
		if err := store.AddFullMessage(context.Background(), sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/sessions/detail-tool-summary-duplicate-content-with-attachments",
		nil,
	)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []sessionChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Messages) != 3 {
		t.Fatalf("len(resp.Messages) = %d, want 3", len(resp.Messages))
	}
	if resp.Messages[1].Role != "assistant" {
		t.Fatalf("assistant message role = %q, want assistant", resp.Messages[1].Role)
	}
	if resp.Messages[1].Content != "" {
		t.Fatalf("assistant content = %q, want duplicate content suppressed", resp.Messages[1].Content)
	}
	if len(resp.Messages[1].Attachments) != 1 {
		t.Fatalf("len(assistant.Attachments) = %d, want 1", len(resp.Messages[1].Attachments))
	}
	if resp.Messages[1].Attachments[0].URL != "https://example.com/report.txt" {
		t.Fatalf("attachment url = %q, want report URL", resp.Messages[1].Attachments[0].URL)
	}
	assertVisibleToolCallMessage(t, resp.Messages[2], "read_file")
}

func TestHandleGetSession_UsesConfiguredToolFeedbackMaxArgsLength(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Agents.Defaults.ToolFeedback.MaxArgsLength = 20
	err = saveTestConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	argsJSON := `{"path":"README.md","start_line":1,"end_line":10,"extra":"abcdefghijklmnopqrstuvwxyz"}`
	explanation := "Read README.md first to confirm the current project structure before editing the config example."
	sessionKey := newMintClawTestSession(t, store, "detail-tool-summary-max-args")
	err = store.AddFullMessage(context.Background(), sessionKey, providers.Message{Role: "user", Content: "check file"})
	if err != nil {
		t.Fatalf("AddFullMessage(user) error = %v", err)
	}
	err = store.AddFullMessage(context.Background(), sessionKey, providers.Message{
		Role: "assistant",
		ToolCalls: []providers.ToolCall{
			testProviderToolCall(t, "call_1", "read_file", argsJSON, explanation),
		},
	})
	if err != nil {
		t.Fatalf("AddFullMessage(assistant) error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/detail-tool-summary-max-args", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []sessionChatMessage `json:"messages"`
	}
	err = json.Unmarshal(rec.Body.Bytes(), &resp)
	if err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Messages) < 2 {
		t.Fatalf("len(resp.Messages) = %d, want at least 2", len(resp.Messages))
	}

	wantArgsPreview := visibleAssistantToolArgsPreview(
		testProviderToolCall(t, "call_1", "read_file", argsJSON, ""),
		20,
	)
	toolCall := assertVisibleToolCallMessage(t, resp.Messages[1], "read_file")
	if toolCall.ExtraContent == nil || toolCall.ExtraContent.ToolFeedbackExplanation != explanation {
		t.Fatalf("tool call = %#v, want full explanation %q", toolCall, explanation)
	}
	if toolCall.Function == nil || toolCall.Function.Arguments != wantArgsPreview {
		t.Fatalf("tool call = %#v, want args preview %q", toolCall, wantArgsPreview)
	}
}

func TestHandleGetSession_UsesToolArgumentsWhenExplanationMissing(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Agents.Defaults.ToolFeedback.MaxArgsLength = 20
	err = saveTestConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	argsJSON := `{"path":"README.md","start_line":1,"end_line":10,"extra":"abcdefghijklmnopqrstuvwxyz"}`
	sessionKey := newMintClawTestSession(t, store, "detail-tool-summary-args")
	if err := store.AddFullMessage(
		context.Background(),
		sessionKey,
		providers.Message{Role: "user", Content: "check file"},
	); err != nil {
		t.Fatalf("AddFullMessage(user) error = %v", err)
	}
	if err := store.AddFullMessage(context.Background(), sessionKey, providers.Message{
		Role: "assistant",
		ToolCalls: []providers.ToolCall{
			testProviderToolCall(t, "call_1", "read_file", argsJSON, ""),
		},
	}); err != nil {
		t.Fatalf("AddFullMessage(assistant) error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/detail-tool-summary-args", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []sessionChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Messages) < 2 {
		t.Fatalf("len(resp.Messages) = %d, want at least 2", len(resp.Messages))
	}

	wantPreview := visibleAssistantToolArgsPreview(
		testProviderToolCall(t, "call_1", "read_file", argsJSON, ""),
		20,
	)
	toolCall := assertVisibleToolCallMessage(t, resp.Messages[1], "read_file")
	if toolCall.Function == nil || toolCall.Function.Arguments != wantPreview {
		t.Fatalf("tool call = %#v, want args preview %q", toolCall, wantPreview)
	}
}

func TestHandleGetSession_ExposesMediaOnlyMessagesAsAttachments(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := newMintClawTestSession(t, store, "detail-media-only")
	if err := store.AddFullMessage(context.Background(), sessionKey, providers.Message{
		Role:  "user",
		Media: []string{"data:image/png;base64,abc123"},
	}); err != nil {
		t.Fatalf("AddFullMessage(user) error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/detail-media-only", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []struct {
			Role        string                  `json:"role"`
			Content     string                  `json:"content"`
			Attachments []sessionChatAttachment `json:"attachments"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Messages) != 1 {
		t.Fatalf("len(resp.Messages) = %d, want 1", len(resp.Messages))
	}
	if resp.Messages[0].Role != "user" || len(resp.Messages[0].Attachments) != 1 ||
		resp.Messages[0].Attachments[0].URL != "data:image/png;base64,abc123" ||
		resp.Messages[0].Attachments[0].Type != "image" {
		t.Fatalf("message = %#v, want user message with image attachment", resp.Messages[0])
	}
}

func TestSessionAttachmentsConsolidatesMediaAndStructuredAttachments(t *testing.T) {
	const reportURL = "https://example.com/report.txt"
	attachments := sessionAttachments(providers.Message{
		Media: []string{
			reportURL,
			"data:image/png;base64,abc123",
			"media://persistent",
		},
		Attachments: []providers.Attachment{{
			Type:        "file",
			URL:         reportURL,
			Filename:    "report.txt",
			ContentType: "text/plain",
		}},
	})

	if len(attachments) != 3 {
		t.Fatalf("attachments = %#v, want three durable attachments", attachments)
	}
	if attachments[0].URL != reportURL || attachments[0].Filename != "report.txt" ||
		attachments[0].ContentType != "text/plain" {
		t.Fatalf("structured attachment = %#v, want metadata preserved", attachments[0])
	}
	if attachments[1].URL != "data:image/png;base64,abc123" || attachments[1].Type != "image" {
		t.Fatalf("media attachment = %#v, want canonical image attachment", attachments[1])
	}
	if attachments[2].URL != "/mintclaw/media/persistent" {
		t.Fatalf("media proxy attachment = %#v, want canonical proxy URL", attachments[2])
	}
}

func TestHandleSessions_SupportsJSONLMessagesUpToStoreCap(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := newMintClawTestSession(t, store, "detail-large-jsonl")
	largeContent := strings.Repeat("x", 9*1024*1024)
	if err := store.AddFullMessage(context.Background(), sessionKey, providers.Message{
		Role:    "user",
		Content: largeContent,
	}); err != nil {
		t.Fatalf("AddFullMessage() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	listRec := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(listRec, listReq)

	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d, body=%s", listRec.Code, http.StatusOK, listRec.Body.String())
	}

	var items []sessionListItem
	if err := json.Unmarshal(listRec.Body.Bytes(), &items); err != nil {
		t.Fatalf("list Unmarshal() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}

	detailRec := httptest.NewRecorder()
	detailReq := httptest.NewRequest(http.MethodGet, "/api/sessions/detail-large-jsonl", nil)
	mux.ServeHTTP(detailRec, detailReq)

	if detailRec.Code != http.StatusOK {
		t.Fatalf(
			"detail status = %d, want %d, body=%s",
			detailRec.Code,
			http.StatusOK,
			detailRec.Body.String(),
		)
	}

	var resp struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(detailRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("detail Unmarshal() error = %v", err)
	}
	if len(resp.Messages) != 1 {
		t.Fatalf("len(resp.Messages) = %d, want 1", len(resp.Messages))
	}
	if resp.Messages[0].Role != "user" {
		t.Fatalf("resp.Messages[0].Role = %q, want %q", resp.Messages[0].Role, "user")
	}
	if got := len(resp.Messages[0].Content); got != len(largeContent) {
		t.Fatalf("len(resp.Messages[0].Content) = %d, want %d", got, len(largeContent))
	}
}

func TestHandleListSessions_UsesImagePreviewForMediaOnlyMessage(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := newMintClawTestSession(t, store, "preview-media-only")
	if err := store.AddFullMessage(context.Background(), sessionKey, providers.Message{
		Role:  "user",
		Media: []string{"data:image/png;base64,abc123"},
	}); err != nil {
		t.Fatalf("AddFullMessage() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var items []sessionListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].Preview != "[image]" {
		t.Fatalf("items[0].Preview = %q, want %q", items[0].Preview, "[image]")
	}
	if items[0].MessageCount != 1 {
		t.Fatalf("items[0].MessageCount = %d, want 1", items[0].MessageCount)
	}
}

func TestHandleDeleteSession_JSONLStorage(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := newMintClawTestSession(t, store, "delete-jsonl")
	if err := store.AddFullMessage(context.Background(), sessionKey, providers.Message{
		Role:    "user",
		Content: "delete me",
	}); err != nil {
		t.Fatalf("AddFullMessage() error = %v", err)
	}
	if err := store.SetSummary(context.Background(), sessionKey, "delete summary"); err != nil {
		t.Fatalf("SetSummary() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/delete-jsonl", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusNoContent, rec.Body.String())
	}

	base := filepath.Join(dir, sanitizeSessionKey(sessionKey))
	for _, path := range []string{base + ".jsonl", base + ".meta.json"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, stat err = %v", path, err)
		}
	}
}

func TestHandleGetSessionRejectsLegacyJSON(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	sessionKey := "agent:main:mintclaw:direct:mintclaw:legacy-json"
	legacyData, err := json.Marshal(sessionFile{
		Key: sessionKey,
		Messages: []providers.Message{
			{Role: "user", Content: "legacy user"},
			{Role: "assistant", Content: "legacy assistant"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "legacy.json"), legacyData, 0o600); err != nil {
		t.Fatal(err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/legacy-json", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestHandleSessions_FiltersEmptyJSONLFiles(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	base := filepath.Join(dir, sanitizeSessionKey(session.BuildOpaqueSessionKey("mintclaw|session=empty-jsonl")))
	if err := os.WriteFile(base+".jsonl", []byte{}, 0o644); err != nil {
		t.Fatalf("WriteFile(jsonl) error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	listRec := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(listRec, listReq)

	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d, body=%s", listRec.Code, http.StatusOK, listRec.Body.String())
	}

	var items []sessionListItem
	if err := json.Unmarshal(listRec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal(list) error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("len(items) = %d, want 0", len(items))
	}

	detailRec := httptest.NewRecorder()
	detailReq := httptest.NewRequest(http.MethodGet, "/api/sessions/empty-jsonl", nil)
	mux.ServeHTTP(detailRec, detailReq)

	if detailRec.Code != http.StatusNotFound {
		t.Fatalf("detail status = %d, want %d, body=%s", detailRec.Code, http.StatusNotFound, detailRec.Body.String())
	}
}

func TestHandleSessionsIgnoresJSONLWithoutCurrentMetadata(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	sessionKey := session.BuildOpaqueSessionKey("mintclaw|session=missing-meta")
	base := filepath.Join(dir, sanitizeSessionKey(sessionKey))
	line, err := json.Marshal(providers.Message{Role: "user", Content: "recover me"})
	if err != nil {
		t.Fatalf("Marshal(message) error = %v", err)
	}
	if err := os.WriteFile(base+".jsonl", append(line, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(jsonl) error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	listRec := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(listRec, listReq)

	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d, body=%s", listRec.Code, http.StatusOK, listRec.Body.String())
	}

	var items []sessionListItem
	if err := json.Unmarshal(listRec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal(list) error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("len(items) = %d, want 0", len(items))
	}

	detailRec := httptest.NewRecorder()
	detailReq := httptest.NewRequest(http.MethodGet, "/api/sessions/missing-meta", nil)
	mux.ServeHTTP(detailRec, detailReq)

	if detailRec.Code != http.StatusNotFound {
		t.Fatalf(
			"detail status = %d, want %d, body=%s",
			detailRec.Code,
			http.StatusNotFound,
			detailRec.Body.String(),
		)
	}
}

func TestHandleSessions_IgnoresMetaJSONInLegacyFallback(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	metaOnly := filepath.Join(dir, "agent_main_mintclaw_direct_mintclaw_meta-only.meta.json")
	metaOnlyContent := []byte(`{"key":"agent:main:mintclaw:direct:mintclaw:meta-only","summary":"meta only"}`)
	if err := os.WriteFile(metaOnly, metaOnlyContent, 0o644); err != nil {
		t.Fatalf("WriteFile(meta) error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	listRec := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(listRec, listReq)

	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d, body=%s", listRec.Code, http.StatusOK, listRec.Body.String())
	}

	var items []sessionListItem
	if err := json.Unmarshal(listRec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal(list) error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("len(items) = %d, want 0", len(items))
	}
}
