package nodes

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/bogdanovich/mintclaw/pkg/nodes/internal/jsonstrict"
)

const (
	MaxInvocationInputBytes = 512 * 1024
	MaxInvocationTimeout    = 60 * 60
	// MaxInvocationOutput leaves room for protocol envelope and recovery-record
	// metadata inside the 1 MiB WebSocket frame. Larger data uses artifacts.
	MaxInvocationOutput     = 512 * 1024
	MaxExecutionPlanBytes   = MaxInvocationInputBytes + 64*1024
	MaxPolicyRevisionLength = 128
	MaxExecutionPlanTTL     = 5 * time.Minute
	MaxExecutionPlanSkew    = 30 * time.Second
)

var (
	ErrInvalidInvocation = errors.New("invalid node invocation")
	ErrCommandDenied     = errors.New("node command denied")
)

// ExecutionProfile is the node-authenticated authority required to prepare an
// execution plan.
type ExecutionProfile struct {
	Executor       string `json:"executor"`
	PolicyRevision string `json:"policy_revision"`
}

func (profile ExecutionProfile) Validate() error {
	if !validInvocationIdentifier(profile.Executor) ||
		len(profile.PolicyRevision) == 0 ||
		len(profile.PolicyRevision) > MaxPolicyRevisionLength ||
		!idPattern.MatchString(profile.PolicyRevision) {
		return fmt.Errorf("%w: malformed execution profile", ErrInvalidInvocation)
	}
	return nil
}

// InvocationRequest is the transport-neutral command request prepared by the
// gateway. It contains no connection details or shell-specific authority.
type InvocationRequest struct {
	InvocationID     string                   `json:"invocation_id"`
	IdempotencyKey   string                   `json:"idempotency_key"`
	NodeID           ID                       `json:"node_id"`
	CatalogHash      string                   `json:"catalog_hash"`
	Command          string                   `json:"command"`
	ServiceProfile   string                   `json:"service_profile,omitempty"`
	JobProfile       string                   `json:"job_profile,omitempty"`
	Update           *NodeUpdatePlanAuthority `json:"update,omitempty"`
	Input            json.RawMessage          `json:"input"`
	AgentID          string                   `json:"agent_id"`
	SessionID        string                   `json:"session_id"`
	ActorID          string                   `json:"actor_id"`
	TimeoutSeconds   int                      `json:"timeout_seconds"`
	OutputLimitBytes int                      `json:"output_limit_bytes"`
}

func (request InvocationRequest) Validate() error {
	return request.validateForProtocol(ProtocolV1)
}

func (request InvocationRequest) validateForProtocol(protocolVersion int) error {
	if _, err := EffectiveProtocolVersion(protocolVersion); err != nil {
		return err
	}
	if !validInvocationIdentifier(request.InvocationID) ||
		!validInvocationIdentifier(request.IdempotencyKey) ||
		!validInvocationIdentifier(request.AgentID) ||
		!validInvocationIdentifier(request.SessionID) ||
		!validInvocationIdentifier(request.ActorID) {
		return fmt.Errorf("%w: malformed identity field", ErrInvalidInvocation)
	}
	if err := request.NodeID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidInvocation, err)
	}
	if !validSHA256Digest(request.CatalogHash) {
		return fmt.Errorf("%w: malformed catalog hash", ErrInvalidInvocation)
	}
	if len(request.Command) == 0 || len(request.Command) > MaxCommandNameLen ||
		!commandPattern.MatchString(request.Command) {
		return fmt.Errorf("%w: malformed command", ErrInvalidInvocation)
	}
	if request.ServiceProfile != "" {
		if err := (Alias(request.ServiceProfile)).Validate(); err != nil {
			return fmt.Errorf("%w: malformed service profile", ErrInvalidInvocation)
		}
	}
	if request.JobProfile != "" {
		if err := (Alias(request.JobProfile)).Validate(); err != nil {
			return fmt.Errorf("%w: malformed job profile", ErrInvalidInvocation)
		}
	}
	if request.Update != nil {
		if err := request.Update.Validate(); err != nil {
			return err
		}
	}
	if request.TimeoutSeconds <= 0 || request.TimeoutSeconds > MaxInvocationTimeout {
		return fmt.Errorf("%w: timeout is outside bounds", ErrInvalidInvocation)
	}
	if request.OutputLimitBytes <= 0 || request.OutputLimitBytes > MaxInvocationOutput {
		return fmt.Errorf("%w: output limit is outside bounds", ErrInvalidInvocation)
	}
	if _, err := canonicalInvocationInputForProtocol(request.Input, protocolVersion); err != nil {
		return err
	}
	return nil
}

