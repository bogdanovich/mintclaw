package nodes

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/nodes/internal/jsonstrict"
)

const (
	ProtocolV1            = 1
	ProtocolV2            = 2
	MaxIDLength           = 128
	MaxAliasLength        = 64
	MaxCommandNameLen     = 128
	MaxSchemaBytes        = 64 * 1024
	MaxCatalogCommands    = 128
	MaxCatalogBytes       = 512 * 1024
	MaxModelContractBytes = 32 * 1024
	MaxModelGuidanceBytes = 2 * 1024
	MaxModelExamples      = 4
	MaxModelExampleBytes  = 8 * 1024
)

// EffectiveProtocolVersion maps legacy omitted protocol fields to v1 and
// rejects versions this binary cannot interpret.
func EffectiveProtocolVersion(version int) (int, error) {
	if version == 0 {
		return ProtocolV1, nil
	}
	if version < ProtocolV1 || version > ProtocolV2 {
		return 0, fmt.Errorf("%w: unsupported protocol version %d", ErrInvalidNode, version)
	}
	return version, nil
}

// NegotiateProtocol selects the newest protocol in the peer's advertised
// range that this binary supports.
func NegotiateProtocol(minimum, maximum int) (int, error) {
	if minimum <= 0 || maximum < minimum || minimum > ProtocolV2 || maximum < ProtocolV1 {
		return 0, fmt.Errorf("%w: incompatible protocol range", ErrInvalidNode)
	}
	return min(maximum, ProtocolV2), nil
}

var (
	ErrInvalidNode       = errors.New("invalid node")
	ErrInvalidCapability = errors.New("invalid node capability")

	idPattern         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
	aliasPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	capabilityPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	commandPattern    = regexp.MustCompile(`^[a-z][a-z0-9_-]*(?:\.[a-z][a-z0-9_-]*)*\.v[1-9][0-9]*$`)
)

type ID string

func (id ID) Validate() error {
	value := string(id)
	if len(value) == 0 || len(value) > MaxIDLength || !idPattern.MatchString(value) {
		return fmt.Errorf("%w: malformed id", ErrInvalidNode)
	}
	return nil
}

type Alias string

func (alias Alias) Validate() error {
	value := string(alias)
	if len(value) == 0 || len(value) > MaxAliasLength || !aliasPattern.MatchString(value) {
		return fmt.Errorf("%w: malformed alias", ErrInvalidNode)
	}
	return nil
}

type State string

const (
	StatePendingPairing State = "pending_pairing"
	StateConnected      State = "connected"
	StateDisconnected   State = "disconnected"
	StateRevoked        State = "revoked"
	StateIncompatible   State = "incompatible"
	StateDegraded       State = "degraded"
)

func (state State) Valid() bool {
	switch state {
	case StatePendingPairing, StateConnected, StateDisconnected, StateRevoked,
		StateIncompatible, StateDegraded:
		return true
	default:
		return false
	}
}

type Risk string

const (
	RiskRead       Risk = "read"
	RiskWrite      Risk = "write"
	RiskPrivileged Risk = "privileged"
)

func (risk Risk) Valid() bool {
	return risk == RiskRead || risk == RiskWrite || risk == RiskPrivileged
}

type ModelAvailability string

const (
	ModelAvailable          ModelAvailability = "available"
	ModelPartiallyDescribed ModelAvailability = "partially_described"
	ModelUnavailable        ModelAvailability = "unavailable"
)

func (availability ModelAvailability) Valid() bool {
	switch availability {
	case ModelAvailable, ModelPartiallyDescribed, ModelUnavailable:
		return true
	default:
		return false
	}
}

type CommandModelConstraints struct {
	ExecutableAliases []string `json:"executable_aliases,omitempty"`
	ProfileAliases    []string `json:"profile_aliases,omitempty"`
	WorkingScopes     []string `json:"working_scopes,omitempty"`
	EnvironmentNames  []string `json:"environment_names,omitempty"`
}

type CommandModelContract struct {
	Availability      ModelAvailability       `json:"availability"`
	TimeoutSecondsMax int                     `json:"timeout_seconds_max"`
	OutputBytesMax    int                     `json:"output_bytes_max"`
	ResultKind        string                  `json:"result_kind"`
	AuthorityDigest   string                  `json:"authority_digest,omitempty"`
	ApprovalMode      string                  `json:"approval_mode,omitempty"`
	Constraints       CommandModelConstraints `json:"constraints"`
	Guidance          []string                `json:"guidance"`
	Examples          []json.RawMessage       `json:"examples"`
}

