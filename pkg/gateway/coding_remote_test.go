package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/browser"
	codingremote "github.com/bogdanovich/mintclaw/pkg/coding/remote"
	codingscope "github.com/bogdanovich/mintclaw/pkg/coding/scope"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type codingRemoteDiscoverySource struct {
	tools.NodeInvocationSource
	record tools.NodeDiscoveryRecord
}

func (source *codingRemoteDiscoverySource) Lookup(string) (tools.NodeDiscoveryRecord, bool, error) {
	return source.record, true, nil
}

type codingRemoteRetainedSource struct {
	tools.NodeInvocationSource
	record      nodes.GatewayInvocationRecord
	remote      nodes.InvocationRecord
	queryCalls  int
	cancelCalls int
}

type codingRemoteBrowserSource struct {
	tools.BrowserToolSource
	openCalls int
	session   browser.Session
}

type fakeCodingRemoteTaskCoordinator struct {
	view     agent.RemoteCodingTaskView
	err      error
	starts   []agent.RemoteCodingTaskStart
	statuses []agent.RemoteCodingTaskControl
	steers   []agent.RemoteCodingTaskControl
	answers  []agent.RemoteCodingTaskControl
	cancels  []agent.RemoteCodingTaskControl
}

func (coordinator *fakeCodingRemoteTaskCoordinator) Start(
	_ context.Context,
	request agent.RemoteCodingTaskStart,
) (agent.RemoteCodingTaskView, error) {
	coordinator.starts = append(coordinator.starts, request)
	return coordinator.view, coordinator.err
}

func (coordinator *fakeCodingRemoteTaskCoordinator) Status(
	_ context.Context,
	request agent.RemoteCodingTaskControl,
) (agent.RemoteCodingTaskView, error) {
	coordinator.statuses = append(coordinator.statuses, request)
	return coordinator.view, coordinator.err
}

func (coordinator *fakeCodingRemoteTaskCoordinator) Steer(
	_ context.Context,
	request agent.RemoteCodingTaskControl,
) (agent.RemoteCodingTaskView, error) {
	coordinator.steers = append(coordinator.steers, request)
	return coordinator.view, coordinator.err
}

func (coordinator *fakeCodingRemoteTaskCoordinator) Answer(
	_ context.Context,
	request agent.RemoteCodingTaskControl,
) (agent.RemoteCodingTaskView, error) {
	coordinator.answers = append(coordinator.answers, request)
	return coordinator.view, coordinator.err
}

func (coordinator *fakeCodingRemoteTaskCoordinator) Cancel(
	_ context.Context,
	request agent.RemoteCodingTaskControl,
) (agent.RemoteCodingTaskView, error) {
	coordinator.cancels = append(coordinator.cancels, request)
	return coordinator.view, coordinator.err
}

func (source *codingRemoteBrowserSource) Available() bool                 { return true }
func (source *codingRemoteBrowserSource) ScreenshotAvailable() bool       { return false }
func (source *codingRemoteBrowserSource) ArtifactTransferAvailable() bool { return false }
func (source *codingRemoteBrowserSource) DownloadAvailable() bool         { return false }
func (source *codingRemoteBrowserSource) HandoffAvailable() bool          { return false }

func (source *codingRemoteBrowserSource) PassiveTargetDiagnostics(
	_ context.Context,
	_ string,
	profiles []string,
) (tools.BrowserTargetDiagnostics, error) {
	readiness := make(map[string]browser.PassiveReadiness, len(profiles))
	for _, profile := range profiles {
		readiness[profile] = browser.PassiveReadiness{
			Status: browser.ReadinessReady, Broker: browser.ReadinessReady,
			Worker: browser.ReadinessReady, Driver: browser.ReadinessReady,
			Browser: browser.ReadinessReady, Proxy: browser.ReadinessReady,
			Compatibility: browser.CompatibilityCompatible,
			Profile:       browser.ProfileAvailability{Status: browser.ReadinessReady}, Passive: true,
		}
	}
	return tools.BrowserTargetDiagnostics{
		Profiles: readiness, Actions: []browser.ActionKind{browser.ActionNavigate},
		Contexts: true, Diagnostics: true,
	}, nil
}

func (source *codingRemoteBrowserSource) Open(
	_ context.Context,
	request browser.OpenRequest,
) (browser.Session, error) {
	source.openCalls++
	session := source.session
	session.Owner = request.Owner
	return session, nil
}

func (source *codingRemoteRetainedSource) LookupInvocationByToolCall(
	_ nodes.GatewayInvocationPrincipal,
	_ string,
) (nodes.GatewayInvocationRecord, bool, error) {
	return source.record, true, nil
}

func (source *codingRemoteRetainedSource) LookupInvocation(
	_ nodes.GatewayInvocationPrincipal,
	invocationID string,
) (nodes.GatewayInvocationRecord, bool, error) {
	if invocationID != source.record.Plan.InvocationID {
		return nodes.GatewayInvocationRecord{}, false, nil
	}
	return source.record, true, nil
}

func (source *codingRemoteRetainedSource) QueryInvocation(
	_ context.Context,
	_ nodes.GatewayInvocationPrincipal,
	_ string,
	_ nodes.ID,
	_ string,
) (nodes.InvocationRecord, error) {
	source.queryCalls++
	return source.remote, nil
}

func (source *codingRemoteRetainedSource) CancelInvocation(
	_ context.Context,
	_ nodes.GatewayInvocationPrincipal,
	_ string,
	_ nodes.ID,
	_ string,
) (nodes.InvocationRecord, bool, error) {
	source.cancelCalls++
	return source.remote, true, nil
}

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

func TestCodingRemoteDiscoveryProjectsOnlyApprovedTaskScopes(t *testing.T) {
	descriptors, err := nodes.CodingCommandDescriptors()
	if err != nil {
		t.Fatal(err)
	}
	catalog := nodes.CapabilityCatalog{Commands: descriptors}
	catalogHash, err := catalog.Hash()
	if err != nil {
		t.Fatal(err)
	}
	allowed := make([]string, len(descriptors))
	for index, descriptor := range descriptors {
		allowed[index] = descriptor.Name
	}
	snapshot := nodes.Snapshot{
		ID: "private-task-node", State: nodes.StateConnected, ProtocolVersion: nodes.ProtocolVersion,
		Catalog: catalog, CatalogHash: catalogHash, Executor: "local", PolicyRevision: "private-policy-v1",
	}
	registration := nodes.Registration{
		Snapshot: snapshot, ApprovedCatalogHash: catalogHash, ApprovedAt: 1,
		AllowedCommands: allowed,
	}
	source := &codingRemoteDiscoverySource{record: tools.NodeDiscoveryRecord{
		Snapshot: snapshot, Registration: &registration, Connected: true,
	}}
	cfg := config.DefaultConfig()
	cfg.Gateway.CodingRemote.Enabled = true
	cfg.Execution.Targets = map[string]config.ExecutionTarget{
		"developer": {Type: "node", Node: "private-task-node"},
	}
	cfg.Agents.Defaults.TargetPolicy = &config.TargetPolicy{AllowedTargets: []string{"developer"}}
	cfg.Execution.RemoteCodingScopes = map[string]config.RemoteCodingScope{
		"mintclaw-dev": {
			Target: "developer", Scope: "private-repository-scope", Revision: "scope-v1",
			Profiles: []codingscope.Profile{
				codingscope.ProfileInvestigate,
				codingscope.ProfileMutate,
				codingscope.ProfileProjectYolo,
			},
			Requesters: []config.RemoteCodingRequester{{
				Agent: "main", Channel: "telegram", Sender: "owner",
			}},
		},
	}
	cfg.Execution.CodingRemoteGrants = map[string]config.CodingRemoteClientGrant{
		"local-development": {
			Revision: "grant-v1", Agent: "main",
			LocalProfiles: []codingscope.Profile{codingscope.ProfileInvestigate, codingscope.ProfileMutate},
			Tasks: []config.CodingRemoteTaskGrant{{
				Scope: "mintclaw-dev",
				Profiles: []codingscope.Profile{
					codingscope.ProfileProjectYolo,
					codingscope.ProfileInvestigate,
				},
			}},
		},
	}
	handler := codingRemoteDiscoveryHandler{
		config: func() *config.Config { return cfg }, now: func() time.Time { return time.UnixMilli(1234) },
		source: func(*config.Config) (tools.NodeInvocationSource, error) { return source, nil },
	}
	request := codingremote.Request{
		Schema: codingremote.SchemaV1, RequestID: "request-task-scope",
		Operation: codingremote.OperationCapabilitiesList,
		Grant:     "local-development", GrantRevision: "grant-v1",
		LocalProfile: codingscope.ProfileInvestigate,
	}
	response := handler.HandleCodingRemote(t.Context(), request)
	if response.Status != codingremote.ResponseOK || response.Snapshot == nil ||
		len(response.Snapshot.TaskScopes) != 1 {
		t.Fatalf("task discovery = %#v", response)
	}
	descriptor := response.Snapshot.TaskScopes[0]
	if descriptor.Alias != "mintclaw-dev" || descriptor.Revision != "scope-v1" ||
		descriptor.Target != "developer" || descriptor.Availability != codingremote.AvailabilityAvailable ||
		!slices.Equal(descriptor.Profiles, []codingscope.Profile{
			codingscope.ProfileInvestigate,
			codingscope.ProfileProjectYolo,
		}) {
		t.Fatalf("task descriptor = %#v", descriptor)
	}
	encoded, err := json.Marshal(response.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private-task-node", "private-repository-scope", "private-policy-v1"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("task discovery exposed private authority %q: %s", private, encoded)
		}
	}
	availableRevision := response.Snapshot.DiscoveryRevision

	source.record.Connected = false
	offline := handler.HandleCodingRemote(t.Context(), request)
	if offline.Status != codingremote.ResponseOK || offline.Snapshot == nil ||
		len(offline.Snapshot.TaskScopes) != 1 ||
		offline.Snapshot.TaskScopes[0].Availability != codingremote.AvailabilityOffline ||
		offline.Snapshot.DiscoveryRevision == availableRevision {
		t.Fatalf("offline task discovery = %#v", offline)
	}

	source.record.Connected = true
	source.record.Registration.AllowedCommands = slices.DeleteFunc(
		append([]string(nil), allowed...),
		func(command string) bool { return command == nodes.CodingCommandTaskCancel },
	)
	denied := handler.HandleCodingRemote(t.Context(), request)
	if denied.Status != codingremote.ResponseOK || denied.Snapshot == nil ||
		len(denied.Snapshot.TaskScopes) != 0 {
		t.Fatalf("incomplete task catalog discovery = %#v", denied)
	}
}

