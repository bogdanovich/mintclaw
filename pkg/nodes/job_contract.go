package nodes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
)

const (
	JobCommandStart     = "job.start.v1"
	JobCommandStatus    = "job.status.v1"
	JobCommandLogs      = "job.logs.v1"
	JobCommandArtifacts = "job.artifacts.v1"
	JobCommandCancel    = "job.cancel.v1"

	MaxJobProfiles           = 8
	MaxJobConcurrency        = 32
	MaxJobLogBytes           = 64 * 1024 * 1024
	MaxJobLogChunkBytes      = 64 * 1024
	MaxJobArtifactCount      = 16
	MaxJobArtifactBytes      = 512 * 1024 * 1024
	MaxJobArtifactsTotal     = 1024 * 1024 * 1024
	MaxJobRetentionSeconds   = 30 * 24 * 60 * 60
	MaxJobTimeoutSeconds     = 24 * 60 * 60
	maxJobArtifactNameLength = 64
	maxJobArtifactPathLength = 4096

	// InternalJobArtifactDownloadCommand identifies the gateway-retained plan
	// behind nodes_download for an immutable job artifact. It is never
	// advertised as a companion invocation command or exposed as a model tool.
	InternalJobArtifactDownloadCommand = "job.artifact.download.v1"
	JobArtifactTransferSourceKind      = "node_job_artifact"
)

type JobProfileApproval struct {
	Start  string `json:"start"`
	Read   string `json:"read"`
	Cancel string `json:"cancel"`
}

type JobProfileDescriptor struct {
	Alias                  string             `json:"alias"`
	Revision               string             `json:"revision"`
	Executor               string             `json:"executor"`
	AuthorityDigest        string             `json:"authority_digest"`
	TimeoutSecondsMax      int                `json:"timeout_seconds_max"`
	ConcurrentJobs         int                `json:"concurrent_jobs"`
	StdoutBytesMax         int64              `json:"stdout_bytes_max"`
	StderrBytesMax         int64              `json:"stderr_bytes_max"`
	ArtifactCountMax       int                `json:"artifact_count_max"`
	ArtifactBytesMax       int64              `json:"artifact_bytes_max"`
	ArtifactsTotalBytesMax int64              `json:"artifacts_total_bytes_max"`
	RetentionSeconds       int                `json:"retention_seconds"`
	CancelGuarantee        string             `json:"cancel_guarantee"`
	ExecutableAliases      []string           `json:"executable_aliases"`
	WorkingScopes          []string           `json:"working_scopes"`
	EnvironmentNames       []string           `json:"environment_names"`
	Approval               JobProfileApproval `json:"approval"`
}

func (profile JobProfileDescriptor) Validate() error {
	if err := (Alias(profile.Alias)).Validate(); err != nil ||
		!validInvocationIdentifier(profile.Revision) ||
		profile.Executor != "system_exec" ||
		!validSHA256Digest(profile.AuthorityDigest) ||
		profile.TimeoutSecondsMax < 1 || profile.TimeoutSecondsMax > MaxJobTimeoutSeconds ||
		profile.ConcurrentJobs < 1 || profile.ConcurrentJobs > MaxJobConcurrency ||
		profile.StdoutBytesMax < 1 || profile.StdoutBytesMax > MaxJobLogBytes ||
		profile.StderrBytesMax < 1 || profile.StderrBytesMax > MaxJobLogBytes ||
		profile.ArtifactCountMax < 1 || profile.ArtifactCountMax > MaxJobArtifactCount ||
		profile.ArtifactBytesMax < 1 || profile.ArtifactBytesMax > MaxJobArtifactBytes ||
		profile.ArtifactsTotalBytesMax < profile.ArtifactBytesMax ||
		profile.ArtifactsTotalBytesMax > MaxJobArtifactsTotal ||
		profile.RetentionSeconds < 60 || profile.RetentionSeconds > MaxJobRetentionSeconds ||
		(profile.CancelGuarantee != "direct_process" && profile.CancelGuarantee != "process_group") ||
		!validJobProfileApproval(profile.Approval) {
		return fmt.Errorf("%w: malformed job profile descriptor", ErrInvalidCapability)
	}
	constraints := CommandModelConstraints{
		ExecutableAliases: profile.ExecutableAliases,
		WorkingScopes:     profile.WorkingScopes,
		EnvironmentNames:  profile.EnvironmentNames,
	}
	if err := validateModelConstraintNames(constraints); err != nil ||
		len(profile.ExecutableAliases) == 0 || len(profile.WorkingScopes) == 0 {
		return fmt.Errorf("%w: malformed job profile discovery", ErrInvalidCapability)
	}
	return nil
}