// ExecutionPlan is the canonical authority reviewed before dispatch. PlanHash
// is a binding digest, not proof of origin; approval and ledger records retain
// the expected digest independently and compare it before dispatch.
type ExecutionPlan struct {
	InvocationRequest
	ProtocolVersion int    `json:"protocol_version,omitempty"`
	Risk            Risk   `json:"risk"`
	DescriptorHash  string `json:"descriptor_hash"`
	Executor        string `json:"executor"`
	PolicyRevision  string `json:"policy_revision"`
	PreparedAt      int64  `json:"prepared_at"`
	ExpiresAt       int64  `json:"expires_at"`
	PlanHash        string `json:"plan_hash"`
}

// InvocationDispatch is the transport-only envelope for an execution plan and
// an optional input that must never enter either durable invocation ledger.
// The durable plan binds the ephemeral bytes by digest and length; the command
// handler independently verifies that binding before ledger acceptance.
type InvocationDispatch struct {
	Plan           ExecutionPlan   `json:"plan"`
	EphemeralInput json.RawMessage `json:"ephemeral_input,omitempty"`
}

func (dispatch InvocationDispatch) Validate() error {
	if err := dispatch.Plan.Validate(); err != nil {
		return err
	}
	if len(dispatch.EphemeralInput) == 0 {
		return nil
	}
	maximum := MaxBrowserEphemeralInputBytes
	if dispatch.Plan.Command == BrowserCommandContexts {
		maximum = MaxBrowserContextInputBytes
	}
	if (dispatch.Plan.Command != BrowserCommandAct && dispatch.Plan.Command != BrowserCommandContexts) ||
		len(dispatch.EphemeralInput) > maximum {
		return fmt.Errorf("%w: ephemeral invocation input is unavailable", ErrInvalidInvocation)
	}
	value, err := jsonstrict.Decode(dispatch.EphemeralInput)
	if err != nil {
		return fmt.Errorf("%w: malformed ephemeral invocation input", ErrInvalidInvocation)
	}
	if _, ok := value.(map[string]any); !ok {
		return fmt.Errorf("%w: ephemeral invocation input must be an object", ErrInvalidInvocation)
	}
	return nil
}

func PrepareExecutionPlan(
	request InvocationRequest,
	descriptor CommandDescriptor,
	executor string,
	policyRevision string,
	preparedAt time.Time,
	ttl time.Duration,
) (ExecutionPlan, error) {
	return PrepareExecutionPlanForProtocol(
		ProtocolV1,
		request,
		descriptor,
		executor,
		policyRevision,
		preparedAt,
		ttl,
	)
}

