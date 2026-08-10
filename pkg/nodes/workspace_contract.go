package nodes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

const (
	WorkspaceCommandRead   = "workspace.read.v1"
	WorkspaceCommandSearch = "workspace.search.v1"
	WorkspaceCommandWrite  = "workspace.write.v1"
	WorkspaceCommandPatch  = "workspace.patch.v1"

	MaxWorkspaceReadBytes    = 512 * 1024
	MaxWorkspaceWriteBytes   = 256 * 1024
	MaxWorkspacePatchBytes   = 256 * 1024
	MaxWorkspacePatchFiles   = 32
	MaxWorkspacePatchTotal   = 1024 * 1024
	MaxWorkspaceSearchResult = 64 * 1024
	MaxWorkspaceSearchFiles  = 10000
	MaxWorkspaceSearchLimit  = 500
)

func IsWorkspaceCommand(command string) bool {
	return command == WorkspaceCommandRead || command == WorkspaceCommandSearch ||
		command == WorkspaceCommandWrite || command == WorkspaceCommandPatch
}

// WorkspaceDescriptors returns node-internal command contracts. They stay
// model-unavailable in generic nodes discovery; the gateway P8a router binds
// profile, scope, and workspace revision before preparing an invocation.
func WorkspaceDescriptors(
	profiles []FileProfileDescriptor,
	workingScopes []string,
) ([]CommandDescriptor, error) {
	profiles = cloneWorkspaceFileProfiles(profiles)
	workingScopes = append([]string(nil), workingScopes...)
	slices.SortFunc(profiles, func(left, right FileProfileDescriptor) int {
		return strings.Compare(left.Alias, right.Alias)
	})
	slices.Sort(workingScopes)
	workingScopes = slices.Compact(workingScopes)
	if len(profiles) == 0 || len(workingScopes) == 0 {
		return nil, fmt.Errorf("%w: workspace read authority is empty", ErrInvalidCapability)
	}
	readableProfiles := make([]FileProfileDescriptor, 0, len(profiles))
	readableRevisions := make([]string, 0, len(profiles))
	writableProfiles := make([]FileProfileDescriptor, 0, len(profiles))
	writableRevisions := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		if len(profile.ReadableRoots) > 0 {
			readableProfiles = append(readableProfiles, profile)
			readableRevisions = append(readableRevisions, profile.Revision)
		}
		if len(profile.WritableRoots) > 0 && (profile.AllowCreate || profile.AllowOverwrite) {
			writableProfiles = append(writableProfiles, profile)
			writableRevisions = append(writableRevisions, profile.Revision)
		}
	}
	slices.Sort(readableRevisions)
	slices.Sort(writableRevisions)
	authority, err := json.Marshal(struct {
		Profiles []FileProfileDescriptor `json:"profiles"`
		Scopes   []string                `json:"scopes"`
	}{Profiles: profiles, Scopes: workingScopes})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(authority)
	contract := &CommandModelContract{
		Availability:      ModelUnavailable,
		TimeoutSecondsMax: 30,
		OutputBytesMax:    MaxWorkspaceReadBytes,
		ResultKind:        "json",
		AuthorityDigest:   hex.EncodeToString(digest[:]),
		Constraints:       CommandModelConstraints{WorkingScopes: append([]string(nil), workingScopes...)},
		Guidance:          []string{},
		Examples:          []json.RawMessage{},
	}
	descriptors := make([]CommandDescriptor, 0, 4)
	if len(readableProfiles) > 0 {
		readContract := cloneCommandModelContractPointer(contract)
		readContract.Constraints.ProfileAliases = append([]string(nil), readableRevisions...)
		descriptors = append(descriptors, CommandDescriptor{
			Name:           WorkspaceCommandRead,
			InputSchema:    workspaceReadInputSchema(readableRevisions, workingScopes),
			OutputSchema:   workspaceReadOutputSchema(),
			Risk:           RiskRead,
			SupportsCancel: true,
			ModelContract:  readContract,
			FileProfiles:   cloneWorkspaceFileProfiles(readableProfiles),
		}, CommandDescriptor{
			Name:           WorkspaceCommandSearch,
			InputSchema:    workspaceSearchInputSchema(readableRevisions, workingScopes),
			OutputSchema:   workspaceSearchOutputSchema(),
			Risk:           RiskRead,
			SupportsCancel: true,
			ModelContract:  cloneCommandModelContractPointer(readContract),
			FileProfiles:   cloneWorkspaceFileProfiles(readableProfiles),
		})
	}
	if len(writableProfiles) > 0 {
		writeContract := cloneCommandModelContractPointer(contract)
		writeContract.Constraints.ProfileAliases = append([]string(nil), writableRevisions...)
		descriptors = append(descriptors,
			CommandDescriptor{
				Name: WorkspaceCommandWrite, InputSchema: workspaceWriteInputSchema(writableRevisions, workingScopes),
				OutputSchema: workspaceWriteOutputSchema(), Risk: RiskWrite, SupportsCancel: true,
				ModelContract: writeContract, FileProfiles: cloneWorkspaceFileProfiles(writableProfiles),
			},
			CommandDescriptor{
				Name: WorkspaceCommandPatch, InputSchema: workspacePatchInputSchema(writableRevisions, workingScopes),
				OutputSchema: workspacePatchOutputSchema(), Risk: RiskWrite, SupportsCancel: true,
				ModelContract: cloneCommandModelContractPointer(writeContract),
				FileProfiles:  cloneWorkspaceFileProfiles(writableProfiles),
			},
		)
	}
	if len(descriptors) == 0 {
		return nil, fmt.Errorf("%w: workspace file authority is empty", ErrInvalidCapability)
	}
	for _, descriptor := range descriptors {
		if err := descriptor.Validate(); err != nil {
			return nil, err
		}
	}
	return descriptors, nil
}

