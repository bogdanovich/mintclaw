package agent

import "context"

func newTestPipeline(al *AgentLoop) *Pipeline {
	return newPipeline(al, al.GetConfig())
}

func runTestTurn(
	al *AgentLoop,
	ctx context.Context,
	ts *turnState,
	pipeline *Pipeline,
) (turnResult, error) {
	return (&turnRunner{host: al, pipeline: pipeline}).run(ctx, ts, nil)
}

func setTestContextManager(al *AgentLoop, manager ContextManager) {
	al.mu.Lock()
	defer al.mu.Unlock()
	al.contextManager = manager
	al.replaceTurnRunnerLocked(al.cfg)
}
