package companion

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

const directJobExecutor = "system_exec"

type JobProfileApproval struct {
	Start  string `json:"start,omitempty"`
	Read   string `json:"read,omitempty"`
	Cancel string `json:"cancel,omitempty"`
}

type JobProfilePolicy struct {
	Enabled                bool               `json:"enabled"`
	Revision               string             `json:"revision,omitempty"`
	Executor               string             `json:"executor,omitempty"`
	TimeoutSecondsMax      int                `json:"timeout_seconds_max,omitempty"`
	ConcurrentJobs         int                `json:"concurrent_jobs,omitempty"`
	StdoutBytesMax         int64              `json:"stdout_bytes_max,omitempty"`
	StderrBytesMax         int64              `json:"stderr_bytes_max,omitempty"`
	ArtifactCountMax       int                `json:"artifact_count_max,omitempty"`
	ArtifactBytesMax       int64              `json:"artifact_bytes_max,omitempty"`
	ArtifactsTotalBytesMax int64              `json:"artifacts_total_bytes_max,omitempty"`
	RetentionSeconds       int                `json:"retention_seconds,omitempty"`
	CancelGuarantee        JobCancelGuarantee `json:"cancel_guarantee,omitempty"`
	Approval               JobProfileApproval `json:"approval,omitempty"`

	alias           string
	limits          DirectJobLimits
	authorityDigest string
}

type JobProfiles map[string]JobProfilePolicy

func HasEnabledJobProfile(profiles JobProfiles) bool {
	for _, profile := range profiles {
		if profile.Enabled {
			return true
		}
	}
	return false
}

func normalizeJobProfiles(
	profiles JobProfiles,
	systemExec *SystemExecPolicy,
) (JobProfiles, error) {
	if len(profiles) > nodes.MaxJobProfiles {
		return nil, fmt.Errorf("node_job_profiles contains more than %d entries", nodes.MaxJobProfiles)
	}
	if len(profiles) == 0 {
		return nil, nil
	}
	normalized := make(JobProfiles, len(profiles))
	revisions := make(map[string]struct{}, len(profiles))
	for alias, configured := range profiles {
		if err := (nodes.Alias(alias)).Validate(); err != nil {
			return nil, fmt.Errorf("invalid node job profile alias %q", alias)
		}
		if !configured.Enabled {
			if !jobProfilePolicyEmpty(configured) {
				return nil, fmt.Errorf("disabled node job profile %q cannot configure authority", alias)
			}
			normalized[alias] = JobProfilePolicy{}
			continue
		}
		if systemExec == nil {
			return nil, fmt.Errorf("enabled node job profile %q requires system_exec", alias)
		}
		profile, err := normalizeJobProfilePolicy(alias, configured, *systemExec)
		if err != nil {
			return nil, err
		}
		if _, duplicate := revisions[profile.Revision]; duplicate {
			return nil, fmt.Errorf("duplicate node job profile revision %q", profile.Revision)
		}
		revisions[profile.Revision] = struct{}{}
		normalized[alias] = profile
	}
	return normalized, nil
}

