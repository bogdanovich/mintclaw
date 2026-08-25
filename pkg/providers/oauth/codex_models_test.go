package oauthprovider

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchCodexModelsAndSelectPreferred(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("client_version") != "v-test" {
			t.Errorf("client_version = %q", request.URL.Query().Get("client_version"))
		}
		if request.Header.Get("Authorization") != "Bearer token" ||
			request.Header.Get("Chatgpt-Account-Id") != "account" ||
			request.Header.Get("Originator") != "codex_cli_rs" {
			t.Errorf("unexpected request headers: %v", request.Header)
		}
		_, _ = w.Write([]byte(`{"models":[
			{"slug":"hidden","context_window":999,"priority":0,"visibility":"hide"},
			{"slug":"second","context_window":200,"max_context_window":300,"priority":2,"visibility":"list"},
			{"slug":"first","context_window":400,"max_context_window":500,"priority":1,"visibility":"list"}
		]}`))
	}))
	defer server.Close()

	models, err := fetchCodexModels(t.Context(), server.Client(), server.URL, "token", "account", "v-test")
	if err != nil {
		t.Fatal(err)
	}
	preferred := PreferredCodexModel(models)
	if preferred.Slug != "first" || preferred.ContextWindow != 400 || preferred.MaxContextWindow != 500 {
		t.Fatalf("preferred model = %+v", preferred)
	}
}

func TestPreferredCodexModelFallsBackToBundledDefault(t *testing.T) {
	got := PreferredCodexModel(nil)
	if got.Slug != CodexDefaultModel || got.ContextWindow != CodexDefaultContextWindow ||
		got.MaxContextWindow != CodexDefaultMaxContextWindow {
		t.Fatalf("fallback model = %+v", got)
	}
}

func TestPreferredCodexModelRejectsInvalidDurableMetadata(t *testing.T) {
	got := PreferredCodexModel([]CodexModelInfo{
		{Slug: "bad model", ContextWindow: 100, MaxContextWindow: 100, Priority: 1, Visibility: "list"},
		{Slug: "too-small-max", ContextWindow: 200, MaxContextWindow: 100, Priority: 2, Visibility: "list"},
		{Slug: "  valid-model  ", ContextWindow: 300, MaxContextWindow: 400, Priority: 3, Visibility: " list "},
	})
	if got.Slug != "valid-model" || got.ContextWindow != 300 || got.Visibility != "list" {
		t.Fatalf("preferred model = %+v", got)
	}

	fallback := PreferredCodexModel([]CodexModelInfo{
		{Slug: "/invalid", ContextWindow: 100, Priority: 1, Visibility: "list"},
	})
	if fallback.Slug != CodexDefaultModel {
		t.Fatalf("fallback model = %+v", fallback)
	}
}

func TestBundledCodexModelNormalizesOpenAINamespace(t *testing.T) {
	got, ok := BundledCodexModel("openai/GPT-5.6-SOL")
	if !ok || got.ContextWindow != CodexDefaultContextWindow {
		t.Fatalf("bundled model = %+v, %v", got, ok)
	}
}
