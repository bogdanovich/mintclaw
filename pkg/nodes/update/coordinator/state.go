package coordinator

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

const (
	StateSchemaVersion = 1
	MaxStateBytes      = 64 * 1024
	MaxPayloadBytes    = 128 * 1024 * 1024
	MaxLaunchAttempts  = 3
)

type Phase string

const (
	PhasePrepared               Phase = "prepared"
	PhaseDownloading            Phase = "downloading"
	PhaseVerified               Phase = "verified"
	PhaseStaged                 Phase = "staged"
	PhaseActivating             Phase = "activating"
	PhaseHealthy                Phase = "healthy"
	PhaseRollingBack            Phase = "rolling_back"
	PhaseRolledBack             Phase = "rolled_back"
	PhaseUnknown                Phase = "unknown"
	PhaseOperatorActionRequired Phase = "operator_action_required"
)

type Slot string

const (
	SlotA Slot = "a"
	SlotB Slot = "b"
)

type Installation struct {
	Instance             string   `json:"instance"`
	Manager              string   `json:"manager"`
	Scope                string   `json:"scope"`
	Service              string   `json:"service"`
	InstallTransactionID string   `json:"install_transaction_id"`
	ConfigPath           string   `json:"config_path"`
	CoordinatorPath      string   `json:"coordinator_path"`
	CoordinatorSHA256    string   `json:"coordinator_sha256"`
	ServiceUID           uint32   `json:"service_uid"`
	ServiceGID           uint32   `json:"service_gid"`
	NodeID               nodes.ID `json:"node_id,omitempty"`
	Platform             string   `json:"platform"`
	Architecture         string   `json:"architecture"`
}