func TestCodingRemoteTaskBrokerUsesCurrentStartAndRetainedObservation(t *testing.T) {
	cfg := gatewayCodingRemoteTaskTestConfig()
	source := approvedCodingRemoteTaskSource(t)
	coordinator := &fakeCodingRemoteTaskCoordinator{view: agent.RemoteCodingTaskView{
		Grant: "local-development", GrantRevision: "grant-v1",
		TaskID: "coding-local-task", GenerationID: uuid.NewString(),
		Scope: "mintclaw-dev", Target: "developer", Profile: codingscope.ProfileInvestigate,
		Status: "running", NodeState: "waiting_for_input", ThreadID: uuid.NewString(),
		WorkerGenerationID: uuid.NewString(), Activity: "waiting_for_input",
		Progress: "coding task is waiting for correlated user input",
		Question: &agent.RemoteCodingTaskQuestion{
			ID: "question_1", Revision: 2, Prompt: "Which file should I inspect?",
			Options: []agent.RemoteCodingTaskQuestionOption{
				{ID: "agents", Label: "AGENTS.md", Description: "Inspect agent instructions."},
				{ID: "readme", Label: "README.md", Description: "Inspect the project overview."},
			},
		},
	}}
	handler := codingRemoteDiscoveryHandler{
		config: func() *config.Config { return cfg }, now: func() time.Time { return time.UnixMilli(1234) },
		source: func(*config.Config) (tools.NodeInvocationSource, error) { return source, nil },
		tasks:  coordinator,
	}
	request := codingRemoteTaskBrokerRequest(t, handler, cfg)
	coordinator.view.DiscoveryRevision = request.DiscoveryRevision
	coordinator.view.TaskID = request.TaskID
	response := handler.HandleCodingRemote(t.Context(), request)
	if response.Status != codingremote.ResponseOK || response.Task == nil ||
		response.Task.TaskID != request.TaskID || response.Task.Question == nil ||
		response.Task.Question.Prompt != "Which file should I inspect?" || len(coordinator.starts) != 1 {
		t.Fatalf("task start response = %#v; starts=%#v", response, coordinator.starts)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("task start response validation = %v", err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private-task-node", "private-repository-scope", "channel", "workspace_path"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("task response exposed private authority %q: %s", private, encoded)
		}
	}
	outage := codingRemoteTaskControlRequest(request, codingremote.OperationTaskStatus, "outage_call")
	coordinator.err = agent.ErrRemoteCodingTaskUnavailable
	response = handler.HandleCodingRemote(t.Context(), outage)
	if response.Status != codingremote.ResponseUnavailable || response.Code != "TASK_UNAVAILABLE" {
		t.Fatalf("unavailable task status = %#v", response)
	}
	coordinator.err = nil
	coordinator.statuses = nil

	stale := request
	stale.RequestID = "request-task-stale"
	stale.TaskScopeRevision = "scope-v0"
	stale.TaskID = codingremote.DeriveTaskID(stale)
	response = handler.HandleCodingRemote(t.Context(), stale)
	if response.Status != codingremote.ResponseDenied || response.Code != "TASK_SCOPE_CHANGED" ||
		len(coordinator.starts) != 1 {
		t.Fatalf("stale task start = %#v; starts=%d", response, len(coordinator.starts))
	}

	approvedCommands := append([]string(nil), source.record.Registration.AllowedCommands...)
	source.record.Registration.AllowedCommands = slices.DeleteFunc(
		append([]string(nil), approvedCommands...),
		func(command string) bool { return command == nodes.CodingCommandTaskCancel },
	)
	discovery := codingRemoteTaskDiscoveryRequest(request)
	missingScope := handler.HandleCodingRemote(t.Context(), discovery)
	if missingScope.Status != codingremote.ResponseOK || missingScope.Snapshot == nil ||
		len(missingScope.Snapshot.TaskScopes) != 0 {
		t.Fatalf("task discovery after catalog revocation = %#v", missingScope)
	}
	steerWithoutCatalog := codingRemoteTaskControlRequest(request, codingremote.OperationTaskSteer, "stale_catalog")
	steerWithoutCatalog.DiscoveryRevision = missingScope.Snapshot.DiscoveryRevision
	steerWithoutCatalog.TaskText = "Continue."
	response = handler.HandleCodingRemote(t.Context(), steerWithoutCatalog)
	if response.Status != codingremote.ResponseDenied || response.Code != "TASK_SCOPE_CHANGED" ||
		len(coordinator.steers) != 0 {
		t.Fatalf("task steer after catalog revocation = %#v; calls=%d", response, len(coordinator.steers))
	}
	source.record.Registration.AllowedCommands = approvedCommands

	delete(cfg.Execution.CodingRemoteGrants, request.Grant)
	status := codingRemoteTaskControlRequest(request, codingremote.OperationTaskStatus, "status_call")
	response = handler.HandleCodingRemote(t.Context(), status)
	if response.Status != codingremote.ResponseOK || response.Task == nil || len(coordinator.statuses) != 1 {
		t.Fatalf("retained task status = %#v; calls=%d", response, len(coordinator.statuses))
	}
	cancel := codingRemoteTaskControlRequest(request, codingremote.OperationTaskCancel, "cancel_call")
	response = handler.HandleCodingRemote(t.Context(), cancel)
	if response.Status != codingremote.ResponseOK || response.Task == nil || len(coordinator.cancels) != 1 {
		t.Fatalf("retained task cancel = %#v; calls=%d", response, len(coordinator.cancels))
	}
	steer := codingRemoteTaskControlRequest(request, codingremote.OperationTaskSteer, "steer_call")
	steer.TaskText = "Continue with the focused validation."
	response = handler.HandleCodingRemote(t.Context(), steer)
	if response.Status != codingremote.ResponseDenied || response.Code != "GRANT_UNAVAILABLE" ||
		len(coordinator.steers) != 0 {
		t.Fatalf("revoked task steer = %#v; calls=%d", response, len(coordinator.steers))
	}
}

