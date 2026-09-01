package nodes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
)

const staleBrowserCatalogReason = "browser capability schema changed; reconnect with current software and renew command approval"

// migratePreRestrictedPolicyBrowserCatalog recognizes exactly the browser
// command generation immediately before restricted policy was introduced. It
// never admits that contract for execution: the browser surface is removed,
// the catalog hash changes, and an approved node must reconnect and be
// explicitly renewed under the current contract.
func migratePreRestrictedPolicyBrowserCatalog(snapshot Snapshot) (Snapshot, bool, error) {
	if !validSHA256Digest(snapshot.CatalogHash) {
		return snapshot, false, fmt.Errorf("%w: malformed catalog hash", ErrInvalidNode)
	}
	storedCatalogHash, err := snapshot.Catalog.canonicalHash()
	if err != nil {
		return snapshot, false, err
	}
	if storedCatalogHash != snapshot.CatalogHash {
		return snapshot, false, fmt.Errorf("%w: catalog hash does not match catalog", ErrInvalidNode)
	}
	recognized, err := isPreRestrictedPolicyBrowserCatalog(snapshot.Catalog)
	if err != nil || !recognized {
		return snapshot, false, err
	}

	migrated := cloneSnapshot(snapshot)
	migrated.Catalog.Commands = removeBrowserCommands(migrated.Catalog.Commands)
	migrated.CatalogHash, err = migrated.Catalog.Hash()
	if err != nil {
		return snapshot, false, err
	}
	if err := migrated.Validate(); err != nil {
		return snapshot, false, err
	}
	if migrated.State != StatePendingPairing && migrated.State != StateRevoked {
		migrated.State = StateIncompatible
		migrated.DisconnectReason = staleBrowserCatalogReason
	}
	if err := migrated.Validate(); err != nil {
		return snapshot, false, err
	}
	return migrated, true, nil
}

func isPreRestrictedPolicyBrowserCatalog(catalog CapabilityCatalog) (bool, error) {
	var profiles []BrowserProfileDescriptor
	actualBrowser := make(map[string]CommandDescriptor)
	nonBrowser := make([]CommandDescriptor, 0, len(catalog.Commands))
	for _, descriptor := range catalog.Commands {
		if !IsBrowserCommand(descriptor.Name) {
			nonBrowser = append(nonBrowser, descriptor)
			continue
		}
		if _, duplicate := actualBrowser[descriptor.Name]; duplicate {
			return false, nil
		}
		if profiles == nil {
			profiles = descriptor.BrowserProfiles
		} else if !reflect.DeepEqual(profiles, descriptor.BrowserProfiles) {
			return false, nil
		}
		actualBrowser[descriptor.Name] = descriptor
	}
	if len(actualBrowser) == 0 || !preRestrictedPolicyProfiles(profiles) {
		return false, nil
	}

	current, err := BrowserCommandDescriptors(profiles)
	if err != nil {
		return false, err
	}
	candidate := CapabilityCatalog{Commands: append(nonBrowser, current...)}
	if err = candidate.Validate(); err != nil {
		return false, err
	}

	expected := make(map[string]CommandDescriptor, len(current)-1)
	for _, descriptor := range current {
		if descriptor.Name == BrowserCommandPolicyEvaluate {
			continue
		}
		if descriptor.Name == BrowserCommandAct {
			descriptor.InputSchema, err = preRestrictedPolicyBrowserActInputSchema(descriptor.InputSchema)
			if err != nil {
				return false, err
			}
		}
		expected[descriptor.Name] = descriptor
	}
	if len(actualBrowser) != len(expected) {
		return false, nil
	}
	for name, actual := range actualBrowser {
		wanted, exists := expected[name]
		if !exists || !sameHistoricalCommandDescriptor(actual, wanted) {
			return false, nil
		}
	}
	return true, nil
}

func preRestrictedPolicyProfiles(profiles []BrowserProfileDescriptor) bool {
	if len(profiles) == 0 {
		return false
	}
	for _, profile := range profiles {
		if browserpolicy.EffectiveCapabilityMode(profile.CapabilityMode) == browserpolicy.CapabilityRestricted ||
			browserpolicy.EffectiveApprovalMode(profile.ApprovalMode) == browserpolicy.ApprovalPolicy ||
			profile.PolicyRevision != "" {
			return false
		}
	}
	return true
}

func removeBrowserCommands(commands []CommandDescriptor) []CommandDescriptor {
	result := make([]CommandDescriptor, 0, len(commands))
	for _, descriptor := range commands {
		if !IsBrowserCommand(descriptor.Name) {
			result = append(result, descriptor)
		}
	}
	return result
}

func preRestrictedPolicyBrowserActInputSchema(current json.RawMessage) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(current))
	decoder.UseNumber()
	var schema map[string]any
	if err := decoder.Decode(&schema); err != nil {
		return nil, err
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: browser action schema lacks properties", ErrInvalidCapability)
	}
	for _, name := range []string{
		"policy_effect",
		"restricted_decision",
		"restricted_origin",
		"restricted_policy_revision",
	} {
		if _, exists := properties[name]; !exists {
			return nil, fmt.Errorf("%w: browser action schema lacks %q", ErrInvalidCapability, name)
		}
		delete(properties, name)
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("encode pre-restricted browser action schema: %w", err)
	}
	return encoded, nil
}

func sameHistoricalCommandDescriptor(actual, expected CommandDescriptor) bool {
	var err error
	actual.InputSchema, err = canonicalJSON(actual.InputSchema)
	if err != nil {
		return false
	}
	actual.OutputSchema, err = canonicalJSON(actual.OutputSchema)
	if err != nil {
		return false
	}
	expected.InputSchema, err = canonicalJSON(expected.InputSchema)
	if err != nil {
		return false
	}
	expected.OutputSchema, err = canonicalJSON(expected.OutputSchema)
	return err == nil && reflect.DeepEqual(actual, expected)
}