// PrepareExecutionPlanForProtocol binds all canonical hashes and JSON payloads
// to the protocol negotiated for the target node.
func PrepareExecutionPlanForProtocol(
	protocolVersion int,
	request InvocationRequest,
	descriptor CommandDescriptor,
	executor string,
	policyRevision string,
	preparedAt time.Time,
	ttl time.Duration,
) (ExecutionPlan, error) {
	protocolVersion, protocolErr := EffectiveProtocolVersion(protocolVersion)
	if protocolErr != nil {
		return ExecutionPlan{}, protocolErr
	}
	if err := request.validateForProtocol(protocolVersion); err != nil {
		return ExecutionPlan{}, err
	}
	if err := descriptor.Validate(); err != nil {
		return ExecutionPlan{}, err
	}
	if descriptor.Name != request.Command {
		return ExecutionPlan{}, fmt.Errorf(
			"%w: descriptor does not match command",
			ErrInvalidInvocation,
		)
	}
	if len(descriptor.ServiceProfiles) == 0 {
		if request.ServiceProfile != "" {
			return ExecutionPlan{}, fmt.Errorf(
				"%w: service profile supplied for non-service command",
				ErrInvalidInvocation,
			)
		}
	} else if len(descriptor.ServiceProfiles) != 1 ||
		request.ServiceProfile != descriptor.ServiceProfiles[0].Alias {
		return ExecutionPlan{}, fmt.Errorf(
			"%w: descriptor does not match service profile",
			ErrInvalidInvocation,
		)
	}
	if len(descriptor.JobProfiles) == 0 {
		if request.JobProfile != "" {
			return ExecutionPlan{}, fmt.Errorf(
				"%w: job profile supplied for non-job command",
				ErrInvalidInvocation,
			)
		}
	} else if len(descriptor.JobProfiles) != 1 ||
		request.JobProfile != descriptor.JobProfiles[0].Alias {
		return ExecutionPlan{}, fmt.Errorf(
			"%w: descriptor does not match job profile",
			ErrInvalidInvocation,
		)
	}
	if len(descriptor.UpdateProfiles) == 0 {
		if request.Update != nil {
			return ExecutionPlan{}, fmt.Errorf(
				"%w: update authority supplied for non-update command",
				ErrInvalidInvocation,
			)
		}
	} else {
		if request.Update == nil || len(descriptor.UpdateProfiles) != 1 {
			return ExecutionPlan{}, fmt.Errorf(
				"%w: descriptor does not match update authority",
				ErrInvalidInvocation,
			)
		}
		profile := descriptor.UpdateProfiles[0]
		matched := false
		for _, release := range profile.Releases {
			if request.Update.matchesDescriptor(profile, release) {
				matched = true
				break
			}
		}
		if !matched {
			return ExecutionPlan{}, fmt.Errorf(
				"%w: descriptor does not match update authority",
				ErrInvalidInvocation,
			)
		}
	}
	descriptorHash, err := descriptor.HashForProtocol(protocolVersion)
	if err != nil {
		return ExecutionPlan{}, err
	}
	if !validInvocationIdentifier(executor) || len(policyRevision) == 0 ||
		len(policyRevision) > MaxPolicyRevisionLength || !idPattern.MatchString(policyRevision) {
		return ExecutionPlan{}, fmt.Errorf("%w: malformed execution policy", ErrInvalidInvocation)
	}
	if preparedAt.Unix() <= 0 || ttl < time.Second || ttl > MaxExecutionPlanTTL {
		return ExecutionPlan{}, fmt.Errorf(
			"%w: plan lifetime is outside bounds",
			ErrInvalidInvocation,
		)
	}
	input, value, err := canonicalInvocationInputValueForProtocol(request.Input, protocolVersion)
	if err != nil {
		return ExecutionPlan{}, err
	}
	if validationErr := validateDescriptorInvocationInput(descriptor, value); validationErr != nil {
		return ExecutionPlan{}, validationErr
	}
	if selectorErr := validateNodeUpdateSelector(value, request.Update); selectorErr != nil {
		return ExecutionPlan{}, selectorErr
	}
	request.Input = input
	plan := ExecutionPlan{
		InvocationRequest: request,
		Risk:              descriptor.Risk,
		DescriptorHash:    descriptorHash,
		Executor:          executor,
		PolicyRevision:    policyRevision,
		PreparedAt:        preparedAt.Unix(),
		ExpiresAt:         preparedAt.Add(ttl).Unix(),
	}
	if protocolVersion != ProtocolV1 {
		plan.ProtocolVersion = protocolVersion
	}
	hash, err := plan.computeHash()
	if err != nil {
		return ExecutionPlan{}, err
	}
	plan.PlanHash = hash
	return plan, nil
}