func TestCodingRemoteTaskCatalogApprovalFailsClosed(t *testing.T) {
	descriptors, err := nodes.CodingCommandDescriptors()
	if err != nil {
		t.Fatal(err)
	}
	catalog := nodes.CapabilityCatalog{Commands: descriptors}
	catalogHash, err := catalog.Hash()
	if err != nil {
		t.Fatal(err)
	}
	allowed := make([]string, len(descriptors))
	for index, descriptor := range descriptors {
		allowed[index] = descriptor.Name
	}
	newRecord := func() tools.NodeDiscoveryRecord {
		snapshot := nodes.Snapshot{
			ID: "private-task-node", State: nodes.StateConnected, ProtocolVersion: nodes.ProtocolVersion,
			Catalog: catalog, CatalogHash: catalogHash, Executor: "local", PolicyRevision: "policy-v1",
		}
		return tools.NodeDiscoveryRecord{
			Snapshot: snapshot,
			Registration: &nodes.Registration{
				Snapshot: snapshot, ApprovedCatalogHash: catalogHash, ApprovedAt: 1,
				AllowedCommands: append([]string(nil), allowed...),
			},
			Connected: true,
		}
	}
	tests := []struct {
		name   string
		mutate func(*tools.NodeDiscoveryRecord)
	}{
		{name: "missing registration", mutate: func(record *tools.NodeDiscoveryRecord) {
			record.Registration = nil
		}},
		{name: "revoked registration", mutate: func(record *tools.NodeDiscoveryRecord) {
			record.Registration.RevokedAt = 2
		}},
		{name: "unapproved registration", mutate: func(record *tools.NodeDiscoveryRecord) {
			record.Registration.ApprovedAt = 0
		}},
		{name: "catalog requires reapproval", mutate: func(record *tools.NodeDiscoveryRecord) {
			record.Registration.ApprovedCatalogHash = strings.Repeat("0", 64)
		}},
		{name: "malformed snapshot", mutate: func(record *tools.NodeDiscoveryRecord) {
			record.Snapshot.CatalogHash = strings.Repeat("0", 64)
		}},
		{name: "revoked snapshot", mutate: func(record *tools.NodeDiscoveryRecord) {
			record.Snapshot.State = nodes.StateRevoked
		}},
		{name: "missing required command", mutate: func(record *tools.NodeDiscoveryRecord) {
			record.Registration.AllowedCommands = slices.DeleteFunc(
				record.Registration.AllowedCommands,
				func(command string) bool { return command == nodes.CodingCommandTaskSteer },
			)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := newRecord()
			test.mutate(&record)
			if codingRemoteTaskCatalogApproved(record) {
				t.Fatal("codingRemoteTaskCatalogApproved() accepted incomplete authority")
			}
		})
	}
	if !codingRemoteTaskCatalogApproved(newRecord()) {
		t.Fatal("codingRemoteTaskCatalogApproved() rejected exact current authority")
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

func TestCodingRemoteDiscoveryProjectsExactExplicitWorkspacePolicy(t *testing.T) {
	profile := nodes.FileProfileDescriptor{
		Alias: "project-files", Revision: "files-v1",
		ReadableRoots: []string{"/private/project"}, WritableRoots: []string{"/private/project"},
		AllowCreate: true, AllowOverwrite: true, MaxFileBytes: 1024,
		Approval: nodes.FileProfileApproval{Metadata: "none", Read: "none", Write: "none"},
	}
	descriptors, err := nodes.WorkspaceDescriptors([]nodes.FileProfileDescriptor{profile}, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	for index := range descriptors {
		contract := *descriptors[index].ModelContract
		contract.Availability = nodes.ModelAvailable
		descriptors[index].ModelContract = &contract
	}
	catalog := nodes.CapabilityCatalog{Commands: descriptors}
	catalogHash, err := catalog.Hash()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := nodes.Snapshot{
		ID: "private-node-id", State: nodes.StateConnected, ProtocolVersion: nodes.ProtocolVersion,
		Catalog: catalog, CatalogHash: catalogHash, Executor: "local", PolicyRevision: "policy-v1",
	}
	registration := nodes.Registration{
		Snapshot: snapshot, ApprovedCatalogHash: catalogHash, ApprovedAt: 1,
		AllowedCommands: []string{nodes.WorkspaceCommandRead, nodes.WorkspaceCommandWrite},
	}
	source := &codingRemoteDiscoverySource{record: tools.NodeDiscoveryRecord{
		Snapshot: snapshot, Registration: &registration, Connected: true,
	}}
	cfg := config.DefaultConfig()
	cfg.Gateway.CodingRemote.Enabled = true
	cfg.Execution.Targets = map[string]config.ExecutionTarget{
		"build": {Type: "node", Node: "builder-node", FileProfile: "project-files"},
	}
	cfg.Execution.RemoteWorkspaces = map[string]config.RemoteWorkspace{
		"project": {
			Target: "build", WorkingScope: "project", Revision: "workspace-v1",
			Tools: []string{"read_file", "write_file"},
		},
	}
	cfg.Agents.Defaults.TargetPolicy = &config.TargetPolicy{AllowedTargets: []string{"build"}}
	cfg.Execution.CodingRemoteCapabilities = map[string]config.CodingRemoteCapability{
		"build-workspace": {
			Kind: config.CodingRemoteCapabilityWorkspace, Revision: "capability-v1",
			RemoteWorkspace: "project", Operations: []string{"write_file", "read_file"},
		},
	}
	cfg.Execution.CodingRemoteGrants = map[string]config.CodingRemoteClientGrant{
		"local-development": {
			Revision: "grant-v1", Agent: "main",
			LocalProfiles: []codingscope.Profile{codingscope.ProfileInvestigate, codingscope.ProfileMutate},
			Capabilities:  []string{"build-workspace"},
		},
	}
	handler := codingRemoteDiscoveryHandler{
		config: func() *config.Config { return cfg }, now: func() time.Time { return time.UnixMilli(1234) },
		source: func(*config.Config) (tools.NodeInvocationSource, error) { return source, nil },
	}
	request := codingremote.Request{
		Schema: codingremote.SchemaV1, RequestID: "request-one",
		Operation: codingremote.OperationCapabilitiesList,
		Grant:     "local-development", GrantRevision: "grant-v1", LocalProfile: codingscope.ProfileMutate,
	}
	response := handler.HandleCodingRemote(t.Context(), request)
	if response.Status != codingremote.ResponseOK || response.Snapshot == nil ||
		len(response.Snapshot.Capabilities) != 1 || len(response.Snapshot.Capabilities[0].Operations) != 2 ||
		response.Snapshot.Capabilities[0].Operations[0].Alias != "read_file" ||
		response.Snapshot.Capabilities[0].Operations[1].Alias != "write_file" {
		t.Fatalf("mutate discovery = %#v", response)
	}
	encoded, err := json.Marshal(response.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private-node-id", "/private/project", "builder-node", "project-files"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("discovery exposed private authority %q: %s", private, encoded)
		}
	}
	invoke := request
	invoke.RequestID = "request-stale"
	invoke.Operation = codingremote.OperationCapabilityInvoke
	invoke.Principal = &runtimecap.Principal{
		Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
		SessionID: "coding:11111111-1111-4111-8111-111111111111", ExecutionID: "turn-1",
	}
	invoke.CallID = "call_stale"
	invoke.DiscoveryRevision = "discovery-stale"
	invoke.Capability = "build-workspace"
	invoke.CapabilityRevision = "capability-v1"
	invoke.CapabilityOperation = "read_file"
	invoke.Arguments = json.RawMessage(`{"path":"README.md"}`)
	invoke.InvocationID = codingremote.DeriveInvocationID(invoke)
	response = handler.HandleCodingRemote(t.Context(), invoke)
	if response.Status != codingremote.ResponseDenied || response.Code != "DISCOVERY_STALE" ||
		response.Result != nil {
		t.Fatalf("stale invoke = %#v", response)
	}
	request.LocalProfile = codingscope.ProfileInvestigate
	response = handler.HandleCodingRemote(t.Context(), request)
	if response.Status != codingremote.ResponseOK || response.Snapshot == nil ||
		len(response.Snapshot.Capabilities) != 1 || len(response.Snapshot.Capabilities[0].Operations) != 1 ||
		response.Snapshot.Capabilities[0].Operations[0].Alias != "read_file" {
		t.Fatalf("investigate discovery = %#v", response)
	}
}

func TestCodingRemoteDiscoveryProjectsClosedServiceOperations(t *testing.T) {
	descriptors := codingRemoteServiceTestDescriptors()
	catalog := nodes.CapabilityCatalog{Commands: descriptors}
	catalogHash, err := catalog.Hash()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := nodes.Snapshot{
		ID: "private-service-node", State: nodes.StateConnected, ProtocolVersion: nodes.ProtocolVersion,
		Catalog: catalog, CatalogHash: catalogHash, Executor: "local", PolicyRevision: "private-policy-v1",
	}
	allowed := make([]string, len(descriptors))
	for index, descriptor := range descriptors {
		allowed[index] = descriptor.Name
	}
	registration := nodes.Registration{
		Snapshot: snapshot, ApprovedCatalogHash: catalogHash, ApprovedAt: 1, AllowedCommands: allowed,
	}
	source := &codingRemoteDiscoverySource{record: tools.NodeDiscoveryRecord{
		Snapshot: snapshot, Registration: &registration, Connected: true,
	}}
	cfg := config.DefaultConfig()
	cfg.Gateway.CodingRemote.Enabled = true
	cfg.Execution.Targets = map[string]config.ExecutionTarget{
		"services": {
			Type: "node", Node: "private-service-node-binding", ServiceProfile: "server-services",
		},
	}
	cfg.Agents.Defaults.TargetPolicy = &config.TargetPolicy{AllowedTargets: []string{"services"}}
	cfg.Tools.Approval.BypassNodeTargets = []string{"services"}
	cfg.Execution.CodingRemoteCapabilities = map[string]config.CodingRemoteCapability{
		"service-control": {
			Kind: config.CodingRemoteCapabilityNode, Revision: "capability-v1", Target: "services",
			Operations: []string{"service.status.v1", "service.action.v1", "service.logs.v1"},
		},
	}
	cfg.Execution.CodingRemoteGrants = map[string]config.CodingRemoteClientGrant{
		"local-development": {
			Revision: "grant-v1", Agent: "main",
			LocalProfiles: []codingscope.Profile{codingscope.ProfileInvestigate, codingscope.ProfileMutate},
			Capabilities:  []string{"service-control"},
		},
	}
	handler := codingRemoteDiscoveryHandler{
		config: func() *config.Config { return cfg }, now: func() time.Time { return time.UnixMilli(1234) },
		source: func(*config.Config) (tools.NodeInvocationSource, error) { return source, nil },
	}
	discover := func(profile codingscope.Profile) codingremote.CapabilityDescriptor {
		t.Helper()
		response := handler.HandleCodingRemote(t.Context(), codingremote.Request{
			Schema: codingremote.SchemaV1, RequestID: "request-service-" + string(profile),
			Operation: codingremote.OperationCapabilitiesList,
			Grant:     "local-development", GrantRevision: "grant-v1", LocalProfile: profile,
		})
		if response.Status != codingremote.ResponseOK || response.Snapshot == nil ||
			len(response.Snapshot.Capabilities) != 1 {
			t.Fatalf("service discovery = %#v", response)
		}
		encoded, marshalErr := json.Marshal(response.Snapshot)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		for _, forbidden := range []string{
			"private-service-node", "private-service-node-binding", "private-policy-v1", "server-services",
		} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("service discovery exposed %q: %s", forbidden, encoded)
			}
		}
		return response.Snapshot.Capabilities[0]
	}
	mutate := discover(codingscope.ProfileMutate)
	aliases := make([]string, 0, len(mutate.Operations))
	for _, operation := range mutate.Operations {
		aliases = append(aliases, operation.Alias)
	}
	if mutate.Kind != codingremote.CapabilityNodeCommand ||
		!slices.Equal(aliases, []string{"service_action", "service_logs", "service_status"}) {
		t.Fatalf("mutate service operations = %#v", mutate)
	}
	investigate := discover(codingscope.ProfileInvestigate)
	aliases = aliases[:0]
	for _, operation := range investigate.Operations {
		aliases = append(aliases, operation.Alias)
	}
	if !slices.Equal(aliases, []string{"service_logs", "service_status"}) {
		t.Fatalf("investigate service operations = %#v", investigate)
	}

	cfg.Tools.Approval.BypassNodeTargets = nil
	mutate = discover(codingscope.ProfileMutate)
	aliases = aliases[:0]
	for _, operation := range mutate.Operations {
		aliases = append(aliases, operation.Alias)
	}
	if !slices.Equal(aliases, []string{"service_logs", "service_status"}) {
		t.Fatalf("service operations without bypass = %#v", mutate)
	}
}