func normalizeJobProfilePolicy(
	alias string,
	profile JobProfilePolicy,
	systemExec SystemExecPolicy,
) (JobProfilePolicy, error) {
	if err := nodes.ID(profile.Revision).Validate(); err != nil {
		return JobProfilePolicy{}, fmt.Errorf("node job profile %q has invalid revision", alias)
	}
	if profile.Executor == "" {
		profile.Executor = directJobExecutor
	}
	if profile.Executor != directJobExecutor {
		return JobProfilePolicy{}, fmt.Errorf("node job profile %q has unsupported executor", alias)
	}
	if profile.TimeoutSecondsMax < 0 || profile.TimeoutSecondsMax > nodes.MaxJobTimeoutSeconds ||
		profile.RetentionSeconds < 0 || profile.RetentionSeconds > nodes.MaxJobRetentionSeconds {
		return JobProfilePolicy{}, fmt.Errorf("node job profile %q has excessive duration", alias)
	}
	if len(systemExec.executableAliases) == 0 || len(systemExec.workingScopeAliases) == 0 {
		return JobProfilePolicy{}, fmt.Errorf("node job profile %q requires complete system_exec discovery", alias)
	}
	profile.Approval = normalizeJobProfileApproval(profile.Approval)
	if !validJobApproval(profile.Approval) {
		return JobProfilePolicy{}, fmt.Errorf("node job profile %q has invalid approval policy", alias)
	}
	limits, err := normalizeDirectJobLimits(DirectJobLimits{
		ConcurrentJobs:  profile.ConcurrentJobs,
		StdoutBytes:     profile.StdoutBytesMax,
		StderrBytes:     profile.StderrBytesMax,
		ArtifactCount:   profile.ArtifactCountMax,
		ArtifactBytes:   profile.ArtifactBytesMax,
		ArtifactsTotal:  profile.ArtifactsTotalBytesMax,
		Timeout:         time.Duration(profile.TimeoutSecondsMax) * time.Second,
		Retention:       time.Duration(profile.RetentionSeconds) * time.Second,
		CancelGuarantee: profile.CancelGuarantee,
	})
	if err != nil {
		return JobProfilePolicy{}, fmt.Errorf("node job profile %q: %w", alias, err)
	}
	profile.alias = alias
	profile.limits = limits
	profile.TimeoutSecondsMax = int(limits.Timeout / time.Second)
	profile.ConcurrentJobs = limits.ConcurrentJobs
	profile.StdoutBytesMax = limits.StdoutBytes
	profile.StderrBytesMax = limits.StderrBytes
	profile.ArtifactCountMax = limits.ArtifactCount
	profile.ArtifactBytesMax = limits.ArtifactBytes
	profile.ArtifactsTotalBytesMax = limits.ArtifactsTotal
	profile.RetentionSeconds = int(limits.Retention / time.Second)
	profile.CancelGuarantee = limits.CancelGuarantee
	profile.authorityDigest = jobProfileAuthorityDigest(profile, systemExec)
	return profile, nil
}

func normalizeJobProfileApproval(approval JobProfileApproval) JobProfileApproval {
	if approval.Start == "" {
		approval.Start = "required"
	}
	if approval.Read == "" {
		approval.Read = "none"
	}
	if approval.Cancel == "" {
		approval.Cancel = "required"
	}
	return approval
}

func validJobApproval(approval JobProfileApproval) bool {
	for _, value := range []string{approval.Start, approval.Read, approval.Cancel} {
		if value != "none" && value != "required" {
			return false
		}
	}
	return true
}

func jobProfilePolicyEmpty(profile JobProfilePolicy) bool {
	profile.Enabled = false
	return profile == (JobProfilePolicy{})
}

func jobProfileDescriptorsForPolicy(
	profiles JobProfiles,
	systemExec SystemExecPolicy,
) ([]nodes.JobProfileDescriptor, error) {
	aliases := make([]string, 0, len(profiles))
	for alias, profile := range profiles {
		if profile.Enabled {
			aliases = append(aliases, alias)
		}
	}
	slices.Sort(aliases)
	executableAliases := sortedSystemExecMapKeys(systemExec.executableAliases)
	workingScopes := sortedSystemExecMapKeys(systemExec.workingScopeAliases)
	environmentNames := []string{}
	if systemExec.Discovery != nil {
		environmentNames = append(environmentNames, systemExec.Discovery.EnvironmentNames...)
	}
	descriptors := make([]nodes.JobProfileDescriptor, 0, len(aliases))
	for _, alias := range aliases {
		profile := profiles[alias]
		if profile.alias != alias || profile.authorityDigest == "" {
			return nil, fmt.Errorf("node job profile %q is not normalized", alias)
		}
		descriptor := nodes.JobProfileDescriptor{
			Alias: alias, Revision: profile.Revision, Executor: profile.Executor,
			AuthorityDigest:        profile.authorityDigest,
			TimeoutSecondsMax:      profile.TimeoutSecondsMax,
			ConcurrentJobs:         profile.ConcurrentJobs,
			StdoutBytesMax:         profile.StdoutBytesMax,
			StderrBytesMax:         profile.StderrBytesMax,
			ArtifactCountMax:       profile.ArtifactCountMax,
			ArtifactBytesMax:       profile.ArtifactBytesMax,
			ArtifactsTotalBytesMax: profile.ArtifactsTotalBytesMax,
			RetentionSeconds:       profile.RetentionSeconds,
			CancelGuarantee:        string(profile.CancelGuarantee),
			ExecutableAliases:      append([]string(nil), executableAliases...),
			WorkingScopes:          append([]string(nil), workingScopes...),
			EnvironmentNames:       append([]string(nil), environmentNames...),
			Approval: nodes.JobProfileApproval{
				Start: profile.Approval.Start, Read: profile.Approval.Read, Cancel: profile.Approval.Cancel,
			},
		}
		if err := descriptor.Validate(); err != nil {
			return nil, err
		}
		descriptors = append(descriptors, descriptor)
	}
	return descriptors, nil
}

