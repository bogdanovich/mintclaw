package config

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/bogdanovich/mintclaw/pkg/credential"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

// NormalizeAllowFrom trims sender entries and removes blanks so every caller
// applies the same empty-list policy.
func NormalizeAllowFrom(values []string) []string {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			normalized = append(normalized, value)
		}
	}
	return normalized
}

func IsPublicAllowFrom(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "*" {
			return true
		}
	}
	return false
}

const (
	// legacySecretPlaceholder is accepted only when reading old public configuration.
	legacySecretPlaceholder = `"[NOT_HERE]"`
)

// SecureStrings is a slice of SecureString
//
//nolint:recvcheck
type SecureStrings []*SecureString

// IsZero returns true if the SecureStrings is nil or empty.
func (s SecureStrings) IsZero() bool {
	return len(s) == 0
}

// Values returns the decrypted/resolved values
func (s *SecureStrings) Values() []string {
	if s == nil {
		return nil
	}
	keys := make([]string, len(*s))
	for i, k := range *s {
		keys[i] = k.String()
	}
	return unique(keys)
}

func SimpleSecureStrings(val ...string) SecureStrings {
	val = unique(val)
	vv := make(SecureStrings, len(val))
	for i, s := range val {
		vv[i] = NewSecureString(s)
	}
	return vv
}

// unique returns a new slice with duplicate elements removed.
func unique[T comparable](input []T) []T {
	m := make(map[T]struct{})
	var result []T
	for _, v := range input {
		if _, ok := m[v]; !ok {
			m[v] = struct{}{}
			result = append(result, v)
		}
	}
	return result
}

func (s SecureStrings) MarshalJSON() ([]byte, error) {
	// Public boundaries remove the field through ProjectPublicConfig. Null is a
	// defense in depth for accidental serialization of the runtime graph.
	return []byte("null"), nil
}

func (s *SecureStrings) UnmarshalJSON(value []byte) error {
	if string(value) == legacySecretPlaceholder {
		return nil
	}
	var v []*SecureString
	err := json.Unmarshal(value, &v)
	if err != nil {
		return err
	}
	// Filter out elements where SecureString.UnmarshalJSON was a no-op
	// (e.g. "[NOT_HERE]" entries), keeping only actually populated values.
	filtered := make(SecureStrings, 0, len(v))
	for _, ss := range v {
		if ss == nil {
			continue
		}
		if ss.resolved != "" || ss.raw != "" {
			filtered = append(filtered, ss)
		}
	}
	if len(filtered) == 0 {
		*s = nil
	} else {
		*s = filtered
	}
	return nil
}

// SecureString the string value that can be decrypted or resolved
//
//nolint:recvcheck
type SecureString struct {
	resolved string // Decrypted/resolved value returned by String()
	raw      string // Persisted raw value (enc://, file://, or plaintext)
}

// IsZero returns true if the SecureString has no persisted or resolved value.
func (s SecureString) IsZero() bool {
	return s.raw == "" && s.resolved == ""
}

func NewSecureString(value string) *SecureString {
	s := &SecureString{}
	if err := s.fromRaw(value); err != nil {
		logger.Warn(fmt.Sprintf("NewSecureString.fromRaw error: %s", err))
	}
	return s
}

func (s *SecureString) String() string {
	if s == nil {
		return ""
	}
	return s.resolved
}

func (s *SecureString) Set(value string) *SecureString {
	s.resolved = value
	s.raw = ""
	return s
}

func (s SecureString) MarshalJSON() ([]byte, error) {
	// Public boundaries remove the field through ProjectPublicConfig. Null is a
	// defense in depth for accidental serialization of the runtime graph.
	return []byte("null"), nil
}

func (s *SecureString) UnmarshalJSON(value []byte) error {
	if string(value) == legacySecretPlaceholder {
		return nil
	}
	var v string
	if err := json.Unmarshal(value, &v); err != nil {
		return err
	}
	return s.fromRaw(v)
}

