package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	codingremote "github.com/bogdanovich/mintclaw/pkg/coding/remote"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

func setupCodingRemoteBroker(
	ctx context.Context,
	cfg *config.Config,
	agentLoop *agent.AgentLoop,
) (*codingremote.Server, error) {
	if cfg == nil || !cfg.Gateway.CodingRemote.Enabled {
		return nil, nil
	}
	if agentLoop == nil {
		return nil, fmt.Errorf("coding remote broker requires an agent loop")
	}
	server, err := codingremote.StartServer(
		ctx,
		cfg.Gateway.CodingRemote.SocketPath,
		codingRemoteDiscoveryHandler{config: agentLoop.GetConfig, now: time.Now},
	)
	if err != nil {
		return nil, err
	}
	logger.InfoCF("coding", "Coding remote broker enabled", map[string]any{
		"transport": "same_user_unix",
	})
	return server, nil
}

type codingRemoteDiscoveryHandler struct {
	config func() *config.Config
	now    func() time.Time
}

func (handler codingRemoteDiscoveryHandler) HandleCodingRemote(
	_ context.Context,
	request codingremote.Request,
) codingremote.Response {
	response := codingremote.Response{
		Schema: codingremote.SchemaV1, RequestID: request.RequestID,
		Status: codingremote.ResponseDenied, Code: "GRANT_UNAVAILABLE",
		Message: "coding remote grant is unavailable",
	}
	if handler.config == nil || handler.now == nil {
		response.Status = codingremote.ResponseUnavailable
		response.Code = "BROKER_UNAVAILABLE"
		response.Message = "coding remote broker is unavailable"
		return response
	}
	cfg := handler.config()
	if cfg == nil || !cfg.Gateway.CodingRemote.Enabled {
		response.Status = codingremote.ResponseUnavailable
		response.Code = "BROKER_DISABLED"
		response.Message = "coding remote broker is disabled"
		return response
	}
	grant, exists := cfg.Execution.CodingRemoteGrants[request.Grant]
	if !exists {
		return response
	}
	if grant.Revision != request.GrantRevision {
		response.Code = "GRANT_CHANGED"
		response.Message = "coding remote grant revision changed"
		return response
	}
	if !slices.Contains(grant.LocalProfiles, request.LocalProfile) {
		response.Code = "PROFILE_DENIED"
		response.Message = "local coding profile is not granted"
		return response
	}
	snapshot := codingremote.CapabilitySnapshot{
		Schema: codingremote.SchemaV1, Grant: request.Grant, GrantRevision: grant.Revision,
		DiscoveryRevision: codingRemoteDiscoveryRevision(cfg, request.Grant, grant),
		GeneratedAtUnixMS: handler.now().UnixMilli(),
		Capabilities:      []codingremote.CapabilityDescriptor{},
		TaskScopes:        []codingremote.TaskScopeDescriptor{},
	}
	return codingremote.Response{
		Schema: codingremote.SchemaV1, RequestID: request.RequestID,
		Status: codingremote.ResponseOK, Snapshot: &snapshot,
	}
}

func codingRemoteDiscoveryRevision(
	cfg *config.Config,
	grantAlias string,
	grant config.CodingRemoteClientGrant,
) string {
	digest := sha256.New()
	_, _ = fmt.Fprintf(
		digest,
		"mintclaw:coding-remote-discovery:v1\x00%s\x00%s\x00%s\n",
		grantAlias,
		grant.Revision,
		grant.Agent,
	)
	profiles := profileStrings(grant.LocalProfiles)
	sort.Strings(profiles)
	for _, profile := range profiles {
		_, _ = fmt.Fprintf(digest, "local:%s\n", profile)
	}
	capabilities := append([]string(nil), grant.Capabilities...)
	sort.Strings(capabilities)
	for _, alias := range capabilities {
		capability := cfg.Execution.CodingRemoteCapabilities[alias]
		_, _ = fmt.Fprintf(digest, "capability:%s:%s\n", alias, capability.Revision)
	}
	tasks := append([]config.CodingRemoteTaskGrant(nil), grant.Tasks...)
	sort.Slice(tasks, func(left, right int) bool { return tasks[left].Scope < tasks[right].Scope })
	for _, task := range tasks {
		scope := cfg.Execution.RemoteCodingScopes[task.Scope]
		_, _ = fmt.Fprintf(digest, "task:%s:%s\n", task.Scope, scope.Revision)
		profiles = profileStrings(task.Profiles)
		sort.Strings(profiles)
		for _, profile := range profiles {
			_, _ = fmt.Fprintf(digest, "task-profile:%s\n", profile)
		}
	}
	return "discovery_" + hex.EncodeToString(digest.Sum(nil))
}

func profileStrings[T ~string](profiles []T) []string {
	result := make([]string, len(profiles))
	for index, profile := range profiles {
		result[index] = string(profile)
	}
	return result
}
