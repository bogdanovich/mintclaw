package companion

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	pathpkg "path"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
	nodeupdate "github.com/bogdanovich/mintclaw/pkg/nodes/update"
	"github.com/bogdanovich/mintclaw/pkg/nodes/update/control"
)

const (
	updateManifestAsset  = "mintclaw-node-manifest.json"
	updateSignatureAsset = "mintclaw-node-manifest.sig"
)

// UpdateCoordinator is the narrow stable-coordinator control surface used by
// the replaceable companion payload.
type UpdateCoordinator interface {
	Call(context.Context, control.Request) (control.Response, error)
}

// WithManagedUpdates resolves the authenticated release catalog before the
// command runtime is created. A managed coordinator is mandatory; callers
// omit the returned option when update authority cannot be proven.
func WithManagedUpdates(
	ctx context.Context,
	sources UpdateSources,
	policies UpdatePolicies,
	coordinator UpdateCoordinator,
	currentVersion string,
) (RuntimeOption, error) {
	handler, err := newUpdateCommandHandler(
		ctx,
		sources,
		policies,
		coordinator,
		currentVersion,
		nil,
	)
	if err != nil {
		return nil, err
	}
	return withUpdateHandler(handler), nil
}

type updateCommandHandler struct {
	descriptorValue nodes.CommandDescriptor
	coordinator     UpdateCoordinator
}

type nodeUpdateResult struct {
	RequestedRelease    string `json:"requested_release,omitempty"`
	PreviousRelease     string `json:"previous_release,omitempty"`
	State               string `json:"state"`
	ActivationAttempted bool   `json:"activation_attempted"`
	SuccessorVerified   bool   `json:"successor_verified"`
	RollbackAttempted   bool   `json:"rollback_attempted"`
	RollbackVerified    bool   `json:"rollback_verified"`
	InstalledVersion    string `json:"installed_version,omitempty"`
	ErrorCode           string `json:"error_code,omitempty"`
	RecoveryAction      string `json:"recovery_action,omitempty"`
}

func newUpdateCommandHandler(
	ctx context.Context,
	sources UpdateSources,
	policies UpdatePolicies,
	coordinator UpdateCoordinator,
	currentVersion string,
	httpClient *http.Client,
) (*updateCommandHandler, error) {
	if coordinator == nil {
		return nil, errors.New("node update coordinator is unavailable")
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return nil, errors.New("node updates are unsupported on this platform")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return nil, errors.New("node updates are unsupported on this architecture")
	}
	if !nodeupdate.ValidReleaseVersion(currentVersion) {
		return nil, errors.New("managed node version is not a release version")
	}
	profiles, err := resolveUpdateProfiles(
		ctx,
		sources,
		policies,
		currentVersion,
		runtime.GOOS,
		runtime.GOARCH,
		httpClient,
	)
	if err != nil {
		return nil, err
	}
	descriptor := nodes.CommandDescriptor{
		Name: "node.update.v1", InputSchema: nodes.NodeUpdateInputSchema(profiles),
		OutputSchema: nodeUpdateOutputSchema(), Risk: nodes.RiskPrivileged,
		SupportsCancel: true, UpdateProfiles: profiles,
	}
	if err = descriptor.Validate(); err != nil {
		return nil, err
	}
	return &updateCommandHandler{descriptorValue: descriptor, coordinator: coordinator}, nil
}

func (handler *updateCommandHandler) descriptor() nodes.CommandDescriptor {
	return handler.descriptorValue
}

func (handler *updateCommandHandler) authorize(plan nodes.ExecutionPlan) error {
	if plan.Update == nil || plan.Update.Validate() != nil || plan.Command != "node.update.v1" {
		return nodes.ErrCommandDenied
	}
	return nil
}