func (s SecureString) MarshalYAML() (any, error) {
	// Preserve raw value if it is already a reference (enc:// or file://)
	if strings.HasPrefix(s.raw, credential.EncScheme) || strings.HasPrefix(s.raw, credential.FileScheme) {
		return s.raw, nil
	}
	// If resolved is a reference format (e.g. set via Set), copy back to raw
	if strings.HasPrefix(s.resolved, credential.EncScheme) || strings.HasPrefix(s.resolved, credential.FileScheme) {
		s.raw = s.resolved
		return s.raw, nil
	}
	// Try to encrypt the resolved value
	if passphrase := credential.PassphraseProvider(); passphrase != "" {
		encrypted, err := credential.Encrypt(passphrase, "", s.resolved)
		if err != nil {
			logger.Errorf("Encrypt error: %v", err)
			return nil, err
		}
		s.raw = encrypted
	} else {
		s.raw = s.resolved
	}
	return s.raw, nil
}

func (s *SecureString) UnmarshalYAML(value *yaml.Node) error {
	return s.fromRaw(value.Value)
}

func (s *SecureString) fromRaw(v string) error {
	s.raw = v
	if strings.HasPrefix(v, credential.FileScheme) {
		// Relative file references need the owning repository path. The loader
		// resolves them after environment and channel settings are initialized.
		s.resolved = ""
		return nil
	}
	if strings.HasPrefix(v, credential.EncScheme) {
		// Encrypted values are path-independent, so standalone channel decoding
		// can retain its historical eager validation and decryption behavior.
		resolved, err := credential.NewResolver("").Resolve(v)
		if err != nil {
			logger.Errorf("Resolve error: %v", err)
			return err
		}
		s.resolved = resolved
		return nil
	}
	s.resolved = v
	return nil
}

func (s *SecureString) UnmarshalText(text []byte) error {
	v := string(text)
	return s.fromRaw(v)
}

type SecureModelList []*ModelConfig

func (v *SecureModelList) UnmarshalYAML(value *yaml.Node) error {
	mm := make(map[string]*ModelConfig)
	if err := value.Decode(&mm); err != nil {
		logger.Errorf("Decode error: %v", err)
		return err
	}
	nameList := toNameIndex(*v)
	for i, m := range *v {
		sec := mm[nameList[i]]
		if sec == nil {
			sec = mm[m.ModelName]
		}
		if sec != nil {
			m.APIKeys = sec.APIKeys
		}
	}
	return nil
}

func (v SecureModelList) MarshalYAML() (any, error) {
	type onlySecureData struct {
		APIKeys SecureStrings `yaml:"api_keys,omitempty"`
	}
	mm := make(map[string]onlySecureData)
	nameList := toNameIndex(v)
	for i, m := range v {
		mm[nameList[i]] = onlySecureData{
			APIKeys: m.APIKeys,
		}
	}

	return mm, nil
}

func (v *SkillsRegistriesConfig) UnmarshalJSON(data []byte) error {
	registriesByName := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &registriesByName); err != nil {
		return err
	}
	if registriesByName == nil {
		return fmt.Errorf("skills registries must be an object")
	}
	existing := *v
	decoded := make(SkillsRegistriesConfig, len(registriesByName))
	for _, name := range sortedRegistryNamesFromJSON(registriesByName) {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
			return fmt.Errorf("skill registry name is required")
		}
		registry := cloneRegistryConfig(existing[name])
		if registry == nil {
			registry = &SkillRegistryConfig{}
		}
		if err := json.Unmarshal(registriesByName[name], registry); err != nil {
			return err
		}
		decoded[name] = registry
	}
	*v = decoded
	return nil
}

func (c *SkillRegistryConfig) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == nil {
		return fmt.Errorf("skill registry config must be an object")
	}
	params := cloneRegistryParams(c.Param)
	if params == nil {
		params = map[string]any{}
	}
	if _, ok := raw["name"]; ok {
		return fmt.Errorf("skill registry name must be the registries object key")
	}
	if _, ok := raw["token"]; ok {
		return fmt.Errorf("skill registry credentials use auth_token")
	}
	if value, ok := raw["enabled"]; ok {
		if err := json.Unmarshal(value, &c.Enabled); err != nil {
			return err
		}
	}
	if value, ok := raw["base_url"]; ok {
		if err := json.Unmarshal(value, &c.BaseURL); err != nil {
			return err
		}
	}
	if value, ok := raw["auth_token"]; ok {
		if err := json.Unmarshal(value, &c.AuthToken); err != nil {
			return err
		}
	}
	for key, value := range raw {
		switch key {
		case "enabled", "base_url", "auth_token":
			continue
		case "param":
			return fmt.Errorf("skill registry parameters are top-level fields")
		case "_auth_token":
			// UI/API shadow secret fields should hydrate SecureString only and must
			// never be persisted as arbitrary registry params.
			continue
		default:
			var decoded any
			if err := json.Unmarshal(value, &decoded); err != nil {
				return err
			}
			params[key] = decoded
		}
	}
	c.Param = params
	return nil
}

