package agent

import (
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

func TestCurrentRuntimeSessionKeysIsolateSharedWorkspaceAgents(t *testing.T) {
	store, err := memory.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sessions := session.NewJSONLBackend(store)

	mainKey := session.BuildOpaqueSessionKey("shared-workspace-main")
	sessions.EnsureSessionMetadata(mainKey, &session.SessionScope{
		Version: session.ScopeVersion,
		AgentID: "main",
		Channel: "mintclaw",
	})
	supportKey := session.BuildOpaqueSessionKey("shared-workspace-support")
	sessions.EnsureSessionMetadata(supportKey, &session.SessionScope{
		Version: session.ScopeVersion,
		AgentID: "support",
		Channel: "mintclaw",
	})

	main := &AgentInstance{ID: "main", Workspace: "/workspace/shared", Sessions: sessions}
	support := &AgentInstance{ID: "support", Workspace: "/workspace/shared", Sessions: sessions}
	if keys := currentRuntimeSessionKeys(main, sessions); len(keys) != 1 || keys[0] != mainKey {
		t.Fatalf("main current sessions = %v, want only %q", keys, mainKey)
	}
	if keys := currentRuntimeSessionKeys(support, sessions); len(keys) != 1 || keys[0] != supportKey {
		t.Fatalf("support current sessions = %v, want only %q", keys, supportKey)
	}
}
