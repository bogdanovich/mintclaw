package nodes

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
)

const staleBrowserCatalogReason = "browser capability schema changed; reconnect with current software and renew command approval"

var preRestrictedPolicyBrowserCommandNames = [...]string{
	"browser.session.open.v1",
	"browser.session.status.v1",
	"browser.observe.v1",
	"browser.act.v1",
	"browser.contexts.v1",
	"browser.session.close.v1",
	"browser.capture.v1",
	"browser.diagnostics.v1",
}

//go:embed testdata/browser-catalog-pre-restricted-policy-template.v1.json
var preRestrictedPolicyBrowserCatalogTemplate []byte

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
	migrated.Catalog.Commands = removePreRestrictedPolicyBrowserCommands(migrated.Catalog.Commands)
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
	browserCommands := make([]CommandDescriptor, 0, len(preRestrictedPolicyBrowserCommandNames))
	nonBrowser := make([]CommandDescriptor, 0, len(catalog.Commands))
	for _, descriptor := range catalog.Commands {
		if !strings.HasPrefix(descriptor.Name, "browser.") {
			nonBrowser = append(nonBrowser, descriptor)
			continue
		}
		if profiles == nil {
			profiles = descriptor.BrowserProfiles
		} else if !reflect.DeepEqual(profiles, descriptor.BrowserProfiles) {
			return false, nil
		}
		browserCommands = append(browserCommands, descriptor)
	}
	if len(browserCommands) != len(preRestrictedPolicyBrowserCommandNames) ||
		!preRestrictedPolicyProfiles(profiles) {
		return false, nil
	}
	if err := (CapabilityCatalog{Commands: nonBrowser}).Validate(); err != nil {
		return false, err
	}
	bindings, ok := preRestrictedPolicyTemplateBindings(profiles)
	if !ok {
		return false, nil
	}
	actual, ok := historicalBrowserJSONValue(CapabilityCatalog{Commands: browserCommands})
	if !ok {
		return false, nil
	}
	template, ok := decodeHistoricalBrowserJSON(preRestrictedPolicyBrowserCatalogTemplate)
	return ok && matchHistoricalBrowserTemplate(actual, template, bindings), nil
}

func preRestrictedPolicyProfiles(profiles []BrowserProfileDescriptor) bool {
	if len(profiles) == 0 || len(profiles) > 8 {
		return false
	}
	priorAlias := ""
	revisions := make(map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		if err := (Alias(profile.Alias)).Validate(); err != nil ||
			!validInvocationIdentifier(profile.Revision) ||
			profile.Driver != "playwright_mcp" || profile.Mode != "managed" ||
			!preRestrictedPolicyNetworkMode(profile.NetworkMode) ||
			!preRestrictedPolicyCapabilityMode(profile.CapabilityMode) ||
			!preRestrictedPolicyApprovalMode(profile.ApprovalMode) || profile.PolicyRevision != "" ||
			profile.DryRun == profile.AllowApprovedActions ||
			!preRestrictedPolicyActions(profile.Actions, profile.DryRun) ||
			!preRestrictedPolicyLimits(profile.Limits) ||
			(priorAlias != "" && profile.Alias <= priorAlias) {
			return false
		}
		if _, duplicate := revisions[profile.Revision]; duplicate {
			return false
		}
		revisions[profile.Revision] = struct{}{}
		priorAlias = profile.Alias
	}
	return true
}

func preRestrictedPolicyNetworkMode(mode string) bool {
	return mode == "exact_origins" || mode == "public_web" || mode == "any_http"
}

func preRestrictedPolicyCapabilityMode(mode string) bool {
	return mode == "" || mode == "full_access" || mode == "legacy_strict"
}

func preRestrictedPolicyApprovalMode(mode string) bool {
	return mode == "" || mode == "none" || mode == "model_requested" || mode == "always_commit"
}

