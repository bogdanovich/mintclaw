package eval

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/auth"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func testSubscriptionPreset(preset Preset) Preset {
	preset.ModelAccess = modelAccessSubscription
	preset.Capabilities = []string{"conversation"}
	preset.TargetCallBudget = &providers.CallBudgetLimits{
		Mode: providers.CodexSubscriptionBudget, MaxCalls: 6, MaxInputBytes: 131072,
		MaxReportedTokens: 24000, MaxInFlight: 1, MaxCallSeconds: 60,
	}
	return preset
}

func TestPrepareSubscriptionProfileDoesNotCopyAuthOrChooseAPIKeys(t *testing.T) {
	fixture, parent := preparationFixture(t)
	preset := testSubscriptionPreset(fixture.preset)
	model := fixture.cfg.ModelList[0]
	model.AuthMethod = "oauth"
	if _, err := config.NewRepository(preset.ConfigPath).Save(fixture.cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareProfile(t.Context(), preset, fixture.home, parent); !errors.Is(err, ErrPreparation) {
		t.Fatalf("mixed credentials admitted: %v", err)
	}
	model.APIKeys = nil
	if _, err := config.NewRepository(preset.ConfigPath).Save(fixture.cfg); err != nil {
		t.Fatal(err)
	}
	api := fixture.preset
	api.ModelAccess = modelAccessAPIKey
	if _, err := PrepareProfile(t.Context(), api, fixture.home, parent); !errors.Is(err, ErrPreparation) {
		t.Fatalf("API mode admitted OAuth-only seed: %v", err)
	}
	writeAdmissionJSON(t, filepath.Join(fixture.home, "auth.json"), map[string]string{"seed": "not-copied"})
	result, err := PrepareProfile(t.Context(), preset, fixture.home, parent)
	if err != nil || result.Outcome != "prepared" {
		t.Fatal("failed to prepare subscription without reading ambient auth", err)
	}
	loaded, err := LoadPreset(result.PresetPath)
	if err != nil || loaded.ModelAccess != modelAccessSubscription {
		t.Fatal("prepared preset lost subscription mode", err)
	}
	for _, name := range []string{"auth.json", subscriptionAuthPath} {
		if _, err := os.Lstat(filepath.Join(result.Home, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("preparation copied credentials", err)
		}
	}
}

func TestSubscriptionRequiresPrivateProfileCredentialAndRedactsIt(t *testing.T) {
	fixture := newAdmissionFixture(t)
	loaded, err := config.LoadConfigReadOnly(fixture.preset.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	profile := preparedProfile{home: fixture.home, config: loaded}
	security := filepath.Join(profile.home, ".security")
	if err := os.Mkdir(security, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MINTCLAW_AUTH_FILE", filepath.Join(profile.home, "auth.json"))
	if _, err := subscriptionEvidenceFilter(profile); !errors.Is(err, ErrSubscriptionProfile) {
		t.Fatal("missing local credential was admitted", err)
	}
	token := "synthetic-subscription-access-not-a-live-credential"
	credential := &auth.AuthCredential{
		Provider: "openai", AuthMethod: "oauth", AccessToken: token,
		RefreshToken: "synthetic-subscription-refresh", ExpiresAt: time.Now().Add(time.Hour),
	}
	store := auth.AuthStore{Credentials: map[string]*auth.AuthCredential{"openai": credential}}
	name := filepath.Join(profile.home, subscriptionAuthPath)
	writeAdmissionJSON(t, name, store)
	filter, err := subscriptionEvidenceFilter(profile)
	if err != nil || filter.Replace(token) == token ||
		filter.Replace(credential.RefreshToken) == credential.RefreshToken ||
		filter.Replace("test-channel-token") == "test-channel-token" {
		t.Fatal("subscription credentials are not included in the evidence guard", err)
	}
	if loaded.SensitiveDataReplacer().Replace(token) != token {
		t.Fatal("subscription credential escaped into the normal configuration cache")
	}
	credential.ExpiresAt = time.Now().Add(time.Minute)
	writeAdmissionJSON(t, name, store)
	if _, err := subscriptionEvidenceFilter(profile); !errors.Is(err, ErrSubscriptionProfile) {
		t.Fatal("credential that expires during the run was admitted", err)
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	credential.ExpiresAt = time.Now().Add(time.Hour)
	other := filepath.Join(profile.home, "auth.json")
	writeAdmissionJSON(t, other, store)
	if err := os.Symlink(other, name); err != nil {
		t.Fatal(err)
	}
	if _, err := subscriptionEvidenceFilter(profile); !errors.Is(err, ErrSubscriptionProfile) {
		t.Fatal("symlinked credential was admitted", err)
	}
}

func TestSubscriptionRunRefusesAttachRepeatAndStrictCasesBeforeOpeningProfile(t *testing.T) {
	fixture := newAdmissionFixture(t)
	preset := testSubscriptionPreset(fixture.preset)
	smoke, err := LoadCorpus("../../evals/subscription")
	if err != nil || len(smoke) != 1 {
		t.Fatal("expected a single valid subscription scenario", err)
	}
	strict := preset
	strict.Capabilities = []string{"conversation", boundedCallsCapability}
	if !errors.Is(strict.Validate(), ErrInvalidPreset) {
		t.Fatal("subscription preset claimed the strict capability")
	}
	if _, err := Run(t.Context(), preset, smoke, RunOptions{}); !errors.Is(err, ErrUnsafeTarget) {
		t.Fatal("subscription attach was not rejected", err)
	}
	if _, err := Run(
		t.Context(),
		preset,
		smoke,
		RunOptions{StartTarget: true, Repeat: 2},
	); !errors.Is(
		err,
		ErrSubscriptionProfile,
	) {
		t.Fatal("subscription repetition was not rejected", err)
	}
	smoke[0].Case.Requires = []string{"conversation", boundedCallsCapability}
	if _, err := Run(
		t.Context(),
		preset,
		smoke,
		RunOptions{StartTarget: true},
	); !errors.Is(
		err,
		ErrSubscriptionProfile,
	) {
		t.Fatal("strict case ran under the subscription policy", err)
	}
	if fixture.probes.Load() != 0 || fixture.tasks.Load() != 0 {
		t.Fatal("rejected profile attempted gateway work")
	}
}
