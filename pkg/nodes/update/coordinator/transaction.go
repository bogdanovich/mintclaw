//go:build linux || darwin

package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	nodeupdate "github.com/bogdanovich/mintclaw/pkg/nodes/update"
)

const MaxTransactionTTL = 30 * time.Minute

var (
	ErrTransactionConflict = errors.New("node update transaction identity conflict")
	ErrTransactionBusy     = errors.New("another node update transaction is active")
	ErrUpdateDenied        = errors.New("node update is not admitted")
)

type StageError struct {
	Code string
}

func (failure *StageError) Error() string {
	if failure == nil {
		return "node update staging failed"
	}
	return "node update staging failed: " + failure.Code
}

type StageRequest struct {
	Identity               ExecutionIdentity
	Profile                string
	ReleaseAlias           string
	ExpectedManifestSHA256 string
	ExpectedArtifactSHA256 string
	ExpiresAt              time.Time
}

type ResolvedRelease struct {
	Profile                  string
	ProfileRevision          string
	ReleaseAlias             string
	Tag                      string
	Version                  string
	BaseURL                  string
	RedirectHosts            []string
	Channel                  nodeupdate.Channel
	AllowDowngrade           bool
	AuthorityHash            string
	TrustedKey               nodeupdate.TrustedKey
	RequirePlatformSignature bool
}

type AuthorityResolver interface {
	ResolveUpdateRelease(context.Context, string, string) (ResolvedRelease, error)
}

type Coordinator struct {
	store       *Store
	resolver    AuthorityResolver
	httpClient  *http.Client
	now         func() time.Time
	version     string
	mu          sync.Mutex
	operationMu sync.Mutex
	operation   *stageOperation
}

type stageOperation struct {
	identity ExecutionIdentity
	cancel   context.CancelFunc
	done     chan struct{}
}

type Option func(*Coordinator)

func WithHTTPClient(client *http.Client) Option {
	return func(coordinator *Coordinator) { coordinator.httpClient = client }
}

func WithClock(now func() time.Time) Option {
	return func(coordinator *Coordinator) { coordinator.now = now }
}

func New(
	store *Store,
	resolver AuthorityResolver,
	coordinatorVersion string,
	options ...Option,
) (*Coordinator, error) {
	if store == nil || resolver == nil || !nodeupdate.ValidReleaseVersion(coordinatorVersion) {
		return nil, errors.New("coordinator requires a store, authority resolver, and release version")
	}
	coordinator := &Coordinator{
		store: store, resolver: resolver, version: coordinatorVersion,
		now: time.Now,
		httpClient: &http.Client{
			Timeout: 2 * time.Minute,
			Transport: &http.Transport{
				Proxy: nil,
			},
		},
	}
	for _, option := range options {
		option(coordinator)
	}
	if coordinator.httpClient == nil || coordinator.now == nil {
		return nil, errors.New("coordinator options are invalid")
	}
	return coordinator, nil
}

func (coordinator *Coordinator) Stage(ctx context.Context, request StageRequest) (State, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	now := coordinator.now().UTC()
	if err := request.Validate(now); err != nil {
		return State{}, err
	}
	authority, err := coordinator.resolver.ResolveUpdateRelease(ctx, request.Profile, request.ReleaseAlias)
	if err != nil || authority.Validate() != nil || authority.Profile != request.Profile ||
		authority.ReleaseAlias != request.ReleaseAlias || authority.AuthorityHash != request.Identity.AuthorityHash {
		return State{}, ErrUpdateDenied
	}
	state, err := coordinator.store.Load()
	if err != nil {
		return State{}, err
	}
	requestHash, err := hashStageRequest(request, authority, state.Installation)
	if err != nil {
		return State{}, err
	}
	if state.Transaction != nil {
		if sameExecution(state.Transaction.Identity, request.Identity) {
			if state.Transaction.RequestHash != requestHash {
				return State{}, ErrTransactionConflict
			}
			if state.Transaction.Canceled || terminalPhase(*state.Transaction) {
				return state, nil
			}
			stageContext, finish := coordinator.beginStageOperation(ctx, request.Identity)
			defer finish()
			return coordinator.resumeStage(stageContext, state, authority)
		}
		if !terminalPhase(*state.Transaction) {
			return State{}, ErrTransactionBusy
		}
	}
	if !authority.AllowDowngrade && compareReleaseVersions(authority.Version, state.Active.Version) < 0 {
		return State{}, ErrUpdateDenied
	}
	transaction := &Transaction{
		Identity:         request.Identity,
		RequestHash:      requestHash,
		Profile:          authority.Profile,
		ProfileRevision:  authority.ProfileRevision,
		ReleaseAlias:     authority.ReleaseAlias,
		RequestedRelease: authority.Tag,
		ManifestSHA256:   request.ExpectedManifestSHA256,
		ArtifactSHA256:   request.ExpectedArtifactSHA256,
		Phase:            PhasePrepared,
		AcceptedAt:       now.Unix(),
		ExpiresAt:        request.ExpiresAt.Unix(),
		UpdatedAt:        now.Unix(),
	}
	state.Generation++
	state.Transaction = transaction
	if err = coordinator.store.Commit(state.Generation-1, state); err != nil {
		return State{}, err
	}
	state.Generation++
	state.Transaction.Phase = PhaseDownloading
	if err = coordinator.store.Commit(state.Generation-1, state); err != nil {
		return State{}, err
	}
	stageContext, finish := coordinator.beginStageOperation(ctx, request.Identity)
	defer finish()
	return coordinator.stageResolved(stageContext, state, authority)
}