func TestCodingRemoteBrowserCapabilityRetainsNoReplayStatusAfterRevocation(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Gateway.CodingRemote.Enabled = true
	cfg.Execution.Targets = map[string]config.ExecutionTarget{
		"companion": {Type: "node", Node: "private-companion-node"},
	}
	cfg.Agents.Defaults.TargetPolicy = &config.TargetPolicy{AllowedTargets: []string{"companion"}}
	cfg.Tools.Browser = config.BrowserToolsConfig{
		Enabled: true,
		Agents:  []string{"main"},
		Targets: map[string]config.BrowserTargetConfig{
			"companion-browser": {
				Enabled: true, Placement: config.BrowserPlacementNode, NodeTarget: "companion",
				Profiles: map[string]config.BrowserProfileConfig{
					"automation": {
						Enabled: true, Revision: "automation-v1", Mode: config.BrowserProfileManaged,
						AllowedAgents: []string{"main"}, AllowedActors: []string{"coding:local:operator"},
						NetworkMode:    config.BrowserNetworkPublicWeb,
						CapabilityMode: config.BrowserCapabilityFullAccess,
						ApprovalMode:   config.BrowserApprovalNone, AllowApprovedActions: true,
					},
				},
			},
		},
	}
	cfg.Execution.CodingRemoteCapabilities = map[string]config.CodingRemoteCapability{
		"browser": {
			Kind: config.CodingRemoteCapabilityBrowser, Revision: "browser-capability-v1",
			Target: "companion-browser", BrowserProfile: "automation",
			Operations: []string{
				"browser_open", "browser_status", "browser_observe", "browser_act",
			},
		},
	}
	cfg.Execution.CodingRemoteGrants = map[string]config.CodingRemoteClientGrant{
		"local-development": {
			Revision: "grant-v1", Agent: "main",
			LocalProfiles: []codingscope.Profile{codingscope.ProfileInvestigate, codingscope.ProfileMutate},
			Capabilities:  []string{"browser"},
		},
	}
	browserSource := &codingRemoteBrowserSource{session: browser.Session{
		ID: "browser_remote_1", Target: "companion-browser", Profile: "automation",
		State: browser.SessionReady, TabID: "tab_primary", ExpiresAt: 100,
	}}
	handler := codingRemoteDiscoveryHandler{
		config: func() *config.Config { return cfg }, now: func() time.Time { return time.UnixMilli(1234) },
		source: func(*config.Config) (tools.NodeInvocationSource, error) {
			return &codingRemoteDiscoverySource{}, nil
		},
		browserSource:      func(*config.Config) (tools.BrowserToolSource, error) { return browserSource, nil },
		browserInvocations: newCodingRemoteBrowserInvocationStore(),
	}
	threadID := uuid.NewString()
	sessionKey := "coding:" + threadID
	projectKey := "git_worktree:" + strings.Repeat("a", 64)
	discoveryRequest := codingremote.Request{
		Schema: codingremote.SchemaV1, RequestID: "request-browser-discovery",
		Operation: codingremote.OperationCapabilitiesList,
		Grant:     "local-development", GrantRevision: "grant-v1",
		ThreadID: threadID, SessionKey: sessionKey, ProjectKey: projectKey,
		LocalProfile: codingscope.ProfileMutate,
	}
	discovery := handler.HandleCodingRemote(t.Context(), discoveryRequest)
	if discovery.Status != codingremote.ResponseOK || discovery.Snapshot == nil ||
		len(discovery.Snapshot.Capabilities) != 1 {
		t.Fatalf("browser discovery = %#v", discovery)
	}
	capability := discovery.Snapshot.Capabilities[0]
	aliases := make([]string, 0, len(capability.Operations))
	for _, operation := range capability.Operations {
		aliases = append(aliases, operation.Alias)
	}
	if capability.Kind != codingremote.CapabilityBrowserProfile ||
		!slices.Equal(aliases, []string{"browser_act", "browser_observe", "browser_open", "browser_status"}) {
		t.Fatalf("browser capability = %#v", capability)
	}
	encoded, err := json.Marshal(capability)
	if err != nil || strings.Contains(string(encoded), "private-companion-node") ||
		strings.Contains(string(encoded), "automation-v1") {
		t.Fatalf("browser capability leaked private authority: %s, %v", encoded, err)
	}
	investigateRequest := discoveryRequest
	investigateRequest.RequestID = "request-browser-investigate-discovery"
	investigateRequest.LocalProfile = codingscope.ProfileInvestigate
	investigate := handler.HandleCodingRemote(t.Context(), investigateRequest)
	if investigate.Status != codingremote.ResponseOK || investigate.Snapshot == nil ||
		len(investigate.Snapshot.Capabilities) != 1 {
		t.Fatalf("investigate browser discovery = %#v", investigate)
	}
	aliases = aliases[:0]
	for _, operation := range investigate.Snapshot.Capabilities[0].Operations {
		aliases = append(aliases, operation.Alias)
	}
	if !slices.Equal(aliases, []string{"browser_observe", "browser_status"}) {
		t.Fatalf("investigate browser aliases = %#v", aliases)
	}

	invoke := discoveryRequest
	invoke.RequestID = "request-browser-open"
	invoke.Operation = codingremote.OperationCapabilityInvoke
	invoke.Principal = &runtimecap.Principal{
		Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
		SessionID: sessionKey, ExecutionID: "turn-1",
	}
	invoke.CallID = "call_browser_open"
	invoke.DiscoveryRevision = discovery.Snapshot.DiscoveryRevision
	invoke.Capability = capability.Alias
	invoke.CapabilityRevision = capability.Revision
	invoke.CapabilityOperation = "browser_open"
	invoke.Arguments = json.RawMessage(`{}`)
	invoke.DeadlineUnixMS = time.Now().Add(time.Minute).UnixMilli()
	invoke.InvocationID = codingremote.DeriveInvocationID(invoke)
	response := handler.HandleCodingRemote(t.Context(), invoke)
	if response.Status != codingremote.ResponseOK || response.Result == nil ||
		response.Result.State != "succeeded" || browserSource.openCalls != 1 {
		t.Fatalf("browser invoke = %#v; open calls = %d", response, browserSource.openCalls)
	}

	replay := invoke
	replay.RequestID = "request-browser-open-replay"
	response = handler.HandleCodingRemote(t.Context(), replay)
	if response.Status != codingremote.ResponseOK || response.Result == nil || browserSource.openCalls != 1 {
		t.Fatalf("browser replay = %#v; open calls = %d", response, browserSource.openCalls)
	}

	delete(cfg.Execution.CodingRemoteGrants, "local-development")
	status := invoke
	status.RequestID = "request-browser-status"
	status.Operation = codingremote.OperationInvocationStatus
	status.CallID = "call_browser_status"
	status.Arguments = nil
	status.Principal = &runtimecap.Principal{
		Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
		SessionID: sessionKey, ExecutionID: "turn-2",
	}
	response = handler.HandleCodingRemote(t.Context(), status)
	if response.Status != codingremote.ResponseOK || response.Result == nil ||
		response.Result.InvocationID != invoke.InvocationID || browserSource.openCalls != 1 {
		t.Fatalf("revoked browser status = %#v; open calls = %d", response, browserSource.openCalls)
	}

	recoveredStatus := status
	recoveredStatus.RequestID = "request-browser-status-recovered"
	recoveredStatus.DiscoveryRevision = codingremote.BrowserReceiptRecoveryRevision
	recoveredStatus.CapabilityRevision = codingremote.BrowserReceiptRecoveryRevision
	recoveredStatus.CapabilityOperation = codingremote.BrowserReceiptRecoveryOperation
	delete(cfg.Execution.CodingRemoteCapabilities, "browser")
	response = handler.HandleCodingRemote(t.Context(), recoveredStatus)
	if response.Status != codingremote.ResponseOK || response.Result == nil ||
		response.Result.InvocationID != invoke.InvocationID ||
		response.Result.Operation != "browser_open" ||
		response.Result.CapabilityRevision != capability.Revision || browserSource.openCalls != 1 {
		t.Fatalf("recovered browser status = %#v; open calls = %d", response, browserSource.openCalls)
	}
	unknownRecovery := recoveredStatus
	unknownRecovery.RequestID = "request-browser-status-recovery-unknown"
	unknownRecovery.InvocationID = "remote_capability_browser_unknown"
	response = handler.HandleCodingRemote(t.Context(), unknownRecovery)
	if response.Status != codingremote.ResponseDenied || response.Code != "INVOCATION_DENIED" ||
		browserSource.openCalls != 1 {
		t.Fatalf("unknown browser recovery = %#v; open calls = %d", response, browserSource.openCalls)
	}

	wrongActor := status
	wrongActor.RequestID = "request-browser-status-wrong-actor"
	wrongActor.Principal = &runtimecap.Principal{
		Runtime: runtimecap.KindCoding, ActorID: "local:other", AgentID: "main",
		SessionID: sessionKey, ExecutionID: "turn-3",
	}
	response = handler.HandleCodingRemote(t.Context(), wrongActor)
	if response.Status != codingremote.ResponseDenied || response.Code != "INVOCATION_DENIED" {
		t.Fatalf("wrong-actor browser status = %#v", response)
	}

	cancel := status
	cancel.RequestID = "request-browser-cancel"
	cancel.Operation = codingremote.OperationInvocationCancel
	response = handler.HandleCodingRemote(t.Context(), cancel)
	if response.Status != codingremote.ResponseDenied || response.Code != "CANCEL_UNSUPPORTED" ||
		browserSource.openCalls != 1 {
		t.Fatalf("browser cancel = %#v; open calls = %d", response, browserSource.openCalls)
	}
}

