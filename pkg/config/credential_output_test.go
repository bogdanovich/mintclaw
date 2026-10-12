package config

import (
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/internal/secretvalues"
)

func TestRuntimeCredentialFilteringCannotBeDisabledOrSkipShortValues(t *testing.T) {
	release, err := secretvalues.Register([]byte("781294"))
	if err != nil {
		t.Fatal("credential guard unavailable")
	}
	defer release()
	cfg := DefaultConfig()
	cfg.Tools.FilterSensitiveData = false
	cfg.Tools.FilterMinLength = 4096
	for _, candidate := range []*Config{nil, cfg} {
		if strings.Contains(candidate.FilterSensitiveData("781294"), "781294") {
			t.Fatal("ordinary filter setting exposed a runtime OTP")
		}
		if candidate.FilterSensitiveData("account=ebay result=success") != "account=ebay result=success" {
			t.Fatal("safe alias and outcome were changed")
		}
	}
}
