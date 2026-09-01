package nodes

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	preRestrictedPolicyBrowserCatalogFixtureSHA256 = "b71a4ad7de46f6c40691e39dc23b3395ef4e0b083c44586ef03add4780017fe5"
	preRestrictedPolicyBrowserTemplateSHA256       = "92006eeb9405c97a79aef12924b399a8f9e3742c17bb45343db8a7414fe5a0d6"
)

type previousBrowserSchemaGeneration string

const (
	previousBrowserSchemaInline              previousBrowserSchemaGeneration = "inline"
	previousBrowserSchemaStreamed            previousBrowserSchemaGeneration = "streamed"
	previousBrowserSchemaPreRestrictedPolicy previousBrowserSchemaGeneration = "pre_restricted_policy"
)

func TestBrowserCatalogRejectsExactPreviousSnapshotSchemaGenerations(t *testing.T) {
	for _, generation := range []previousBrowserSchemaGeneration{
		previousBrowserSchemaInline,
		previousBrowserSchemaStreamed,
		previousBrowserSchemaPreRestrictedPolicy,
	} {
		t.Run(string(generation), func(t *testing.T) {
			descriptors := previousBrowserCatalogFixture(t, generation)
			if err := (CapabilityCatalog{Commands: descriptors}).Validate(); !errors.Is(
				err,
				ErrInvalidCapability,
			) {
				t.Fatalf("previous %s browser catalog error = %v", generation, err)
			}
		})
	}
}

func TestFileRegistryRejectsUnsupportedPreviousSnapshotSchemaGenerations(t *testing.T) {
	for _, generation := range []previousBrowserSchemaGeneration{
		previousBrowserSchemaInline,
		previousBrowserSchemaStreamed,
	} {
		t.Run(string(generation), func(t *testing.T) {
			pairing := testPendingPairing(t, 1)
			catalog := CapabilityCatalog{Commands: previousBrowserCatalogFixture(t, generation)}
			catalogHash, err := catalog.canonicalHash()
			if err != nil {
				t.Fatal(err)
			}
			pairing.Node.Catalog = catalog
			pairing.Node.CatalogHash = catalogHash
			document := registryDocument{Version: registryFileVersion, Records: map[string]registryRecord{
				string(pairing.Node.ID): {
					Snapshot: pairing.Node, PublicKey: pairing.PublicKey, KeyAlgorithm: pairing.KeyAlgorithm,
					RequestedRole: pairing.RequestedRole, RequestedAt: pairing.RequestedAt,
				},
			}}
			encoded, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "registry.json")
			if err = os.WriteFile(path, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err = NewFileRegistry(path, 4); !errors.Is(err, ErrInvalidCapability) {
				t.Fatalf("load registry with previous %s browser catalog error = %v", generation, err)
			}
		})
	}
}

func TestFileRegistryQuarantinesPreRestrictedPolicyBrowserSchema(t *testing.T) {
	for _, test := range []struct {
		name              string
		commands          func(*testing.T) []CommandDescriptor
		remainingCommands int
	}{
		{
			name: "ordered browser commands",
			commands: func(t *testing.T) []CommandDescriptor {
				return previousBrowserCatalogFixture(t, previousBrowserSchemaPreRestrictedPolicy)
			},
		},
		{
			name: "reordered and interleaved commands",
			commands: func(t *testing.T) []CommandDescriptor {
				browser := previousBrowserCatalogFixture(t, previousBrowserSchemaPreRestrictedPolicy)
				commands := make([]CommandDescriptor, 0, len(browser)+1)
				for index := len(browser) - 1; index >= 0; index-- {
					if index == len(browser)/2 {
						commands = append(commands, testCatalog(t).Commands[0])
					}
					commands = append(commands, browser[index])
				}
				return commands
			},
			remainingCommands: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			pairing := testPendingPairing(t, 1)
			catalog := CapabilityCatalog{Commands: test.commands(t)}
			catalogHash, err := catalog.canonicalHash()
			if err != nil {
				t.Fatal(err)
			}
			pairing.Node.Catalog = catalog
			pairing.Node.CatalogHash = catalogHash
			document := registryDocument{Version: registryFileVersion, Records: map[string]registryRecord{
				string(pairing.Node.ID): {
					Snapshot: pairing.Node, PublicKey: pairing.PublicKey, KeyAlgorithm: pairing.KeyAlgorithm,
					RequestedRole: pairing.RequestedRole, RequestedAt: pairing.RequestedAt,
				},
			}}
			encoded, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "registry.json")
			if err = os.WriteFile(path, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			registry, err := NewFileRegistry(path, 4)
			if err != nil {
				t.Fatal(err)
			}
			pending, exists, err := registry.Pending(pairing.Node.ID)
			if err != nil || !exists {
				t.Fatalf("Pending() = exists %v, error %v", exists, err)
			}
			if len(pending.Node.Catalog.Commands) != test.remainingCommands {
				t.Fatalf("quarantined pre-restricted commands = %#v", pending.Node.Catalog.Commands)
			}
			if err := pending.Node.Validate(); err != nil {
				t.Fatalf("quarantined pre-restricted snapshot: %v", err)
			}
		})
	}
}