// FileProfileDescriptor is the authenticated, model-safe projection of one
// node-local regular-file profile. The gateway selects it only through
// operator-owned target configuration; model tool input never carries Alias,
// Revision, roots, approval, or limits.
type FileProfileDescriptor struct {
	Alias          string              `json:"alias"`
	Revision       string              `json:"revision"`
	ReadableRoots  []string            `json:"readable_roots,omitempty"`
	WritableRoots  []string            `json:"writable_roots,omitempty"`
	AllowCreate    bool                `json:"allow_create,omitempty"`
	AllowOverwrite bool                `json:"allow_overwrite,omitempty"`
	MaxFileBytes   int64               `json:"max_file_bytes"`
	Approval       FileProfileApproval `json:"approval"`
}

type FileProfileApproval struct {
	Metadata string `json:"metadata"`
	Read     string `json:"read"`
	Write    string `json:"write"`
}

func (profile FileProfileDescriptor) Validate() error {
	if err := (Alias(profile.Alias)).Validate(); err != nil ||
		!validInvocationIdentifier(profile.Revision) ||
		profile.MaxFileBytes < 0 ||
		profile.MaxFileBytes > MaxTransferArtifactBytes ||
		len(profile.ReadableRoots) > 32 ||
		len(profile.WritableRoots) > 32 ||
		!validFileProfileApproval(profile.Approval) {
		return fmt.Errorf("%w: malformed file profile descriptor", ErrInvalidCapability)
	}
	if !sort.StringsAreSorted(profile.ReadableRoots) ||
		!sort.StringsAreSorted(profile.WritableRoots) ||
		!validModelFileRoots(profile.ReadableRoots) ||
		!validModelFileRoots(profile.WritableRoots) {
		return fmt.Errorf("%w: malformed file profile roots", ErrInvalidCapability)
	}
	if len(profile.WritableRoots) == 0 &&
		(profile.AllowCreate || profile.AllowOverwrite) {
		return fmt.Errorf("%w: file publication lacks a writable root", ErrInvalidCapability)
	}
	return nil
}

func validFileProfileApproval(approval FileProfileApproval) bool {
	for _, value := range []string{approval.Metadata, approval.Read, approval.Write} {
		if value != "none" && value != "required" {
			return false
		}
	}
	return true
}

func validModelFileRoots(roots []string) bool {
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		if root == "" ||
			len(root) > 4096 ||
			root != strings.TrimSpace(root) ||
			!utf8.ValidString(root) ||
			strings.IndexFunc(root, containsModelControlRune) >= 0 {
			return false
		}
		if _, duplicate := seen[root]; duplicate {
			return false
		}
		seen[root] = struct{}{}
	}
	return true
}

func containsModelControlRune(character rune) bool {
	return character < 0x20 || character == 0x7f
}

func (contract CommandModelContract) Validate(inputSchema json.RawMessage) error {
	if !contract.Availability.Valid() ||
		contract.TimeoutSecondsMax <= 0 ||
		contract.TimeoutSecondsMax > MaxInvocationTimeout ||
		contract.OutputBytesMax <= 0 ||
		contract.OutputBytesMax > MaxInvocationOutput ||
		contract.ResultKind != "json" ||
		(contract.AuthorityDigest != "" && !validSHA256Digest(contract.AuthorityDigest)) ||
		(contract.ApprovalMode != "" &&
			contract.ApprovalMode != "each_command" &&
			contract.ApprovalMode != "session_start") ||
		contract.Guidance == nil ||
		contract.Examples == nil {
		return fmt.Errorf("%w: malformed model contract", ErrInvalidCapability)
	}
	if err := validateModelConstraintNames(contract.Constraints); err != nil {
		return err
	}
	guidanceBytes := 0
	for _, statement := range contract.Guidance {
		guidanceBytes += len(statement)
		if statement == "" || statement != strings.TrimSpace(statement) ||
			!utf8.ValidString(statement) || containsModelControl(statement) ||
			guidanceBytes > MaxModelGuidanceBytes {
			return fmt.Errorf("%w: malformed model guidance", ErrInvalidCapability)
		}
	}
	if len(contract.Examples) > MaxModelExamples {
		return fmt.Errorf("%w: too many model examples", ErrInvalidCapability)
	}
	for _, example := range contract.Examples {
		if len(example) == 0 || len(example) > MaxModelExampleBytes {
			return fmt.Errorf("%w: model example exceeds size limit", ErrInvalidCapability)
		}
		value, err := jsonstrict.Decode(example)
		if err != nil {
			return fmt.Errorf("%w: invalid model example", ErrInvalidCapability)
		}
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: model example must be an object", ErrInvalidCapability)
		}
		if err := validateInvocationInput(inputSchema, object); err != nil {
			return fmt.Errorf("%w: model example violates input schema", ErrInvalidCapability)
		}
	}
	data, err := json.Marshal(contract)
	if err != nil || len(data) > MaxModelContractBytes {
		return fmt.Errorf("%w: model contract exceeds size limit", ErrInvalidCapability)
	}
	return nil
}

