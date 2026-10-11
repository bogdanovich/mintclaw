package config

import "testing"

func TestPublicFingerprintExcludesCredentialsAndBuild(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ModelList = SecureModelList{{
		ModelName: "synthetic", Provider: "openai", Model: "synthetic", Enabled: true,
		APIKeys: SimpleSecureStrings("initial-private-credential"),
	}}
	before, err := PublicFingerprint(cfg)
	if err != nil || len(before) != 64 {
		t.Fatal("public fingerprint unavailable", err)
	}
	cfg.BuildInfo.GitCommit = "different-build"
	for _, model := range cfg.ModelList {
		model.SetAPIKey("private-credential-canary")
	}
	after, err := PublicFingerprint(cfg)
	if err != nil || before != after {
		t.Fatal("typed credentials or binary identity changed the public fingerprint")
	}
	cfg.Agents.Defaults.Workspace = "/different-workspace"
	after, err = PublicFingerprint(cfg)
	if err != nil || before == after {
		t.Fatal("workspace change was not fingerprinted")
	}
	if value, err := PublicFingerprint(nil); err == nil || value != "" {
		t.Fatal("missing config was admitted")
	}
}

func TestPublicFingerprintErrorsDoNotEcho(t *testing.T) {
	cfg := DefaultConfig()
	cycle := map[string]any{}
	cycle["private-projection-canary"] = cycle
	cfg.ModelList = SecureModelList{{ModelName: "synthetic", ExtraBody: cycle}}
	value, err := PublicFingerprint(cfg)
	if err == nil || value != "" || err.Error() != "public configuration fingerprint unavailable" {
		t.Fatal("invalid public projection was admitted or exposed")
	}
}