func TestCodingRemoteBrowserInvocationStoreReservesBeforeExecutionAndFailsClosedAtCapacity(t *testing.T) {
	store := newCodingRemoteBrowserInvocationStore()
	threadID := uuid.NewString()
	sessionKey := "coding:" + threadID
	principal := &runtimecap.Principal{
		Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
		SessionID: sessionKey, ExecutionID: "turn-browser-capacity",
	}
	capability := codingremote.CapabilityDescriptor{
		Alias: "browser", Revision: "browser-capability-v1", Target: "companion-browser",
		Kind: codingremote.CapabilityBrowserProfile, Availability: codingremote.AvailabilityAvailable,
	}
	operation := codingremote.OperationDescriptor{Alias: "browser_act", Risk: codingremote.RiskWrite}
	requestFor := func(index int) codingremote.Request {
		request := codingremote.Request{
			Schema: codingremote.SchemaV1, RequestID: fmt.Sprintf("request-browser-%d", index),
			Operation: codingremote.OperationCapabilityInvoke,
			Grant:     "local-development", GrantRevision: "grant-v1",
			ThreadID: threadID, SessionKey: sessionKey,
			ProjectKey: "git_worktree:" + strings.Repeat("a", 64), LocalProfile: codingscope.ProfileMutate,
			Principal: principal, CallID: fmt.Sprintf("call_browser_%d", index),
			DiscoveryRevision: "discovery-v1", Capability: capability.Alias,
			CapabilityRevision: capability.Revision, CapabilityOperation: operation.Alias,
			Arguments:      json.RawMessage(`{"browser_session_id":"browser_1"}`),
			DeadlineUnixMS: time.Now().Add(time.Minute).UnixMilli(),
		}
		request.InvocationID = codingremote.DeriveInvocationID(request)
		return request
	}

	first := requestFor(0)
	running, reservation := store.reserve(first, codingRemoteBrowserRunningResult(first, capability, operation))
	if reservation != codingRemoteBrowserReservationClaimed || running.State != "running" {
		t.Fatalf("first reservation = %#v, %v", running, reservation)
	}
	duplicate, reservation := store.reserve(first, codingRemoteBrowserRunningResult(first, capability, operation))
	if reservation != codingRemoteBrowserReservationExisting || duplicate.State != "running" {
		t.Fatalf("duplicate reservation = %#v, %v", duplicate, reservation)
	}
	terminal := codingRemoteBrowserRunningResult(first, capability, operation)
	terminal.State = "succeeded"
	terminal.RecoveryAction = ""
	terminal.Result = json.RawMessage(`{"status":"ok"}`)
	if !store.complete(first, terminal) {
		t.Fatal("failed to complete reserved browser invocation")
	}
	duplicate, reservation = store.reserve(first, codingRemoteBrowserRunningResult(first, capability, operation))
	if reservation != codingRemoteBrowserReservationExisting || duplicate.State != "succeeded" {
		t.Fatalf("terminal duplicate reservation = %#v, %v", duplicate, reservation)
	}

	for index := 1; index < maxCodingRemoteBrowserInvocations; index++ {
		request := requestFor(index)
		_, reservation = store.reserve(request, codingRemoteBrowserRunningResult(request, capability, operation))
		if reservation != codingRemoteBrowserReservationClaimed {
			t.Fatalf("reservation %d = %v", index, reservation)
		}
	}
	overflow := requestFor(maxCodingRemoteBrowserInvocations)
	_, reservation = store.reserve(overflow, codingRemoteBrowserRunningResult(overflow, capability, operation))
	if reservation != codingRemoteBrowserReservationFull {
		t.Fatalf("overflow reservation = %v", reservation)
	}
	status := first
	status.Operation = codingremote.OperationInvocationStatus
	status.Arguments = nil
	retained, found, authorized := store.lookup(status)
	if !found || !authorized || retained.State != "succeeded" {
		t.Fatalf("oldest retained receipt = %#v, found=%v authorized=%v", retained, found, authorized)
	}
}