func validateModelConstraintNames(constraints CommandModelConstraints) error {
	groups := [][]string{
		constraints.ExecutableAliases,
		constraints.ProfileAliases,
		constraints.WorkingScopes,
		constraints.EnvironmentNames,
	}
	limits := []int{64, 32, 32, 64}
	for index, values := range groups {
		if len(values) > limits[index] {
			return fmt.Errorf("%w: too many model constraint names", ErrInvalidCapability)
		}
		seen := make(map[string]struct{}, len(values))
		for _, value := range values {
			if len(value) == 0 || len(value) > MaxAliasLength ||
				value != strings.TrimSpace(value) || !idPattern.MatchString(value) {
				return fmt.Errorf("%w: malformed model constraint name", ErrInvalidCapability)
			}
			if _, duplicate := seen[value]; duplicate {
				return fmt.Errorf("%w: duplicate model constraint name", ErrInvalidCapability)
			}
			seen[value] = struct{}{}
		}
		if !sort.StringsAreSorted(values) {
			return fmt.Errorf("%w: model constraint names are not sorted", ErrInvalidCapability)
		}
	}
	return nil
}

func containsModelControl(value string) bool {
	return strings.IndexFunc(value, func(character rune) bool {
		return character < 0x20 || character == 0x7f
	}) >= 0
}

type CommandDescriptor struct {
	Name             string                     `json:"name"`
	InputSchema      json.RawMessage            `json:"input_schema"`
	OutputSchema     json.RawMessage            `json:"output_schema"`
	Risk             Risk                       `json:"risk"`
	SupportsProgress bool                       `json:"supports_progress,omitempty"`
	SupportsCancel   bool                       `json:"supports_cancel,omitempty"`
	ModelContract    *CommandModelContract      `json:"model_contract,omitempty"`
	FileProfiles     []FileProfileDescriptor    `json:"file_profiles,omitempty"`
	ServiceProfiles  []ServiceProfileDescriptor `json:"service_profiles,omitempty"`
	BrowserProfiles  []BrowserProfileDescriptor `json:"browser_profiles,omitempty"`
	UpdateProfiles   []UpdateProfileDescriptor  `json:"update_profiles,omitempty"`
	JobProfiles      []JobProfileDescriptor     `json:"job_profiles,omitempty"`
}