func (coordinator *Coordinator) resumeStage(
	ctx context.Context,
	state State,
	authority ResolvedRelease,
) (State, error) {
	switch state.Transaction.Phase {
	case PhasePrepared:
		state.Generation++
		state.Transaction.Phase = PhaseDownloading
		state.Transaction.UpdatedAt = min(coordinator.now().UTC().Unix(), state.Transaction.ExpiresAt)
		if err := coordinator.store.Commit(state.Generation-1, state); err != nil {
			return State{}, err
		}
		return coordinator.stageResolved(ctx, state, authority)
	case PhaseDownloading, PhaseVerified:
		return coordinator.stageResolved(ctx, state, authority)
	default:
		return state, nil
	}
}

func (request StageRequest) Validate(now time.Time) error {
	if err := request.Identity.Validate(); err != nil {
		return err
	}
	if !boundedNamePattern.MatchString(request.Profile) || !boundedNamePattern.MatchString(request.ReleaseAlias) ||
		!isDigest(request.ExpectedManifestSHA256, sha256.Size) ||
		!isDigest(request.ExpectedArtifactSHA256, sha256.Size) {
		return errors.New("invalid node update stage request")
	}
	if !request.ExpiresAt.After(now) || request.ExpiresAt.Sub(now) > MaxTransactionTTL {
		return errors.New("node update stage request is expired or unbounded")
	}
	return nil
}

func (authority ResolvedRelease) Validate() error {
	if !boundedNamePattern.MatchString(authority.Profile) ||
		!boundedNamePattern.MatchString(authority.ProfileRevision) ||
		!boundedNamePattern.MatchString(authority.ReleaseAlias) || authority.Tag != authority.Version ||
		!nodeupdate.ValidReleaseVersion(authority.Tag) || !isDigest(authority.AuthorityHash, sha256.Size) ||
		(authority.Channel != nodeupdate.ChannelStable && authority.Channel != nodeupdate.ChannelNightly) {
		return errors.New("invalid resolved node update authority")
	}
	parsed, err := url.Parse(authority.BaseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" || path.Clean(parsed.Path) != parsed.Path {
		return errors.New("invalid resolved node update source")
	}
	if len(authority.RedirectHosts) > 8 {
		return errors.New("too many node update redirect hosts")
	}
	prior := ""
	for _, host := range authority.RedirectHosts {
		if !validRedirectHost(host) || host <= prior {
			return errors.New("invalid or unsorted node update redirect hosts")
		}
		prior = host
	}
	if authority.TrustedKey.KeyID == "" || len(authority.TrustedKey.PublicKey) == 0 {
		return errors.New("node update authority lacks a trusted signing key")
	}
	if nodeupdate.KeyID(authority.TrustedKey.PublicKey) != authority.TrustedKey.KeyID {
		return errors.New("node update authority signing key identity is invalid")
	}
	prerelease := strings.Contains(authority.Version, "-")
	if (authority.Channel == nodeupdate.ChannelStable && prerelease) ||
		(authority.Channel == nodeupdate.ChannelNightly && !prerelease) {
		return errors.New("node update authority release does not match its channel")
	}
	return nil
}