func TestCodingRemoteDiscoveryProjectsTypedWorkspaceJobsWithoutRawJobIDs(t *testing.T) {
	jobProfile := nodes.JobProfileDescriptor{
		Alias: "project-jobs", Revision: "jobs-v1", Executor: "system_exec",
		AuthorityDigest: strings.Repeat("a", 64), TimeoutSecondsMax: 600, ConcurrentJobs: 2,
		StdoutBytesMax: 4096, StderrBytesMax: 4096,
		ArtifactCountMax: 2, ArtifactBytesMax: 4096, ArtifactsTotalBytesMax: 8192,
		RetentionSeconds: 300, CancelGuarantee: "process_group",
		ExecutableAliases: []string{"go"}, WorkingScopes: []string{"project"},
		EnvironmentNames: []string{"PATH"},
		Approval:         nodes.JobProfileApproval{Start: "none", Read: "none", Cancel: "none"},
	}
	jobDescriptors, err := nodes.JobCommandDescriptors([]nodes.JobProfileDescriptor{jobProfile})
	if err != nil {
		t.Fatal(err)
	}
	systemDescriptor := nodes.CommandDescriptor{
		Name: "system.exec.v1",
		InputSchema: json.RawMessage(
			`{"type":"object","required":["argv","cwd","timeout_seconds","env"],"properties":{"argv":{"type":"array","minItems":1,"maxItems":128,"items":{"type":"string","minLength":1,"maxLength":4096}},"cwd":{"type":"string","minLength":1,"maxLength":4096},"timeout_seconds":{"type":"integer","minimum":1,"maximum":3600},"env":{"type":"object","maxProperties":64,"additionalProperties":{"type":"string","maxLength":16384}}},"additionalProperties":false}`,
		),
		OutputSchema: json.RawMessage(`{"type":"object"}`), Risk: nodes.RiskWrite,
		ModelContract: &nodes.CommandModelContract{
			Availability: nodes.ModelAvailable, TimeoutSecondsMax: 120, OutputBytesMax: 4096,
			ResultKind: "json", AuthorityDigest: strings.Repeat("b", 64),
			Constraints: nodes.CommandModelConstraints{
				ExecutableAliases: []string{"go"}, WorkingScopes: []string{"project"},
				EnvironmentNames: []string{"PATH"},
			},
			Guidance: []string{}, Examples: []json.RawMessage{},
		},
	}
	catalog := nodes.CapabilityCatalog{Commands: append([]nodes.CommandDescriptor{systemDescriptor}, jobDescriptors...)}
	catalogHash, err := catalog.Hash()
	if err != nil {
		t.Fatal(err)
	}
	allowed := []string{"system.exec.v1"}
	for _, descriptor := range jobDescriptors {
		allowed = append(allowed, descriptor.Name)
	}
	snapshot := nodes.Snapshot{
		ID: "private-node-id", State: nodes.StateConnected, ProtocolVersion: nodes.ProtocolVersion,
		Catalog: catalog, CatalogHash: catalogHash, Executor: "local", PolicyRevision: "policy-v1",
	}
	registration := nodes.Registration{
		Snapshot: snapshot, ApprovedCatalogHash: catalogHash, ApprovedAt: 1, AllowedCommands: allowed,
	}
	source := &codingRemoteDiscoverySource{record: tools.NodeDiscoveryRecord{
		Snapshot: snapshot, Registration: &registration, Connected: true,
	}}
	cfg := config.DefaultConfig()
	cfg.Gateway.CodingRemote.Enabled = true
	cfg.Execution.Targets = map[string]config.ExecutionTarget{
		"build": {Type: "node", Node: "builder-node", JobProfile: "project-jobs"},
	}
	cfg.Execution.RemoteWorkspaces = map[string]config.RemoteWorkspace{
		"project": {
			Target: "build", WorkingScope: "project", Revision: "workspace-v1",
			Tools: []string{"workspace_exec", "jobs"},
		},
	}
	cfg.Agents.Defaults.TargetPolicy = &config.TargetPolicy{AllowedTargets: []string{"build"}}
	cfg.Execution.CodingRemoteCapabilities = map[string]config.CodingRemoteCapability{
		"build-workspace": {
			Kind: config.CodingRemoteCapabilityWorkspace, Revision: "capability-v1",
			RemoteWorkspace: "project",
			Operations: []string{
				"workspace_exec", "job_status", "job_logs", "job_artifacts", "job_cancel",
			},
		},
	}
	cfg.Execution.CodingRemoteGrants = map[string]config.CodingRemoteClientGrant{
		"local-development": {
			Revision: "grant-v1", Agent: "main",
			LocalProfiles: []codingscope.Profile{codingscope.ProfileInvestigate, codingscope.ProfileMutate},
			Capabilities:  []string{"build-workspace"},
		},
	}
	handler := codingRemoteDiscoveryHandler{
		config: func() *config.Config { return cfg }, now: func() time.Time { return time.UnixMilli(1234) },
		source: func(*config.Config) (tools.NodeInvocationSource, error) { return source, nil },
	}
	discover := func(profile codingscope.Profile) codingremote.CapabilityDescriptor {
		t.Helper()
		response := handler.HandleCodingRemote(t.Context(), codingremote.Request{
			Schema: codingremote.SchemaV1, RequestID: "request-jobs-" + string(profile),
			Operation: codingremote.OperationCapabilitiesList,
			Grant:     "local-development", GrantRevision: "grant-v1", LocalProfile: profile,
		})
		if response.Status != codingremote.ResponseOK || response.Snapshot == nil ||
			len(response.Snapshot.Capabilities) != 1 {
			t.Fatalf("job discovery = %#v", response)
		}
		return response.Snapshot.Capabilities[0]
	}
	mutate := discover(codingscope.ProfileMutate)
	aliases := make([]string, 0, len(mutate.Operations))
	for _, operation := range mutate.Operations {
		aliases = append(aliases, operation.Alias)
		if operation.Alias == "workspace_exec" {
			var schema struct {
				OneOf []struct {
					Properties map[string]struct {
						Enum []string `json:"enum"`
					} `json:"properties"`
				} `json:"oneOf"`
			}
			if err = json.Unmarshal(operation.InputSchema, &schema); err != nil || len(schema.OneOf) != 2 ||
				!slices.Equal(schema.OneOf[0].Properties["executable"].Enum, []string{"go"}) ||
				!slices.Equal(schema.OneOf[0].Properties["mode"].Enum, []string{"foreground"}) ||
				!slices.Equal(schema.OneOf[1].Properties["executable"].Enum, []string{"go"}) ||
				!slices.Equal(schema.OneOf[1].Properties["mode"].Enum, []string{"job"}) {
				t.Fatalf("workspace exec schema = %s, %v", operation.InputSchema, err)
			}
		}
		if strings.HasPrefix(operation.Alias, "job_") {
			var schema struct {
				Properties map[string]json.RawMessage `json:"properties"`
			}
			if err = json.Unmarshal(operation.InputSchema, &schema); err != nil ||
				schema.Properties["job_invocation_id"] == nil || schema.Properties["job_id"] != nil {
				t.Fatalf("job schema %q = %s, %v", operation.Alias, operation.InputSchema, err)
			}
		}
	}
	if !slices.Equal(
		aliases,
		[]string{"job_artifacts", "job_cancel", "job_logs", "job_status", "workspace_exec"},
	) {
		t.Fatalf("mutate job operations = %#v", aliases)
	}
	investigate := discover(codingscope.ProfileInvestigate)
	aliases = aliases[:0]
	for _, operation := range investigate.Operations {
		aliases = append(aliases, operation.Alias)
	}
	if !slices.Equal(aliases, []string{"job_artifacts", "job_logs", "job_status"}) {
		t.Fatalf("read-only job operations = %#v", aliases)
	}
}

