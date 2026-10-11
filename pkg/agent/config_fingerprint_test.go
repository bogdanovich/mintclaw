package agent

import (
	"sync"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestPublicConfigFingerprintFollowsLockedActiveSnapshot(t *testing.T) {
	first, second := config.DefaultConfig(), config.DefaultConfig()
	second.Agents.Defaults.Workspace = "/changed-workspace"
	one, err := config.PublicFingerprint(first)
	if err != nil {
		t.Fatal(err)
	}
	two, err := config.PublicFingerprint(second)
	if err != nil {
		t.Fatal(err)
	}
	loop := &AgentLoop{cfg: first}
	var workers sync.WaitGroup
	workers.Go(func() {
		for range 32 {
			loop.mu.Lock()
			loop.cfg = second
			loop.mu.Unlock()
			loop.mu.Lock()
			loop.cfg = first
			loop.mu.Unlock()
		}
	})
	for range 32 {
		observed, err := loop.PublicConfigFingerprint()
		if err != nil || observed != one && observed != two {
			t.Error("fingerprint did not observe a complete active snapshot", err)
		}
	}
	workers.Wait()
}
