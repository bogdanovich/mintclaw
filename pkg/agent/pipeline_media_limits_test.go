package agent

import (
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestPipelineMaxMediaSizeUsesConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.MaxMediaSize = 5678
	pipeline := &Pipeline{Cfg: cfg}

	if got := pipeline.maxMediaSize(); got != 5678 {
		t.Fatalf("maxMediaSize() = %d, want 5678", got)
	}
}

func TestPipelineMaxMediaSize_DefaultsWhenConfigMissing(t *testing.T) {
	pipeline := &Pipeline{}

	if got := pipeline.maxMediaSize(); got != config.DefaultMaxMediaSize {
		t.Fatalf("maxMediaSize() = %d, want %d", got, config.DefaultMaxMediaSize)
	}
}
