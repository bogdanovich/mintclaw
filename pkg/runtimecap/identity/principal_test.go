package identity

import (
	"strings"
	"testing"
)

func TestPrincipalValidationPreservesExactIdentity(t *testing.T) {
	principal := Principal{
		Runtime:     KindGateway,
		ActorID:     "actor",
		AgentID:     "agent",
		SessionID:   "thread",
		ExecutionID: "turn",
	}
	if err := principal.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", " actor", "actor ", "actor\n", "actor\x7f", strings.Repeat("a", 1025)} {
		for _, field := range []string{"actor", "agent", "session", "execution"} {
			forged := principal
			switch field {
			case "actor":
				forged.ActorID = invalid
			case "agent":
				forged.AgentID = invalid
			case "session":
				forged.SessionID = invalid
			case "execution":
				forged.ExecutionID = invalid
			}
			if forged.Validate() == nil {
				t.Fatal("invalid principal component admitted", field)
			}
		}
	}
	principal.Runtime = "shell"
	if principal.Validate() == nil {
		t.Fatal("unrecognized runtime admitted")
	}
}