func validJobProfileApproval(approval JobProfileApproval) bool {
	for _, value := range []string{approval.Start, approval.Read, approval.Cancel} {
		if value != "none" && value != "required" {
			return false
		}
	}
	return true
}

func IsJobCommand(name string) bool {
	switch name {
	case JobCommandStart, JobCommandStatus, JobCommandLogs, JobCommandArtifacts, JobCommandCancel:
		return true
	default:
		return false
	}
}

func (descriptor CommandDescriptor) validateJobProfiles() error {
	if len(descriptor.JobProfiles) == 0 {
		if IsJobCommand(descriptor.Name) {
			return fmt.Errorf("%w: job command lacks job profiles", ErrInvalidCapability)
		}
		return nil
	}
	if !IsJobCommand(descriptor.Name) {
		return fmt.Errorf("%w: non-job command carries job profiles", ErrInvalidCapability)
	}
	if err := validateJobProfileSet(descriptor.JobProfiles); err != nil {
		return err
	}
	if descriptor.ModelContract == nil || descriptor.SupportsCancel || descriptor.SupportsProgress {
		return fmt.Errorf("%w: malformed job command behavior", ErrInvalidCapability)
	}
	wantRisk := RiskRead
	if descriptor.Name == JobCommandStart || descriptor.Name == JobCommandCancel {
		wantRisk = RiskWrite
	}
	if descriptor.Risk != wantRisk {
		return fmt.Errorf("%w: malformed job command risk", ErrInvalidCapability)
	}
	wantInput, err := canonicalJSON(JobCommandInputSchema(descriptor.Name, descriptor.JobProfiles))
	if err != nil {
		return err
	}
	actualInput, err := canonicalJSON(descriptor.InputSchema)
	if err != nil || !slices.Equal(actualInput, wantInput) {
		return fmt.Errorf("%w: job command input schema does not match profiles", ErrInvalidCapability)
	}
	wantOutput, err := canonicalJSON(JobCommandOutputSchema(descriptor.Name))
	if err != nil {
		return err
	}
	actualOutput, err := canonicalJSON(descriptor.OutputSchema)
	if err != nil || !slices.Equal(actualOutput, wantOutput) {
		return fmt.Errorf("%w: job command output schema is not canonical", ErrInvalidCapability)
	}
	wantConstraints := jobProfileConstraints(descriptor.JobProfiles)
	actualConstraints := descriptor.ModelContract.Constraints
	projected := len(descriptor.JobProfiles) == 1 && len(actualConstraints.ProfileAliases) == 0
	if projected {
		wantConstraints.ProfileAliases = nil
	}
	if !slices.Equal(actualConstraints.ExecutableAliases, wantConstraints.ExecutableAliases) ||
		!slices.Equal(actualConstraints.ProfileAliases, wantConstraints.ProfileAliases) ||
		!slices.Equal(actualConstraints.WorkingScopes, wantConstraints.WorkingScopes) ||
		!slices.Equal(actualConstraints.EnvironmentNames, wantConstraints.EnvironmentNames) {
		return fmt.Errorf("%w: job model constraints do not match profiles", ErrInvalidCapability)
	}
	return nil
}

func CloneJobProfileDescriptors(profiles []JobProfileDescriptor) []JobProfileDescriptor {
	cloned := make([]JobProfileDescriptor, len(profiles))
	for index, profile := range profiles {
		cloned[index] = profile
		cloned[index].ExecutableAliases = append([]string(nil), profile.ExecutableAliases...)
		cloned[index].WorkingScopes = append([]string(nil), profile.WorkingScopes...)
		cloned[index].EnvironmentNames = append([]string(nil), profile.EnvironmentNames...)
	}
	return cloned
}