func (descriptor CommandDescriptor) Validate() error {
	if len(descriptor.Name) == 0 || len(descriptor.Name) > MaxCommandNameLen ||
		!commandPattern.MatchString(descriptor.Name) {
		return fmt.Errorf("%w: malformed command name", ErrInvalidCapability)
	}
	if !descriptor.Risk.Valid() {
		return fmt.Errorf("%w: unsupported risk %q", ErrInvalidCapability, descriptor.Risk)
	}
	if err := validateObjectSchema("input", descriptor.InputSchema); err != nil {
		return err
	}
	if err := validateObjectSchema("output", descriptor.OutputSchema); err != nil {
		return err
	}
	if descriptor.Name == "shell.exec.v1" &&
		(descriptor.Risk != RiskPrivileged ||
			descriptor.ModelContract == nil ||
			descriptor.ModelContract.ApprovalMode != "each_command") {
		return fmt.Errorf("%w: shell.exec requires privileged per-command approval", ErrInvalidCapability)
	}
	if descriptor.ModelContract != nil {
		if err := descriptor.ModelContract.Validate(descriptor.InputSchema); err != nil {
			return err
		}
		switch descriptor.Name {
		case "system.exec.v1":
			modelSchema, err := SystemExecModelInputSchema(*descriptor.ModelContract)
			if err != nil {
				return err
			}
			if err := descriptor.ModelContract.Validate(modelSchema); err != nil {
				return err
			}
		case "shell.exec.v1":
			modelSchema, err := ShellExecModelInputSchema(*descriptor.ModelContract)
			if err != nil {
				return err
			}
			if err := descriptor.ModelContract.Validate(modelSchema); err != nil {
				return err
			}
			for _, example := range descriptor.ModelContract.Examples {
				value, err := jsonstrict.Decode(example)
				if err != nil {
					return fmt.Errorf("%w: invalid shell.exec example", ErrInvalidCapability)
				}
				input, ok := value.(map[string]any)
				if !ok {
					return fmt.Errorf("%w: shell.exec example must be an object", ErrInvalidCapability)
				}
				if err := ValidateShellExecModelInput(input); err != nil {
					return err
				}
			}
		}
	}
	if len(descriptor.FileProfiles) > 0 {
		switch descriptor.Name {
		case "file.info.v1", "file.download.v1", "file.upload.v1",
			WorkspaceCommandRead, WorkspaceCommandSearch, WorkspaceCommandWrite, WorkspaceCommandPatch:
		default:
			return fmt.Errorf("%w: non-file command carries file profiles", ErrInvalidCapability)
		}
		if len(descriptor.FileProfiles) > 32 {
			return fmt.Errorf("%w: too many file profiles", ErrInvalidCapability)
		}
		aliases := make(map[string]struct{}, len(descriptor.FileProfiles))
		revisions := make(map[string]struct{}, len(descriptor.FileProfiles))
		priorAlias := ""
		for _, profile := range descriptor.FileProfiles {
			if err := profile.Validate(); err != nil {
				return err
			}
			if priorAlias != "" && profile.Alias <= priorAlias {
				return fmt.Errorf("%w: file profiles are not sorted", ErrInvalidCapability)
			}
			if _, duplicate := aliases[profile.Alias]; duplicate {
				return fmt.Errorf("%w: duplicate file profile alias", ErrInvalidCapability)
			}
			if _, duplicate := revisions[profile.Revision]; duplicate {
				return fmt.Errorf("%w: duplicate file profile revision", ErrInvalidCapability)
			}
			aliases[profile.Alias] = struct{}{}
			revisions[profile.Revision] = struct{}{}
			priorAlias = profile.Alias
		}
	} else {
		switch descriptor.Name {
		case "file.info.v1", "file.download.v1", "file.upload.v1":
			return fmt.Errorf("%w: file command lacks file profiles", ErrInvalidCapability)
		}
	}
	if err := descriptor.validateServiceProfiles(); err != nil {
		return err
	}
	if err := descriptor.validateBrowserProfiles(); err != nil {
		return err
	}
	if err := descriptor.validateUpdateProfiles(); err != nil {
		return err
	}
	if err := descriptor.validateJobProfiles(); err != nil {
		return err
	}
	return nil
}

func (descriptor CommandDescriptor) validateBrowserProfiles() error {
	if len(descriptor.BrowserProfiles) == 0 {
		if IsBrowserCommand(descriptor.Name) {
			return fmt.Errorf("%w: browser command lacks browser profiles", ErrInvalidCapability)
		}
		return nil
	}
	if !IsBrowserCommand(descriptor.Name) {
		return fmt.Errorf("%w: non-browser command carries browser profiles", ErrInvalidCapability)
	}
	if err := validateBrowserProfiles(descriptor.BrowserProfiles); err != nil {
		return err
	}
	if descriptor.ModelContract != nil {
		return fmt.Errorf("%w: internal browser command must not have a model contract", ErrInvalidCapability)
	}
	wantRisk := RiskRead
	if descriptor.Name == BrowserCommandSessionOpen ||
		descriptor.Name == BrowserCommandAct || descriptor.Name == BrowserCommandContexts ||
		descriptor.Name == BrowserCommandSessionClose {
		wantRisk = RiskWrite
	}
	if descriptor.Risk != wantRisk {
		return fmt.Errorf("%w: browser command has incorrect risk", ErrInvalidCapability)
	}
	expectedInput, err := canonicalJSON(BrowserCommandInputSchema(
		descriptor.Name,
		descriptor.BrowserProfiles,
	))
	if err != nil {
		return err
	}
	actualInput, err := canonicalJSON(descriptor.InputSchema)
	if err != nil || !bytes.Equal(actualInput, expectedInput) {
		return fmt.Errorf("%w: browser input schema does not match typed contract", ErrInvalidCapability)
	}
	expectedOutput, err := canonicalJSON(BrowserCommandOutputSchema(
		descriptor.Name,
		descriptor.BrowserProfiles,
	))
	if err != nil {
		return err
	}
	actualOutput, err := canonicalJSON(descriptor.OutputSchema)
	if err != nil || !bytes.Equal(actualOutput, expectedOutput) {
		return fmt.Errorf("%w: browser output schema does not match typed contract", ErrInvalidCapability)
	}
	return nil
}