func preRestrictedPolicyActions(actions []string, dryRun bool) bool {
	if len(actions) == 0 || len(actions) > 16 || !sort.StringsAreSorted(actions) {
		return false
	}
	seen := make(map[string]struct{}, len(actions))
	for _, action := range actions {
		if !preRestrictedPolicyAction(action) || dryRun && action == "drag" {
			return false
		}
		if _, duplicate := seen[action]; duplicate {
			return false
		}
		seen[action] = struct{}{}
	}
	return true
}

func preRestrictedPolicyAction(action string) bool {
	switch action {
	case "navigate", "click", "fill", "select", "press", "scroll", "dialog",
		"check", "uncheck", "hover", "drag", "file_chooser", "upload", "download":
		return true
	default:
		return false
	}
}

func preRestrictedPolicyLimits(limits BrowserLimits) bool {
	return limits.Sessions == 1 && limits.Tabs > 0 && limits.Tabs <= 4 &&
		limits.SessionSeconds > 0 && limits.SessionSeconds <= 3600 &&
		limits.IdleSeconds > 0 && limits.IdleSeconds <= limits.SessionSeconds && limits.IdleSeconds <= 600 &&
		limits.PreparedSeconds > 0 && limits.PreparedSeconds <= limits.SessionSeconds &&
		limits.PreparedSeconds <= 300 && limits.ActionSeconds > 0 && limits.ActionSeconds <= 60 &&
		limits.SnapshotBytes > 0 && limits.SnapshotBytes <= 256*1024 &&
		limits.ScreenshotBytes > 0 && limits.ScreenshotBytes <= 8*1024*1024 &&
		limits.UploadBytes > 0 && limits.UploadBytes <= 32*1024*1024 &&
		limits.DownloadBytes > 0 && limits.DownloadBytes <= 32*1024*1024 &&
		limits.SnapshotRefs > 0 && limits.SnapshotRefs <= 500 &&
		limits.TextInputBytes > 0 && limits.TextInputBytes <= 16*1024 &&
		limits.ToolResultBytes >= 64*1024 && limits.ToolResultBytes <= 320*1024 &&
		limits.RetentionSecs > 0 && limits.RetentionSecs <= 7*24*60*60
}

func removePreRestrictedPolicyBrowserCommands(commands []CommandDescriptor) []CommandDescriptor {
	result := make([]CommandDescriptor, 0, len(commands))
	for _, descriptor := range commands {
		if !preRestrictedPolicyBrowserCommand(descriptor.Name) {
			result = append(result, descriptor)
		}
	}
	return result
}

func preRestrictedPolicyBrowserCommand(name string) bool {
	for _, command := range preRestrictedPolicyBrowserCommandNames {
		if command == name {
			return true
		}
	}
	return false
}

func preRestrictedPolicyTemplateBindings(profiles []BrowserProfileDescriptor) (map[string]any, bool) {
	limits := preRestrictedPolicyStrictestLimits(profiles)
	values := map[string]any{
		"profiles":               profiles,
		"profile_branches":       preRestrictedPolicyProfileBranches(profiles),
		"profile_revisions":      preRestrictedPolicyProfileRevisions(profiles),
		"action_schema":          preRestrictedPolicyActionSchema(preRestrictedPolicyActionUnion(profiles)),
		"snapshot_refs":          limits.SnapshotRefs,
		"snapshot_payload_bytes": preRestrictedPolicySnapshotPayloadLimit(limits),
		"screenshot_bytes":       limits.ScreenshotBytes,
		"snapshot_bytes":         limits.SnapshotBytes,
		"download_bytes":         limits.DownloadBytes,
	}
	bindings := make(map[string]any, len(values))
	for name, value := range values {
		decoded, ok := historicalBrowserJSONValue(value)
		if !ok {
			return nil, false
		}
		bindings[name] = decoded
	}
	return bindings, true
}

func preRestrictedPolicyProfileBranches(profiles []BrowserProfileDescriptor) []any {
	branches := make([]any, 0, len(profiles))
	for _, profile := range profiles {
		branches = append(branches, map[string]any{
			"required": []string{"profile", "profile_revision", "dry_run"},
			"properties": map[string]any{
				"profile":          map[string]any{"const": profile.Alias},
				"profile_revision": map[string]any{"const": profile.Revision},
				"limits":           preRestrictedPolicyLimitsSchema(profile.Limits),
				"dry_run":          map[string]any{"const": profile.DryRun},
			},
		})
	}
	return branches
}

