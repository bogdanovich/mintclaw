package companion

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

const (
	DefaultJobRecordLimit        = 256
	DefaultJobIndexBytes         = 16 * 1024 * 1024
	DefaultJobStorePayloadBytes  = 4 * 1024 * 1024 * 1024
	DefaultJobConcurrency        = 2
	DefaultJobLogBytes           = 8 * 1024 * 1024
	DefaultJobArtifactCount      = 8
	DefaultJobArtifactBytes      = 256 * 1024 * 1024
	DefaultJobRetention          = 24 * time.Hour
	MaxJobRecordLimit            = 1024
	MaxJobStorePayloadBytes      = 64 * 1024 * 1024 * 1024
	MaxJobConcurrency            = nodes.MaxJobConcurrency
	MaxJobLogBytes               = nodes.MaxJobLogBytes
	MaxJobArtifactCount          = nodes.MaxJobArtifactCount
	MaxJobArtifactBytes          = nodes.MaxJobArtifactBytes
	MaxJobArtifactsTotalBytes    = nodes.MaxJobArtifactsTotal
	MaxJobRetention              = 30 * 24 * time.Hour
	MaxJobTimeout                = time.Duration(nodes.MaxJobTimeoutSeconds) * time.Second
	maxJobArtifactNameLength     = 64
	maxJobArtifactRelativeLength = 4096
)

var (
	ErrJobConflict            = errors.New("node job conflicts with durable state")
	ErrJobNotFound            = errors.New("node job not found")
	ErrJobStoreFull           = errors.New("node job store is full")
	ErrJobBusy                = errors.New("node job concurrency limit reached")
	ErrJobPlatformUnsupported = errors.New("durable node jobs are unsupported on this platform")
)

// JobState is the durable, node-local observation of one process. Unknown is
// intentionally terminal for P5a: a successor never relaunches or signals a
// process from a persisted PID.
type JobState string

const (
	JobAccepted           JobState = "accepted"
	JobLaunchAttempted    JobState = "launch_attempted"
	JobRunning            JobState = "running"
	JobSucceeded          JobState = "succeeded"
	JobFailed             JobState = "failed"
	JobFailedBeforeLaunch JobState = "failed_before_launch"
	JobCancelRequested    JobState = "cancel_requested"
	JobCanceled           JobState = "canceled"
	JobCancelUnknown      JobState = "cancel_unknown"
	JobTimedOut           JobState = "timed_out"
	JobUnknown            JobState = "unknown"
)

func (state JobState) terminal() bool {
	switch state {
	case JobSucceeded, JobFailed, JobFailedBeforeLaunch, JobCanceled,
		JobCancelUnknown, JobTimedOut, JobUnknown:
		return true
	default:
		return false
	}
}

func (state JobState) valid() bool {
	switch state {
	case JobAccepted, JobLaunchAttempted, JobRunning, JobSucceeded, JobFailed,
		JobFailedBeforeLaunch, JobCancelRequested, JobCanceled, JobCancelUnknown,
		JobTimedOut, JobUnknown:
		return true
	default:
		return false
	}
}

type JobCancelGuarantee string

const (
	JobCancelDirectProcess JobCancelGuarantee = "direct_process"
	JobCancelProcessGroup  JobCancelGuarantee = "process_group"
)

func (guarantee JobCancelGuarantee) valid() bool {
	return guarantee == JobCancelDirectProcess || guarantee == JobCancelProcessGroup
}

// JobOwner is the companion-visible part of the durable authority binding.
// Workspace and execution identity remain enforced by the gateway store.
type JobOwner struct {
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id"`
	ActorID   string `json:"actor_id"`
}

func (owner JobOwner) validate() error {
	for _, value := range []string{owner.AgentID, owner.SessionID, owner.ActorID} {
		if err := nodes.ID(value).Validate(); err != nil {
			return fmt.Errorf("invalid node job owner: %w", err)
		}
	}
	return nil
}

type JobLogRecord struct {
	Bytes     int64 `json:"bytes"`
	Truncated bool  `json:"truncated"`
}

type JobArtifactDeclaration struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func (declaration JobArtifactDeclaration) validate() error {
	if err := (nodes.Alias(declaration.Name)).Validate(); err != nil ||
		len(declaration.Name) > maxJobArtifactNameLength {
		return errors.New("invalid node job artifact name")
	}
	if declaration.Path == "" || len(declaration.Path) > maxJobArtifactRelativeLength ||
		!utf8.ValidString(declaration.Path) || strings.ContainsRune(declaration.Path, 0) ||
		filepath.IsAbs(declaration.Path) || filepath.Clean(declaration.Path) != declaration.Path ||
		declaration.Path == "." || declaration.Path == ".." ||
		strings.HasPrefix(declaration.Path, ".."+string(filepath.Separator)) {
		return errors.New("invalid node job artifact path")
	}
	return nil
}

