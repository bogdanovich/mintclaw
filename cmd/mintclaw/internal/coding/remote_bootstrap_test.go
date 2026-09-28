package coding

import (
	"context"
	"strings"
	"testing"
	"time"

	codingremote "github.com/bogdanovich/mintclaw/pkg/coding/remote"
	codingscope "github.com/bogdanovich/mintclaw/pkg/coding/scope"
	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

type codingRemoteDiscoveryClientFunc func(
	context.Context,
	codingremote.Request,
) (codingremote.CapabilitySnapshot, error)

func (fn codingRemoteDiscoveryClientFunc) Discover(
	ctx context.Context,
	request codingremote.Request,
) (codingremote.CapabilitySnapshot, error) {
	return fn(ctx, request)
}

func (codingRemoteDiscoveryClientFunc) Execute(
	context.Context,
	codingremote.Request,
) (codingremote.CapabilityResult, error) {
	return codingremote.CapabilityResult{}, context.Canceled
}

func (codingRemoteDiscoveryClientFunc) Artifact(
	context.Context,
	codingremote.Request,
) (codingremote.ArtifactResult, error) {
	return codingremote.ArtifactResult{}, context.Canceled
}

func TestBootstrapCodingRemoteLeavesDisabledRuntimeUntouched(t *testing.T) {
	cfg := config.DefaultConfig()
	called := false
	state := bootstrapCodingRemote(cfg, codingRemoteBootstrapRequest(false), func(string) (
		codingRemoteDiscoveryClient,
		error,
	) {
		called = true
		return nil, nil
	})
	if called || state != (codingRemoteBootstrap{}) {
		t.Fatalf("bootstrapCodingRemote() = %#v, factory called = %v", state, called)
	}
}

func TestBootstrapCodingRemoteDiscoversExactThreadAuthority(t *testing.T) {
	cfg := codingRemoteBootstrapConfig()
	wantSnapshot := codingremote.CapabilitySnapshot{
		Schema: codingremote.SchemaV1, Grant: "local-development", GrantRevision: "grant-v1",
		DiscoveryRevision: "discovery-v1", GeneratedAtUnixMS: time.Now().UnixMilli(),
		Capabilities: []codingremote.CapabilityDescriptor{}, TaskScopes: []codingremote.TaskScopeDescriptor{},
	}
	state := bootstrapCodingRemote(cfg, codingRemoteBootstrapRequest(true), func(socketPath string) (
		codingRemoteDiscoveryClient,
		error,
	) {
		if socketPath != "/tmp/mintclaw-coding-remote.sock" {
			t.Fatalf("socket path = %q", socketPath)
		}
		return codingRemoteDiscoveryClientFunc(func(
			ctx context.Context,
			request codingremote.Request,
		) (codingremote.CapabilitySnapshot, error) {
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > codingRemoteBootstrapTimeout {
				t.Fatalf("discovery context deadline = %v, %v", deadline, ok)
			}
			if err := request.Validate(); err != nil {
				t.Fatalf("request.Validate() error = %v", err)
			}
			if request.Grant != "local-development" || request.GrantRevision != "grant-v1" ||
				request.ThreadID != "11111111-1111-4111-8111-111111111111" ||
				request.SessionKey != "coding:11111111-1111-4111-8111-111111111111" ||
				request.ProjectKey != "directory:"+strings.Repeat("a", 64) ||
				request.LocalProfile != codingscope.ProfileInvestigate {
				t.Fatalf("discovery request = %#v", request)
			}
			return wantSnapshot, nil
		}), nil
	})
	if !state.Configured || !state.Available || state.Code != "" || state.Snapshot == nil ||
		state.Snapshot.DiscoveryRevision != wantSnapshot.DiscoveryRevision {
		t.Fatalf("bootstrapCodingRemote() = %#v", state)
	}
}

func TestBootstrapCodingRemoteProjectsSafeBrokerFailure(t *testing.T) {
	cfg := codingRemoteBootstrapConfig()
	state := bootstrapCodingRemote(cfg, codingRemoteBootstrapRequest(false), func(string) (
		codingRemoteDiscoveryClient,
		error,
	) {
		return codingRemoteDiscoveryClientFunc(func(
			context.Context,
			codingremote.Request,
		) (codingremote.CapabilitySnapshot, error) {
			return codingremote.CapabilitySnapshot{}, &codingremote.BrokerError{
				Status: codingremote.ResponseDenied,
				Code:   "GRANT_CHANGED",
			}
		}), nil
	})
	if !state.Configured || state.Available || state.Code != "grant_changed" || state.Snapshot != nil ||
		state.Client == nil {
		t.Fatalf("bootstrapCodingRemote() = %#v", state)
	}
}

func TestBootstrapCodingRemoteRejectsMissingLocalGrantBeforeDial(t *testing.T) {
	cfg := codingRemoteBootstrapConfig()
	cfg.Coding.Remote.Grant = "missing"
	called := false
	state := bootstrapCodingRemote(cfg, codingRemoteBootstrapRequest(false), func(string) (
		codingRemoteDiscoveryClient,
		error,
	) {
		called = true
		return nil, nil
	})
	if called || !state.Configured || state.Available || state.Code != "configuration_invalid" {
		t.Fatalf("bootstrapCodingRemote() = %#v, factory called = %v", state, called)
	}
}

func codingRemoteBootstrapConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Coding.Remote = config.CodingRemoteClient{
		Enabled: true, SocketPath: "/tmp/mintclaw-coding-remote.sock", Grant: "local-development",
	}
	cfg.Execution.CodingRemoteGrants = map[string]config.CodingRemoteClientGrant{
		"local-development": {Revision: "grant-v1"},
	}
	return cfg
}

func codingRemoteBootstrapRequest(readOnly bool) codingTurnRequest {
	const threadID = "11111111-1111-4111-8111-111111111111"
	return codingTurnRequest{
		ReadOnly: readOnly,
		Metadata: thread.Metadata{
			ThreadID:   threadID,
			SessionKey: thread.SessionKey(threadID),
			Project: thread.ProjectIdentity{
				ProjectKey: "directory:" + strings.Repeat("a", 64),
			},
		},
	}
}
