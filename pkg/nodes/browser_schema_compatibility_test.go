package nodes

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Reconstruct the immediately preceding wire schema independently of the
// production compatibility factory: only the two new optional properties differ.
func previousNavigationSchema(t *testing.T, schema json.RawMessage) json.RawMessage {
	t.Helper()
	var value any
	if err := json.Unmarshal(schema, &value); err != nil {
		t.Fatal(err)
	}
	var visit func(any)
	visit = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			if properties, ok := value["properties"].(map[string]any); ok && properties["snapshot_generation"] != nil {
				delete(properties, "http_status")
				delete(properties, "anti_bot_block")
			}
			for _, child := range value {
				visit(child)
			}
		case []any:
			for _, child := range value {
				visit(child)
			}
		}
	}
	visit(value)
	result, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestBrowserCommandsAcceptPreviousNavigationOutputSchema(t *testing.T) {
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{browserProfileDescriptorFixture()})
	if err != nil {
		t.Fatal(err)
	}
	for _, descriptor := range descriptors {
		t.Run(descriptor.Name, func(t *testing.T) {
			descriptor.OutputSchema = previousNavigationSchema(t, descriptor.OutputSchema)
			if err := descriptor.Validate(); err != nil {
				t.Fatalf("previous companion capability rejected: %v", err)
			}
			var schema map[string]any
			if err := json.Unmarshal(descriptor.OutputSchema, &schema); err != nil {
				t.Fatal(err)
			}
			schema["additionalProperties"] = true
			descriptor.OutputSchema, err = json.Marshal(schema)
			if err != nil {
				t.Fatal(err)
			}
			if err := descriptor.Validate(); !errors.Is(err, ErrInvalidCapability) {
				t.Fatalf("schema drift accepted through compatibility: %v", err)
			}
		})
	}
}

func TestFileRegistryReloadsApprovedPreviousNavigationCatalogWithoutChangingAuthority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	registry, err := NewFileRegistry(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	pairing := testPendingPairing(t, 1)
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{browserProfileDescriptorFixture()})
	if err != nil {
		t.Fatal(err)
	}
	pairing.Node.Catalog = CapabilityCatalog{Commands: descriptors}
	pairing.Node.CatalogHash, err = pairing.Node.Catalog.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.UpsertPending(pairing); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Approve(pairing.Node.ID, PairingApproval{
		Aliases: []Alias{"browser-node"}, AllowedCommands: []string{BrowserCommandObserve}, At: 2,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document registryDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	record := document.Records[string(pairing.Node.ID)]
	for index := range record.Snapshot.Catalog.Commands {
		descriptor := &record.Snapshot.Catalog.Commands[index]
		descriptor.OutputSchema = previousNavigationSchema(t, descriptor.OutputSchema)
	}
	record.Snapshot.CatalogHash, err = record.Snapshot.Catalog.canonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	record.ApprovedCatalogHash = record.Snapshot.CatalogHash
	document.Records[string(pairing.Node.ID)] = record
	raw, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewFileRegistry(path, 4)
	if err != nil {
		t.Fatalf("upgrade rejected existing approved node registry: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(raw) {
		t.Fatalf("loading old catalog rewrote its authority: %v", err)
	}
	if _, exists, err := reloaded.Registration(pairing.Node.ID); err != nil || !exists {
		t.Fatalf("approved node missing after reload: exists=%v err=%v", exists, err)
	}
}
