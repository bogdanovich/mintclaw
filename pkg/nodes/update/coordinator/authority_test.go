//go:build linux || darwin

package coordinator

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigAuthorityResolverLoadsOnlyPinnedEnabledAuthority(t *testing.T) {
	configPath := writeAuthorityConfig(t, true, false, "v1.2.3", "v1.2.3")
	resolver, err := LoadConfigAuthorityResolver(configPath)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.ResolveUpdateRelease(t.Context(), "stable", "current")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Tag != "v1.2.3" || resolved.Version != "v1.2.3" || resolved.ProfileRevision != "stable-v1" ||
		resolved.AuthorityHash == "" || len(resolved.RedirectHosts) != 1 {
		t.Fatalf("resolved authority = %#v", resolved)
	}
	if _, err = resolver.ResolveUpdateRelease(t.Context(), "stable", "other"); !errors.Is(err, ErrUpdateDenied) {
		t.Fatalf("unknown release error = %v", err)
	}
}

func TestConfigAuthorityResolverCanonicalizesTrailingSourceSlash(t *testing.T) {
	configPath := writeAuthorityConfig(t, true, false, "v1.2.3", "v1.2.3")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.ReplaceAll(
		string(data),
		"https://releases.example/download",
		"https://releases.example/download/",
	))
	if err = os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	resolver, err := LoadConfigAuthorityResolver(configPath)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.ResolveUpdateRelease(t.Context(), "stable", "current")
	if err != nil || resolved.BaseURL != "https://releases.example/download" {
		t.Fatalf("resolved authority = %#v, %v", resolved, err)
	}
}

func TestConfigAuthorityResolverReloadsPolicyAndFailsClosedAfterRevocation(t *testing.T) {
	configPath := writeAuthorityConfig(t, true, false, "v1.2.3", "v1.2.3")
	resolver, err := LoadConfigAuthorityResolver(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = resolver.ResolveUpdateRelease(t.Context(), "stable", "current"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `"revoked":false`, `"revoked":true`, 1))
	if err = os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = resolver.ResolveUpdateRelease(t.Context(), "stable", "current"); !errors.Is(err, ErrUpdateDenied) {
		t.Fatalf("revoked authority error = %v", err)
	}
}

func TestConfigAuthorityResolverFailsClosedForChangedOrUnsafeAuthority(t *testing.T) {
	for name, configure := range map[string]func(*testing.T) string{
		"revoked":      func(t *testing.T) string { return writeAuthorityConfig(t, true, true, "v1.2.3", "v1.2.3") },
		"tag mismatch": func(t *testing.T) string { return writeAuthorityConfig(t, true, false, "v1.2.4", "v1.2.3") },
		"writable": func(t *testing.T) string {
			path := writeAuthorityConfig(t, true, false, "v1.2.3", "v1.2.3")
			if err := os.Chmod(path, 0o622); err != nil {
				t.Fatal(err)
			}
			return path
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadConfigAuthorityResolver(configure(t)); err == nil {
				t.Fatal("unsafe authority was accepted")
			}
		})
	}

	disabled := writeAuthorityConfig(t, false, false, "", "")
	resolver, err := LoadConfigAuthorityResolver(disabled)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = resolver.ResolveUpdateRelease(t.Context(), "stable", "current"); !errors.Is(err, ErrUpdateDenied) {
		t.Fatalf("disabled authority error = %v", err)
	}
}

func writeAuthorityConfig(
	t *testing.T,
	enabled bool,
	revoked bool,
	tag string,
	version string,
) string {
	t.Helper()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	releases := map[string]any(nil)
	if enabled {
		releases = map[string]any{"current": map[string]any{"tag": tag, "version": version}}
	}
	config := map[string]any{
		"gateway_url": "wss://gateway.example/nodes/v1/ws",
		"node_update_sources": map[string]any{
			"production": map[string]any{
				"base_url": "https://releases.example/download", "redirect_hosts": []string{"objects.example"},
				"public_key": base64.RawStdEncoding.EncodeToString(publicKey), "revoked": revoked,
			},
		},
		"node_update_policies": map[string]any{
			"stable": map[string]any{
				"enabled": enabled, "revision": "stable-v1", "source": "production", "channel": "stable",
				"approval": "required", "releases": releases,
			},
		},
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err = os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