func (plan ExecutionPlan) Validate() error {
	protocolVersion, protocolErr := EffectiveProtocolVersion(plan.ProtocolVersion)
	if protocolErr != nil {
		return fmt.Errorf("%w: unsupported plan protocol", ErrInvalidInvocation)
	}
	if err := plan.validateForProtocol(protocolVersion); err != nil {
		return err
	}
	if !plan.Risk.Valid() || !validSHA256Digest(plan.DescriptorHash) ||
		!validInvocationIdentifier(plan.Executor) ||
		len(plan.PolicyRevision) == 0 || len(plan.PolicyRevision) > MaxPolicyRevisionLength ||
		!idPattern.MatchString(plan.PolicyRevision) {
		return fmt.Errorf("%w: malformed execution policy", ErrInvalidInvocation)
	}
	if plan.PreparedAt <= 0 || plan.ExpiresAt <= plan.PreparedAt ||
		plan.ExpiresAt-plan.PreparedAt > int64(MaxExecutionPlanTTL/time.Second) {
		return fmt.Errorf("%w: plan lifetime is outside bounds", ErrInvalidInvocation)
	}
	wantHash, err := plan.computeHash()
	if err != nil {
		return err
	}
	if plan.PlanHash != wantHash {
		return fmt.Errorf("%w: plan hash mismatch", ErrInvalidInvocation)
	}
	return nil
}

// ValidateAgainstHash verifies both plan self-consistency and the binding to a
// digest retained outside the mutable plan, such as an approval record.
func (plan ExecutionPlan) ValidateAgainstHash(expected string) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	decoded, err := hex.DecodeString(expected)
	if err != nil || len(decoded) != sha256.Size ||
		subtle.ConstantTimeCompare([]byte(plan.PlanHash), []byte(expected)) != 1 {
		return fmt.Errorf("%w: plan does not match retained hash", ErrCommandDenied)
	}
	return nil
}