func workspaceWriteInputSchema(profiles, scopes []string) json.RawMessage {
	return mustWorkspaceSchema(map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{
			"profile_revision", "workspace_revision", "working_scope", "path", "content", "overwrite",
		},
		"properties": map[string]any{
			"profile_revision":   map[string]any{"type": "string", "enum": profiles},
			"workspace_revision": boundedWorkspaceString(1, MaxIDLength),
			"working_scope":      map[string]any{"type": "string", "enum": scopes},
			"path":               boundedWorkspaceString(1, 4096),
			"content":            boundedWorkspaceString(0, MaxWorkspaceWriteBytes),
			"overwrite":          map[string]any{"type": "boolean"},
			"expected_sha256": map[string]any{
				"type": "string", "minLength": 64, "maxLength": 64, "pattern": "^[A-Fa-f0-9]{64}$",
			},
		},
	})
}

func workspacePatchInputSchema(profiles, scopes []string) json.RawMessage {
	return mustWorkspaceSchema(map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"profile_revision", "workspace_revision", "working_scope", "input"},
		"properties": map[string]any{
			"profile_revision":   map[string]any{"type": "string", "enum": profiles},
			"workspace_revision": boundedWorkspaceString(1, MaxIDLength),
			"working_scope":      map[string]any{"type": "string", "enum": scopes},
			"input":              boundedWorkspaceString(1, MaxWorkspacePatchBytes),
		},
	})
}

func cloneWorkspaceFileProfiles(profiles []FileProfileDescriptor) []FileProfileDescriptor {
	cloned := make([]FileProfileDescriptor, len(profiles))
	for index, profile := range profiles {
		cloned[index] = profile
		cloned[index].ReadableRoots = append([]string(nil), profile.ReadableRoots...)
		cloned[index].WritableRoots = append([]string(nil), profile.WritableRoots...)
	}
	return cloned
}

func cloneCommandModelContractPointer(contract *CommandModelContract) *CommandModelContract {
	if contract == nil {
		return nil
	}
	cloned := cloneCommandModelContract(*contract)
	return &cloned
}