func TestCodingRemoteStatusRetainsOwnedObservationAfterGrantRevocation(t *testing.T) {
	threadID := uuid.NewString()
	sessionKey := "coding:" + threadID
	invocationID := "node_invocation_1"
	invocationReference := "remote_capability_test_reference"
	source := &codingRemoteRetainedSource{
		record: nodes.GatewayInvocationRecord{
			Target: "build", State: nodes.GatewayInvocationDispatched,
			Plan: nodes.ExecutionPlan{InvocationRequest: nodes.InvocationRequest{
				InvocationID: invocationID, NodeID: "private-node-id", Command: nodes.WorkspaceCommandRead,
			}, Risk: nodes.RiskRead},
		},
		remote: nodes.InvocationRecord{
			InvocationID: invocationID, Command: nodes.WorkspaceCommandRead,
			State: nodes.InvocationSucceeded, Result: json.RawMessage(`{"path":"README.md","content":"ok"}`),
		},
	}
	cfg := config.DefaultConfig()
	cfg.Gateway.CodingRemote.Enabled = true
	handler := codingRemoteDiscoveryHandler{
		config: func() *config.Config { return cfg }, now: time.Now,
		source: func(*config.Config) (tools.NodeInvocationSource, error) { return source, nil },
	}
	request := codingremote.Request{
		Schema: codingremote.SchemaV1, RequestID: "request-status",
		Operation: codingremote.OperationInvocationStatus,
		Grant:     "removed-grant", GrantRevision: "grant-v1", ThreadID: threadID, SessionKey: sessionKey,
		ProjectKey: "git_worktree:" + strings.Repeat("d", 64), LocalProfile: codingscope.ProfileMutate,
		Principal: &runtimecap.Principal{
			Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
			SessionID: sessionKey, ExecutionID: "turn-2",
		},
		CallID: "call_status", DiscoveryRevision: "discovery-old",
		Capability: "build-workspace", CapabilityRevision: "capability-v1",
		CapabilityOperation: "read_file", InvocationID: invocationReference,
	}
	response := handler.HandleCodingRemote(t.Context(), request)
	if response.Status != codingremote.ResponseOK || response.Result == nil ||
		response.Result.State != string(nodes.InvocationSucceeded) ||
		response.Result.Operation != "read_file" || response.Result.InvocationID != invocationReference ||
		source.queryCalls != 1 {
		t.Fatalf("retained status = %#v; query calls = %d", response, source.queryCalls)
	}
	mismatched := request
	mismatched.RequestID = "request-status-wrong-operation"
	mismatched.CapabilityOperation = "write_file"
	response = handler.HandleCodingRemote(t.Context(), mismatched)
	if response.Status != codingremote.ResponseDenied || response.Code != "INVOCATION_DENIED" {
		t.Fatalf("mismatched retained status = %#v", response)
	}
	source.remote.State = nodes.InvocationCanceled
	source.remote.Cancellation = &nodes.InvocationCancellation{RequestedAt: 1, TerminationConfirmed: true}
	request.RequestID = "request-cancel"
	request.Operation = codingremote.OperationInvocationCancel
	request.CallID = "call_cancel"
	response = handler.HandleCodingRemote(t.Context(), request)
	if response.Status != codingremote.ResponseOK || response.Result == nil ||
		response.Result.State != string(nodes.InvocationCanceled) ||
		!response.Result.CancellationConfirmed || source.cancelCalls != 1 {
		t.Fatalf("retained cancel = %#v; cancel calls = %d", response, source.cancelCalls)
	}
}

func TestCodingRemoteServiceStatusRetainsOwnedObservationWithoutReplay(t *testing.T) {
	threadID := uuid.NewString()
	sessionKey := "coding:" + threadID
	invocationID := "node_service_invocation_1"
	invocationReference := "remote_capability_service_reference"
	source := &codingRemoteRetainedSource{
		record: nodes.GatewayInvocationRecord{
			Target: "services", State: nodes.GatewayInvocationDispatched,
			Plan: nodes.ExecutionPlan{InvocationRequest: nodes.InvocationRequest{
				InvocationID: invocationID, NodeID: "private-node-id", Command: "service.action.v1",
			}, Risk: nodes.RiskPrivileged},
		},
		remote: nodes.InvocationRecord{
			InvocationID: invocationID, Command: "service.action.v1",
			State: nodes.InvocationSucceeded, Result: json.RawMessage(`{"service":"vpn","action":"restart"}`),
		},
	}
	cfg := config.DefaultConfig()
	cfg.Gateway.CodingRemote.Enabled = true
	handler := codingRemoteDiscoveryHandler{
		config: func() *config.Config { return cfg }, now: time.Now,
		source: func(*config.Config) (tools.NodeInvocationSource, error) { return source, nil },
	}
	request := codingremote.Request{
		Schema: codingremote.SchemaV1, RequestID: "request-service-status",
		Operation: codingremote.OperationInvocationStatus,
		Grant:     "removed-grant", GrantRevision: "grant-v1", ThreadID: threadID, SessionKey: sessionKey,
		ProjectKey: "git_worktree:" + strings.Repeat("d", 64), LocalProfile: codingscope.ProfileMutate,
		Principal: &runtimecap.Principal{
			Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
			SessionID: sessionKey, ExecutionID: "turn-2",
		},
		CallID: "call_service_status", DiscoveryRevision: "discovery-old",
		Capability: "service-control", CapabilityRevision: "capability-v1",
		CapabilityOperation: "service_action", InvocationID: invocationReference,
	}
	response := handler.HandleCodingRemote(t.Context(), request)
	if response.Status != codingremote.ResponseOK || response.Result == nil ||
		response.Result.State != string(nodes.InvocationSucceeded) ||
		response.Result.Operation != "service_action" || response.Result.Risk != codingremote.RiskWrite ||
		response.Result.InvocationID != invocationReference || source.queryCalls != 1 {
		t.Fatalf("retained service status = %#v; query calls = %d", response, source.queryCalls)
	}
	mismatched := request
	mismatched.RequestID = "request-service-status-wrong-operation"
	mismatched.CapabilityOperation = "service_status"
	response = handler.HandleCodingRemote(t.Context(), mismatched)
	if response.Status != codingremote.ResponseDenied || response.Code != "INVOCATION_DENIED" ||
		source.queryCalls != 1 {
		t.Fatalf("mismatched retained service status = %#v; query calls = %d", response, source.queryCalls)
	}
}

func TestCodingRemoteCapabilityExecutionIdentityBindsExactCapabilityOperation(t *testing.T) {
	threadID := uuid.NewString()
	sessionKey := "coding:" + threadID
	request := codingremote.Request{
		ThreadID: threadID, SessionKey: sessionKey,
		ProjectKey: "git_worktree:" + strings.Repeat("e", 64),
		Grant:      "local-development", GrantRevision: "grant-v1",
		DiscoveryRevision: "discovery-v1", Capability: "build-workspace",
		CapabilityRevision: "capability-v1", CapabilityOperation: "read_file",
		CallID: "call_invoke", InvocationID: "remote_capability_reference",
		Principal: &runtimecap.Principal{
			Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
			SessionID: sessionKey, ExecutionID: "turn-1",
		},
	}
	executionID := func(candidate codingremote.Request) string {
		ctx := codingRemoteExecutionContext(t.Context(), candidate, "main")
		return toolshared.ToolExecutionID(ctx)
	}
	baseline := executionID(request)
	if got := toolshared.ToolCallID(
		codingRemoteExecutionContext(t.Context(), request, "main"),
	); got != request.InvocationID {
		t.Fatalf("invoke tool-call identity = %q, want %q", got, request.InvocationID)
	}
	continued := request
	continued.CallID = "call_status"
	continued.DiscoveryRevision = "discovery-v2"
	continued.Principal = &runtimecap.Principal{
		Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
		SessionID: sessionKey, ExecutionID: "turn-2",
	}
	if got := executionID(continued); got != baseline {
		t.Fatalf("continued capability execution ID = %q, want %q", got, baseline)
	}
	if got := toolshared.ToolCallID(
		codingRemoteExecutionContext(t.Context(), continued, "main"),
	); got != request.InvocationID {
		t.Fatalf("continued tool-call identity = %q, want %q", got, request.InvocationID)
	}
	for name, mutate := range map[string]func(*codingremote.Request){
		"grant revision": func(value *codingremote.Request) { value.GrantRevision = "grant-v2" },
		"capability":     func(value *codingremote.Request) { value.Capability = "other-workspace" },
		"capability revision": func(value *codingremote.Request) {
			value.CapabilityRevision = "capability-v2"
		},
		"operation": func(value *codingremote.Request) { value.CapabilityOperation = "write_file" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := request
			mutate(&candidate)
			if got := executionID(candidate); got == baseline {
				t.Fatalf("authority mutation retained execution ID %q", got)
			}
		})
	}
}

