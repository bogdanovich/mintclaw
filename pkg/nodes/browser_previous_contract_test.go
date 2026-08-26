package nodes

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type previousBrowserSchemaGeneration string

const (
	previousBrowserSchemaInline   previousBrowserSchemaGeneration = "inline"
	previousBrowserSchemaStreamed previousBrowserSchemaGeneration = "streamed"
)

func TestBrowserCatalogRejectsExactPreviousSnapshotSchemaGenerations(t *testing.T) {
	for _, generation := range []previousBrowserSchemaGeneration{
		previousBrowserSchemaInline,
		previousBrowserSchemaStreamed,
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

func TestFileRegistryRejectsExactPreviousSnapshotSchemaGenerations(t *testing.T) {
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

// previousBrowserCatalogFixture preserves the two exact schema deltas that
// immediately preceded the current streamed-snapshot contract. It is test-only
// so runtime code has no way to generate or admit either historical contract.
func previousBrowserCatalogFixture(
	t *testing.T,
	generation previousBrowserSchemaGeneration,
) []CommandDescriptor {
	t.Helper()
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
