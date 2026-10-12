package thread

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"testing"
)

type stateInventory struct {
	SchemaVersion  string                `json:"schema_version"`
	BaselineCommit string                `json:"baseline_commit"`
	Classes        []stateInventoryClass `json:"classes"`
}

type stateInventoryClass struct {
	ID               string   `json:"id"`
	Owner            string   `json:"owner"`
	Scope            string   `json:"scope"`
	ThreadEntry      string   `json:"thread_entry,omitempty"`
	Locations        []string `json:"locations"`
	Description      string   `json:"description"`
	Privacy          string   `json:"privacy"`
	ExportClass      string   `json:"export_class"`
	Retention        string   `json:"retention"`
	Deletion         string   `json:"deletion"`
	ProjectAuthority bool     `json:"project_authority"`
}

// The inventory is a test-only audit artifact, never a runtime registry. This
// contract forces a new disposable thread entry to acquire an explicit owner,
// privacy/export class, retention policy and deletion boundary.
func TestCodingStateOwnershipInventoryContract(t *testing.T) {
	data, err := os.ReadFile("testdata/state_ownership_inventory.json")
	if err != nil {
		t.Fatal(err)
	}
	var inventory stateInventory
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&inventory); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		t.Fatalf("inventory trailing JSON: %v", err)
	}
	if inventory.SchemaVersion != "mintclaw.coding_state_inventory.v1" ||
		!regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(inventory.BaselineCommit) {
		t.Fatalf("invalid inventory schema or baseline: %+v", inventory)
	}
	seen := make(map[string]bool)
	threadEntries := make(map[string]bool)
	for _, class := range inventory.Classes {
		if seen[class.ID] || class.ID == "" || class.Owner == "" || class.Scope == "" ||
			len(class.Locations) == 0 || class.Description == "" || class.Privacy == "" ||
			class.Retention == "" || class.Deletion == "" {
			t.Fatalf("incomplete or duplicate state class: %+v", class)
		}
		seen[class.ID] = true
		if class.ExportClass != "excluded" && class.ExportClass != "metadata_safe" {
			t.Fatalf("unreviewed export class for %q: %q", class.ID, class.ExportClass)
		}
		switch class.Privacy {
		case "private_content",
			"operational_metadata",
			"credential_and_private_content",
			"credentials",
			"external_content":
		default:
			t.Fatalf("unreviewed privacy class for %q: %q", class.ID, class.Privacy)
		}
		if (class.Privacy == "credentials" || class.Privacy == "credential_and_private_content") &&
			class.ExportClass != "excluded" {
			t.Fatalf("credential-bearing class %q admitted to export", class.ID)
		}
		if class.ThreadEntry != "" {
			if threadEntries[class.ThreadEntry] || !ownedThreadEntry(class.ThreadEntry) ||
				class.Scope != "thread" || class.Owner != "coding_thread" || class.ProjectAuthority ||
				class.Deletion != "thread_trash_then_purge" {
				t.Fatalf("unsafe disposable thread class: %+v", class)
			}
			threadEntries[class.ThreadEntry] = true
		} else if class.Deletion == "thread_trash_then_purge" {
			t.Fatalf("non-thread owner admitted to thread deletion: %+v", class)
		}
	}
	for entry := range ownedThreadEntries {
		if !threadEntries[entry] {
			t.Fatalf("disposable thread entry %q has no privacy/lifecycle inventory", entry)
		}
	}
	for _, required := range []string{
		"shared_attachment_blobs", "fork_and_gc_quarantine", "model_recents", "config_and_credentials",
		"worktree_allocation", "gateway_and_companion_ledgers", "service_logs_and_support_exports",
		"transient_views_and_drafts", "project_git_and_external_effects",
	} {
		if !seen[required] {
			t.Fatalf("inventory lost separate owner %q", required)
		}
	}
}