func TestCodingRemoteInvokePreservesUncertainInvocationForNoReplayRecovery(t *testing.T) {
	request := codingremote.Request{
		Grant: "local-development", GrantRevision: "grant-v1", DiscoveryRevision: "discovery-v1",
		Capability: "build-workspace", CapabilityRevision: "capability-v1",
		InvocationID: "remote_capability_uncertain_reference",
	}
	capability := codingremote.CapabilityDescriptor{Target: "build"}
	operation := codingremote.OperationDescriptor{Alias: "write_file", Risk: codingremote.RiskWrite}
	toolResult := toolshared.ErrorResult(`{
		"error":"node dispatch outcome is uncertain",
		"error_code":"DISPATCH_UNCERTAIN",
		"invocation":{
			"invocation_id":"invocation_1",
			"target":"build",
			"command":"workspace.write.v1",
			"gateway_state":"dispatched",
			"state":"unknown",
			"error_code":"DISPATCH_UNCERTAIN",
			"recovery_action":"Call nodes_status with this invocation ID; do not replay the write."
		}
	}`)
	result, err := codingRemoteInvokeResult(request, capability, operation, "invocation_1", toolResult)
	if err != nil {
		t.Fatal(err)
	}
	if result.InvocationID != request.InvocationID || result.State != "unknown" ||
		result.ErrorCode != "DISPATCH_UNCERTAIN" ||
		!strings.Contains(result.RecoveryAction, "remote_capability status") ||
		strings.Contains(result.RecoveryAction, "nodes_status") ||
		!strings.Contains(result.RecoveryAction, "do not replay") {
		t.Fatalf("uncertain invocation = %#v", result)
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

func gatewayCodingRemoteTaskTestConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Gateway.CodingRemote.Enabled = true
	cfg.Execution.Targets = map[string]config.ExecutionTarget{
		"developer": {Type: "node", Node: "private-task-node"},
	}
	cfg.Execution.RemoteCodingScopes = map[string]config.RemoteCodingScope{
		"mintclaw-dev": {
			Target: "developer", Scope: "private-repository-scope", Revision: "scope-v1",
			Profiles: []codingscope.Profile{codingscope.ProfileInvestigate, codingscope.ProfileMutate},
			Requesters: []config.RemoteCodingRequester{{
				Agent: "main", Channel: "telegram", Sender: "owner",
			}},
		},
	}
	cfg.Execution.CodingRemoteGrants = map[string]config.CodingRemoteClientGrant{
		"local-development": {
			Revision: "grant-v1", Agent: "main",
			LocalProfiles: []codingscope.Profile{codingscope.ProfileInvestigate, codingscope.ProfileMutate},
			Tasks: []config.CodingRemoteTaskGrant{{
				Scope:    "mintclaw-dev",
				Profiles: []codingscope.Profile{codingscope.ProfileInvestigate, codingscope.ProfileMutate},
			}},
		},
	}
	return cfg
}

func approvedCodingRemoteTaskSource(t *testing.T) *codingRemoteDiscoverySource {
	t.Helper()
	descriptors, err := nodes.CodingCommandDescriptors()
	if err != nil {
		t.Fatal(err)
	}
	catalog := nodes.CapabilityCatalog{Commands: descriptors}
	catalogHash, err := catalog.Hash()
	if err != nil {
		t.Fatal(err)
	}
	allowed := make([]string, len(descriptors))
	for index, descriptor := range descriptors {
		allowed[index] = descriptor.Name
	}
	snapshot := nodes.Snapshot{
		ID: "private-task-node", State: nodes.StateConnected, ProtocolVersion: nodes.ProtocolVersion,
		Catalog: catalog, CatalogHash: catalogHash, Executor: "local", PolicyRevision: "private-policy-v1",
	}
	registration := nodes.Registration{
		Snapshot: snapshot, ApprovedCatalogHash: catalogHash, ApprovedAt: 1,
		AllowedCommands: allowed,
	}
	return &codingRemoteDiscoverySource{record: tools.NodeDiscoveryRecord{
		Snapshot: snapshot, Registration: &registration, Connected: true,
	}}
}

func codingRemoteTaskBrokerRequest(
	t *testing.T,
	handler codingRemoteDiscoveryHandler,
	cfg *config.Config,
) codingremote.Request {
	t.Helper()
	threadID := uuid.NewString()
	sessionKey := "coding:" + threadID
	discovery := codingremote.Request{
		Schema: codingremote.SchemaV1, RequestID: "request-task-discovery",
		Operation: codingremote.OperationCapabilitiesList,
		Grant:     "local-development", GrantRevision: "grant-v1",
		ThreadID: threadID, SessionKey: sessionKey,
		ProjectKey:   "git_worktree:" + strings.Repeat("a", 64),
		LocalProfile: codingscope.ProfileMutate,
	}
	snapshot := handler.HandleCodingRemote(t.Context(), discovery)
	if snapshot.Status != codingremote.ResponseOK || snapshot.Snapshot == nil {
		t.Fatalf("task discovery = %#v", snapshot)
	}
	request := discovery
	request.RequestID = "request-task-start"
	request.Operation = codingremote.OperationTaskStart
	request.Principal = &runtimecap.Principal{
		Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
		SessionID: sessionKey, ExecutionID: "turn-execution-one",
	}
	request.CallID = "task_call"
	request.DiscoveryRevision = snapshot.Snapshot.DiscoveryRevision
	request.DeadlineUnixMS = time.Now().Add(time.Minute).UnixMilli()
	request.TaskScope = "mintclaw-dev"
	request.TaskScopeRevision = cfg.Execution.RemoteCodingScopes[request.TaskScope].Revision
	request.TaskProfile = codingscope.ProfileInvestigate
	request.TaskObjective = "Inspect the failing test without changing files."
	request.TaskDoneCriteria = "Return the root cause and supporting evidence."
	request.TaskID = codingremote.DeriveTaskID(request)
	if err := request.Validate(); err != nil {
		t.Fatalf("task request validation = %v", err)
	}
	return request
}

func codingRemoteTaskControlRequest(
	start codingremote.Request,
	operation codingremote.Operation,
	callID string,
) codingremote.Request {
	return codingremote.Request{
		Schema: codingremote.SchemaV1, RequestID: "request-" + callID, Operation: operation,
		Grant: start.Grant, GrantRevision: start.GrantRevision,
		ThreadID: start.ThreadID, SessionKey: start.SessionKey,
		ProjectKey: start.ProjectKey, LocalProfile: start.LocalProfile,
		Principal: start.Principal, CallID: callID, DiscoveryRevision: start.DiscoveryRevision,
		DeadlineUnixMS: time.Now().Add(time.Minute).UnixMilli(), TaskID: start.TaskID,
		TaskScope: start.TaskScope, TaskScopeRevision: start.TaskScopeRevision,
		TaskProfile: start.TaskProfile,
	}
}

func codingRemoteTaskDiscoveryRequest(start codingremote.Request) codingremote.Request {
	return codingremote.Request{
		Schema: codingremote.SchemaV1, RequestID: "request-task-rediscovery",
		Operation: codingremote.OperationCapabilitiesList,
		Grant:     start.Grant, GrantRevision: start.GrantRevision,
		ThreadID: start.ThreadID, SessionKey: start.SessionKey,
		ProjectKey: start.ProjectKey, LocalProfile: start.LocalProfile,
	}
}

func codingRemoteServiceTestDescriptors() []nodes.CommandDescriptor {
	baseProfile := nodes.ServiceProfileDescriptor{
		Alias: "server-services", Revision: "services-v1", Manager: "systemd",
		LogLimits:      nodes.ServiceLogLimits{EntriesMax: 50, BytesMax: 4096, AgeSecondsMax: 3600},
		ActionApproval: "required",
	}
	commands := []struct {
		name     string
		risk     nodes.Risk
		approval string
	}{
		{name: "service.action.v1", risk: nodes.RiskPrivileged, approval: "each_command"},
		{name: "service.logs.v1", risk: nodes.RiskRead},
		{name: "service.status.v1", risk: nodes.RiskRead},
	}
	descriptors := make([]nodes.CommandDescriptor, 0, len(commands))
	for _, command := range commands {
		profile := baseProfile
		service := nodes.ServiceDescriptor{Alias: "vpn", Description: "Private network service"}
		switch command.name {
		case "service.action.v1":
			service.Actions = []nodes.ServiceAction{nodes.ServiceActionRestart}
		case "service.logs.v1":
			service.Logs = true
		case "service.status.v1":
			service.Status = true
		}
		profile.Services = []nodes.ServiceDescriptor{service}
		descriptors = append(descriptors, nodes.CommandDescriptor{
			Name: command.name,
			InputSchema: nodes.ServiceCommandInputSchema(
				command.name,
				[]nodes.ServiceProfileDescriptor{profile},
			),
			OutputSchema:   nodes.ServiceCommandOutputSchema(command.name),
			Risk:           command.risk,
			SupportsCancel: true,
			ModelContract: &nodes.CommandModelContract{
				Availability: nodes.ModelUnavailable, TimeoutSecondsMax: 30,
				OutputBytesMax: 4096, ResultKind: "json",
				AuthorityDigest: strings.Repeat("a", 64), ApprovalMode: command.approval,
				Guidance: []string{}, Examples: []json.RawMessage{},
			},
			ServiceProfiles: []nodes.ServiceProfileDescriptor{profile},
		})
	}
	return descriptors
}
