// MintClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

const (
	SecurityConfigFile = ".security.yml"
)

// securityPath returns the path to security.yml relative to the config file
func securityPath(configPath string) string {
	configDir := filepath.Dir(configPath)
	return filepath.Join(configDir, SecurityConfigFile)
}

// loadSecurityConfig loads the security configuration from security.yml
// and merges secure field values into the config.
func loadSecurityConfig(cfg *Config, securityPath string) error {
	return loadSecurityConfigForChannels(cfg, securityPath, nil)
}

func loadSecurityConfigForChannels(
	cfg *Config,
	securityPath string,
	allowedChannels map[string]struct{},
) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	data, err := os.ReadFile(securityPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read security config: %w", err)
	}

	// Parse YAML into a yaml.Node tree so channels can be validated and merged
	// separately from the other security fields.
	var rootNode yaml.Node
	if err = yaml.Unmarshal(data, &rootNode); err != nil {
		return fmt.Errorf("failed to parse security config: %w", err)
	}

	channelsNode, nonChannelData, err := splitChannelSecurityDocument(&rootNode)
	if err != nil {
		return fmt.Errorf("failed to parse security config: %w", err)
	}
	if allowedChannels != nil {
		channelsNode = filterChannelSecuritySettings(channelsNode, allowedChannels)
	}

	// Resolve encrypted values for model_list, tools, and other non-channel
	// settings. channel_list is absent here so an overlay cannot mutate a
	// channel before its current schema has been validated below.
	decoder := yaml.NewDecoder(bytes.NewReader(nonChannelData))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return fmt.Errorf("failed to parse security config %s: %w", securityPath, err)
	}

	if channelsNode != nil {
		if err := validateChannelSecuritySettings(channelsNode, cfg, securityPath); err != nil {
			return err
		}
		if err := cfg.Channels.UnmarshalYAML(channelsNode); err != nil {
			return fmt.Errorf("failed to merge channels from security config: %w", err)
		}
	}

	return nil
}

func splitChannelSecurityDocument(root *yaml.Node) (*yaml.Node, []byte, error) {
	if root == nil || len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		data, err := yaml.Marshal(root)
		return nil, data, err
	}

	document := *root
	mapping := *root.Content[0]
	mapping.Content = make([]*yaml.Node, 0, len(root.Content[0].Content))
	var channels *yaml.Node
	content := root.Content[0].Content
	for index := 0; index+1 < len(content); index += 2 {
		if content[index].Value == "channel_list" {
			channels = content[index+1]
			continue
		}
		mapping.Content = append(mapping.Content, content[index], content[index+1])
	}
	document.Content = []*yaml.Node{&mapping}
	data, err := yaml.Marshal(&document)
	return channels, data, err
}

func filterChannelSecuritySettings(node *yaml.Node, allowed map[string]struct{}) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return node
	}
	filtered := *node
	filtered.Content = make([]*yaml.Node, 0, len(node.Content))
	for index := 0; index+1 < len(node.Content); index += 2 {
		if _, ok := allowed[node.Content[index].Value]; !ok {
			continue
		}
		filtered.Content = append(filtered.Content, node.Content[index], node.Content[index+1])
	}
	return &filtered
}

func validateChannelSecuritySettings(node *yaml.Node, current *Config, label string) error {
	var channels map[string]any
	if err := node.Decode(&channels); err != nil {
		return fmt.Errorf("failed to validate channel security config: %w", err)
	}

	normalized := make(map[string]any, len(channels))
	var unknownFields []string
	var nonObjectEntries []string
	for name, rawChannel := range channels {
		existing := current.Channels.Get(name)
		if existing == nil {
			unknownFields = append(unknownFields, appendJSONPath("channel_list", name))
			continue
		}
		channel, ok := rawChannel.(map[string]any)
		if !ok {
			nonObjectEntries = append(nonObjectEntries, appendJSONPath("channel_list", name))
			continue
		}
		for field := range channel {
			if field != "settings" {
				unknownFields = append(
					unknownFields,
					appendJSONPath(appendJSONPath("channel_list", name), field),
				)
			}
		}
		normalized[name] = map[string]any{
			"type":     existing.Type,
			"settings": channel["settings"],
		}
	}
	if err := unknownJSONFieldsError(unknownFields, label); err != nil {
		return fmt.Errorf("failed to validate channel security config: %w", err)
	}
	if len(nonObjectEntries) != 0 {
		sort.Strings(nonObjectEntries)
		return fmt.Errorf(
			"failed to validate channel security config: %s channel entries must be objects: %s",
			label,
			strings.Join(nonObjectEntries, ", "),
		)
	}

	raw := map[string]any{"channel_list": normalized}
	if err := validateChannelSettingsJSON(raw, current, label); err != nil {
		return fmt.Errorf("failed to validate channel security config: %w", err)
	}
	return nil
}