func (c SkillRegistryConfig) MarshalJSON() ([]byte, error) {
	m := map[string]any{
		"enabled":  c.Enabled,
		"base_url": c.BaseURL,
	}
	for key, value := range c.Param {
		if key == "" || key == "param" || strings.HasPrefix(key, "_") {
			continue
		}
		if _, exists := m[key]; exists {
			continue
		}
		m[key] = value
	}
	return json.Marshal(m)
}

func (c *SkillRegistryConfig) UnmarshalYAML(value *yaml.Node) error {
	if value == nil || value.Kind != yaml.MappingNode {
		return fmt.Errorf("skill registry config must be a mapping")
	}
	foundAuthToken := false
	for i := 0; i+1 < len(value.Content); i += 2 {
		keyNode := value.Content[i]
		valueNode := value.Content[i+1]
		if keyNode.Value != "auth_token" {
			return fmt.Errorf("skill registry security config only accepts auth_token")
		}
		if foundAuthToken {
			return fmt.Errorf("skill registry security config contains duplicate auth_token")
		}
		if valueNode.Kind != yaml.ScalarNode || valueNode.Tag != "!!str" {
			return fmt.Errorf("skill registry auth_token must be a string")
		}
		if err := valueNode.Decode(&c.AuthToken); err != nil {
			return err
		}
		foundAuthToken = true
	}
	return nil
}

func (c SkillRegistryConfig) MarshalYAML() (any, error) {
	m := map[string]any{}
	if !c.AuthToken.IsZero() {
		m["auth_token"] = c.AuthToken
	}
	return m, nil
}

func (v *SkillsRegistriesConfig) UnmarshalYAML(value *yaml.Node) error {
	if value == nil || value.Kind != yaml.MappingNode {
		return fmt.Errorf("skills registries must be a mapping")
	}
	if *v == nil {
		*v = make(SkillsRegistriesConfig, len(value.Content)/2)
	}
	seen := make(map[string]struct{}, len(value.Content)/2)
	for i := 0; i+1 < len(value.Content); i += 2 {
		nameNode := value.Content[i]
		registryNode := value.Content[i+1]
		if nameNode == nil || registryNode == nil {
			continue
		}
		name := strings.TrimSpace(nameNode.Value)
		if name == "" || name != nameNode.Value {
			return fmt.Errorf("skill registry name is required")
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("skills registries contain duplicate registry %q", name)
		}
		seen[name] = struct{}{}
		if registryNode.Tag == "!!null" {
			return fmt.Errorf("skill registry %q config must be a mapping", name)
		}
		registry := cloneRegistryConfig((*v)[name])
		if registry == nil {
			return fmt.Errorf("skill registry %q is not configured", name)
		}
		if err := registryNode.Decode(registry); err != nil {
			return err
		}
		(*v)[name] = registry
	}
	return nil
}

func cloneRegistryParams(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	cloned := make(map[string]any, len(src))
	for key, value := range src {
		cloned[key] = value
	}
	return cloned
}

func cloneRegistryConfig(src *SkillRegistryConfig) *SkillRegistryConfig {
	if src == nil {
		return nil
	}
	cloned := *src
	cloned.Param = cloneRegistryParams(src.Param)
	return &cloned
}

func sortedRegistryNamesFromJSON(mm map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(mm))
	for name := range mm {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	return keys
}

func (v SkillsRegistriesConfig) MarshalYAML() (any, error) {
	type onlySecureRegistryData struct {
		AuthToken SecureString `yaml:"auth_token,omitempty"`
	}
	mm := make(map[string]onlySecureRegistryData)
	for name, registry := range v {
		if strings.TrimSpace(name) == "" || registry == nil {
			continue
		}
		if registry.AuthToken.IsZero() {
			continue
		}
		mm[name] = onlySecureRegistryData{
			AuthToken: registry.AuthToken,
		}
	}

	return mm, nil
}
