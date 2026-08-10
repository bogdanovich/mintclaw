package nodes

// ProjectFileDescriptorForProfile narrows one authenticated file-capability
// descriptor to the operator-selected profile used by an execution target.
// The input schema remains the catalog schema; the prepared input binds the
// exact profile revision separately.
func ProjectFileDescriptorForProfile(
	descriptor CommandDescriptor,
	profileAlias string,
) (CommandDescriptor, bool) {
	if len(descriptor.FileProfiles) == 0 {
		return descriptor, true
	}
	if profileAlias == "" || descriptor.ModelContract == nil {
		return CommandDescriptor{}, false
	}
	for _, profile := range descriptor.FileProfiles {
		if profile.Alias != profileAlias {
			continue
		}
		descriptor.FileProfiles = []FileProfileDescriptor{profile}
		contract := *descriptor.ModelContract
		switch descriptor.Name {
		case "file.info.v1":
			if profile.Approval.Metadata == "required" {
				contract.ApprovalMode = "each_command"
			}
		case "file.download.v1":
			if profile.Approval.Read == "required" {
				contract.ApprovalMode = "each_command"
			}
		case "file.upload.v1":
			if profile.Approval.Write == "required" {
				contract.ApprovalMode = "each_command"
			}
		case WorkspaceCommandRead, WorkspaceCommandSearch:
			if profile.Approval.Read == "required" {
				contract.ApprovalMode = "each_command"
			}
		case WorkspaceCommandWrite, WorkspaceCommandPatch:
			if profile.Approval.Write == "required" {
				contract.ApprovalMode = "each_command"
			}
		default:
			return CommandDescriptor{}, false
		}
		descriptor.ModelContract = &contract
		return descriptor, true
	}
	return CommandDescriptor{}, false
}