func (descriptor CommandDescriptor) validateUpdateProfiles() error {
	if len(descriptor.UpdateProfiles) == 0 {
		if descriptor.Name == "node.update.v1" {
			return fmt.Errorf("%w: node update command lacks update profiles", ErrInvalidCapability)
		}
		return nil
	}
	if descriptor.Name != "node.update.v1" || descriptor.Risk != RiskPrivileged ||
		len(descriptor.UpdateProfiles) > MaxUpdateProfiles {
		return fmt.Errorf("%w: malformed node update descriptor", ErrInvalidCapability)
	}
	if descriptor.ModelContract != nil && descriptor.ModelContract.ApprovalMode != "each_command" {
		return fmt.Errorf("%w: node update requires per-command approval", ErrInvalidCapability)
	}
	priorAlias := ""
	revisions := make(map[string]struct{}, len(descriptor.UpdateProfiles))
	for index, profile := range descriptor.UpdateProfiles {
		if err := profile.Validate(); err != nil {
			return err
		}
		if err := profile.validateRuntimeAuthority(); err != nil {
			return err
		}
		if index > 0 {
			first := descriptor.UpdateProfiles[0]
			if profile.CurrentVersion != first.CurrentVersion ||
				profile.Platform != first.Platform || profile.Architecture != first.Architecture {
				return fmt.Errorf("%w: update profiles disagree on managed runtime facts", ErrInvalidCapability)
			}
		}
		if priorAlias != "" && profile.Alias <= priorAlias {
			return fmt.Errorf("%w: update profiles are not sorted", ErrInvalidCapability)
		}
		if _, duplicate := revisions[profile.Revision]; duplicate {
			return fmt.Errorf("%w: duplicate update profile revision", ErrInvalidCapability)
		}
		revisions[profile.Revision] = struct{}{}
		priorAlias = profile.Alias
	}
	expectedInput, err := canonicalJSON(NodeUpdateInputSchema(descriptor.UpdateProfiles))
	if err != nil {
		return err
	}
	actualInput, err := canonicalJSON(descriptor.InputSchema)
	if err != nil || !bytes.Equal(actualInput, expectedInput) {
		return fmt.Errorf("%w: node update input does not match profile authority", ErrInvalidCapability)
	}
	return nil
}

func (descriptor CommandDescriptor) validateServiceProfiles() error {
	if len(descriptor.ServiceProfiles) == 0 {
		if IsServiceCommand(descriptor.Name) {
			return fmt.Errorf("%w: service command lacks service profiles", ErrInvalidCapability)
		}
		return nil
	}
	if !IsServiceCommand(descriptor.Name) {
		return fmt.Errorf("%w: non-service command carries service profiles", ErrInvalidCapability)
	}
	if len(descriptor.ServiceProfiles) > MaxServiceProfiles {
		return fmt.Errorf("%w: too many service profiles", ErrInvalidCapability)
	}
	priorAlias := ""
	revisions := make(map[string]struct{}, len(descriptor.ServiceProfiles))
	for _, profile := range descriptor.ServiceProfiles {
		if err := profile.Validate(); err != nil {
			return err
		}
		if priorAlias != "" && profile.Alias <= priorAlias {
			return fmt.Errorf("%w: service profiles are not sorted", ErrInvalidCapability)
		}
		if _, duplicate := revisions[profile.Revision]; duplicate {
			return fmt.Errorf("%w: duplicate service profile revision", ErrInvalidCapability)
		}
		for _, service := range profile.Services {
			switch descriptor.Name {
			case "service.status.v1":
				if !service.Status || service.Logs || len(service.Actions) > 0 {
					return fmt.Errorf("%w: status command carries broader service authority", ErrInvalidCapability)
				}
			case "service.logs.v1":
				if !service.Logs || service.Status || len(service.Actions) > 0 {
					return fmt.Errorf("%w: logs command carries broader service authority", ErrInvalidCapability)
				}
			case "service.action.v1":
				if service.Status || service.Logs || len(service.Actions) == 0 {
					return fmt.Errorf("%w: action command carries malformed service authority", ErrInvalidCapability)
				}
			}
		}
		revisions[profile.Revision] = struct{}{}
		priorAlias = profile.Alias
	}
	if descriptor.Name == "service.action.v1" && descriptor.Risk != RiskPrivileged {
		return fmt.Errorf("%w: service action requires privileged risk", ErrInvalidCapability)
	}
	if descriptor.Name != "service.action.v1" && descriptor.Risk != RiskRead {
		return fmt.Errorf("%w: service observation requires read risk", ErrInvalidCapability)
	}
	expectedInput, err := canonicalJSON(ServiceCommandInputSchema(descriptor.Name, descriptor.ServiceProfiles))
	if err != nil {
		return err
	}
	actualInput, err := canonicalJSON(descriptor.InputSchema)
	if err != nil || !bytes.Equal(actualInput, expectedInput) {
		return fmt.Errorf("%w: service input schema does not match profile authority", ErrInvalidCapability)
	}
	expectedOutput, err := canonicalJSON(ServiceCommandOutputSchema(descriptor.Name))
	if err != nil {
		return err
	}
	actualOutput, err := canonicalJSON(descriptor.OutputSchema)
	if err != nil || !bytes.Equal(actualOutput, expectedOutput) {
		return fmt.Errorf("%w: service output schema does not match typed contract", ErrInvalidCapability)
	}
	return nil
}