func (handler *updateCommandHandler) execute(
	ctx context.Context,
	invocation commandInvocation,
) (any, error) {
	authority := invocation.Plan.Update
	if authority == nil {
		return nil, newCommandFailure("COMMAND_DENIED", "node update authority is unavailable", nodes.ErrCommandDenied)
	}
	response, err := handler.coordinator.Call(ctx, control.Request{
		Kind: control.KindUpdate,
		Update: &control.UpdateRequest{
			Identity: updateControlIdentity(invocation.Plan, *authority),
			Profile:  authority.Profile, ReleaseAlias: authority.ReleaseAlias,
			ExpectedManifestSHA256: authority.ManifestSHA256,
			ExpectedArtifactSHA256: authority.ArtifactSHA256,
			ExpiresAt:              invocation.Plan.ExpiresAt,
		},
	})
	if err != nil {
		if ctx.Err() != nil {
			return handler.cancelAfterSignal(invocation.Plan, *authority)
		}
		return nil, fmt.Errorf("%w: coordinator update response unavailable", ErrInvocationOutcomeUnknown)
	}
	result := updateResult(response)
	if result.RequestedRelease == "" && definitiveUpdatePreacceptError(response.ErrorCode) {
		return nil, newCommandFailure(
			"UPDATE_DENIED",
			"node update was denied before durable acceptance",
			errors.New(response.ErrorCode),
		)
	}
	if !updateObservationBound(response, authority.ReleaseVersion) {
		return nil, fmt.Errorf("%w: coordinator update response is not transaction-bound", ErrInvocationOutcomeUnknown)
	}
	if result.State == "canceled" {
		return nil, fmt.Errorf("%w: canceled update requires ledger reconciliation", ErrInvocationOutcomeUnknown)
	}
	if updateObservationTerminal(result.State) {
		return result, nil
	}
	return nil, fmt.Errorf("%w: node update requires status recovery", ErrInvocationOutcomeUnknown)
}

func definitiveUpdatePreacceptError(code string) bool {
	switch code {
	case "update_denied", "identity_conflict", "update_busy":
		return true
	default:
		return false
	}
}

func (handler *updateCommandHandler) cancelAfterSignal(
	plan nodes.ExecutionPlan,
	authority nodes.NodeUpdatePlanAuthority,
) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := handler.coordinator.Call(ctx, control.Request{
		Kind:     control.KindCancel,
		Identity: pointerToUpdateControlIdentity(updateControlIdentity(plan, authority)),
	})
	if err != nil {
		return nil, fmt.Errorf("%w: update cancellation outcome is unknown", ErrInvocationOutcomeUnknown)
	}
	result := updateResult(response)
	if !updateObservationBound(response, authority.ReleaseVersion) {
		return nil, fmt.Errorf("%w: update cancellation response is not transaction-bound", ErrInvocationOutcomeUnknown)
	}
	if result.State == "canceled" {
		return nil, fmt.Errorf("%w: coordinator confirmed cancellation", errCommandCancellationConfirmed)
	}
	return nil, fmt.Errorf("%w: update cancellation was too late", ErrInvocationOutcomeUnknown)
}

func queryUpdateStatus(
	ctx context.Context,
	coordinator UpdateCoordinator,
	record nodes.InvocationRecord,
) (nodeUpdateResult, bool, bool, error) {
	identity, ok := updateControlIdentityFromRecord(record)
	if !ok {
		return nodeUpdateResult{}, false, false, errors.New("update recovery authority is unavailable")
	}
	response, err := coordinator.Call(ctx, control.Request{
		Kind: control.KindStatus, Identity: &identity,
	})
	if err != nil {
		return nodeUpdateResult{}, false, false, err
	}
	result := updateResult(response)
	if !updateObservationBound(response, record.Update.ReleaseVersion) {
		return nodeUpdateResult{}, false, false, ErrInvocationOutcomeUnknown
	}
	terminal := updateObservationTerminal(result.State)
	return result, terminal, result.State == "canceled", nil
}

func cancelUpdate(
	ctx context.Context,
	coordinator UpdateCoordinator,
	record nodes.InvocationRecord,
) (nodeUpdateResult, error) {
	identity, ok := updateControlIdentityFromRecord(record)
	if !ok {
		return nodeUpdateResult{}, errors.New("update cancellation authority is unavailable")
	}
	response, err := coordinator.Call(ctx, control.Request{
		Kind: control.KindCancel, Identity: &identity,
	})
	if err != nil {
		return nodeUpdateResult{}, err
	}
	if !updateObservationBound(response, record.Update.ReleaseVersion) {
		return nodeUpdateResult{}, ErrInvocationOutcomeUnknown
	}
	return updateResult(response), nil
}

func updateControlIdentity(
	plan nodes.ExecutionPlan,
	authority nodes.NodeUpdatePlanAuthority,
) control.ExecutionIdentity {
	return control.ExecutionIdentity{
		InvocationID: plan.InvocationID, ExecutionID: authority.ExecutionID,
		PlanHash: plan.PlanHash, CatalogHash: plan.CatalogHash, AuthorityHash: authority.AuthorityHash,
	}
}