// saveSecurityConfig saves the security configuration to security.yml
func saveSecurityConfig(securityPath string, sec *Config) error {
	data, err := marshalSecurityConfig(sec)
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(securityPath, data, 0o600)
}

func marshalSecurityConfig(sec *Config) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	err := enc.Encode(sec)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal security config: %w", err)
	}
	if err = enc.Close(); err != nil {
		return nil, fmt.Errorf("close security config encoder: %w", err)
	}
	return buf.Bytes(), nil
}

// SensitiveDataCache caches the strings.Replacer for filtering sensitive data.
// Computed once on first access via sync.Once.
type SensitiveDataCache struct {
	replacer *strings.Replacer
	once     sync.Once
}

// SensitiveDataReplacer returns the strings.Replacer for filtering sensitive data.
// It is computed once on first access via sync.Once.
func (sec *Config) SensitiveDataReplacer() *strings.Replacer {
	sec.initSensitiveCache()
	return sec.sensitiveCache.replacer
}

// initSensitiveCache initializes the sensitive data cache if not already done.
func (sec *Config) initSensitiveCache() {
	if sec.sensitiveCache == nil {
		sec.sensitiveCache = &SensitiveDataCache{}
	}
	sec.sensitiveCache.once.Do(func() {
		values := sec.collectSensitiveValues()
		if len(values) == 0 {
			sec.sensitiveCache.replacer = strings.NewReplacer()
			return
		}

		// Build old/new pairs for strings.Replacer
		var pairs []string
		for _, v := range values {
			if len(v) > 3 {
				pairs = append(pairs, v, "[FILTERED]")
			}
		}
		if len(pairs) == 0 {
			sec.sensitiveCache.replacer = strings.NewReplacer()
			return
		}
		sec.sensitiveCache.replacer = strings.NewReplacer(pairs...)
	})
}

// collectSensitiveValues collects all sensitive strings from SecurityConfig using reflection.
func (sec *Config) collectSensitiveValues() []string {
	var values []string
	collectSensitive(reflect.ValueOf(sec), &values)
	return values
}

// collectSensitive recursively traverses the value and collects SecureString/SecureStrings values.
func collectSensitive(v reflect.Value, values *[]string) {
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return
		}
		v = v.Elem()
	}

	t := v.Type()

	// Channel: use CollectSensitiveValues() method
	if t == reflect.TypeOf(Channel{}) {
		if method := v.MethodByName("CollectSensitiveValues"); method.IsValid() {
			results := method.Call(nil)
			if len(results) > 0 {
				if vals, ok := results[0].Interface().([]string); ok {
					*values = append(*values, vals...)
				}
			}
		}
		return
	}

	// SecureString: collect via String() method (defined on *SecureString)
	if t == reflect.TypeOf(SecureString{}) {
		// Create a new pointer to make it addressable for method calls
		ptr := reflect.New(t)
		ptr.Elem().Set(v)
		result := ptr.MethodByName("String").Call(nil)
		if len(result) > 0 {
			if s := result[0].String(); s != "" {
				*values = append(*values, s)
			}
		}
		return
	}

	// SecureStrings ([]*SecureString): iterate and collect each element
	if t == reflect.TypeOf(SecureStrings{}) {
		for i := 0; i < v.Len(); i++ {
			elem := v.Index(i)
			for elem.Kind() == reflect.Ptr || elem.Kind() == reflect.Interface {
				if elem.IsNil() {
					elem = reflect.Value{}
					break
				}
				elem = elem.Elem()
			}
			if elem.IsValid() && elem.Type() == reflect.TypeOf(SecureString{}) {
				result := elem.Addr().MethodByName("String").Call(nil)
				if len(result) > 0 {
					if s := result[0].String(); s != "" {
						*values = append(*values, s)
					}
				}
			}
		}
		return
	}

	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if !t.Field(i).IsExported() {
				continue
			}
			collectSensitive(v.Field(i), values)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			collectSensitive(v.Index(i), values)
		}
	case reflect.Map:
		for _, key := range v.MapKeys() {
			collectSensitive(v.MapIndex(key), values)
		}
	}
}
