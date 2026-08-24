package agent

import "testing"

func TestTurnRuntimeDoesNotShareMutableState(t *testing.T) {
	first := newTurnRuntime(nil, nil)
	second := newTurnRuntime(nil, nil)
	if first == second || first.admissions == second.admissions || first.activeRequests == second.activeRequests ||
		first.inbound == second.inbound {
		t.Fatal("turn runtimes share a mutable owner")
	}

	scope := newRuntimeSessionScope("/workspace/first", "session")
	turn := &turnState{workspace: scope.workspace, sessionKey: scope.sessionKey}
	first.registerActiveTurn(turn)
	if got := second.activeTurnState(scope); got != nil {
		t.Fatalf("second runtime observed first runtime turn: %p", got)
	}

	first.markPendingStop(scope)
	if second.takePendingStop(scope) {
		t.Fatal("second runtime consumed first runtime pending stop")
	}
	if !first.takePendingStop(scope) {
		t.Fatal("first runtime lost its pending stop")
	}

	runner := &turnRunner{runtime: first}
	first.replaceRunner(runner)
	if first.currentRunner() != runner || second.currentRunner() != nil {
		t.Fatal("runner generation is not owned by one turn runtime")
	}
}