func (descriptor CommandDescriptor) Capability() string {
	prefix, _, _ := strings.Cut(descriptor.Name, ".")
	if !capabilityPattern.MatchString(prefix) {
		return ""
	}
	return prefix
}

// Hash returns the canonical identity of one command contract.
func (descriptor CommandDescriptor) Hash() (string, error) {
	return descriptor.HashForProtocol(ProtocolV1)
}

// HashForProtocol returns the command identity under the selected protocol's
// canonical JSON representation.
func (descriptor CommandDescriptor) HashForProtocol(protocolVersion int) (string, error) {
	if err := descriptor.Validate(); err != nil {
		return "", err
	}
	return (CapabilityCatalog{Commands: []CommandDescriptor{descriptor}}).canonicalHashForProtocol(protocolVersion)
}

type CapabilityCatalog struct {
	Commands []CommandDescriptor `json:"commands"`
}

func (catalog CapabilityCatalog) Validate() error {
	if len(catalog.Commands) > MaxCatalogCommands {
		return fmt.Errorf("%w: catalog contains too many commands", ErrInvalidCapability)
	}
	seen := make(map[string]struct{}, len(catalog.Commands))
	totalBytes := 0
	var browserProfiles []BrowserProfileDescriptor
	browserCommandCount := 0
	for _, descriptor := range catalog.Commands {
		totalBytes += len(descriptor.Name) + len(descriptor.InputSchema) + len(descriptor.OutputSchema)
		if descriptor.ModelContract != nil {
			modelContract, err := json.Marshal(descriptor.ModelContract)
			if err != nil {
				return fmt.Errorf("%w: encode model contract", ErrInvalidCapability)
			}
			totalBytes += len(modelContract)
		}
		if len(descriptor.FileProfiles) > 0 || len(descriptor.ServiceProfiles) > 0 ||
			len(descriptor.BrowserProfiles) > 0 || len(descriptor.UpdateProfiles) > 0 ||
			len(descriptor.JobProfiles) > 0 {
			profiles, err := json.Marshal(struct {
				File    []FileProfileDescriptor    `json:"file,omitempty"`
				Service []ServiceProfileDescriptor `json:"service,omitempty"`
				Browser []BrowserProfileDescriptor `json:"browser,omitempty"`
				Update  []UpdateProfileDescriptor  `json:"update,omitempty"`
				Job     []JobProfileDescriptor     `json:"job,omitempty"`
			}{
				File: descriptor.FileProfiles, Service: descriptor.ServiceProfiles,
				Browser: descriptor.BrowserProfiles, Update: descriptor.UpdateProfiles,
				Job: descriptor.JobProfiles,
			})
			if err != nil {
				return fmt.Errorf("%w: encode command profiles", ErrInvalidCapability)
			}
			totalBytes += len(profiles)
		}
		if totalBytes > MaxCatalogBytes {
			return fmt.Errorf("%w: catalog exceeds size limit", ErrInvalidCapability)
		}
		if err := descriptor.Validate(); err != nil {
			return err
		}
		if IsBrowserCommand(descriptor.Name) {
			browserCommandCount++
			if browserProfiles == nil {
				browserProfiles = descriptor.BrowserProfiles
			} else if !reflect.DeepEqual(browserProfiles, descriptor.BrowserProfiles) {
				return fmt.Errorf(
					"%w: browser commands disagree on the current profile set",
					ErrInvalidCapability,
				)
			}
		}
		if _, exists := seen[descriptor.Name]; exists {
			return fmt.Errorf("%w: duplicate command %q", ErrInvalidCapability, descriptor.Name)
		}
		seen[descriptor.Name] = struct{}{}
	}
	if browserCommandCount != 0 {
		if browserCommandCount != len(currentBrowserCommandSpecs) {
			return fmt.Errorf("%w: browser catalog lacks a complete supported command set", ErrInvalidCapability)
		}
		for _, command := range currentBrowserCommandSpecs {
			if _, present := seen[command.name]; !present {
				return fmt.Errorf("%w: browser catalog lacks a complete supported command set", ErrInvalidCapability)
			}
		}
	}
	return nil
}