func updateControlIdentityFromRecord(record nodes.InvocationRecord) (control.ExecutionIdentity, bool) {
	if record.Update == nil || record.Update.Validate() != nil {
		return control.ExecutionIdentity{}, false
	}
	return control.ExecutionIdentity{
		InvocationID: record.InvocationID, ExecutionID: record.Update.ExecutionID,
		PlanHash: record.PlanHash, CatalogHash: record.CatalogHash, AuthorityHash: record.Update.AuthorityHash,
	}, true
}

func pointerToUpdateControlIdentity(identity control.ExecutionIdentity) *control.ExecutionIdentity {
	return &identity
}

func updateResult(response control.Response) nodeUpdateResult {
	result := nodeUpdateResult{
		RequestedRelease:    response.Observation.RequestedRelease,
		PreviousRelease:     response.Observation.PreviousRelease,
		State:               response.Observation.Phase,
		ActivationAttempted: response.Observation.ActivationAttempted,
		SuccessorVerified:   response.Observation.SuccessorVerified,
		RollbackAttempted:   response.Observation.RollbackAttempted,
		RollbackVerified:    response.Observation.RollbackVerified,
		InstalledVersion:    response.Observation.InstalledVersion,
		ErrorCode:           response.Observation.FailureCode,
	}
	if result.State == "" {
		result.State = "unknown"
	}
	if result.State == "unknown" || result.State == "operator_action_required" {
		result.RecoveryAction = "inspect the managed node locally; do not replay the update"
	}
	return result
}

func updateObservationBound(response control.Response, expectedRelease string) bool {
	return response.ErrorCode == "" &&
		expectedRelease != "" &&
		response.Observation.RequestedRelease == expectedRelease
}

func updateObservationTerminal(state string) bool {
	switch state {
	case "healthy", "rolled_back", "unknown", "operator_action_required", "canceled":
		return true
	default:
		return false
	}
}

func nodeUpdateOutputSchema() json.RawMessage {
	return json.RawMessage(
		`{"type":"object","required":["state","activation_attempted","successor_verified","rollback_attempted","rollback_verified"],"properties":{"requested_release":{"type":"string"},"previous_release":{"type":"string"},"state":{"type":"string","enum":["healthy","rolled_back","unknown","operator_action_required","canceled"]},"activation_attempted":{"type":"boolean"},"successor_verified":{"type":"boolean"},"rollback_attempted":{"type":"boolean"},"rollback_verified":{"type":"boolean"},"installed_version":{"type":"string"},"error_code":{"type":"string"},"recovery_action":{"type":"string"}},"additionalProperties":false}`,
	)
}

func resolveUpdateProfiles(
	ctx context.Context,
	sources UpdateSources,
	policies UpdatePolicies,
	currentVersion string,
	platform string,
	architecture string,
	httpClient *http.Client,
) ([]nodes.UpdateProfileDescriptor, error) {
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{Proxy: nil},
		}
	}
	profiles := make([]nodes.UpdateProfileDescriptor, 0, len(policies))
	for alias, policy := range policies {
		if !policy.Enabled {
			continue
		}
		source, found := sources[policy.Source]
		if !found || source.Revoked {
			return nil, errors.New("node update source is unavailable")
		}
		profile := nodes.UpdateProfileDescriptor{
			Alias: alias, Revision: policy.Revision, Channel: string(policy.Channel),
			Approval: policy.Approval, CurrentVersion: currentVersion,
			Platform: platform, Architecture: architecture, Downgrade: policy.AllowDowngrade,
		}
		for releaseAlias, release := range policy.Releases {
			resolved, err := resolveUpdateRelease(
				ctx,
				httpClient,
				source,
				policy,
				alias,
				releaseAlias,
				release,
				platform,
				architecture,
			)
			if err != nil {
				return nil, fmt.Errorf("resolve node update release %q: %w", releaseAlias, err)
			}
			profile.Releases = append(profile.Releases, resolved)
		}
		slices.SortFunc(profile.Releases, func(a, b nodes.UpdateReleaseDescriptor) int {
			return cmp.Compare(a.Alias, b.Alias)
		})
		profiles = append(profiles, profile)
	}
	slices.SortFunc(profiles, func(a, b nodes.UpdateProfileDescriptor) int { return cmp.Compare(a.Alias, b.Alias) })
	if len(profiles) == 0 {
		return nil, errors.New("node update policy grants no enabled profile")
	}
	return profiles, nil
}

