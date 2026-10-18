package runtimecap

import (
	"encoding/json"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/runtimecap/identity"
)

var _ func(Principal) Principal = func(principal identity.Principal) identity.Principal { return principal }

func TestPrincipalAliasPreservesTypeAndWireContract(t *testing.T) {
	principal := Principal{
		Runtime:     KindGateway,
		ActorID:     "actor",
		AgentID:     "agent",
		SessionID:   "thread",
		ExecutionID: "turn",
	}
	if principal.Validate() != nil {
		t.Fatal("runtime identity diverged")
	}
	raw, err := json.Marshal(principal)
	if err != nil ||
		string(
			raw,
		) != `{"runtime":"gateway","actor_id":"actor","agent_id":"agent","session_id":"thread","execution_id":"turn"}` {
		t.Fatal("principal wire contract changed", err)
	}
}
