package gateway

import "testing"

func TestNodeBrowserWorkerPreservesNavigationEvidence(t *testing.T) {
	worker := &nodeBrowserWorker{sessionID: "session_1", tabID: "tab_primary"}
	result := browserNodeTestObservation("session_1", "tab_primary", 1, "https://example.com/", "https://example.com")
	result.HTTPStatus, result.AntiBotBlock = 403, "cloudflare"
	observation, err := worker.acceptObservation(result, 1)
	if err != nil || observation.HTTPStatus != 403 || observation.AntiBotBlock != "cloudflare" {
		t.Fatalf("evidence dropped: %+v err=%v", observation, err)
	}
}