func JobCommandDescriptors(profiles []JobProfileDescriptor) ([]CommandDescriptor, error) {
	profiles = CloneJobProfileDescriptors(profiles)
	if err := validateJobProfileSet(profiles); err != nil {
		return nil, err
	}
	authority, err := json.Marshal(profiles)
	if err != nil {
		return nil, fmt.Errorf("%w: encode job profile authority", ErrInvalidCapability)
	}
	digest := sha256.Sum256(authority)
	authorityDigest := hex.EncodeToString(digest[:])
	constraints := jobProfileConstraints(profiles)
	descriptors := make([]CommandDescriptor, 0, 5)
	for _, command := range []string{
		JobCommandStart,
		JobCommandStatus,
		JobCommandLogs,
		JobCommandArtifacts,
		JobCommandCancel,
	} {
		risk := RiskRead
		approval := ""
		if command == JobCommandStart || command == JobCommandCancel {
			risk = RiskWrite
			approval = "each_command"
		}
		descriptor := CommandDescriptor{
			Name:         command,
			InputSchema:  JobCommandInputSchema(command, profiles),
			OutputSchema: JobCommandOutputSchema(command),
			Risk:         risk,
			ModelContract: &CommandModelContract{
				Availability:      ModelUnavailable,
				TimeoutSecondsMax: MaxInvocationTimeout,
				OutputBytesMax:    MaxInvocationOutput,
				ResultKind:        "json",
				AuthorityDigest:   authorityDigest,
				ApprovalMode:      approval,
				Constraints:       constraints,
				Guidance:          []string{},
				Examples:          []json.RawMessage{},
			},
			JobProfiles: CloneJobProfileDescriptors(profiles),
		}
		if err := descriptor.Validate(); err != nil {
			return nil, err
		}
		descriptors = append(descriptors, descriptor)
	}
	return descriptors, nil
}

func ProjectJobDescriptorForProfile(
	descriptor CommandDescriptor,
	profileAlias string,
) (CommandDescriptor, bool) {
	if len(descriptor.JobProfiles) == 0 {
		return descriptor, true
	}
	if !IsJobCommand(descriptor.Name) || profileAlias == "" || descriptor.ModelContract == nil {
		return CommandDescriptor{}, false
	}
	for _, profile := range descriptor.JobProfiles {
		if profile.Alias != profileAlias {
			continue
		}
		descriptor.JobProfiles = CloneJobProfileDescriptors([]JobProfileDescriptor{profile})
		descriptor.InputSchema = JobCommandInputSchema(descriptor.Name, descriptor.JobProfiles)
		contract := cloneCommandModelContract(*descriptor.ModelContract)
		contract.Availability = ModelAvailable
		contract.AuthorityDigest = profile.AuthorityDigest
		contract.Constraints = jobProfileConstraints(descriptor.JobProfiles)
		contract.Constraints.ProfileAliases = nil
		switch descriptor.Name {
		case JobCommandStart:
			if profile.Approval.Start == "required" {
				contract.ApprovalMode = "each_command"
			} else {
				contract.ApprovalMode = ""
			}
		case JobCommandCancel:
			if profile.Approval.Cancel == "required" {
				contract.ApprovalMode = "each_command"
			} else {
				contract.ApprovalMode = ""
			}
		default:
			if profile.Approval.Read == "required" {
				contract.ApprovalMode = "each_command"
			} else {
				contract.ApprovalMode = ""
			}
		}
		descriptor.ModelContract = &contract
		if err := descriptor.Validate(); err != nil {
			return CommandDescriptor{}, false
		}
		return descriptor, true
	}
	return CommandDescriptor{}, false
}

func JobCommandInputSchema(command string, profiles []JobProfileDescriptor) json.RawMessage {
	switch command {
	case JobCommandStart:
		constraints := jobProfileConstraints(profiles)
		jobTimeout := maxJobProfileTimeout(profiles)
		contract := CommandModelContract{
			TimeoutSecondsMax: min(jobTimeout, MaxInvocationTimeout),
			Constraints:       constraints,
		}
		base, err := SystemExecModelInputSchema(contract)
		if err != nil {
			return json.RawMessage("false")
		}
		var schema map[string]any
		if schemaErr := json.Unmarshal(base, &schema); schemaErr != nil {
			return json.RawMessage("false")
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			return json.RawMessage("false")
		}
		timeout, ok := properties["timeout_seconds"].(map[string]any)
		if !ok {
			return json.RawMessage("false")
		}
		timeout["maximum"] = jobTimeout
		properties["artifacts"] = map[string]any{
			"type":     "array",
			"maxItems": maxJobProfileArtifactCount(profiles),
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"name", "path"},
				"properties": map[string]any{
					"name": map[string]any{
						"type": "string", "minLength": 1, "maxLength": maxJobArtifactNameLength,
					},
					"path": map[string]any{
						"type": "string", "minLength": 1, "maxLength": maxJobArtifactPathLength,
					},
				},
			},
		}
		encoded, err := json.Marshal(schema)
		if err != nil {
			return json.RawMessage("false")
		}
		return encoded
	case JobCommandStatus, JobCommandArtifacts, JobCommandCancel:
		return mustJobSchema(map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"job_id"},
			"properties": map[string]any{
				"job_id": jobIDSchema(),
			},
		})
	case JobCommandLogs:
		return mustJobSchema(map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"job_id", "stream", "cursor", "limit_bytes"},
			"properties": map[string]any{
				"job_id":      jobIDSchema(),
				"stream":      map[string]any{"type": "string", "enum": []string{"stderr", "stdout"}},
				"cursor":      map[string]any{"type": "integer", "minimum": 0, "maximum": MaxJobLogBytes},
				"limit_bytes": map[string]any{"type": "integer", "minimum": 1, "maximum": MaxJobLogChunkBytes},
			},
		})
	default:
		return json.RawMessage("false")
	}
}