func (plan ExecutionPlan) computeHash() (string, error) {
	protocolVersion, err := EffectiveProtocolVersion(plan.ProtocolVersion)
	if err != nil {
		return "", fmt.Errorf("%w: unsupported plan protocol", ErrInvalidInvocation)
	}
	plan.PlanHash = ""
	data, err := json.Marshal(plan)
	if err != nil {
		return "", fmt.Errorf("%w: encode plan: %w", ErrInvalidInvocation, err)
	}
	canonical, err := canonicalBytesForProtocol(data, protocolVersion, MaxExecutionPlanBytes)
	if err != nil {
		return "", fmt.Errorf("%w: canonicalize plan: %w", ErrInvalidInvocation, err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// ApprovedCommand applies the durable pairing command surface. It does not
// replace agent, approval, or node-local policy checks.
func (registration Registration) ApprovedCommand(name string) (CommandDescriptor, error) {
	if err := registration.Snapshot.Validate(); err != nil {
		return CommandDescriptor{}, fmt.Errorf("%w: invalid registered catalog", ErrCommandDenied)
	}
	if registration.Snapshot.State != StateConnected || registration.ApprovedAt <= 0 {
		return CommandDescriptor{}, fmt.Errorf("%w: node is not connected", ErrCommandDenied)
	}
	descriptor, advertised := registration.Snapshot.Catalog.command(name)
	if !advertised {
		return CommandDescriptor{}, fmt.Errorf("%w: command is not advertised", ErrCommandDenied)
	}
	if !slices.Contains(registration.AllowedCommands, name) {
		return CommandDescriptor{}, fmt.Errorf("%w: command is not approved", ErrCommandDenied)
	}
	if !validSHA256Digest(registration.ApprovedCatalogHash) ||
		registration.ApprovedCatalogHash != registration.Snapshot.CatalogHash {
		return CommandDescriptor{}, fmt.Errorf(
			"%w: capability catalog requires reapproval",
			ErrCommandDenied,
		)
	}
	return descriptor, nil
}

// LocalCommandPolicy is the companion-owned maximum authority. Empty command
// lists deny all commands, including commands approved by the gateway.
type LocalCommandPolicy struct {
	Revision          string   `json:"revision"`
	AllowedCommands   []string `json:"allowed_commands"`
	MaximumRisk       Risk     `json:"maximum_risk"`
	MaxTimeoutSeconds int      `json:"max_timeout_seconds"`
	MaxOutputBytes    int      `json:"max_output_bytes"`
}

func (policy LocalCommandPolicy) Validate() error {
	if len(policy.Revision) == 0 || len(policy.Revision) > MaxPolicyRevisionLength ||
		!idPattern.MatchString(policy.Revision) || !policy.MaximumRisk.Valid() {
		return fmt.Errorf("%w: malformed local policy", ErrCommandDenied)
	}
	if policy.MaxTimeoutSeconds <= 0 || policy.MaxTimeoutSeconds > MaxInvocationTimeout ||
		policy.MaxOutputBytes <= 0 || policy.MaxOutputBytes > MaxInvocationOutput {
		return fmt.Errorf("%w: local policy limits are outside bounds", ErrCommandDenied)
	}
	seen := make(map[string]struct{}, len(policy.AllowedCommands))
	for _, command := range policy.AllowedCommands {
		if !commandPattern.MatchString(command) {
			return fmt.Errorf("%w: malformed allowed command", ErrCommandDenied)
		}
		if _, exists := seen[command]; exists {
			return fmt.Errorf("%w: duplicate allowed command", ErrCommandDenied)
		}
		seen[command] = struct{}{}
	}
	return nil
}

func (policy LocalCommandPolicy) Authorize(
	plan ExecutionPlan,
	runtimeCatalog CapabilityCatalog,
	receivingNodeID ID,
	actualExecutor string,
	now time.Time,
) error {
	return policy.authorize(plan, runtimeCatalog, receivingNodeID, actualExecutor, now, true)
}

// AuthorizeReplay verifies current local authority for returning a recorded
// invocation without requiring the original execution window to remain live.
func (policy LocalCommandPolicy) AuthorizeReplay(
	plan ExecutionPlan,
	runtimeCatalog CapabilityCatalog,
	receivingNodeID ID,
	actualExecutor string,
) error {
	return policy.authorize(
		plan,
		runtimeCatalog,
		receivingNodeID,
		actualExecutor,
		time.Time{},
		false,
	)
}

func (policy LocalCommandPolicy) authorize(
	plan ExecutionPlan,
	runtimeCatalog CapabilityCatalog,
	receivingNodeID ID,
	actualExecutor string,
	now time.Time,
	requireLiveWindow bool,
) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	if err := runtimeCatalog.Validate(); err != nil {
		return err
	}
	protocolVersion, err := EffectiveProtocolVersion(plan.ProtocolVersion)
	if err != nil {
		return err
	}
	actualCatalogHash, err := runtimeCatalog.HashForProtocol(protocolVersion)
	if err != nil {
		return err
	}
	if nodeErr := receivingNodeID.Validate(); nodeErr != nil || !validInvocationIdentifier(actualExecutor) ||
		plan.NodeID != receivingNodeID ||
		plan.Executor != actualExecutor ||
		plan.CatalogHash != actualCatalogHash {
		return fmt.Errorf("%w: plan target does not match local runtime", ErrCommandDenied)
	}
	descriptor, advertised := runtimeCatalog.command(plan.Command)
	if !advertised {
		return fmt.Errorf("%w: command is not advertised by local runtime", ErrCommandDenied)
	}
	_, input, err := canonicalInvocationInputValueForProtocol(plan.Input, protocolVersion)
	if err != nil {
		return err
	}
	if len(descriptor.FileProfiles) > 0 {
		profileRevision, _ := input["profile_revision"].(string)
		profileAlias := ""
		for _, profile := range descriptor.FileProfiles {
			if profile.Revision == profileRevision {
				profileAlias = profile.Alias
				break
			}
		}
		projected, available := ProjectFileDescriptorForProfile(descriptor, profileAlias)
		if !available {
			return fmt.Errorf(
				"%w: file profile is not advertised by local runtime",
				ErrCommandDenied,
			)
		}
		descriptor = projected
	}
	if plan.ServiceProfile != "" || len(descriptor.ServiceProfiles) > 0 {
		projected, available := ProjectServiceDescriptorForProfile(
			descriptor,
			plan.ServiceProfile,
		)
		if !available {
			return fmt.Errorf(
				"%w: service profile is not advertised by local runtime",
				ErrCommandDenied,
			)
		}
		descriptor = projected
	}
	if plan.Update != nil || len(descriptor.UpdateProfiles) > 0 {
		if plan.Update == nil {
			return fmt.Errorf("%w: update authority is missing", ErrCommandDenied)
		}
		projected, available := ProjectUpdateDescriptorForProfile(descriptor, plan.Update.Profile)
		if !available {
			return fmt.Errorf(
				"%w: update profile is not advertised by local runtime",
				ErrCommandDenied,
			)
		}
		descriptor = projected
	}
	if plan.JobProfile != "" || len(descriptor.JobProfiles) > 0 {
		projected, available := ProjectJobDescriptorForProfile(descriptor, plan.JobProfile)
		if !available {
			return fmt.Errorf(
				"%w: job profile is not advertised by local runtime",
				ErrCommandDenied,
			)
		}
		descriptor = projected
	}
	descriptorHash, hashErr := descriptor.HashForProtocol(protocolVersion)
	if hashErr != nil ||
		descriptor.Name != plan.Command || descriptor.Risk != plan.Risk ||
		descriptorHash != plan.DescriptorHash ||
		plan.PolicyRevision != policy.Revision {
		return fmt.Errorf("%w: plan does not match current policy or descriptor", ErrCommandDenied)
	}
	if requireLiveWindow {
		nowUnix := now.Unix()
		if nowUnix <= 0 ||
			(plan.PreparedAt > nowUnix && plan.PreparedAt-nowUnix > int64(MaxExecutionPlanSkew/time.Second)) ||
			nowUnix >= plan.ExpiresAt {
			return fmt.Errorf("%w: plan is not currently valid", ErrCommandDenied)
		}
	}
	if err := validateDescriptorInvocationInput(descriptor, input); err != nil {
		return err
	}
	if err := validateNodeUpdateSelector(input, plan.Update); err != nil {
		return err
	}
	if !slices.Contains(policy.AllowedCommands, plan.Command) ||
		riskRank(plan.Risk) > riskRank(policy.MaximumRisk) ||
		plan.TimeoutSeconds > policy.MaxTimeoutSeconds ||
		plan.OutputLimitBytes > policy.MaxOutputBytes {
		return fmt.Errorf("%w: plan exceeds local policy", ErrCommandDenied)
	}
	return nil
}

func validateNodeUpdateSelector(
	input map[string]any,
	authority *NodeUpdatePlanAuthority,
) error {
	if authority == nil {
		return nil
	}
	release, ok := input["release"].(string)
	if !ok || release != authority.ReleaseAlias {
		return fmt.Errorf("%w: update input conflicts with retained authority", ErrCommandDenied)
	}
	return nil
}

func (catalog CapabilityCatalog) command(name string) (CommandDescriptor, bool) {
	for _, descriptor := range catalog.Commands {
		if descriptor.Name == name {
			return descriptor, true
		}
	}
	return CommandDescriptor{}, false
}

func canonicalInvocationInputForProtocol(
	raw json.RawMessage,
	protocolVersion int,
) (json.RawMessage, error) {
	canonical, _, err := canonicalInvocationInputValueForProtocol(raw, protocolVersion)
	return canonical, err
}

func canonicalInvocationInputValue(raw json.RawMessage) (json.RawMessage, map[string]any, error) {
	return canonicalInvocationInputValueForProtocol(raw, ProtocolV1)
}

func canonicalInvocationInputValueForProtocol(
	raw json.RawMessage,
	protocolVersion int,
) (json.RawMessage, map[string]any, error) {
	if len(raw) == 0 || len(raw) > MaxInvocationInputBytes {
		return nil, nil, fmt.Errorf("%w: input is outside bounds", ErrInvalidInvocation)
	}
	value, err := jsonstrict.Decode(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: invalid input: %w", ErrInvalidInvocation, err)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("%w: input must be an object", ErrInvalidInvocation)
	}
	canonical, err := canonicalBytesForProtocol(raw, protocolVersion, MaxInvocationInputBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: canonicalize input: %w", ErrInvalidInvocation, err)
	}
	if protocolVersion == ProtocolV2 {
		value, err = jsonstrict.Decode(canonical)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: decode canonical input: %w", ErrInvalidInvocation, err)
		}
		object = value.(map[string]any)
	}
	return json.RawMessage(canonical), object, nil
}

func validateInvocationInput(rawSchema json.RawMessage, input map[string]any) error {
	return validateInvocationValue(rawSchema, input, "input")
}

func validateDescriptorInvocationInput(descriptor CommandDescriptor, input map[string]any) error {
	if err := validateInvocationInput(descriptor.InputSchema, input); err != nil {
		return err
	}
	if IsBrowserCommand(descriptor.Name) {
		return validateBrowserInvocationInput(descriptor, input)
	}
	return nil
}

// ValidateInvocationOutput validates and canonicalizes a command result before
// it crosses the node transport boundary.
func ValidateInvocationOutput(
	descriptor CommandDescriptor,
	raw json.RawMessage,
	limit int,
) (json.RawMessage, error) {
	return ValidateInvocationOutputForProtocol(ProtocolV1, descriptor, raw, limit)
}

// ValidateInvocationOutputForProtocol validates and canonicalizes an output
// using the representation negotiated for its execution plan.
func ValidateInvocationOutputForProtocol(
	protocolVersion int,
	descriptor CommandDescriptor,
	raw json.RawMessage,
	limit int,
) (json.RawMessage, error) {
	if _, err := EffectiveProtocolVersion(protocolVersion); err != nil {
		return nil, err
	}
	if err := descriptor.Validate(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > MaxInvocationOutput || len(raw) == 0 || len(raw) > limit {
		return nil, fmt.Errorf("%w: output is outside bounds", ErrInvalidInvocation)
	}
	value, err := jsonstrict.Decode(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid output: %w", ErrInvalidInvocation, err)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: output must be an object", ErrInvalidInvocation)
	}
	canonical, err := canonicalBytesForProtocol(raw, protocolVersion, limit)
	if err != nil {
		return nil, fmt.Errorf("%w: canonicalize output: %w", ErrInvalidInvocation, err)
	}
	if protocolVersion == ProtocolV2 {
		value, err = jsonstrict.Decode(canonical)
		if err != nil {
			return nil, fmt.Errorf("%w: decode canonical output: %w", ErrInvalidInvocation, err)
		}
		object = value.(map[string]any)
	}
	if validationErr := validateInvocationValue(descriptor.OutputSchema, object, "output"); validationErr != nil {
		return nil, validationErr
	}
	if IsBrowserCommand(descriptor.Name) {
		if validationErr := validateBrowserInvocationOutput(
			descriptor.Name,
			strictestBrowserLimits(descriptor.BrowserProfiles),
			object,
		); validationErr != nil {
			return nil, validationErr
		}
	}
	return json.RawMessage(canonical), nil
}

func canonicalBytesForProtocol(raw []byte, protocolVersion int, maxBytes int) ([]byte, error) {
	protocolVersion, err := EffectiveProtocolVersion(protocolVersion)
	if err != nil {
		return nil, err
	}
	if protocolVersion == ProtocolV2 {
		return jsonstrict.CanonicalV2Bounded(raw, maxBytes)
	}
	canonical, err := jsonstrict.Canonical(raw)
	if err == nil && len(canonical) > maxBytes {
		err = jsonstrict.ErrCanonicalTooLarge
	}
	return canonical, err
}

func validateInvocationValue(rawSchema json.RawMessage, value map[string]any, label string) error {
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	schemaURL := "urn:mintclaw:node-command-" + label
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(rawSchema))
	if err != nil {
		return fmt.Errorf("%w: decode %s schema: %w", ErrInvalidInvocation, label, err)
	}
	if err = compiler.AddResource(schemaURL, document); err != nil {
		return fmt.Errorf("%w: register %s schema: %w", ErrInvalidInvocation, label, err)
	}
	resolved, err := compiler.Compile(schemaURL)
	if err != nil {
		return fmt.Errorf("%w: resolve %s schema: %w", ErrInvalidInvocation, label, err)
	}
	if err := resolved.Validate(value); err != nil {
		return fmt.Errorf("%w: %s violates command schema: %w", ErrInvalidInvocation, label, err)
	}
	return nil
}

func validInvocationIdentifier(value string) bool {
	return len(value) > 0 && len(value) <= MaxIDLength && idPattern.MatchString(value)
}

func riskRank(risk Risk) int {
	switch risk {
	case RiskRead:
		return 1
	case RiskWrite:
		return 2
	case RiskPrivileged:
		return 3
	default:
		return 4
	}
}