func preRestrictedPolicyProfileRevisions(profiles []BrowserProfileDescriptor) []string {
	revisions := make([]string, len(profiles))
	for index := range profiles {
		revisions[index] = profiles[index].Revision
	}
	return revisions
}

func preRestrictedPolicyLimitsSchema(limits BrowserLimits) map[string]any {
	integer := func(maximum int) map[string]any {
		return map[string]any{"type": "integer", "minimum": 1, "maximum": maximum}
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required": []string{
			"sessions", "tabs", "session_seconds", "idle_seconds", "prepared_seconds", "action_seconds",
			"snapshot_bytes", "screenshot_bytes", "upload_bytes", "download_bytes", "snapshot_refs",
			"text_input_bytes", "tool_result_bytes", "retention_seconds",
		},
		"properties": map[string]any{
			"sessions":         map[string]any{"const": limits.Sessions},
			"tabs":             integer(limits.Tabs),
			"session_seconds":  integer(limits.SessionSeconds),
			"idle_seconds":     integer(limits.IdleSeconds),
			"prepared_seconds": integer(limits.PreparedSeconds),
			"action_seconds":   integer(limits.ActionSeconds),
			"snapshot_bytes":   integer(limits.SnapshotBytes),
			"screenshot_bytes": integer(limits.ScreenshotBytes),
			"upload_bytes":     integer(limits.UploadBytes),
			"download_bytes":   integer(limits.DownloadBytes),
			"snapshot_refs":    integer(limits.SnapshotRefs),
			"text_input_bytes": integer(limits.TextInputBytes),
			"tool_result_bytes": map[string]any{
				"type": "integer", "minimum": 64 * 1024, "maximum": limits.ToolResultBytes,
			},
			"retention_seconds": integer(limits.RetentionSecs),
		},
	}
}

func preRestrictedPolicyActionUnion(profiles []BrowserProfileDescriptor) []string {
	set := make(map[string]struct{})
	for _, profile := range profiles {
		for _, action := range profile.Actions {
			set[action] = struct{}{}
		}
	}
	actions := make([]string, 0, len(set))
	for action := range set {
		actions = append(actions, action)
	}
	sort.Strings(actions)
	return actions
}

func preRestrictedPolicyActionSchema(actions []string) map[string]any {
	properties := map[string]any{
		"kind": map[string]any{"type": "string", "enum": actions},
	}
	contains := func(wanted string) bool {
		index := sort.SearchStrings(actions, wanted)
		return index < len(actions) && actions[index] == wanted
	}
	containsAny := func(wanted ...string) bool {
		for _, action := range wanted {
			if contains(action) {
				return true
			}
		}
		return false
	}
	identifier := map[string]any{"type": "string", "minLength": 1, "maxLength": 128}
	if contains("navigate") {
		properties["url"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 2048}
	}
	if containsAny("click", "check", "uncheck", "hover", "fill", "select", "file_chooser", "upload", "download") {
		properties["ref"] = identifier
	}
	if contains("drag") {
		properties["source_ref"] = identifier
		properties["destination_ref"] = identifier
	}
	if contains("dialog") {
		properties["dialog_id"] = identifier
		properties["decision"] = map[string]any{"type": "string", "enum": []string{"accept", "dismiss"}}
		properties["prompt_provided"] = map[string]any{"type": "boolean"}
	}
	if contains("press") {
		properties["target"] = map[string]any{"type": "string", "enum": []string{"document"}}
		properties["key"] = map[string]any{"type": "string", "enum": []string{
			"Enter", "Space", "Escape", "Tab", "Shift+Tab", "ArrowUp", "ArrowDown", "ArrowLeft",
			"ArrowRight", "Home", "End", "PageUp", "PageDown", "Backspace", "Delete",
		}}
	}
	if containsAny("fill", "select", "dialog") {
		properties["value"] = map[string]any{"type": "string", "maxLength": 16 * 1024}
	}
	if contains("scroll") {
		properties["direction"] = map[string]any{"type": "string", "enum": []string{"up", "down"}}
		properties["amount"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 5}
	}
	if containsAny("file_chooser", "upload") {
		properties["artifact_ref"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 512}
	}
	if contains("download") {
		properties["deliver"] = map[string]any{"type": "boolean"}
	}
	return map[string]any{
		"type": "object", "properties": properties, "required": []string{"kind"},
		"additionalProperties": true,
	}
}

