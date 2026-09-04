package agent

import (
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestPipelineNativeSearchEnabledUsesConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Web.Enabled = true
	cfg.Tools.Web.PreferNative = true
	pipeline := &Pipeline{Cfg: cfg, turnPolicy: newPipelineTurnPolicy(cfg)}

	if !pipeline.nativeSearchEnabled(
		config.EffectiveTurnProfile{},
		&nativeSearchProvider{supported: true},
	) {
		t.Fatal("nativeSearchEnabled() = false, want true")
	}
}