type JobArtifactState string

const (
	JobArtifactPending   JobArtifactState = "pending"
	JobArtifactAvailable JobArtifactState = "available"
	JobArtifactMissing   JobArtifactState = "missing"
	JobArtifactFailed    JobArtifactState = "failed"
)

func (state JobArtifactState) valid() bool {
	return state == JobArtifactPending || state == JobArtifactAvailable ||
		state == JobArtifactMissing || state == JobArtifactFailed
}

// JobArtifactRecord never contains the source path. FileName is a private
// store-local name and ArtifactRef is the only value later exposed to callers.
type JobArtifactRecord struct {
	Name        string           `json:"name"`
	State       JobArtifactState `json:"state"`
	ArtifactRef string           `json:"artifact_ref,omitempty"`
	FileName    string           `json:"file_name,omitempty"`
	Size        int64            `json:"size,omitempty"`
	SHA256      string           `json:"sha256,omitempty"`
	FailureCode string           `json:"failure_code,omitempty"`
}

// JobRecord is intentionally redacted. Raw argv, cwd, environment, source
// paths, log content, PID, and host connection details are never persisted in
// the index.
type JobRecord struct {
	JobID               string              `json:"job_id"`
	StartInvocationID   string              `json:"start_invocation_id"`
	StartIdempotencyKey string              `json:"start_idempotency_key"`
	PlanHash            string              `json:"plan_hash"`
	ProfileAlias        string              `json:"profile_alias"`
	ProfileRevision     string              `json:"profile_revision"`
	RetentionSeconds    int                 `json:"retention_seconds"`
	Owner               JobOwner            `json:"owner"`
	State               JobState            `json:"state"`
	CancelGuarantee     JobCancelGuarantee  `json:"cancel_guarantee"`
	Stdout              JobLogRecord        `json:"stdout"`
	Stderr              JobLogRecord        `json:"stderr"`
	Artifacts           []JobArtifactRecord `json:"artifacts,omitempty"`
	CreatedAt           int64               `json:"created_at"`
	UpdatedAt           int64               `json:"updated_at"`
	LaunchAttemptedAt   int64               `json:"launch_attempted_at,omitempty"`
	StartedAt           int64               `json:"started_at,omitempty"`
	TimeoutAt           int64               `json:"timeout_at,omitempty"`
	CompletedAt         int64               `json:"completed_at,omitempty"`
	ExitCode            *int                `json:"exit_code,omitempty"`
	Signal              string              `json:"signal,omitempty"`
	FailureCode         string              `json:"failure_code,omitempty"`
	CancelRequestedAt   int64               `json:"cancel_requested_at,omitempty"`
	CancellationSignal  bool                `json:"cancellation_signal,omitempty"`
}

func (record JobRecord) validate() error {
	for _, value := range []string{
		record.JobID,
		record.StartInvocationID,
		record.StartIdempotencyKey,
		record.ProfileRevision,
	} {
		if err := nodes.ID(value).Validate(); err != nil {
			return fmt.Errorf("invalid node job record identity: %w", err)
		}
	}
	if err := (nodes.Alias(record.ProfileAlias)).Validate(); err != nil ||
		record.RetentionSeconds < 60 || record.RetentionSeconds > nodes.MaxJobRetentionSeconds {
		return errors.New("invalid node job profile binding")
	}
	if digest, err := hex.DecodeString(record.PlanHash); err != nil || len(digest) != 32 ||
		record.CreatedAt <= 0 || record.UpdatedAt < record.CreatedAt ||
		!record.State.valid() || !record.CancelGuarantee.valid() {
		return errors.New("invalid node job record")
	}
	if err := record.Owner.validate(); err != nil {
		return err
	}
	if record.Stdout.Bytes < 0 || record.Stdout.Bytes > MaxJobLogBytes ||
		record.Stderr.Bytes < 0 || record.Stderr.Bytes > MaxJobLogBytes ||
		len(record.FailureCode) > nodes.MaxIDLength || len(record.Signal) > nodes.MaxIDLength {
		return errors.New("invalid node job result metadata")
	}
	if len(record.Artifacts) > MaxJobArtifactCount {
		return errors.New("node job record has too many artifacts")
	}
	names := make(map[string]struct{}, len(record.Artifacts))
	for _, artifact := range record.Artifacts {
		if err := (nodes.Alias(artifact.Name)).Validate(); err != nil || !artifact.State.valid() {
			return errors.New("invalid node job artifact record")
		}
		if _, duplicate := names[artifact.Name]; duplicate {
			return errors.New("duplicate node job artifact record")
		}
		names[artifact.Name] = struct{}{}
		if artifact.State == JobArtifactAvailable {
			if err := nodes.ID(artifact.ArtifactRef).Validate(); err != nil ||
				validateJobStoreName(artifact.FileName) != nil ||
				artifact.Size < 0 || artifact.Size > MaxJobArtifactBytes {
				return errors.New("invalid available node job artifact")
			}
			digest, digestErr := hex.DecodeString(artifact.SHA256)
			if digestErr != nil || len(digest) != sha256.Size {
				return errors.New("invalid available node job artifact")
			}
		} else if artifact.ArtifactRef != "" || artifact.FileName != "" || artifact.Size != 0 ||
			artifact.SHA256 != "" {
			return errors.New("non-available node job artifact contains snapshot authority")
		}
	}
	if record.State == JobLaunchAttempted && record.LaunchAttemptedAt == 0 ||
		record.State == JobRunning && (record.LaunchAttemptedAt == 0 || record.StartedAt == 0) ||
		record.State == JobCancelRequested && record.CancelRequestedAt == 0 ||
		record.State.terminal() && record.CompletedAt == 0 {
		return errors.New("node job record lifecycle timestamps are incomplete")
	}
	if record.LaunchAttemptedAt > record.UpdatedAt || record.StartedAt > record.UpdatedAt ||
		record.CancelRequestedAt > record.UpdatedAt || record.CompletedAt > record.UpdatedAt ||
		record.TimeoutAt != 0 && record.TimeoutAt <= record.StartedAt {
		return errors.New("node job record lifecycle timestamps are inconsistent")
	}
	return nil
}