func TestFileRegistryRejectsPreRestrictedPolicyCatalogAggregateOverflow(t *testing.T) {
	largeSchema := `{"type":"object","description":"` + strings.Repeat("x", 60*1024) + `"}`
	for _, test := range []struct {
		name        string
		descriptors func(*testing.T) []CommandDescriptor
	}{
		{
			name: "command count",
			descriptors: func(t *testing.T) []CommandDescriptor {
				descriptors := frozenPreRestrictedPolicyBrowserCatalogFixture(t)
				for index := len(descriptors); index <= preRestrictedPolicyMaxCatalogCommands; index++ {
					descriptors = append(descriptors, descriptor(
						fmt.Sprintf("system.extra%d.v1", index),
						`{}`,
					))
				}
				return descriptors
			},
		},
		{
			name: "catalog bytes",
			descriptors: func(t *testing.T) []CommandDescriptor {
				descriptors := frozenPreRestrictedPolicyBrowserCatalogFixture(t)
				for index := range 4 {
					large := descriptor(fmt.Sprintf("system.large%d.v1", index), largeSchema)
					large.OutputSchema = json.RawMessage(largeSchema)
					descriptors = append(descriptors, large)
				}
				return descriptors
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			pairing := testPendingPairing(t, 1)
			pairing.Node.Catalog = CapabilityCatalog{Commands: test.descriptors(t)}
			nonBrowser := CapabilityCatalog{Commands: removePreRestrictedPolicyBrowserCommands(
				pairing.Node.Catalog.Commands,
			)}
			if err := nonBrowser.Validate(); err != nil {
				t.Fatalf("non-browser subset must remain individually valid: %v", err)
			}
			if err := validatePreRestrictedPolicyCatalogResources(pairing.Node.Catalog); !errors.Is(
				err,
				ErrInvalidCapability,
			) {
				t.Fatalf("aggregate resource validation error = %v", err)
			}
			var err error
			pairing.Node.CatalogHash, err = pairing.Node.Catalog.canonicalHash()
			if err != nil {
				t.Fatal(err)
			}
			document := registryDocument{Version: registryFileVersion, Records: map[string]registryRecord{
				string(pairing.Node.ID): {
					Snapshot: pairing.Node, PublicKey: pairing.PublicKey, KeyAlgorithm: pairing.KeyAlgorithm,
					RequestedRole: pairing.RequestedRole, RequestedAt: pairing.RequestedAt,
				},
			}}
			encoded, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "registry.json")
			if err = os.WriteFile(path, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err = NewFileRegistry(path, 4); !errors.Is(err, ErrInvalidCapability) {
				t.Fatalf("NewFileRegistry() error = %v, want ErrInvalidCapability", err)
			}
		})
	}
}

func TestPreRestrictedPolicyBrowserMatcherRejectsFrozenStaticSchemaDrift(t *testing.T) {
	descriptors := frozenPreRestrictedPolicyBrowserCatalogFixture(t)
	recognized, err := isPreRestrictedPolicyBrowserCatalog(CapabilityCatalog{Commands: descriptors})
	if err != nil || !recognized {
		t.Fatalf("frozen pre-restricted catalog = recognized %v, error %v", recognized, err)
	}
	for index := range descriptors {
		if descriptors[index].Name != BrowserCommandAct {
			continue
		}
		var schema map[string]any
		if err := json.Unmarshal(descriptors[index].InputSchema, &schema); err != nil {
			t.Fatal(err)
		}
		schema["future_contract_field"] = true
		descriptors[index].InputSchema = mustJSON(schema)
	}
	recognized, err = isPreRestrictedPolicyBrowserCatalog(CapabilityCatalog{Commands: descriptors})
	if err != nil || recognized {
		t.Fatalf("drifted pre-restricted catalog = recognized %v, error %v", recognized, err)
	}
}

func TestPreRestrictedPolicyBrowserFrozenArtifactsHavePinnedDigests(t *testing.T) {
	for _, fixture := range []struct {
		path string
		want string
	}{
		{
			path: filepath.Join("testdata", "browser-catalog-pre-restricted-policy.v1.json"),
			want: preRestrictedPolicyBrowserCatalogFixtureSHA256,
		},
		{
			path: filepath.Join("testdata", "browser-catalog-pre-restricted-policy-template.v1.json"),
			want: preRestrictedPolicyBrowserTemplateSHA256,
		},
	} {
		data, err := os.ReadFile(fixture.path)
		if err != nil {
			t.Fatal(err)
		}
		if digest := fmt.Sprintf("%x", sha256.Sum256(data)); digest != fixture.want {
			t.Fatalf("%s digest = %s, want %s", fixture.path, digest, fixture.want)
		}
	}
}