type Payload struct {
	Slot    Slot   `json:"slot"`
	Release string `json:"release"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

type ExecutionIdentity struct {
	InvocationID  string `json:"invocation_id"`
	ExecutionID   string `json:"execution_id"`
	PlanHash      string `json:"plan_hash"`
	CatalogHash   string `json:"catalog_hash"`
	AuthorityHash string `json:"authority_hash"`
}

type Transaction struct {
	Identity            ExecutionIdentity `json:"identity"`
	RequestHash         string            `json:"request_hash"`
	Profile             string            `json:"profile"`
	ProfileRevision     string            `json:"profile_revision"`
	ReleaseAlias        string            `json:"release_alias"`
	RequestedRelease    string            `json:"requested_release"`
	ManifestSHA256      string            `json:"manifest_sha256"`
	ArtifactSHA256      string            `json:"artifact_sha256"`
	Phase               Phase             `json:"phase"`
	Candidate           *Payload          `json:"candidate,omitempty"`
	Previous            *Payload          `json:"previous,omitempty"`
	ActivationAttempted bool              `json:"activation_attempted"`
	SuccessorVerified   bool              `json:"successor_verified"`
	RollbackAttempted   bool              `json:"rollback_attempted"`
	RollbackVerified    bool              `json:"rollback_verified"`
	LaunchAttempts      int               `json:"launch_attempts"`
	FailureCode         string            `json:"failure_code,omitempty"`
	Canceled            bool              `json:"canceled,omitempty"`
	CanceledAt          int64             `json:"canceled_at,omitempty"`
	AcceptedAt          int64             `json:"accepted_at"`
	ExpiresAt           int64             `json:"expires_at"`
	UpdatedAt           int64             `json:"updated_at"`
}

type State struct {
	SchemaVersion int          `json:"schema_version"`
	Generation    uint64       `json:"generation"`
	Installation  Installation `json:"installation"`
	Active        Payload      `json:"active"`
	Transaction   *Transaction `json:"transaction,omitempty"`
}

var (
	boundedNamePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	nodeIdentityPattern = regexp.MustCompile(`^node_[a-z2-7]{52}$`)
)

func (state State) Validate() error {
	if state.SchemaVersion != StateSchemaVersion || state.Generation == 0 {
		return errors.New("invalid coordinator state schema or generation")
	}
	if err := state.Installation.Validate(); err != nil {
		return err
	}
	if err := state.Active.Validate(); err != nil {
		return fmt.Errorf("active payload: %w", err)
	}
	if state.Transaction == nil {
		return nil
	}
	if err := state.Transaction.Validate(); err != nil {
		return err
	}
	return validateSlotTransition(state.Active, *state.Transaction)
}

func (installation Installation) Validate() error {
	if !boundedNamePattern.MatchString(installation.Instance) ||
		!boundedNamePattern.MatchString(installation.Service) ||
		!isDigest(installation.InstallTransactionID, 16) ||
		!filepath.IsAbs(
			installation.ConfigPath,
		) || filepath.Clean(installation.ConfigPath) != installation.ConfigPath ||
		!filepath.IsAbs(installation.CoordinatorPath) ||
		filepath.Clean(installation.CoordinatorPath) != installation.CoordinatorPath ||
		!isDigest(installation.CoordinatorSHA256, sha256.Size) ||
		installation.ServiceUID == 0 || installation.ServiceGID == 0 {
		return errors.New("invalid managed installation identity")
	}
	if installation.Manager != "systemd" && installation.Manager != "launchd" {
		return errors.New("unsupported managed service manager")
	}
	if installation.Scope != "user" && installation.Scope != "system" {
		return errors.New("unsupported managed service scope")
	}
	if (installation.Manager == "systemd" && !strings.HasSuffix(installation.Service, ".service")) ||
		(installation.Manager == "launchd" && strings.Contains(installation.Service, "/")) {
		return errors.New("managed service identity does not match its manager")
	}
	if installation.Platform != "linux" && installation.Platform != "darwin" {
		return errors.New("unsupported installation platform")
	}
	if (installation.Platform == "linux" && installation.Manager != "systemd") ||
		(installation.Platform == "darwin" && installation.Manager != "launchd") {
		return errors.New("managed service manager does not match its platform")
	}
	if installation.Architecture != "amd64" && installation.Architecture != "arm64" {
		return errors.New("unsupported installation architecture")
	}
	if err := installation.NodeID.Validate(); err != nil {
		return fmt.Errorf("invalid installed node identity: %w", err)
	}
	if !nodeIdentityPattern.MatchString(string(installation.NodeID)) {
		return errors.New("installed node identity is not a companion key identity")
	}
	return nil
}

func (payload Payload) Validate() error {
	if payload.Slot != SlotA && payload.Slot != SlotB {
		return errors.New("invalid payload slot")
	}
	if !boundedText(payload.Release, 128) || !boundedText(payload.Version, 128) ||
		!isDigest(payload.SHA256, sha256.Size) || payload.Size <= 0 || payload.Size > MaxPayloadBytes {
		return errors.New("invalid payload identity")
	}
	return nil
}

func (identity ExecutionIdentity) Validate() error {
	if !boundedText(identity.InvocationID, nodes.MaxIDLength) ||
		!boundedText(identity.ExecutionID, nodes.MaxIDLength) ||
		!isDigest(identity.PlanHash, sha256.Size) ||
		!isDigest(identity.CatalogHash, sha256.Size) ||
		!isDigest(identity.AuthorityHash, sha256.Size) {
		return errors.New("invalid update execution identity")
	}
	return nil
}

func (transaction Transaction) Validate() error {
	if err := transaction.Identity.Validate(); err != nil {
		return err
	}
	if !isDigest(transaction.RequestHash, sha256.Size) ||
		!boundedNamePattern.MatchString(transaction.Profile) ||
		!boundedNamePattern.MatchString(transaction.ProfileRevision) ||
		!boundedNamePattern.MatchString(transaction.ReleaseAlias) ||
		!boundedText(transaction.RequestedRelease, 128) ||
		!isDigest(transaction.ManifestSHA256, sha256.Size) ||
		!isDigest(transaction.ArtifactSHA256, sha256.Size) {
		return errors.New("invalid update transaction authority")
	}
	if !validPhase(transaction.Phase) || transaction.AcceptedAt <= 0 || transaction.ExpiresAt <= transaction.AcceptedAt ||
		transaction.UpdatedAt < transaction.AcceptedAt ||
		transaction.UpdatedAt > transaction.ExpiresAt ||
		transaction.LaunchAttempts < 0 ||
		transaction.LaunchAttempts > MaxLaunchAttempts {
		return errors.New("invalid update transaction lifecycle")
	}
	if transaction.FailureCode != "" && !boundedNamePattern.MatchString(transaction.FailureCode) {
		return errors.New("invalid update failure code")
	}
	if transaction.Canceled {
		if transaction.CanceledAt < transaction.AcceptedAt || transaction.CanceledAt > transaction.ExpiresAt ||
			transaction.FailureCode != "canceled" || transaction.ActivationAttempted ||
			(transaction.Phase != PhasePrepared && transaction.Phase != PhaseDownloading &&
				transaction.Phase != PhaseVerified && transaction.Phase != PhaseStaged) {
			return errors.New("invalid canceled update transaction")
		}
	} else if transaction.CanceledAt != 0 {
		return errors.New("uncanceled update transaction retains cancellation time")
	}
	if transaction.Candidate != nil {
		if err := transaction.Candidate.Validate(); err != nil {
			return fmt.Errorf("candidate payload: %w", err)
		}
	}
	if transaction.Previous != nil {
		if err := transaction.Previous.Validate(); err != nil {
			return fmt.Errorf("previous payload: %w", err)
		}
	}
	return validatePhaseFacts(transaction)
}

func validatePhaseFacts(transaction Transaction) error {
	requiresCandidate := transaction.Phase == PhaseVerified || transaction.Phase == PhaseStaged ||
		transaction.Phase == PhaseActivating || transaction.Phase == PhaseHealthy ||
		transaction.Phase == PhaseRollingBack || transaction.Phase == PhaseRolledBack
	if requiresCandidate && transaction.Candidate == nil {
		return errors.New("update phase requires a candidate payload")
	}
	requiresPrevious := transaction.Phase == PhaseActivating || transaction.Phase == PhaseHealthy ||
		transaction.Phase == PhaseRollingBack || transaction.Phase == PhaseRolledBack
	if requiresPrevious && transaction.Previous == nil {
		return errors.New("update phase requires a previous payload")
	}
	if transaction.SuccessorVerified && transaction.Phase != PhaseHealthy {
		return errors.New("successor verification is valid only for healthy state")
	}
	if transaction.Phase == PhaseHealthy && !transaction.SuccessorVerified {
		return errors.New("healthy state requires verified successor health")
	}
	if transaction.RollbackVerified && transaction.Phase != PhaseRolledBack {
		return errors.New("rollback verification is valid only for rolled_back state")
	}
	if transaction.RollbackVerified && !transaction.RollbackAttempted {
		return errors.New("rollback verification requires an attempted rollback")
	}
	if transaction.Phase == PhaseRolledBack && !transaction.RollbackVerified {
		return errors.New("rolled_back state requires verified rollback health")
	}
	if transaction.Phase == PhaseActivating && !transaction.ActivationAttempted {
		return errors.New("activating state requires durable activation intent")
	}
	if (transaction.Phase == PhaseRollingBack || transaction.Phase == PhaseRolledBack) &&
		(!transaction.ActivationAttempted || !transaction.RollbackAttempted) {
		return errors.New("rollback state requires activation and rollback intent")
	}
	return nil
}

func validateSlotTransition(active Payload, transaction Transaction) error {
	if transaction.Candidate == nil {
		return nil
	}
	if transaction.Phase == PhaseVerified || transaction.Phase == PhaseStaged {
		if active.Slot == transaction.Candidate.Slot {
			return errors.New("staged candidate cannot occupy the active slot")
		}
		return nil
	}
	if transaction.Phase == PhaseActivating || transaction.Phase == PhaseHealthy {
		if active != *transaction.Candidate || transaction.Previous == nil ||
			active.Slot == transaction.Previous.Slot {
			return errors.New("active selector does not identify the candidate")
		}
	}
	if transaction.Phase == PhaseRollingBack || transaction.Phase == PhaseRolledBack {
		if transaction.Previous == nil || active != *transaction.Previous ||
			active.Slot == transaction.Candidate.Slot {
			return errors.New("active selector does not identify the rollback payload")
		}
	}
	return nil
}

func validPhase(phase Phase) bool {
	switch phase {
	case PhasePrepared, PhaseDownloading, PhaseVerified, PhaseStaged, PhaseActivating, PhaseHealthy,
		PhaseRollingBack, PhaseRolledBack, PhaseUnknown, PhaseOperatorActionRequired:
		return true
	default:
		return false
	}
}

func boundedText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && value == strings.TrimSpace(value) &&
		utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func isDigest(value string, bytes int) bool {
	if len(value) != bytes*2 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == bytes
}