func JobCommandOutputSchema(command string) json.RawMessage {
	state := map[string]any{"type": "string", "enum": []string{
		"accepted", "launch_attempted", "running", "succeeded", "failed",
		"failed_before_launch", "cancel_requested", "canceled", "cancel_unknown",
		"timed_out", "unknown",
	}}
	jobID := jobIDSchema()
	switch command {
	case JobCommandStart:
		return mustJobSchema(map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"job_id", "state", "created_at", "started_at", "timeout_at", "cancel_guarantee"},
			"properties": map[string]any{
				"job_id": jobID, "state": state,
				"created_at": map[string]any{"type": "integer", "minimum": 0},
				"started_at": map[string]any{"type": "integer", "minimum": 0},
				"timeout_at": map[string]any{"type": "integer", "minimum": 0},
				"cancel_guarantee": map[string]any{
					"type": "string", "enum": []string{"direct_process", "process_group"},
				},
			},
		})
	case JobCommandStatus:
		return mustJobSchema(jobStatusSchema(jobID, state))
	case JobCommandLogs:
		return mustJobSchema(map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"job_id", "stream", "data", "next_cursor", "available_bytes", "truncated", "state"},
			"properties": map[string]any{
				"job_id": jobID, "stream": map[string]any{"type": "string", "enum": []string{"stderr", "stdout"}},
				"data":            map[string]any{"type": "string"},
				"next_cursor":     map[string]any{"type": "integer", "minimum": 0},
				"available_bytes": map[string]any{"type": "integer", "minimum": 0},
				"truncated":       map[string]any{"type": "boolean"}, "state": state,
			},
		})
	case JobCommandArtifacts:
		return mustJobSchema(map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"job_id", "state", "artifacts"},
			"properties": map[string]any{
				"job_id": jobID, "state": state,
				"artifacts": map[string]any{
					"type": "array", "maxItems": MaxJobArtifactCount,
					"items": map[string]any{
						"type": "object", "additionalProperties": false,
						"required": []string{"name", "state", "artifact_ref", "size", "sha256", "failure_code"},
						"properties": map[string]any{
							"name": map[string]any{"type": "string"},
							"state": map[string]any{
								"type": "string",
								"enum": []string{"pending", "available", "missing", "failed"},
							},
							"artifact_ref": map[string]any{"type": "string"},
							"size":         map[string]any{"type": "integer", "minimum": 0},
							"sha256":       map[string]any{"type": "string"},
							"failure_code": map[string]any{"type": "string"},
						},
					},
				},
			},
		})
	case JobCommandCancel:
		return mustJobSchema(map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{
				"job_id", "state", "disposition", "cancel_requested_at", "cancellation_signal", "failure_code",
			},
			"properties": map[string]any{
				"job_id": jobID, "state": state,
				"disposition": map[string]any{
					"type": "string",
					"enum": []string{"cancel_requested", "canceled", "cancel_unknown", "already_terminal"},
				},
				"cancel_requested_at": map[string]any{"type": "integer", "minimum": 0},
				"cancellation_signal": map[string]any{"type": "boolean"},
				"failure_code":        map[string]any{"type": "string"},
			},
		})
	default:
		return json.RawMessage("false")
	}
}