func workspaceReadInputSchema(profiles, scopes []string) json.RawMessage {
	return mustWorkspaceSchema(map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"profile_revision", "workspace_revision", "working_scope", "path"},
		"properties": map[string]any{
			"profile_revision":   map[string]any{"type": "string", "enum": profiles},
			"workspace_revision": boundedWorkspaceString(1, MaxIDLength),
			"working_scope":      map[string]any{"type": "string", "enum": scopes},
			"path":               boundedWorkspaceString(1, 4096),
			"offset":             map[string]any{"type": "integer", "minimum": 0},
			"length": map[string]any{
				"type": "integer", "minimum": 1, "maximum": MaxWorkspaceReadBytes,
			},
			"start_line": map[string]any{"type": "integer", "minimum": 1},
			"max_lines":  map[string]any{"type": "integer", "minimum": 1, "maximum": 5000},
		},
	})
}

func workspaceSearchInputSchema(profiles, scopes []string) json.RawMessage {
	return mustWorkspaceSchema(map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"profile_revision", "workspace_revision", "working_scope", "pattern"},
		"properties": map[string]any{
			"profile_revision":   map[string]any{"type": "string", "enum": profiles},
			"workspace_revision": boundedWorkspaceString(1, MaxIDLength),
			"working_scope":      map[string]any{"type": "string", "enum": scopes},
			"pattern":            boundedWorkspaceString(1, 2048),
			"target": map[string]any{
				"type": "string", "enum": []string{"content", "files"},
			},
			"path":      boundedWorkspaceString(0, 4096),
			"file_glob": boundedWorkspaceString(0, 256),
			"output_mode": map[string]any{
				"type": "string", "enum": []string{"content", "files_only", "count"},
			},
			"context":         map[string]any{"type": "integer", "minimum": 0, "maximum": 10},
			"limit":           map[string]any{"type": "integer", "minimum": 1, "maximum": MaxWorkspaceSearchLimit},
			"include_ignored": map[string]any{"type": "boolean"},
		},
	})
}

func workspaceReadOutputSchema() json.RawMessage {
	return mustWorkspaceSchema(map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"path", "content", "size", "sha256", "truncated"},
		"properties": map[string]any{
			"path":      boundedWorkspaceString(1, 4096),
			"content":   boundedWorkspaceString(0, MaxWorkspaceReadBytes),
			"size":      map[string]any{"type": "integer", "minimum": 0},
			"sha256":    boundedWorkspaceString(64, 64),
			"truncated": map[string]any{"type": "boolean"},
		},
	})
}

func workspaceSearchOutputSchema() json.RawMessage {
	return mustWorkspaceSchema(map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"result", "matches", "files_visited", "truncated"},
		"properties": map[string]any{
			"result":        boundedWorkspaceString(0, MaxWorkspaceSearchResult),
			"matches":       map[string]any{"type": "integer", "minimum": 0},
			"files_visited": map[string]any{"type": "integer", "minimum": 0},
			"truncated":     map[string]any{"type": "boolean"},
		},
	})
}

func workspaceWriteOutputSchema() json.RawMessage {
	return mustWorkspaceSchema(map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"path", "action", "size", "sha256"},
		"properties": map[string]any{
			"path":   boundedWorkspaceString(1, 4096),
			"action": map[string]any{"type": "string", "enum": []string{"create", "replace"}},
			"size":   map[string]any{"type": "integer", "minimum": 0},
			"sha256": boundedWorkspaceString(64, 64),
		},
	})
}

func workspacePatchOutputSchema() json.RawMessage {
	entry := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"path", "action"},
		"properties": map[string]any{
			"path":   boundedWorkspaceString(1, 4096),
			"action": map[string]any{"type": "string", "enum": []string{"add", "update", "delete"}},
			"size":   map[string]any{"type": "integer", "minimum": 0},
			"sha256": boundedWorkspaceString(64, 64),
		},
	}
	return mustWorkspaceSchema(map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"state", "committed"},
		"properties": map[string]any{
			"state": map[string]any{"type": "string", "enum": []string{"completed", "partial"}},
			"committed": map[string]any{
				"type": "array", "maxItems": MaxWorkspacePatchFiles, "items": entry,
			},
			"code": boundedWorkspaceString(1, 64),
		},
	})
}

func boundedWorkspaceString(minimum, maximum int) map[string]any {
	return map[string]any{"type": "string", "minLength": minimum, "maxLength": maximum}
}

func mustWorkspaceSchema(schema map[string]any) json.RawMessage {
	encoded, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	return encoded
}