func hashStageRequest(
	request StageRequest,
	authority ResolvedRelease,
	installation Installation,
) (string, error) {
	redirectHosts := append([]string(nil), authority.RedirectHosts...)
	sort.Strings(redirectHosts)
	transcript := struct {
		Domain                   string             `json:"domain"`
		Identity                 ExecutionIdentity  `json:"identity"`
		Installation             Installation       `json:"installation"`
		Profile                  string             `json:"profile"`
		ProfileRevision          string             `json:"profile_revision"`
		ReleaseAlias             string             `json:"release_alias"`
		Tag                      string             `json:"tag"`
		Version                  string             `json:"version"`
		Channel                  nodeupdate.Channel `json:"channel"`
		AuthorityHash            string             `json:"authority_hash"`
		ManifestSHA256           string             `json:"manifest_sha256"`
		ArtifactSHA256           string             `json:"artifact_sha256"`
		ExpiresAt                int64              `json:"expires_at"`
		RedirectHosts            []string           `json:"redirect_hosts"`
		RequirePlatformSignature bool               `json:"require_platform_signature"`
	}{
		Domain: "mintclaw-node-update-stage-v1", Identity: request.Identity, Installation: installation,
		Profile: authority.Profile, ProfileRevision: authority.ProfileRevision,
		ReleaseAlias: authority.ReleaseAlias, Tag: authority.Tag, Version: authority.Version,
		Channel: authority.Channel, AuthorityHash: authority.AuthorityHash,
		ManifestSHA256: request.ExpectedManifestSHA256, ArtifactSHA256: request.ExpectedArtifactSHA256,
		ExpiresAt: request.ExpiresAt.Unix(), RedirectHosts: redirectHosts,
		RequirePlatformSignature: authority.RequirePlatformSignature,
	}
	data, err := json.Marshal(transcript)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func sameExecution(left ExecutionIdentity, right ExecutionIdentity) bool {
	return left == right
}

func terminalPhase(transaction Transaction) bool {
	return transaction.Canceled || transaction.Phase == PhaseHealthy || transaction.Phase == PhaseRolledBack ||
		transaction.Phase == PhaseUnknown || transaction.Phase == PhaseOperatorActionRequired
}

func (coordinator *Coordinator) beginStageOperation(
	ctx context.Context,
	identity ExecutionIdentity,
) (context.Context, func()) {
	stageContext, cancel := context.WithCancel(ctx)
	operation := &stageOperation{identity: identity, cancel: cancel, done: make(chan struct{})}
	coordinator.operationMu.Lock()
	coordinator.operation = operation
	coordinator.operationMu.Unlock()
	return stageContext, func() {
		cancel()
		coordinator.operationMu.Lock()
		if coordinator.operation == operation {
			coordinator.operation = nil
		}
		close(operation.done)
		coordinator.operationMu.Unlock()
	}
}

func validRedirectHost(host string) bool {
	if host == "" || host != strings.ToLower(host) || strings.ContainsAny(host, "/:@[]") {
		return false
	}
	parsed, err := url.Parse("https://" + host)
	return err == nil && parsed.Hostname() == host && parsed.Port() == ""
}

func compareReleaseVersions(left string, right string) int {
	return nodeupdate.CompareReleaseVersions(left, right)
}

func (coordinator *Coordinator) stageResolved(
	ctx context.Context,
	state State,
	authority ResolvedRelease,
) (State, error) {
	return coordinator.stageDownloaded(ctx, state, authority)
}

func (coordinator *Coordinator) failStage(state State, code string) (State, error) {
	now := coordinator.now().UTC().Unix()
	state.Generation++
	state.Transaction.Phase = PhaseOperatorActionRequired
	state.Transaction.FailureCode = code
	state.Transaction.UpdatedAt = min(now, state.Transaction.ExpiresAt)
	if err := coordinator.store.Commit(state.Generation-1, state); err != nil {
		return State{}, &StageError{Code: "state_unknown"}
	}
	return state, &StageError{Code: code}
}

func min(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func (coordinator *Coordinator) Close() error {
	if coordinator == nil || coordinator.store == nil {
		return nil
	}
	return coordinator.store.Close()
}