// Hash returns a stable digest regardless of descriptor or schema key order.
func (catalog CapabilityCatalog) Hash() (string, error) {
	return catalog.HashForProtocol(ProtocolV1)
}

// HashForProtocol returns a stable digest using the selected protocol's
// canonical number representation.
func (catalog CapabilityCatalog) HashForProtocol(protocolVersion int) (string, error) {
	if err := catalog.Validate(); err != nil {
		return "", err
	}
	return catalog.canonicalHashForProtocol(protocolVersion)
}

// canonicalHash hashes catalog bytes without validating command semantics.
// Callers must establish their appropriate invariants first; opaque dispatched
// tombstones use it only to verify the identity stored in their signed plan.
func (catalog CapabilityCatalog) canonicalHash() (string, error) {
	return catalog.canonicalHashForProtocol(ProtocolV1)
}

func (catalog CapabilityCatalog) canonicalHashForProtocol(protocolVersion int) (string, error) {
	protocolVersion, protocolErr := EffectiveProtocolVersion(protocolVersion)
	if protocolErr != nil {
		return "", protocolErr
	}
	commands := append([]CommandDescriptor(nil), catalog.Commands...)
	if commands == nil {
		commands = make([]CommandDescriptor, 0)
	}
	slices.SortFunc(commands, func(a, b CommandDescriptor) int { return cmp.Compare(a.Name, b.Name) })
	for i := range commands {
		var canonicalErr error
		commands[i].InputSchema, canonicalErr = canonicalJSONForProtocol(
			commands[i].InputSchema,
			protocolVersion,
		)
		if canonicalErr != nil {
			return "", canonicalErr
		}
		commands[i].OutputSchema, canonicalErr = canonicalJSONForProtocol(
			commands[i].OutputSchema,
			protocolVersion,
		)
		if canonicalErr != nil {
			return "", canonicalErr
		}
		if commands[i].ModelContract != nil {
			contract := cloneCommandModelContract(*commands[i].ModelContract)
			for exampleIndex := range contract.Examples {
				contract.Examples[exampleIndex], canonicalErr = canonicalJSONForProtocol(
					contract.Examples[exampleIndex],
					protocolVersion,
				)
				if canonicalErr != nil {
					return "", canonicalErr
				}
			}
			commands[i].ModelContract = &contract
		}
	}
	data, err := json.Marshal(CapabilityCatalog{Commands: commands})
	if err != nil {
		return "", fmt.Errorf("marshal capability catalog: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func cloneCommandModelContract(contract CommandModelContract) CommandModelContract {
	contract.Constraints.ExecutableAliases = append(
		[]string(nil),
		contract.Constraints.ExecutableAliases...,
	)
	contract.Constraints.ProfileAliases = append(
		[]string(nil),
		contract.Constraints.ProfileAliases...,
	)
	contract.Constraints.WorkingScopes = append([]string(nil), contract.Constraints.WorkingScopes...)
	contract.Constraints.EnvironmentNames = append(
		[]string(nil),
		contract.Constraints.EnvironmentNames...,
	)
	contract.Guidance = append([]string(nil), contract.Guidance...)
	contract.Examples = append([]json.RawMessage(nil), contract.Examples...)
	for index := range contract.Examples {
		contract.Examples[index] = bytes.Clone(contract.Examples[index])
	}
	if contract.Guidance == nil {
		contract.Guidance = []string{}
	}
	if contract.Examples == nil {
		contract.Examples = []json.RawMessage{}
	}
	return contract
}

type Snapshot struct {
	ID               ID                `json:"id"`
	Aliases          []Alias           `json:"aliases,omitempty"`
	DisplayName      string            `json:"display_name,omitempty"`
	State            State             `json:"state"`
	ProtocolVersion  int               `json:"protocol_version,omitempty"`
	Platform         string            `json:"platform,omitempty"`
	Architecture     string            `json:"architecture,omitempty"`
	SoftwareVersion  string            `json:"software_version,omitempty"`
	CatalogHash      string            `json:"catalog_hash,omitempty"`
	Catalog          CapabilityCatalog `json:"catalog,omitempty"`
	Executor         string            `json:"executor"`
	PolicyRevision   string            `json:"policy_revision"`
	LastSeenAt       int64             `json:"last_seen_at,omitempty"`
	DisconnectReason string            `json:"disconnect_reason,omitempty"`
}

func (snapshot Snapshot) Validate() error {
	if err := snapshot.ID.Validate(); err != nil {
		return err
	}
	if !snapshot.State.Valid() {
		return fmt.Errorf("%w: unsupported state %q", ErrInvalidNode, snapshot.State)
	}
	seen := make(map[Alias]struct{}, len(snapshot.Aliases))
	for _, alias := range snapshot.Aliases {
		if err := alias.Validate(); err != nil {
			return err
		}
		if _, exists := seen[alias]; exists {
			return fmt.Errorf("%w: duplicate alias %q", ErrInvalidNode, alias)
		}
		seen[alias] = struct{}{}
	}
	protocolVersion, protocolErr := EffectiveProtocolVersion(snapshot.ProtocolVersion)
	if protocolErr != nil {
		return protocolErr
	}
	if err := snapshot.Catalog.Validate(); err != nil {
		return err
	}
	if err := (ExecutionProfile{
		Executor:       snapshot.Executor,
		PolicyRevision: snapshot.PolicyRevision,
	}).Validate(); err != nil {
		return err
	}
	if snapshot.CatalogHash == "" {
		return nil
	}
	if !validSHA256Digest(snapshot.CatalogHash) {
		return fmt.Errorf("%w: malformed catalog hash", ErrInvalidNode)
	}
	catalogHash, err := snapshot.Catalog.HashForProtocol(protocolVersion)
	if err != nil {
		return err
	}
	if snapshot.CatalogHash != catalogHash {
		return fmt.Errorf("%w: catalog hash does not match catalog", ErrInvalidNode)
	}
	return nil
}

type Filter struct {
	States []State
	Alias  Alias
}

type Disconnect struct {
	Reason string
	At     int64
}

// PairingApproval is the operator-owned authority granted to a pending or
// already paired node.
// AllowedCommands must be a subset of the capability catalog presented during
// admission; an empty list grants no executable command surface.
type PairingApproval struct {
	Aliases         []Alias
	DisplayName     string
	AllowedCommands []string
	At              int64
}

// Revocation records an operator decision that prevents an identity from
// returning to pending admission on its next connection.
type Revocation struct {
	Reason string
	At     int64
}

// Registration is the durable operator view of a node identity. PublicKey is
// intentionally retained here so authentication can bind approval to the
// exact admitted device rather than to a mutable alias.
type Registration struct {
	Snapshot            Snapshot
	PublicKey           []byte
	KeyAlgorithm        KeyAlgorithm
	RequestedRole       string
	RequestedAt         int64
	AllowedCommands     []string
	ApprovedCatalogHash string
	ApprovedAt          int64
	RevokedAt           int64
}

func validSHA256Digest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

// Registry is the durable node-state boundary. Connection ownership remains
// in the gateway transport layer and is represented here only as snapshots.
type Registry interface {
	List(Filter) ([]Snapshot, error)
	Resolve(string) (Snapshot, bool, error)
	Upsert(Snapshot) error
	MarkDisconnected(ID, Disconnect) error
}

func validateObjectSchema(label string, raw json.RawMessage) error {
	if len(raw) == 0 || len(raw) > MaxSchemaBytes || !json.Valid(raw) {
		return fmt.Errorf("%w: invalid %s schema", ErrInvalidCapability, label)
	}
	value, err := jsonstrict.Decode(raw)
	if err != nil {
		return fmt.Errorf("%w: invalid %s schema: %w", ErrInvalidCapability, label, err)
	}
	if _, ok := value.(map[string]any); !ok {
		return fmt.Errorf("%w: %s schema must be an object", ErrInvalidCapability, label)
	}
	if err := validateJSONSchema(raw); err != nil {
		return fmt.Errorf("%w: invalid %s schema: %w", ErrInvalidCapability, label, err)
	}
	return nil
}

func canonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	return canonicalJSONForProtocol(raw, ProtocolV1)
}

func canonicalJSONForProtocol(raw json.RawMessage, protocolVersion int) (json.RawMessage, error) {
	protocolVersion, err := EffectiveProtocolVersion(protocolVersion)
	if err != nil {
		return nil, err
	}
	var data []byte
	if protocolVersion == ProtocolV2 {
		data, err = jsonstrict.CanonicalV2(raw)
	} else {
		data, err = jsonstrict.Canonical(raw)
	}
	if err != nil {
		return nil, fmt.Errorf("canonicalize json: %w", err)
	}
	return json.RawMessage(data), nil
}