func jobProfileAuthorityDigest(profile JobProfilePolicy, systemExec SystemExecPolicy) string {
	payload := struct {
		Alias                  string             `json:"alias"`
		Revision               string             `json:"revision"`
		Executor               string             `json:"executor"`
		TimeoutSecondsMax      int                `json:"timeout_seconds_max"`
		ConcurrentJobs         int                `json:"concurrent_jobs"`
		StdoutBytesMax         int64              `json:"stdout_bytes_max"`
		StderrBytesMax         int64              `json:"stderr_bytes_max"`
		ArtifactCountMax       int                `json:"artifact_count_max"`
		ArtifactBytesMax       int64              `json:"artifact_bytes_max"`
		ArtifactsTotalBytesMax int64              `json:"artifacts_total_bytes_max"`
		RetentionSeconds       int                `json:"retention_seconds"`
		CancelGuarantee        JobCancelGuarantee `json:"cancel_guarantee"`
		Approval               JobProfileApproval `json:"approval"`
		SystemExecAuthority    string             `json:"system_exec_authority"`
	}{
		Alias: profile.alias, Revision: profile.Revision, Executor: profile.Executor,
		TimeoutSecondsMax: profile.TimeoutSecondsMax, ConcurrentJobs: profile.ConcurrentJobs,
		StdoutBytesMax: profile.StdoutBytesMax, StderrBytesMax: profile.StderrBytesMax,
		ArtifactCountMax: profile.ArtifactCountMax, ArtifactBytesMax: profile.ArtifactBytesMax,
		ArtifactsTotalBytesMax: profile.ArtifactsTotalBytesMax, RetentionSeconds: profile.RetentionSeconds,
		CancelGuarantee: profile.CancelGuarantee, Approval: profile.Approval,
		SystemExecAuthority: directJobSystemExecAuthorityDigest(systemExec),
	}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func directJobSystemExecAuthorityDigest(policy SystemExecPolicy) string {
	payload := struct {
		WorkingRoots []string `json:"working_roots"`
		Executables  []string `json:"executables"`
		Environment  []string `json:"environment"`
		Discovery    string   `json:"discovery"`
	}{
		WorkingRoots: append([]string(nil), policy.WorkingRoots...),
		Executables:  append([]string(nil), policy.Executables...),
		Environment:  append([]string(nil), policy.Environment...),
		Discovery:    systemExecDiscoveryAuthorityDigest(policy),
	}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func enabledJobProfileLimits(profiles JobProfiles) (time.Duration, error) {
	retention := time.Duration(0)
	for alias, profile := range profiles {
		if !profile.Enabled {
			continue
		}
		if profile.alias != alias || profile.limits.Retention == 0 {
			return 0, errors.New("node job profiles are not normalized")
		}
		retention = max(retention, profile.limits.Retention)
	}
	if retention == 0 {
		return 0, errors.New("node job profiles have no enabled entries")
	}
	return retention, nil
}