func preRestrictedPolicyStrictestLimits(profiles []BrowserProfileDescriptor) BrowserLimits {
	limits := profiles[0].Limits
	for _, profile := range profiles[1:] {
		candidate := profile.Limits
		limits.Sessions = min(limits.Sessions, candidate.Sessions)
		limits.Tabs = min(limits.Tabs, candidate.Tabs)
		limits.SessionSeconds = min(limits.SessionSeconds, candidate.SessionSeconds)
		limits.IdleSeconds = min(limits.IdleSeconds, candidate.IdleSeconds)
		limits.PreparedSeconds = min(limits.PreparedSeconds, candidate.PreparedSeconds)
		limits.ActionSeconds = min(limits.ActionSeconds, candidate.ActionSeconds)
		limits.SnapshotBytes = min(limits.SnapshotBytes, candidate.SnapshotBytes)
		limits.ScreenshotBytes = min(limits.ScreenshotBytes, candidate.ScreenshotBytes)
		limits.UploadBytes = min(limits.UploadBytes, candidate.UploadBytes)
		limits.DownloadBytes = min(limits.DownloadBytes, candidate.DownloadBytes)
		limits.SnapshotRefs = min(limits.SnapshotRefs, candidate.SnapshotRefs)
		limits.TextInputBytes = min(limits.TextInputBytes, candidate.TextInputBytes)
		limits.ToolResultBytes = min(limits.ToolResultBytes, candidate.ToolResultBytes)
		limits.RetentionSecs = min(limits.RetentionSecs, candidate.RetentionSecs)
	}
	return limits
}

func preRestrictedPolicySnapshotPayloadLimit(limits BrowserLimits) int {
	value := (limits.SnapshotBytes+limits.SnapshotRefs*(128+128+4096))*6 + 4096
	maximum := (256*1024+500*(128+128+4096))*6 + 4096
	return min(value, maximum)
}

func historicalBrowserJSONValue(value any) (any, bool) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	return decodeHistoricalBrowserJSON(encoded)
}

func decodeHistoricalBrowserJSON(raw []byte) (any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	return value, decoder.Decode(&struct{}{}) == io.EOF
}

func matchHistoricalBrowserTemplate(actual, template any, bindings map[string]any) bool {
	if object, ok := template.(map[string]any); ok {
		if len(object) == 1 {
			if name, marker := object["$binding"].(string); marker {
				value, exists := bindings[name]
				return exists && sameHistoricalBrowserJSON(actual, value)
			}
		}
		actualObject, ok := actual.(map[string]any)
		if !ok || len(actualObject) != len(object) {
			return false
		}
		for name, expected := range object {
			value, exists := actualObject[name]
			if !exists || !matchHistoricalBrowserTemplate(value, expected, bindings) {
				return false
			}
		}
		return true
	}
	if list, ok := template.([]any); ok {
		actualList, ok := actual.([]any)
		if !ok || len(actualList) != len(list) {
			return false
		}
		for index := range list {
			if !matchHistoricalBrowserTemplate(actualList[index], list[index], bindings) {
				return false
			}
		}
		return true
	}
	return sameHistoricalBrowserJSON(actual, template)
}

func sameHistoricalBrowserJSON(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	leftJSON, leftErr = canonicalJSON(leftJSON)
	rightJSON, rightErr = canonicalJSON(rightJSON)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}