type DirectJobLimits struct {
	ConcurrentJobs  int
	StdoutBytes     int64
	StderrBytes     int64
	ArtifactCount   int
	ArtifactBytes   int64
	ArtifactsTotal  int64
	Timeout         time.Duration
	Retention       time.Duration
	CancelGuarantee JobCancelGuarantee
}

type JobStoreLimits struct {
	Records      int
	IndexBytes   int
	PayloadBytes int64
	Retention    time.Duration
}

func normalizeJobStoreLimits(limits JobStoreLimits) (JobStoreLimits, error) {
	if limits.Records == 0 {
		limits.Records = DefaultJobRecordLimit
	}
	if limits.IndexBytes == 0 {
		limits.IndexBytes = DefaultJobIndexBytes
	}
	if limits.PayloadBytes == 0 {
		limits.PayloadBytes = DefaultJobStorePayloadBytes
	}
	if limits.Retention == 0 {
		limits.Retention = DefaultJobRetention
	}
	if limits.Records < 1 || limits.Records > MaxJobRecordLimit ||
		limits.IndexBytes < 1024 || limits.IndexBytes > DefaultJobIndexBytes ||
		limits.PayloadBytes < 1024 || limits.PayloadBytes > MaxJobStorePayloadBytes ||
		limits.Retention < time.Minute || limits.Retention > MaxJobRetention {
		return JobStoreLimits{}, errors.New("node job store limits are outside hard bounds")
	}
	return limits, nil
}

func normalizeDirectJobLimits(limits DirectJobLimits) (DirectJobLimits, error) {
	if limits.ConcurrentJobs == 0 {
		limits.ConcurrentJobs = DefaultJobConcurrency
	}
	if limits.StdoutBytes == 0 {
		limits.StdoutBytes = DefaultJobLogBytes
	}
	if limits.StderrBytes == 0 {
		limits.StderrBytes = DefaultJobLogBytes
	}
	if limits.ArtifactCount == 0 {
		limits.ArtifactCount = DefaultJobArtifactCount
	}
	if limits.ArtifactBytes == 0 {
		limits.ArtifactBytes = DefaultJobArtifactBytes
	}
	if limits.ArtifactsTotal == 0 {
		limits.ArtifactsTotal = DefaultJobArtifactBytes
	}
	if limits.Timeout == 0 {
		limits.Timeout = time.Hour
	}
	if limits.Retention == 0 {
		limits.Retention = DefaultJobRetention
	}
	if limits.CancelGuarantee == "" {
		limits.CancelGuarantee = JobCancelProcessGroup
	}
	if limits.ConcurrentJobs < 1 || limits.ConcurrentJobs > MaxJobConcurrency ||
		limits.StdoutBytes < 1 || limits.StdoutBytes > MaxJobLogBytes ||
		limits.StderrBytes < 1 || limits.StderrBytes > MaxJobLogBytes ||
		limits.ArtifactCount < 1 || limits.ArtifactCount > MaxJobArtifactCount ||
		limits.ArtifactBytes < 1 || limits.ArtifactBytes > MaxJobArtifactBytes ||
		limits.ArtifactsTotal < limits.ArtifactBytes ||
		limits.ArtifactsTotal > MaxJobArtifactsTotalBytes ||
		limits.Timeout < time.Second || limits.Timeout > MaxJobTimeout ||
		limits.Retention < time.Minute || limits.Retention > MaxJobRetention ||
		!limits.CancelGuarantee.valid() {
		return DirectJobLimits{}, errors.New("node job limits are outside hard bounds")
	}
	return limits, nil
}
