package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/internal/credentials/githubread"
	"github.com/bogdanovich/mintclaw/internal/credentials/vault"
	"github.com/bogdanovich/mintclaw/internal/secretvalues"
	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
	"github.com/bogdanovich/mintclaw/pkg/routing"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type credentialGitHubReader interface {
	ListPullRequests(context.Context, string, []byte) ([]githubread.PullRequest, error)
}

type credentialReadSource struct {
	runtime *credentialRuntime
	reader  credentialGitHubReader
	agents  map[string]bool
}

func setupCredentialTools(cfg *config.Config, loop *agent.AgentLoop, running *services) error {
	if cfg == nil || loop == nil || running == nil {
		return nil
	}
	return loop.RegisterRuntimeTool("github_pull_requests", func(reload *config.Config) (toolshared.Tool, error) {
		source := &credentialReadSource{runtime: running.Credentials, agents: map[string]bool{}}
		if running.Credentials != nil {
			source.reader = running.Credentials.githubReader
		}
		if reload.Credentials.GitHubReadEnabled {
			for _, account := range reload.Credentials.Accounts {
				if account.GitHub == nil {
					continue
				}
				for _, principal := range account.Principals {
					source.agents[principal.AgentID] = true
				}
			}
		}
		return tools.NewCredentialReadTool(source), nil
	})
}

func (source *credentialReadSource) CredentialReadAvailableForAgent(agentID string) bool {
	return source != nil && source.runtime != nil && source.runtime.githubReadEnabled &&
		strings.TrimSpace(agentID) != "" && source.runtime.consumeAPIKey != nil && source.reader != nil &&
		source.agents[routing.NormalizeAgentID(agentID)]
}

func credentialPrincipalFromContext(
	ctx context.Context,
	runtime *credentialRuntime,
) (credentialbroker.Principal, error) {
	if ctx == nil || runtime == nil {
		return credentialbroker.Principal{}, errCredentialsUnavailable
	}
	actorID := toolshared.CanonicalInboundActorID(ctx)
	agentID := strings.TrimSpace(toolshared.ToolAgentID(ctx))
	if capabilities, ok := toolshared.RuntimeCapabilities(ctx); ok && capabilities.Kind() == runtimecap.KindCoding {
		// Initial output qualification covers the gateway agent pipeline.
		return credentialbroker.Principal{}, errCredentialsUnavailable
	}
	if actorID == "" || agentID == "" {
		return credentialbroker.Principal{}, errCredentialsUnavailable
	}
	return credentialbroker.Principal{
		OwnerID: runtime.ownerID, ActorID: browser.OpaqueActorID(actorID),
		AgentID: routing.NormalizeAgentID(agentID), HostID: runtime.hostID,
	}, nil
}

func credentialReadOutcome(alias, outcome string) string {
	encoded, _ := json.Marshal(map[string]string{"alias": alias, "outcome": outcome})
	return string(encoded)
}

func (source *credentialReadSource) ReadCredentialPullRequests(
	ctx context.Context,
	alias, repository string,
) (result string) {
	defer func() {
		if recover() != nil {
			result = `{"outcome":"unavailable"}`
		}
	}()
	denied := `{"outcome":"denied"}`
	if source == nil {
		return denied
	}
	principal, err := credentialPrincipalFromContext(ctx, source.runtime)
	if err != nil || !source.CredentialReadAvailableForAgent(principal.AgentID) {
		return denied
	}
	scope, canonical, err := source.runtime.controller.GitHubRequest(ctx, principal, alias, repository)
	if err != nil {
		return denied
	}
	lease, err := source.runtime.requestCredentialLease(ctx, scope)
	if errors.Is(err, errCredentialApprovalRequired) {
		return credentialReadOutcome(alias, "approval_required")
	}
	if err != nil {
		return denied
	}
	remaining, err := lease.Remaining(ctx, scope)
	if err != nil {
		return denied
	}
	operationCtx, cancel := context.WithTimeout(ctx, min(remaining, 10*time.Second))
	defer cancel()
	go func() {
		select {
		case <-lease.Done():
			cancel()
		case <-operationCtx.Done():
		}
	}()
	binding, err := lease.Binding(operationCtx, scope)
	if err != nil {
		return denied
	}
	result = credentialReadOutcome(alias, "unavailable")
	err = source.runtime.consumeAPIKey(operationCtx, vault.Binding{VaultID: binding.VaultID, ItemID: binding.ItemID},
		func(key []byte) error {
			if lease.Check(operationCtx, scope) != nil {
				return errCredentialsUnavailable
			}
			release, registerErr := secretvalues.Register(key)
			if registerErr != nil {
				return errCredentialsUnavailable
			}
			defer release()
			pulls, readErr := source.reader.ListPullRequests(operationCtx, canonical, key)
			if readErr != nil || lease.Check(operationCtx, scope) != nil {
				return errCredentialsUnavailable
			}
			encoded, encodeErr := json.Marshal(struct {
				Alias        string                   `json:"alias"`
				Outcome      string                   `json:"outcome"`
				Repository   string                   `json:"repository"`
				PullRequests []githubread.PullRequest `json:"pull_requests"`
			}{alias, "success", canonical, pulls})
			if encodeErr != nil {
				return errCredentialsUnavailable
			}
			result = secretvalues.Redact(string(encoded))
			if !json.Valid([]byte(result)) {
				return errCredentialsUnavailable
			}
			return nil
		})
	if err != nil || lease.Check(operationCtx, scope) != nil {
		return credentialReadOutcome(alias, "unavailable")
	}
	return result
}
