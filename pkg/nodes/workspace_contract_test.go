package nodes

import (
	"encoding/json"
	"testing"
)

func TestWorkspaceReadDescriptorsAreHiddenAndBounded(t *testing.T) {
	descriptors, err := WorkspaceReadDescriptors(
		[]FileProfileDescriptor{
			workspaceTestFileProfile("project-b", "project-v2"),
			workspaceTestFileProfile("project-a", "project-v1"),
		},
		[]string{"source", "build"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(descriptors) != 2 || descriptors[0].Name != WorkspaceCommandRead ||
		descriptors[1].Name != WorkspaceCommandSearch {
		t.Fatalf("workspace descriptors = %#v", descriptors)
	}
	for _, descriptor := range descriptors {
		if descriptor.ModelContract == nil || descriptor.ModelContract.Availability != ModelUnavailable ||
			len(descriptor.ModelContract.AuthorityDigest) != 64 || !descriptor.SupportsCancel ||
			descriptor.Risk != RiskRead {
			t.Fatalf("workspace descriptor = %#v", descriptor)
		}
		if err := descriptor.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	input := map[string]any{
		"profile_revision": "project-v1", "workspace_revision": "workspace-v1",
		"working_scope": "build", "path": "README.md",
	}
	if err := validateInvocationInput(descriptors[0].InputSchema, input); err != nil {
		t.Fatal(err)
	}
	input["path"] = ""
	if err := validateInvocationInput(descriptors[0].InputSchema, input); err == nil {
		t.Fatal("empty path passed workspace read schema")
	}
}

func TestWorkspaceReadDescriptorAuthorityChangesWithProfiles(t *testing.T) {
	first, err := WorkspaceReadDescriptors(
		[]FileProfileDescriptor{workspaceTestFileProfile("project", "project-v1")},
		[]string{"project"},
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := WorkspaceReadDescriptors(
		[]FileProfileDescriptor{workspaceTestFileProfile("project", "project-v2")},
		[]string{"project"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if first[0].ModelContract.AuthorityDigest == second[0].ModelContract.AuthorityDigest {
		t.Fatal("workspace authority digest did not change with profile revision")
	}
	if _, err := json.Marshal(first); err != nil {
		t.Fatal(err)
	}
}

func workspaceTestFileProfile(alias, revision string) FileProfileDescriptor {
	return FileProfileDescriptor{
		Alias: alias, Revision: revision, ReadableRoots: []string{"/workspace"}, MaxFileBytes: 1024,
		Approval: FileProfileApproval{Metadata: "none", Read: "required", Write: "required"},
	}
}
