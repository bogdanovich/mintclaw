package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/caarlos0/env/v11"
	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v3"

	"github.com/bogdanovich/mintclaw/pkg/credential"
)

func TestLoadSecurityValue(t *testing.T) {
	type valueStruct struct {
		Url     string        `json:"url,omitempty"      yaml:"-"`
		Token   *SecureString `json:"token,omitempty"    yaml:"token,omitempty"    env:"MINTCLAW_TOKEN"`
		ApiKeys SecureStrings `json:"api_keys,omitempty" yaml:"api_keys,omitempty" env:"MINTCLAW_API_KEYS"`
	}

	type testStruct struct {
		MintClaw *valueStruct `json:"mintclaw,omitempty" yaml:"mintclaw,omitempty"`
	}

	v1 := &testStruct{
		MintClaw: &valueStruct{
			Url:     "https://example.com",
			Token:   NewSecureString("token1"),
			ApiKeys: SecureStrings{NewSecureString("api-key1"), NewSecureString("api-key2")},
		},
	}
	bytes, err := yaml.Marshal(v1)
	assert.NoError(t, err)
	jsonBytes, err := json.Marshal(v1)
	assert.NoError(t, err)
	const want = `mintclaw:
    token: token1
    api_keys:
        - api-key1
        - api-key2
`
	const jsonPost = `{"mintclaw":{"url":"https://example.com","token":"token0"}}`
	v0 := &testStruct{}
	err = json.Unmarshal([]byte(jsonPost), v0)
	assert.NoError(t, err)
	assert.Equal(t, "https://example.com", v0.MintClaw.Url)
	assert.Equal(t, "token0", v0.MintClaw.Token.String())

	const jsonWant = `{"mintclaw":{"url":"https://example.com","token":"[NOT_HERE]","api_keys":"[NOT_HERE]"}}`
	assert.Equal(t, want, string(bytes))
	assert.Equal(t, jsonWant, string(jsonBytes))

	v2 := &testStruct{}
	err = json.Unmarshal(jsonBytes, v2)
	assert.NoError(t, err)
	err = yaml.Unmarshal(bytes, v2)
	assert.NoError(t, err)
	assert.Equal(t, "https://example.com", v2.MintClaw.Url)
	if v2.MintClaw.Token != nil {
		assert.Equal(t, "token1", v2.MintClaw.Token.String())
		assert.Equal(t, "token1", v2.MintClaw.Token.raw)
	}

	v2.MintClaw.Token = NewSecureString("token1")
	v2.MintClaw.Token.raw = "abc"
	err = yaml.Unmarshal(bytes, v2)
	assert.NoError(t, err)
	assert.Equal(t, "token1", v2.MintClaw.Token.raw)

	if err := os.Setenv("MINTCLAW_TOKEN", "token_env"); err != nil {
		t.Fatal(err)
	}
	err = env.Parse(v2)
	assert.NoError(t, err)
	assert.NotNil(t, v2.MintClaw.Token)
	assert.Equal(t, "token1", v2.MintClaw.Token.String())

	v3 := &testStruct{MintClaw: &valueStruct{}}
	err = env.Parse(v3)
	assert.NoError(t, err)
	if v3.MintClaw.Token != nil {
		assert.Equal(t, "token_env", v3.MintClaw.Token.String())
	}

	type toolsStruct struct {
		MintClaw valueStruct `json:"mintclaw,omitempty" yaml:"mintclaw,omitempty"`
	}

	type testStruct2 struct {
		Tools toolsStruct `json:"tools,omitempty" yaml:",inline"`
	}

	v4 := &testStruct2{
		Tools: toolsStruct{
			MintClaw: valueStruct{
				Url:     "https://example.com",
				Token:   NewSecureString("token1"),
				ApiKeys: SecureStrings{NewSecureString("api-key1"), NewSecureString("api-key2")},
			},
		},
	}
	bytes, err = yaml.Marshal(v4)
	assert.NoError(t, err)
	assert.Equal(t, want, string(bytes))
	jsonBytes, err = json.Marshal(v4)
	assert.NoError(t, err)
	assert.Equal(
		t,
		`{"tools":{"mintclaw":{"url":"https://example.com","token":"[NOT_HERE]","api_keys":"[NOT_HERE]"}}}`,
		string(jsonBytes),
	)

	v5 := &testStruct2{}
	err = json.Unmarshal(jsonBytes, v5)
	assert.NoError(t, err)
	assert.Equal(t, "https://example.com", v5.Tools.MintClaw.Url)
	err = yaml.Unmarshal(bytes, v5)
	assert.NoError(t, err)
	assert.NotNil(t, v5.Tools.MintClaw.Token)
	assert.Equal(t, "token1", v5.Tools.MintClaw.Token.raw)

	dir := t.TempDir()
	sshKeyPath := filepath.Join(dir, "mintclaw_ed25519.key")
	if err = os.WriteFile(sshKeyPath, []byte("fake-ssh-key-material\n"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	const passphrase = "test-passphrase-32bytes-long-ok!"

	t.Setenv(credential.SSHKeyPathEnvVar, sshKeyPath)

	t.Setenv(credential.PassphraseEnvVar, passphrase)

	v5.Tools.MintClaw.Token.Set("newtoken1")
	v5.Tools.MintClaw.ApiKeys[0].Set("newapi-key1")
	bytes, err = yaml.Marshal(v5)
	assert.NoError(t, err)
	t.Logf("yaml: %s", string(bytes))

	v6 := &testStruct2{}
	err = yaml.Unmarshal(bytes, v6)
	assert.NoError(t, err)
	assert.NotNil(t, v6.Tools.MintClaw.Token)
	assert.Equal(t, "newtoken1", v6.Tools.MintClaw.Token.String())
}

func TestSkillRegistryConfigDecodeParam(t *testing.T) {
	registry := SkillRegistryConfig{
		Param: map[string]any{
			"proxy": "http://127.0.0.1:7890",
		},
	}

	var private struct {
		Proxy string `json:"proxy"`
	}
	err := registry.DecodeParam(&private)
	assert.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:7890", private.Proxy)
}

func TestNormalizeAllowFrom(t *testing.T) {
	got := NormalizeAllowFrom([]string{" owner-1 ", "", "  ", "*"})
	if !reflect.DeepEqual(got, []string{"owner-1", "*"}) {
		t.Fatalf("NormalizeAllowFrom() = %#v", got)
	}
	if !IsPublicAllowFrom(got) {
		t.Fatal("IsPublicAllowFrom() = false, want true")
	}
	if IsPublicAllowFrom([]string{"owner-1"}) {
		t.Fatal("private allowlist reported as public")
	}
}

func TestSkillRegistryConfigJSONFlattensParam(t *testing.T) {
	registry := SkillRegistryConfig{
		Enabled: true,
		BaseURL: "https://github.com",
		Param: map[string]any{
			"proxy": "http://127.0.0.1:7890",
		},
	}

	data, err := json.Marshal(registry)
	assert.NoError(t, err)
	assert.Contains(t, string(data), `"proxy":"http://127.0.0.1:7890"`)
	assert.NotContains(t, string(data), `"param"`)

	var loaded SkillRegistryConfig
	err = json.Unmarshal(data, &loaded)
	assert.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:7890", loaded.Param["proxy"])
}

func TestSkillRegistryConfigJSONIgnoresShadowSecretFields(t *testing.T) {
	var registry SkillRegistryConfig
	err := json.Unmarshal([]byte(`{
		"enabled": true,
		"base_url": "https://github.com",
		"_auth_token": "shadow-secret",
		"proxy": "http://127.0.0.1:7890"
	}`), &registry)
	assert.NoError(t, err)
	assert.Equal(t, "https://github.com", registry.BaseURL)
	assert.Equal(t, "http://127.0.0.1:7890", registry.Param["proxy"])
	_, exists := registry.Param["_auth_token"]
	assert.False(t, exists)

	registry.Param["_auth_token"] = "should-not-round-trip"
	data, err := json.Marshal(registry)
	assert.NoError(t, err)
	assert.NotContains(t, string(data), "_auth_token")
	assert.Contains(t, string(data), `"proxy":"http://127.0.0.1:7890"`)

	yamlData, err := yaml.Marshal(registry)
	assert.NoError(t, err)
	assert.NotContains(t, string(yamlData), "_auth_token")
	assert.NotContains(t, string(yamlData), "proxy")
}

func TestSkillRegistryConfigYAMLAcceptsOnlyScalarAuthToken(t *testing.T) {
	for name, input := range map[string]string{
		"enabled":      "enabled: true\n",
		"base_url":     "base_url: https://github.com\n",
		"proxy":        "proxy: http://127.0.0.1:7890\n",
		"shadow_token": "_auth_token: shadow-secret\n",
		"token_map":    "auth_token:\n  value: secret\n",
		"token_bool":   "auth_token: true\n",
		"duplicate":    "auth_token: one\nauth_token: two\n",
	} {
		t.Run(name, func(t *testing.T) {
			var registry SkillRegistryConfig
			assert.Error(t, yaml.Unmarshal([]byte(input), &registry))
		})
	}

	var registry SkillRegistryConfig
	assert.NoError(t, yaml.Unmarshal([]byte("auth_token: secret\n"), &registry))
	assert.Equal(t, "secret", registry.AuthToken.String())
}

func TestSkillRegistryConfigRejectsRemovedFields(t *testing.T) {
	var fromJSON SkillRegistryConfig
	assert.Error(t, json.Unmarshal([]byte(`{"name":"github"}`), &fromJSON))
	assert.Error(t, json.Unmarshal([]byte(`{"token":"legacy"}`), &fromJSON))
	assert.Error(t, json.Unmarshal([]byte(`{"param":{"proxy":"legacy"}}`), &fromJSON))
	assert.Error(t, json.Unmarshal([]byte(`null`), &fromJSON))

	var fromYAML SkillRegistryConfig
	assert.Error(t, yaml.Unmarshal([]byte("name: github\n"), &fromYAML))
	assert.Error(t, yaml.Unmarshal([]byte("token: legacy\n"), &fromYAML))
	assert.Error(t, yaml.Unmarshal([]byte("param:\n  proxy: legacy\n"), &fromYAML))
}

func TestSkillsRegistriesConfigMarshalYAMLIncludesRegistryToken(t *testing.T) {
	registries := SkillsRegistriesConfig{
		"github": {
			AuthToken: *NewSecureString("registry-auth-token"),
		},
	}

	data, err := yaml.Marshal(registries)
	assert.NoError(t, err)
	assert.Contains(t, string(data), "github:")
	assert.Contains(t, string(data), "auth_token: registry-auth-token")

	loaded := SkillsRegistriesConfig{
		"github": {},
	}
	err = yaml.Unmarshal(data, &loaded)
	assert.NoError(t, err)
	github, ok := loaded.Get("github")
	assert.True(t, ok)
	assert.Equal(t, "registry-auth-token", github.AuthToken.String())
}

func TestSkillsRegistriesConfigUnmarshalYAMLRejectsUnconfiguredRegistry(t *testing.T) {
	var registries SkillsRegistriesConfig
	err := yaml.Unmarshal([]byte(`github:
  auth_token: secret
`), &registries)
	assert.Error(t, err)
}

func TestSkillsRegistriesConfigMarshalJSONPreservesObjectShape(t *testing.T) {
	registries := SkillsRegistriesConfig{
		"github": {
			Enabled: true,
			BaseURL: "https://ghe.example.com/git",
			Param: map[string]any{
				"proxy": "http://127.0.0.1:7890",
			},
		},
		"clawhub": {
			Enabled: true,
			BaseURL: "https://clawhub.ai",
		},
	}

	data, err := json.Marshal(registries)
	assert.NoError(t, err)
	assert.Contains(t, string(data), `"github":{`)
	assert.Contains(t, string(data), `"clawhub":{`)
	assert.NotContains(t, string(data), `[{`)
	assert.NotContains(t, string(data), `"name":"github"`)
	assert.NotContains(t, string(data), `"name":"clawhub"`)

	var decoded map[string]json.RawMessage
	err = json.Unmarshal(data, &decoded)
	assert.NoError(t, err)
	assert.Contains(t, decoded, "github")
	assert.Contains(t, decoded, "clawhub")

	var roundTripped SkillsRegistriesConfig
	err = json.Unmarshal(data, &roundTripped)
	assert.NoError(t, err)

	github, ok := roundTripped.Get("github")
	assert.True(t, ok)
	assert.Equal(t, "https://ghe.example.com/git", github.BaseURL)
	assert.Equal(t, "http://127.0.0.1:7890", github.Param["proxy"])

	clawhub, ok := roundTripped.Get("clawhub")
	assert.True(t, ok)
	assert.Equal(t, "https://clawhub.ai", clawhub.BaseURL)
}

func TestSkillsRegistriesConfigUnmarshalJSONUsesDefaultsOnlyForConfiguredRegistries(t *testing.T) {
	registries := DefaultConfig().Tools.Skills.Registries

	err := json.Unmarshal([]byte(`{
		"clawhub": {
			"base_url": "https://clawhub.example.com"
		}
	}`), &registries)
	assert.NoError(t, err)

	clawhub, ok := registries.Get("clawhub")
	assert.True(t, ok)
	assert.True(t, clawhub.Enabled)
	assert.Equal(t, "https://clawhub.example.com", clawhub.BaseURL)

	_, ok = registries.Get("github")
	assert.False(t, ok)
}

func TestSkillsRegistriesConfigUnmarshalJSONRejectsListShape(t *testing.T) {
	registries := DefaultConfig().Tools.Skills.Registries

	err := json.Unmarshal([]byte(`[
		{
			"name": "clawhub",
			"base_url": "https://clawhub.example.com"
		}
	]`), &registries)
	assert.Error(t, err)
}

func TestSkillsRegistriesConfigUnmarshalJSONRejectsNull(t *testing.T) {
	registries := DefaultConfig().Tools.Skills.Registries
	assert.Error(t, json.Unmarshal([]byte(`null`), &registries))
}

func TestSkillsRegistriesConfigUnmarshalYAMLRejectsNullRegistry(t *testing.T) {
	registries := DefaultConfig().Tools.Skills.Registries
	assert.Error(t, yaml.Unmarshal([]byte("github: null\n"), &registries))
}

func TestSkillsRegistriesConfigUnmarshalYAMLRejectsUnknownRegistry(t *testing.T) {
	registries := DefaultConfig().Tools.Skills.Registries

	err := yaml.Unmarshal([]byte(`custom:
  auth_token: secret
`), &registries)
	assert.Error(t, err)
}

func TestSkillsRegistriesConfigUnmarshalYAMLRejectsDuplicateRegistry(t *testing.T) {
	registries := DefaultConfig().Tools.Skills.Registries

	err := yaml.Unmarshal([]byte(`github:
  auth_token: first
github:
  auth_token: second
`), &registries)
	assert.ErrorContains(t, err, `duplicate registry "github"`)
}

func TestSkillsRegistriesConfigUnmarshalYAMLOnlySetsAuthToken(t *testing.T) {
	registries := DefaultConfig().Tools.Skills.Registries

	err := yaml.Unmarshal([]byte(`github:
  auth_token: secret
`), &registries)
	assert.NoError(t, err)

	github, ok := registries.Get("github")
	assert.True(t, ok)
	assert.True(t, github.Enabled)
	assert.Equal(t, "https://github.com", github.BaseURL)
	assert.Equal(t, "secret", github.AuthToken.String())
}

func TestSkillsRegistriesConfigUnmarshalYAMLRetainsDefaultsForOmittedFields(t *testing.T) {
	registries := DefaultConfig().Tools.Skills.Registries

	err := yaml.Unmarshal([]byte(`github:
  auth_token: registry-token
`), &registries)
	assert.NoError(t, err)

	github, ok := registries.Get("github")
	assert.True(t, ok)
	assert.True(t, github.Enabled)
	assert.Equal(t, "https://github.com", github.BaseURL)
	assert.Equal(t, "registry-token", github.AuthToken.String())
	assert.Empty(t, github.Param)
}
