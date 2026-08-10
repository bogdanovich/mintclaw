package nodes

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkspaceReadDescriptorsAreHiddenAndBounded(t *testing.T) {
	descriptors, err := WorkspaceDescriptors(
		[]FileProfileDescriptor{
			workspaceTestFileProfile("project-b", "project-v2"),
			workspaceTestFileProfile("project-a", "project-v1"),
		},
		[]string{"source", "build"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(descriptors) != 4 || descriptors[0].Name != WorkspaceCommandRead ||
		descriptors[1].Name != WorkspaceCommandSearch || descriptors[2].Name != WorkspaceCommandWrite ||
		descriptors[3].Name != WorkspaceCommandPatch {
		t.Fatalf("workspace descriptors = %#v", descriptors)
	}
	for _, descriptor := range descriptors {
		if descriptor.ModelContract == nil || descriptor.ModelContract.Availability != ModelUnavailable ||
			len(descriptor.ModelContract.AuthorityDigest) != 64 || !descriptor.SupportsCancel ||
			(descriptor.Risk != RiskRead && descriptor.Risk != RiskWrite) {
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
	write := map[string]any{
		"profile_revision": "project-v1", "workspace_revision": "workspace-v1",
		"working_scope": "build", "path": "README.md", "content": "new\n", "overwrite": true,
		"expected_sha256": strings.Repeat("a", 64),
	}
	if err := validateInvocationInput(descriptors[2].InputSchema, write); err != nil {
		t.Fatal(err)
	}
	write["expected_sha256"] = strings.Repeat("z", 64)
	if err := validateInvocationInput(descriptors[2].InputSchema, write); err == nil {
		t.Fatal("malformed digest passed workspace write schema")
	}
}

func TestWorkspaceReadDescriptorAuthorityChangesWithProfiles(t *testing.T) {
	first, err := WorkspaceDescriptors(
		[]FileProfileDescriptor{workspaceTestFileProfile("project", "project-v1")},
		[]string{"project"},
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := WorkspaceDescriptors(
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

func TestWorkspaceDescriptorsDoNotAdvertiseMutationWithoutWritableAuthority(t *testing.T) {
	profile := workspaceTestFileProfile("project", "project-v1")
	profile.WritableRoots = nil
	profile.AllowCreate = false
	profile.AllowOverwrite = false
	descriptors, err := WorkspaceDescriptors([]FileProfileDescriptor{profile}, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	if len(descriptors) != 2 || descriptors[0].Name != WorkspaceCommandRead ||
		descriptors[1].Name != WorkspaceCommandSearch {
		t.Fatalf("read-only workspace descriptors = %#v", descriptors)
	}
}

func workspaceTestFileProfile(alias, revision string) FileProfileDescriptor {
	return FileProfileDescriptor{
		Alias: alias, Revision: revision, ReadableRoots: []string{"/workspace"},
		WritableRoots: []string{"/workspace"}, AllowCreate: true, AllowOverwrite: true, MaxFileBytes: 1024,
		Approval: FileProfileApproval{Metadata: "none", Read: "required", Write: "required"},
	}
}
