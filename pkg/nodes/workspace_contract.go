package nodes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
)

const (
	WorkspaceCommandRead   = "workspace.read.v1"
	WorkspaceCommandSearch = "workspace.search.v1"

	MaxWorkspaceReadBytes    = 512 * 1024
	MaxWorkspaceSearchResult = 64 * 1024
	MaxWorkspaceSearchFiles  = 10000
	MaxWorkspaceSearchLimit  = 500
)

func IsWorkspaceCommand(command string) bool {
	return command == WorkspaceCommandRead || command == WorkspaceCommandSearch
}

// WorkspaceReadDescriptors returns node-internal command contracts. They stay
// model-unavailable in generic nodes discovery; the gateway P8a router binds
// profile, scope, and workspace revision before preparing an invocation.
func WorkspaceReadDescriptors(
	profileRevisions []string,
	workingScopes []string,
) ([]CommandDescriptor, error) {
	profileRevisions = append([]string(nil), profileRevisions...)
	workingScopes = append([]string(nil), workingScopes...)
	slices.Sort(profileRevisions)
	slices.Sort(workingScopes)
	profileRevisions = slices.Compact(profileRevisions)
	workingScopes = slices.Compact(workingScopes)
	if len(profileRevisions) == 0 || len(workingScopes) == 0 {
		return nil, fmt.Errorf("%w: workspace read authority is empty", ErrInvalidCapability)
	}
	authority, err := json.Marshal(struct {
		Profiles []string `json:"profiles"`
		Scopes   []string `json:"scopes"`
	}{Profiles: profileRevisions, Scopes: workingScopes})
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
		Constraints: CommandModelConstraints{
			ProfileAliases: append([]string(nil), profileRevisions...),
			WorkingScopes:  append([]string(nil), workingScopes...),
		},
		Guidance: []string{},
		Examples: []json.RawMessage{},
	}
	descriptors := []CommandDescriptor{
		{
			Name: WorkspaceCommandRead, InputSchema: workspaceReadInputSchema(profileRevisions, workingScopes),
			OutputSchema: workspaceReadOutputSchema(), Risk: RiskRead, SupportsCancel: true,
			ModelContract: cloneCommandModelContractPointer(contract),
		},
		{
			Name: WorkspaceCommandSearch, InputSchema: workspaceSearchInputSchema(profileRevisions, workingScopes),
			OutputSchema: workspaceSearchOutputSchema(), Risk: RiskRead, SupportsCancel: true,
			ModelContract: cloneCommandModelContractPointer(contract),
		},
	}
	for _, descriptor := range descriptors {
		if err := descriptor.Validate(); err != nil {
			return nil, err
		}
	}
	return descriptors, nil
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