func validateJobProfileSet(profiles []JobProfileDescriptor) error {
	if len(profiles) == 0 || len(profiles) > MaxJobProfiles {
		return fmt.Errorf("%w: job profile count is outside bounds", ErrInvalidCapability)
	}
	aliases := make(map[string]struct{}, len(profiles))
	revisions := make(map[string]struct{}, len(profiles))
	prior := ""
	for _, profile := range profiles {
		if err := profile.Validate(); err != nil {
			return err
		}
		if prior != "" && profile.Alias <= prior {
			return fmt.Errorf("%w: job profiles are not sorted", ErrInvalidCapability)
		}
		if _, duplicate := aliases[profile.Alias]; duplicate {
			return fmt.Errorf("%w: duplicate job profile alias", ErrInvalidCapability)
		}
		if _, duplicate := revisions[profile.Revision]; duplicate {
			return fmt.Errorf("%w: duplicate job profile revision", ErrInvalidCapability)
		}
		aliases[profile.Alias] = struct{}{}
		revisions[profile.Revision] = struct{}{}
		prior = profile.Alias
	}
	return nil
}

func jobProfileConstraints(profiles []JobProfileDescriptor) CommandModelConstraints {
	aliases := make(map[string]struct{})
	executables := make(map[string]struct{})
	scopes := make(map[string]struct{})
	environment := make(map[string]struct{})
	for _, profile := range profiles {
		aliases[profile.Alias] = struct{}{}
		for _, value := range profile.ExecutableAliases {
			executables[value] = struct{}{}
		}
		for _, value := range profile.WorkingScopes {
			scopes[value] = struct{}{}
		}
		for _, value := range profile.EnvironmentNames {
			environment[value] = struct{}{}
		}
	}
	return CommandModelConstraints{
		ExecutableAliases: sortedJobKeys(executables),
		ProfileAliases:    sortedJobKeys(aliases),
		WorkingScopes:     sortedJobKeys(scopes),
		EnvironmentNames:  sortedJobKeys(environment),
	}
}

func sortedJobKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func maxJobProfileTimeout(profiles []JobProfileDescriptor) int {
	value := 0
	for _, profile := range profiles {
		value = max(value, profile.TimeoutSecondsMax)
	}
	return value
}

func maxJobProfileArtifactCount(profiles []JobProfileDescriptor) int {
	value := 0
	for _, profile := range profiles {
		value = max(value, profile.ArtifactCountMax)
	}
	return value
}

func jobIDSchema() map[string]any {
	return map[string]any{
		"type": "string", "minLength": 36, "maxLength": 36, "pattern": `^job_[0-9a-f]{32}$`,
	}
}

func jobStatusSchema(jobID, state map[string]any) map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{
			"job_id", "state", "created_at", "updated_at", "launch_attempted_at", "started_at", "timeout_at",
			"completed_at", "exit_code", "exit_code_known", "signal", "failure_code", "cancel_requested_at",
			"cancellation_signal",
			"stdout_bytes", "stderr_bytes", "stdout_truncated", "stderr_truncated", "artifact_count",
		},
		"properties": map[string]any{
			"job_id": jobID, "state": state,
			"created_at":          map[string]any{"type": "integer", "minimum": 0},
			"updated_at":          map[string]any{"type": "integer", "minimum": 0},
			"launch_attempted_at": map[string]any{"type": "integer", "minimum": 0},
			"started_at":          map[string]any{"type": "integer", "minimum": 0},
			"timeout_at":          map[string]any{"type": "integer", "minimum": 0},
			"completed_at":        map[string]any{"type": "integer", "minimum": 0},
			"exit_code":           map[string]any{"type": "integer"},
			"exit_code_known":     map[string]any{"type": "boolean"},
			"signal":              map[string]any{"type": "string"},
			"failure_code":        map[string]any{"type": "string"},
			"cancel_requested_at": map[string]any{"type": "integer", "minimum": 0},
			"cancellation_signal": map[string]any{"type": "boolean"},
			"stdout_bytes":        map[string]any{"type": "integer", "minimum": 0},
			"stderr_bytes":        map[string]any{"type": "integer", "minimum": 0},
			"stdout_truncated":    map[string]any{"type": "boolean"},
			"stderr_truncated":    map[string]any{"type": "boolean"},
			"artifact_count":      map[string]any{"type": "integer", "minimum": 0, "maximum": MaxJobArtifactCount},
		},
	}
}

func mustJobSchema(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage("false")
	}
	return encoded
}