// previousBrowserCatalogFixture preserves three exact historical schema
// generations. The pre-restricted fixture was serialized by commit
// 6d5db643dc2c704cac07f51b9151bf67ad56bf2e, before restricted policy landed,
// so neither the fixture nor its matching template can move with the current
// descriptor generator.
func previousBrowserCatalogFixture(
	t *testing.T,
	generation previousBrowserSchemaGeneration,
) []CommandDescriptor {
	t.Helper()
	if generation == previousBrowserSchemaPreRestrictedPolicy {
		return frozenPreRestrictedPolicyBrowserCatalogFixture(t)
	}
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{browserProfileDescriptorFixture()})
	if err != nil {
		t.Fatal(err)
	}
	for index := range descriptors {
		descriptor := &descriptors[index]
		switch generation {
		case previousBrowserSchemaInline:
			descriptor.InputSchema = previousInlineBrowserInputFixture(t, *descriptor)
			descriptor.OutputSchema = previousInlineBrowserOutputFixture(t, *descriptor)
		case previousBrowserSchemaStreamed:
			descriptor.OutputSchema = previousStreamedBrowserOutputFixture(t, *descriptor)
		default:
			t.Fatalf("unknown previous browser schema generation %q", generation)
		}
	}
	return descriptors
}

func frozenPreRestrictedPolicyBrowserCatalogFixture(t *testing.T) []CommandDescriptor {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "browser-catalog-pre-restricted-policy.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var catalog CapabilityCatalog
	if err = json.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Commands) != len(preRestrictedPolicyBrowserCommandNames) {
		t.Fatalf("frozen pre-restricted command count = %d", len(catalog.Commands))
	}
	return catalog.Commands
}

func previousInlineBrowserInputFixture(t *testing.T, descriptor CommandDescriptor) json.RawMessage {
	t.Helper()
	if descriptor.Name != BrowserCommandObserve && descriptor.Name != BrowserCommandContexts {
		return descriptor.InputSchema
	}
	rewritten := false
	result := rewriteBrowserSchemaFixture(t, descriptor.InputSchema, func(schema map[string]any) {
		if rewritten {
			return
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			return
		}
		if _, ok = properties["workspace_id"]; !ok {
			return
		}
		delete(properties, "workspace_id")
		delete(properties, "browser_target")
		rewritten = true
	})
	if !rewritten {
		t.Fatalf("%s input schema has no streamed-snapshot routing properties", descriptor.Name)
	}
	return result
}

func previousInlineBrowserOutputFixture(t *testing.T, descriptor CommandDescriptor) json.RawMessage {
	t.Helper()
	return rewriteBrowserSchemaFixture(t, descriptor.OutputSchema, func(schema map[string]any) {
		properties, ok := browserObservationProperties(schema)
		if !ok {
			return
		}
		delete(properties, "output")
		delete(schema, "oneOf")
	})
}

func previousStreamedBrowserOutputFixture(t *testing.T, descriptor CommandDescriptor) json.RawMessage {
	t.Helper()
	maximum := strictestBrowserLimits(descriptor.BrowserProfiles).ToolResultBytes
	return rewriteBrowserSchemaFixture(t, descriptor.OutputSchema, func(schema map[string]any) {
		properties, ok := browserObservationProperties(schema)
		if !ok {
			return
		}
		output, ok := properties["output"].(map[string]any)
		if !ok {
			t.Fatal("streamed browser observation has no output descriptor")
		}
		outputProperties, ok := output["properties"].(map[string]any)
		if !ok {
			t.Fatal("streamed browser output descriptor has no properties")
		}
		size, ok := outputProperties["size"].(map[string]any)
		if !ok {
			t.Fatal("streamed browser output descriptor has no size schema")
		}
		size["maximum"] = maximum
	})
}

func browserObservationProperties(schema map[string]any) (map[string]any, bool) {
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return nil, false
	}
	_, hasSnapshot := properties["snapshot"]
	_, hasElements := properties["elements"]
	_, hasTruncated := properties["truncated"]
	return properties, hasSnapshot && hasElements && hasTruncated
}

func rewriteBrowserSchemaFixture(
	t *testing.T,
	raw json.RawMessage,
	rewrite func(map[string]any),
) json.RawMessage {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var schema any
	if err := decoder.Decode(&schema); err != nil {
		t.Fatal(err)
	}
	walkBrowserSchemaFixture(schema, rewrite)
	return mustJSON(schema)
}

func walkBrowserSchemaFixture(value any, rewrite func(map[string]any)) {
	switch typed := value.(type) {
	case map[string]any:
		rewrite(typed)
		for _, child := range typed {
			walkBrowserSchemaFixture(child, rewrite)
		}
	case []any:
		for _, child := range typed {
			walkBrowserSchemaFixture(child, rewrite)
		}
	}
}