func resolveUpdateRelease(
	ctx context.Context,
	httpClient *http.Client,
	source UpdateSourceConfig,
	policy UpdatePolicyProfile,
	profileAlias string,
	releaseAlias string,
	release UpdateReleaseConfig,
	platform string,
	architecture string,
) (nodes.UpdateReleaseDescriptor, error) {
	manifestURL, err := updateReleaseAssetURL(source.BaseURL, release.Tag, updateManifestAsset)
	if err != nil {
		return nodes.UpdateReleaseDescriptor{}, err
	}
	signatureURL, err := updateReleaseAssetURL(source.BaseURL, release.Tag, updateSignatureAsset)
	if err != nil {
		return nodes.UpdateReleaseDescriptor{}, err
	}
	manifestData, err := fetchUpdateMetadata(ctx, httpClient, manifestURL, nodeupdate.MaxManifestBytes, source)
	if err != nil {
		return nodes.UpdateReleaseDescriptor{}, err
	}
	signatureData, err := fetchUpdateMetadata(ctx, httpClient, signatureURL, nodeupdate.MaxSignatureBytes, source)
	if err != nil {
		return nodes.UpdateReleaseDescriptor{}, err
	}
	manifest, err := nodeupdate.Verify(manifestData, signatureData, source.trustedKey)
	if err != nil || manifest.Release != release.Tag || manifest.Channel != policy.Channel {
		return nodes.UpdateReleaseDescriptor{}, errors.New("release manifest is not trusted")
	}
	var artifact nodeupdate.Artifact
	found := false
	for _, candidate := range manifest.Artifacts {
		if candidate.Platform == platform && candidate.Architecture == architecture {
			artifact = candidate
			found = true
			break
		}
	}
	if !found {
		return nodes.UpdateReleaseDescriptor{}, errors.New("release lacks the local platform artifact")
	}
	authorityHash, err := nodeupdate.HashReleaseAuthority(nodeupdate.ReleaseAuthority{
		Profile: profileAlias, ProfileRevision: policy.Revision, ReleaseAlias: releaseAlias,
		Tag: release.Tag, Version: release.Version, BaseURL: source.BaseURL,
		RedirectHosts: source.RedirectHosts, Channel: policy.Channel,
		AllowDowngrade: policy.AllowDowngrade, KeyID: source.trustedKey.KeyID,
		RequirePlatformSignature: source.RequirePlatformSignature,
	})
	if err != nil {
		return nodes.UpdateReleaseDescriptor{}, err
	}
	manifestDigest := sha256.Sum256(manifestData)
	return nodes.UpdateReleaseDescriptor{
		Alias: releaseAlias, Version: release.Version, Description: release.Description,
		ManifestSHA256: hex.EncodeToString(manifestDigest[:]), ArtifactSHA256: artifact.SHA256,
		ArtifactSize: artifact.Size, AuthorityHash: authorityHash,
	}, nil
}

func updateReleaseAssetURL(baseURL string, tag string, asset string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", errors.New("invalid update source")
	}
	parsed.Path = pathpkg.Join(parsed.Path, tag, asset)
	return parsed.String(), nil
}

func fetchUpdateMetadata(
	ctx context.Context,
	httpClient *http.Client,
	assetURL string,
	maximum int64,
	source UpdateSourceConfig,
) ([]byte, error) {
	client := *httpClient
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 8 || request.URL.Scheme != "https" || !updateRedirectAllowed(request.URL.Hostname(), source) {
			return errors.New("update metadata redirect denied")
		}
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK || response.ContentLength > maximum {
		return nil, errors.New("update metadata is unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil || int64(len(data)) > maximum {
		return nil, errors.New("update metadata exceeds its bound")
	}
	return data, nil
}

func updateRedirectAllowed(host string, source UpdateSourceConfig) bool {
	base, err := url.Parse(source.BaseURL)
	if err == nil && strings.EqualFold(host, base.Hostname()) {
		return true
	}
	for _, allowed := range source.RedirectHosts {
		if host == allowed {
			return true
		}
	}
	return false
}
