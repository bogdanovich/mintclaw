package coding

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	codingremote "github.com/bogdanovich/mintclaw/pkg/coding/remote"
	codingscope "github.com/bogdanovich/mintclaw/pkg/coding/scope"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

const codingRemoteBootstrapTimeout = 750 * time.Millisecond

type codingRemoteDiscoveryClient interface {
	Discover(context.Context, codingremote.Request) (codingremote.CapabilitySnapshot, error)
	Execute(context.Context, codingremote.Request) (codingremote.CapabilityResult, error)
}

type codingRemoteClientFactory func(string) (codingRemoteDiscoveryClient, error)

type codingRemoteBootstrap struct {
	Configured bool
	Available  bool
	Code       string
	Snapshot   *codingremote.CapabilitySnapshot
	Client     codingremote.BrokerClient
}

func newCodingRemoteClient(socketPath string) (codingRemoteDiscoveryClient, error) {
	return codingremote.NewClient(socketPath)
}

// bootstrapCodingRemote is deliberately non-fatal. Explicit remote config
// makes the future remote tool report availability; it never prevents local
// coding or starts a hidden gateway when the broker is absent.
func bootstrapCodingRemote(
	cfg *config.Config,
	request codingTurnRequest,
	factory codingRemoteClientFactory,
) codingRemoteBootstrap {
	if cfg == nil || !cfg.Coding.Remote.Enabled {
		return codingRemoteBootstrap{}
	}
	state := codingRemoteBootstrap{Configured: true, Code: "broker_unavailable"}
	grant, exists := cfg.Execution.CodingRemoteGrants[cfg.Coding.Remote.Grant]
	if !exists || factory == nil {
		state.Code = "configuration_invalid"
		return state
	}
	client, err := factory(cfg.Coding.Remote.SocketPath)
	if err != nil || client == nil {
		state.Code = "endpoint_invalid"
		return state
	}
	state.Client = client
	profile := profileForCodingRemote(request)
	discoveryRequest := codingremote.Request{
		Schema: codingremote.SchemaV1, RequestID: "request_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		Operation: codingremote.OperationCapabilitiesList,
		Grant:     cfg.Coding.Remote.Grant, GrantRevision: grant.Revision,
		ThreadID: request.Metadata.ThreadID, SessionKey: request.Metadata.SessionKey,
		ProjectKey: request.Metadata.Project.ProjectKey, LocalProfile: profile,
	}
	ctx, cancel := context.WithTimeout(context.Background(), codingRemoteBootstrapTimeout)
	defer cancel()
	snapshot, err := client.Discover(ctx, discoveryRequest)
	if err != nil {
		var brokerErr *codingremote.BrokerError
		if errors.As(err, &brokerErr) && brokerErr.Code != "" {
			state.Code = strings.ToLower(brokerErr.Code)
		}
		return state
	}
	state.Available = true
	state.Code = ""
	state.Snapshot = &snapshot
	return state
}

func profileForCodingRemote(request codingTurnRequest) codingscope.Profile {
	profile := request.Profile
	if profile == "" {
		profile = codingscope.ProfileMutate
		if request.ReadOnly {
			profile = codingscope.ProfileInvestigate
		}
	}
	return profile
}
