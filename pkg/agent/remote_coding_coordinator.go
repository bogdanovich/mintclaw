package agent

import (
	"context"
	"errors"
	"strings"

	codingscope "github.com/bogdanovich/mintclaw/pkg/coding/scope"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	taskregistry "github.com/bogdanovich/mintclaw/pkg/tasks"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// RemoteCodingTaskCoordinator is the channel-independent facade over the
// existing P7.4/P7.7 task runtime. It does not expose a workspace path or node
// authority: the configured agent and durable task projection resolve both.
type RemoteCodingTaskCoordinator struct {
	runtime *remoteCodingRuntime
}

var ErrRemoteCodingTaskUnavailable = errors.New("remote coding task coordinator is unavailable")

// RemoteCodingTaskAuthority is fixed by the trusted local coding runtime and
// the exact gateway grant. None of these values are model-authored paths or
// node identities.
type RemoteCodingTaskAuthority struct {
	AgentID           string
	Grant             string
	GrantRevision     string
	DiscoveryRevision string
	ThreadID          string
	SessionKey        string
	ProjectKey        string
	LocalProfile      codingscope.Profile
	Principal         runtimecap.Principal
	CallID            string
}

type RemoteCodingTaskStart struct {
	Authority     RemoteCodingTaskAuthority
	TaskID        string
	Scope         string
	ScopeRevision string
	Profile       codingscope.Profile
	Objective     string
	DoneCriteria  string
}

type RemoteCodingTaskControl struct {
	Authority        RemoteCodingTaskAuthority
	TaskID           string
	Scope            string
	ScopeRevision    string
	Profile          codingscope.Profile
	Text             string
	QuestionID       string
	QuestionRevision uint64
	AnswerID         string
}

type RemoteCodingTaskQuestion struct {
	ID       string
	Revision uint64
	Prompt   string
	Options  []RemoteCodingTaskQuestionOption
}

type RemoteCodingTaskQuestionOption struct {
	ID          string
	Label       string
	Description string
}

// RemoteCodingTaskView is the bounded broker-facing task projection. It
// intentionally omits node-local scope aliases, paths, channel state, raw
// transcripts, credentials, and deliverable metadata.
type RemoteCodingTaskView struct {
	Grant              string
	GrantRevision      string
	DiscoveryRevision  string
	TaskID             string
	GenerationID       string
	Scope              string
	Target             string
	Profile            codingscope.Profile
	Status             string
	NodeState          string
	ThreadID           string
	WorkerGenerationID string
	Activity           string
	Progress           string
	Branch             string
	HandoffID          string
	FailureCode        string
	Question           *RemoteCodingTaskQuestion
	TerminalSummary    string
}

type remoteCodingLocalContextKey struct{}

func remoteCodingLocalIdentity(ctx context.Context) (remoteCodingIdentity, bool) {
	identity, ok := ctx.Value(remoteCodingLocalContextKey{}).(remoteCodingIdentity)
	return identity, ok
}

// RemoteCodingTaskCoordinator returns the shared coordinator only after the
// remote coding runtime has been configured.
func (al *AgentLoop) RemoteCodingTaskCoordinator() *RemoteCodingTaskCoordinator {
	if al == nil {
		return nil
	}
	al.mu.RLock()
	runtime := al.remoteCoding
	al.mu.RUnlock()
	if runtime == nil {
		return nil
	}
	return &RemoteCodingTaskCoordinator{runtime: runtime}
}

func (coordinator *RemoteCodingTaskCoordinator) Start(
	ctx context.Context,
	request RemoteCodingTaskStart,
) (RemoteCodingTaskView, error) {
	identity, err := coordinator.identity(request.Authority, request.TaskID)
	if err != nil {
		return RemoteCodingTaskView{}, err
	}
	scope, allowed := remoteCodingLocalScopeFor(
		coordinator.runtime.loop.GetConfig(),
		identity,
		request.Scope,
		request.ScopeRevision,
		request.Profile,
	)
	if !allowed || scope.Revision != request.ScopeRevision {
		return RemoteCodingTaskView{}, errors.New("remote coding task grant is unavailable")
	}
	localCtx := context.WithValue(ctx, remoteCodingLocalContextKey{}, identity)
	result := coordinator.runtime.startTask(localCtx, identity.AgentID, map[string]any{
		"scope": request.Scope, "profile": string(request.Profile),
		"objective": request.Objective, "done_criteria": request.DoneCriteria,
	})
	if err = remoteCodingCoordinatorError(result); err != nil {
		return RemoteCodingTaskView{}, err
	}
	return coordinator.viewForAuthority(identity.Workspace, request.TaskID, request.Authority)
}

func (coordinator *RemoteCodingTaskCoordinator) Status(
	ctx context.Context,
	request RemoteCodingTaskControl,
) (RemoteCodingTaskView, error) {
	identity, err := coordinator.identity(request.Authority, request.TaskID)
	if err != nil {
		return RemoteCodingTaskView{}, err
	}
	if _, err = coordinator.controlRecord(identity, request); err != nil {
		return RemoteCodingTaskView{}, err
	}
	localCtx := context.WithValue(ctx, remoteCodingLocalContextKey{}, identity)
	result := coordinator.runtime.statusTask(
		localCtx,
		identity.AgentID,
		map[string]any{"task_id": request.TaskID},
	)
	if err = remoteCodingCoordinatorError(result); err != nil {
		return RemoteCodingTaskView{}, err
	}
	return coordinator.viewForAuthority(identity.Workspace, request.TaskID, request.Authority)
}

func (coordinator *RemoteCodingTaskCoordinator) Steer(
	ctx context.Context,
	request RemoteCodingTaskControl,
) (RemoteCodingTaskView, error) {
	return coordinator.steer(ctx, request, nil)
}

func (coordinator *RemoteCodingTaskCoordinator) Answer(
	ctx context.Context,
	request RemoteCodingTaskControl,
) (RemoteCodingTaskView, error) {
	identity, err := coordinator.identity(request.Authority, request.TaskID)
	if err != nil {
		return RemoteCodingTaskView{}, err
	}
	record, err := coordinator.controlRecord(identity, request)
	if err != nil {
		return RemoteCodingTaskView{}, err
	}
	if record.Coding.Question == nil ||
		record.Coding.Question.ID != request.QuestionID ||
		record.Coding.Question.Revision != request.QuestionRevision {
		return RemoteCodingTaskView{}, errors.New("remote coding task question is no longer current")
	}
	answer := &nodes.CodingQuestionAnswer{
		QuestionID: request.QuestionID, QuestionRevision: request.QuestionRevision,
		AnswerID: request.AnswerID,
	}
	return coordinator.steerWithIdentity(ctx, request, identity, answer)
}

func (coordinator *RemoteCodingTaskCoordinator) steer(
	ctx context.Context,
	request RemoteCodingTaskControl,
	answer *nodes.CodingQuestionAnswer,
) (RemoteCodingTaskView, error) {
	identity, err := coordinator.identity(request.Authority, request.TaskID)
	if err != nil {
		return RemoteCodingTaskView{}, err
	}
	if _, err = coordinator.controlRecord(identity, request); err != nil {
		return RemoteCodingTaskView{}, err
	}
	return coordinator.steerWithIdentity(ctx, request, identity, answer)
}

func (coordinator *RemoteCodingTaskCoordinator) steerWithIdentity(
	ctx context.Context,
	request RemoteCodingTaskControl,
	identity remoteCodingIdentity,
	answer *nodes.CodingQuestionAnswer,
) (RemoteCodingTaskView, error) {
	localCtx := context.WithValue(ctx, remoteCodingLocalContextKey{}, identity)
	localCtx = toolshared.WithToolCallID(localCtx, identity.ToolCallID)
	result := coordinator.runtime.steerTask(localCtx, identity.AgentID, map[string]any{
		"task_id": request.TaskID, "text": request.Text,
	}, answer)
	if err := remoteCodingCoordinatorError(result); err != nil {
		return RemoteCodingTaskView{}, err
	}
	return coordinator.viewForAuthority(identity.Workspace, request.TaskID, request.Authority)
}

func (coordinator *RemoteCodingTaskCoordinator) Cancel(
	ctx context.Context,
	request RemoteCodingTaskControl,
) (RemoteCodingTaskView, error) {
	identity, err := coordinator.identity(request.Authority, request.TaskID)
	if err != nil {
		return RemoteCodingTaskView{}, err
	}
	if _, err = coordinator.controlRecord(identity, request); err != nil {
		return RemoteCodingTaskView{}, err
	}
	localCtx := context.WithValue(ctx, remoteCodingLocalContextKey{}, identity)
	result := coordinator.runtime.cancelTask(
		localCtx,
		identity.AgentID,
		map[string]any{"task_id": request.TaskID},
	)
	if err = remoteCodingCoordinatorError(result); err != nil {
		return RemoteCodingTaskView{}, err
	}
	return coordinator.viewForAuthority(identity.Workspace, request.TaskID, request.Authority)
}

func (coordinator *RemoteCodingTaskCoordinator) controlRecord(
	identity remoteCodingIdentity,
	request RemoteCodingTaskControl,
) (taskregistry.Record, error) {
	tasks := coordinator.runtime.loop.taskRegistryForWorkspace(identity.Workspace)
	record, found := tasks.Get(request.TaskID)
	if !found || record.Runtime != taskregistry.RuntimeCoding || record.Coding == nil ||
		record.Coding.OwnerKind != taskregistry.CodingOwnerLocal ||
		record.Coding.Alias != request.Scope || record.Coding.Revision != request.ScopeRevision ||
		record.Coding.Profile != request.Profile {
		return taskregistry.Record{}, errors.New("remote coding task binding is invalid")
	}
	return record, nil
}

func (coordinator *RemoteCodingTaskCoordinator) identity(
	authority RemoteCodingTaskAuthority,
	taskID string,
) (remoteCodingIdentity, error) {
	if coordinator == nil || coordinator.runtime == nil || coordinator.runtime.loop == nil ||
		authority.Principal.Validate() != nil || authority.Principal.Runtime != runtimecap.KindCoding ||
		authority.Principal.AgentID != strings.TrimSpace(authority.AgentID) ||
		authority.Principal.SessionID != strings.TrimSpace(authority.SessionKey) ||
		authority.SessionKey != "coding:"+authority.ThreadID || strings.TrimSpace(authority.CallID) == "" ||
		strings.TrimSpace(taskID) == "" {
		return remoteCodingIdentity{}, errors.New("remote coding task authority is invalid")
	}
	agentInstance, found := coordinator.runtime.loop.GetRegistry().GetAgent(authority.AgentID)
	if !found || agentInstance == nil || strings.TrimSpace(agentInstance.Workspace) == "" {
		return remoteCodingIdentity{}, errors.New("remote coding task agent is unavailable")
	}
	return remoteCodingIdentity{
		AgentID: authority.AgentID, SessionKey: authority.SessionKey,
		RouteSessionKey: authority.SessionKey, ActorID: authority.Principal.ActorID,
		SenderID: authority.Principal.ActorID, Workspace: agentInstance.Workspace,
		Channel: "coding", ChatID: authority.ThreadID,
		ExecutionID: authority.Principal.ExecutionID, ToolCallID: authority.CallID,
		OwnerKind: taskregistry.CodingOwnerLocal, TaskID: taskID,
		LocalGrant: authority.Grant, LocalGrantRevision: authority.GrantRevision,
		LocalDiscoveryRevision: authority.DiscoveryRevision,
		LocalProjectKey:        authority.ProjectKey, LocalProfile: authority.LocalProfile,
	}, nil
}

func (coordinator *RemoteCodingTaskCoordinator) view(
	workspace string,
	taskID string,
) (RemoteCodingTaskView, error) {
	tasks := coordinator.runtime.loop.taskRegistryForWorkspace(workspace)
	record, found := tasks.Get(taskID)
	if !found || record.Coding == nil || record.Coding.OwnerKind != taskregistry.CodingOwnerLocal {
		return RemoteCodingTaskView{}, errors.New("remote coding task was not found")
	}
	projection := record.Coding
	view := RemoteCodingTaskView{
		Grant: projection.LocalGrant, GrantRevision: projection.LocalGrantRevision,
		DiscoveryRevision: projection.LocalDiscoveryRevision,
		TaskID:            record.TaskID, GenerationID: record.GenerationID,
		Scope: projection.Alias, Target: projection.Target, Profile: projection.Profile,
		Status: string(record.Status), NodeState: projection.NodeState,
		ThreadID: projection.ThreadID, WorkerGenerationID: projection.WorkerGenerationID,
		Activity: projection.Activity, Progress: record.ProgressSummary,
		Branch: projection.Branch, HandoffID: projection.HandoffID,
		FailureCode: projection.FailureCode,
	}
	if projection.Question != nil {
		view.Question = &RemoteCodingTaskQuestion{
			ID: projection.Question.ID, Revision: projection.Question.Revision,
			Prompt: projection.Question.Prompt,
		}
		for _, option := range projection.Question.Options {
			view.Question.Options = append(view.Question.Options, RemoteCodingTaskQuestionOption{
				ID: option.ID, Label: option.Label, Description: option.Description,
			})
		}
	}
	if record.Deliverable != nil {
		view.TerminalSummary = record.Deliverable.Text
	}
	return view, nil
}

func (coordinator *RemoteCodingTaskCoordinator) viewForAuthority(
	workspace string,
	taskID string,
	authority RemoteCodingTaskAuthority,
) (RemoteCodingTaskView, error) {
	view, err := coordinator.view(workspace, taskID)
	if err != nil {
		return RemoteCodingTaskView{}, err
	}
	view.DiscoveryRevision = authority.DiscoveryRevision
	return view, nil
}

func remoteCodingCoordinatorError(result *toolshared.ToolResult) error {
	if result == nil {
		return errors.New("remote coding task operation failed")
	}
	if !result.IsError {
		return nil
	}
	message := strings.TrimSpace(result.ContentForLLM())
	if message == "" {
		message = "remote coding task operation failed"
	}
	if strings.Contains(message, "unavailable") || strings.Contains(message, "uncertain") {
		return errors.Join(ErrRemoteCodingTaskUnavailable, errors.New(message))
	}
	return errors.New(message)
}

func remoteCodingLocalScopeFor(
	cfg *config.Config,
	identity remoteCodingIdentity,
	alias string,
	revision string,
	profile codingscope.Profile,
) (config.RemoteCodingScope, bool) {
	if cfg == nil || identity.OwnerKind != taskregistry.CodingOwnerLocal ||
		identity.LocalProfile != codingscope.ProfileInvestigate &&
			identity.LocalProfile != codingscope.ProfileMutate {
		return config.RemoteCodingScope{}, false
	}
	grant, found := cfg.Execution.CodingRemoteGrants[identity.LocalGrant]
	if !found || grant.Revision != identity.LocalGrantRevision || grant.Agent != identity.AgentID ||
		!containsCodingProfile(grant.LocalProfiles, identity.LocalProfile) {
		return config.RemoteCodingScope{}, false
	}
	allowed := false
	for _, taskGrant := range grant.Tasks {
		if taskGrant.Scope == alias && containsCodingProfile(taskGrant.Profiles, profile) {
			allowed = true
			break
		}
	}
	scope, found := cfg.Execution.RemoteCodingScopes[alias]
	if !allowed || !found || revision != "" && scope.Revision != revision ||
		!containsCodingProfile(scope.Profiles, profile) {
		return config.RemoteCodingScope{}, false
	}
	return scope, true
}

func containsCodingProfile(values []codingscope.Profile, wanted codingscope.Profile) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
