package gateway

import (
	"testing"
	"time"

	codingremote "github.com/bogdanovich/mintclaw/pkg/coding/remote"
	codingscope "github.com/bogdanovich/mintclaw/pkg/coding/scope"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestCodingRemoteDiscoveryRequiresExactGrantAndProfile(t *testing.T) {
	cfg := gatewayCodingRemoteTestConfig()
	handler := codingRemoteDiscoveryHandler{
		config: func() *config.Config { return cfg },
		now:    func() time.Time { return time.UnixMilli(1234) },
	}
	request := codingremote.Request{
		Schema: codingremote.SchemaV1, RequestID: "request-one",
		Operation: codingremote.OperationCapabilitiesList,
		Grant:     "local-development", GrantRevision: "grant-v1",
		LocalProfile: codingscope.ProfileMutate,
	}
	response := handler.HandleCodingRemote(t.Context(), request)
	if response.Status != codingremote.ResponseOK || response.Snapshot == nil ||
		response.Snapshot.Grant != request.Grant || response.Snapshot.GrantRevision != request.GrantRevision ||
		response.Snapshot.GeneratedAtUnixMS != 1234 || len(response.Snapshot.Capabilities) != 0 ||
		len(response.Snapshot.TaskScopes) != 0 {
		t.Fatalf("HandleCodingRemote() = %#v", response)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("response.Validate() error = %v", err)
	}

	changed := request
	changed.GrantRevision = "old"
	response = handler.HandleCodingRemote(t.Context(), changed)
	if response.Status != codingremote.ResponseDenied || response.Code != "GRANT_CHANGED" ||
		response.Snapshot != nil {
		t.Fatalf("changed grant response = %#v", response)
	}
	denied := request
	denied.LocalProfile = codingscope.ProfileInvestigate
	response = handler.HandleCodingRemote(t.Context(), denied)
	if response.Status != codingremote.ResponseDenied || response.Code != "PROFILE_DENIED" {
		t.Fatalf("profile response = %#v", response)
	}
	missing := request
	missing.Grant = "missing"
	response = handler.HandleCodingRemote(t.Context(), missing)
	if response.Status != codingremote.ResponseDenied || response.Code != "GRANT_UNAVAILABLE" {
		t.Fatalf("missing grant response = %#v", response)
	}
}

func TestCodingRemoteDiscoveryRevisionBindsAuthorityIndependentOfOrdering(t *testing.T) {
	cfg := gatewayCodingRemoteTestConfig()
	grant := cfg.Execution.CodingRemoteGrants["local-development"]
	first := codingRemoteDiscoveryRevision(cfg, "local-development", grant)
	grant.Capabilities = []string{"status", "workspace"}
	grant.LocalProfiles = []codingscope.Profile{codingscope.ProfileMutate}
	grant.Tasks[0].Profiles = []codingscope.Profile{
		codingscope.ProfileMutate,
		codingscope.ProfileInvestigate,
	}
	second := codingRemoteDiscoveryRevision(cfg, "local-development", grant)
	if first != second {
		t.Fatalf("reordered discovery revisions = %q / %q", first, second)
	}
	capability := cfg.Execution.CodingRemoteCapabilities["status"]
	capability.Revision = "cap-v2"
	cfg.Execution.CodingRemoteCapabilities["status"] = capability
	third := codingRemoteDiscoveryRevision(cfg, "local-development", grant)
	if third == second {
		t.Fatal("capability revision did not change discovery revision")
	}
}

func TestCodingRemoteDiscoveryFailsClosedWhenDisabled(t *testing.T) {
	cfg := gatewayCodingRemoteTestConfig()
	cfg.Gateway.CodingRemote.Enabled = false
	handler := codingRemoteDiscoveryHandler{
		config: func() *config.Config { return cfg },
		now:    time.Now,
	}
	response := handler.HandleCodingRemote(t.Context(), codingremote.Request{RequestID: "request-one"})
	if response.Status != codingremote.ResponseUnavailable || response.Code != "BROKER_DISABLED" ||
		response.Snapshot != nil {
		t.Fatalf("disabled response = %#v", response)
	}
}

func gatewayCodingRemoteTestConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Gateway.CodingRemote.Enabled = true
	cfg.Execution.CodingRemoteCapabilities = map[string]config.CodingRemoteCapability{
		"workspace": {Revision: "cap-v1"},
		"status":    {Revision: "cap-v1"},
	}
	cfg.Execution.RemoteCodingScopes = map[string]config.RemoteCodingScope{
		"mintclaw-dev": {Revision: "scope-v1"},
	}
	cfg.Execution.CodingRemoteGrants = map[string]config.CodingRemoteClientGrant{
		"local-development": {
			Revision: "grant-v1", Agent: "main", LocalProfiles: []codingscope.Profile{codingscope.ProfileMutate},
			Capabilities: []string{"workspace", "status"},
			Tasks: []config.CodingRemoteTaskGrant{{
				Scope:    "mintclaw-dev",
				Profiles: []codingscope.Profile{codingscope.ProfileInvestigate, codingscope.ProfileMutate},
			}},
		},
	}
	return cfg
}
