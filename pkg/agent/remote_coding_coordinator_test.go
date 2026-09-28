package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	codingscope "github.com/bogdanovich/mintclaw/pkg/coding/scope"
	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	taskregistry "github.com/bogdanovich/mintclaw/pkg/tasks"
)

func TestRemoteCodingCoordinatorClassifiesUnavailableResult(t *testing.T) {
	err := remoteCodingCoordinatorError(remoteCodingTaskError(
		"coding-local-one",
		"coding node or its approved command is unavailable",
	))
	if !errors.Is(err, ErrRemoteCodingTaskUnavailable) {
		t.Fatalf("coordinator error = %v, want unavailable classification", err)
	}
}

func TestRemoteCodingCoordinatorUsesSilentLocalOwnerPlane(t *testing.T) {
	fixture := newAgentLoopTestFixture(t, &mockProvider{})
	configureRemoteCodingCoordinatorGrant(fixture.Config)
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, fixture.Loop, manager)
	invoker := newFakeRemoteCodingInvoker()
	if err := fixture.Loop.ConfigureRemoteCodingTaskRuntime(
		func(*config.Config) (RemoteCodingInvoker, error) { return invoker, nil },
	); err != nil {
		t.Fatal(err)
	}
	runtimeCtx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	fixture.Loop.StartRemoteCodingTaskRuntime(runtimeCtx)
	coordinator := fixture.Loop.RemoteCodingTaskCoordinator()
	authority := remoteCodingCoordinatorAuthority(fixture.Agent.ID)
	started, err := coordinator.Start(t.Context(), RemoteCodingTaskStart{
		Authority: authority, TaskID: "coding-local-one",
		Scope: "mintclaw", ScopeRevision: "project-v1",
		Profile:      codingscope.ProfileInvestigate,
		Objective:    "Inspect the failing test without changing files.",
		DoneCriteria: "Return the root cause and supporting evidence.",
	})
	if err != nil || started.TaskID != "coding-local-one" || started.Grant != authority.Grant {
		t.Fatalf("Start() = %#v, %v", started, err)
	}
	if repeated, repeatErr := coordinator.Start(t.Context(), RemoteCodingTaskStart{
		Authority: authority, TaskID: "coding-local-one",
		Scope: "mintclaw", ScopeRevision: "project-v1",
		Profile:      codingscope.ProfileInvestigate,
		Objective:    "Inspect the failing test without changing files.",
		DoneCriteria: "Return the root cause and supporting evidence.",
	}); repeatErr != nil || repeated.TaskID != started.TaskID {
		t.Fatalf("repeated Start() = %#v, %v", repeated, repeatErr)
	}
	if _, conflictErr := coordinator.Start(t.Context(), RemoteCodingTaskStart{
		Authority: authority, TaskID: "coding-local-one",
		Scope: "mintclaw", ScopeRevision: "project-v1",
		Profile:   codingscope.ProfileInvestigate,
		Objective: "A changed objective under the same durable identity.",
	}); conflictErr == nil {
		t.Fatal("Start() accepted changed content under the same task ID")
	}
	tasks := fixture.Loop.taskRegistryForWorkspace(fixture.Agent.Workspace)
	waitRemoteCodingTest(t, func() bool {
		record, found := tasks.Get(started.TaskID)
		return found && record.Coding != nil && record.Coding.ThreadID == invoker.threadID
	})
	record, found := tasks.Get(started.TaskID)
	if !found || record.Coding.OwnerKind != taskregistry.CodingOwnerLocal ||
		record.Coding.LocalProjectKey != authority.ProjectKey ||
		record.DeliveryStatus != taskregistry.DeliveryNotApplicable ||
		record.NotifyPolicy != taskregistry.NotifySilent || record.DeliveryMode != "" {
		t.Fatalf("local coding task = %#v, %v", record, found)
	}
	startCalls := 0
	for _, call := range invoker.snapshot() {
		if call.command == nodes.CodingCommandTaskStart {
			startCalls++
		}
	}
	if startCalls != 1 {
		t.Fatalf("local task start calls = %d, want 1", startCalls)
	}

	waiting := nodes.CodingTaskResult{
		TaskID: record.TaskID, TaskGenerationID: record.GenerationID,
		ScopeAlias: record.Coding.Scope, ScopeRevision: record.Coding.Revision,
		Profile: record.Coding.Profile, ThreadID: invoker.threadID,
		ThreadOpenMode: codingtask.ThreadOpenNew, WorkerGenerationID: invoker.workerID,
		State: codingtask.StateWaitingInput, Revision: 2, Activity: codingtask.ActivityWaitingInput,
		AcceptedAt: 1, UpdatedAt: 2,
		Question: &nodes.CodingQuestionResult{
			QuestionID: "question-one", Revision: 3, Prompt: "Which file should I inspect?",
			Options: []nodes.CodingQuestionOption{
				{ID: "agents", Label: "AGENTS.md", Description: "Inspect agent instructions."},
				{ID: "readme", Label: "README.md", Description: "Inspect the project overview."},
			},
		},
	}
	if err = fixture.Loop.remoteCoding.projectResult(
		fixture.Agent.Workspace,
		tasks,
		record,
		waiting,
	); err != nil {
		t.Fatal(err)
	}
	control := remoteCodingCoordinatorControl(authority, record.TaskID)
	status, err := coordinator.Status(t.Context(), control)
	if err != nil || status.Question == nil || status.Question.Prompt != "Which file should I inspect?" ||
		status.Question.Options[0].Description != "Inspect agent instructions." {
		t.Fatalf("Status() = %#v, %v", status, err)
	}
	select {
	case message := <-manager.sent:
		t.Fatalf("local task emitted channel interaction: %#v", message)
	case <-time.After(150 * time.Millisecond):
	}
	answer := control
	answer.Text = "AGENTS.md"
	answer.QuestionID = "question-one"
	answer.QuestionRevision = 3
	answer.AnswerID = "answer-one"
	answer.Authority.DiscoveryRevision = "discovery-v2"
	answered, err := coordinator.Answer(t.Context(), answer)
	if err != nil || answered.Question != nil || answered.DiscoveryRevision != "discovery-v2" {
		t.Fatalf("Answer() = %#v, %v", answered, err)
	}
	var typedAnswer *nodes.CodingQuestionAnswer
	for _, call := range invoker.snapshot() {
		if call.command != nodes.CodingCommandTaskSteer {
			continue
		}
		input, ok := call.input.(nodes.CodingTaskSteerInput)
		if ok && input.QuestionAnswer != nil {
			typedAnswer = input.QuestionAnswer
		}
	}
	if typedAnswer == nil || typedAnswer.QuestionID != "question-one" ||
		typedAnswer.QuestionRevision != 3 || typedAnswer.AnswerID != "answer-one" {
		t.Fatalf("typed local task answer = %#v", typedAnswer)
	}
	answeredRecord, _ := tasks.Get(record.TaskID)
	waiting.Revision = 4
	waiting.UpdatedAt = 4
	waiting.Question.QuestionID = "question-two"
	waiting.Question.Revision = 1
	waiting.Question.Prompt = "Continue?"
	if err = fixture.Loop.remoteCoding.projectResult(
		fixture.Agent.Workspace,
		tasks,
		answeredRecord,
		waiting,
	); err != nil {
		t.Fatal(err)
	}

	wrong := authority
	wrong.ThreadID = uuid.NewString()
	wrong.SessionKey = "coding:" + wrong.ThreadID
	wrong.Principal.SessionID = wrong.SessionKey
	wrongControl := control
	wrongControl.Authority = wrong
	if _, err = coordinator.Status(t.Context(), wrongControl); err == nil {
		t.Fatal("Status() accepted a different local thread owner")
	}

	delete(fixture.Config.Execution.CodingRemoteGrants, authority.Grant)
	if _, err = coordinator.Status(t.Context(), control); err != nil {
		t.Fatalf("retained Status() after grant revocation error = %v", err)
	}
	steer := control
	steer.Text = "Continue."
	if _, err = coordinator.Steer(t.Context(), steer); err == nil {
		t.Fatal("Steer() accepted a revoked grant")
	}
	revokedAnswer := control
	revokedAnswer.Text = "AGENTS.md"
	revokedAnswer.QuestionID = "question-two"
	revokedAnswer.QuestionRevision = 1
	revokedAnswer.AnswerID = "answer-two"
	if _, err = coordinator.Answer(t.Context(), revokedAnswer); err == nil {
		t.Fatal("Answer() accepted a revoked grant")
	}
	canceled, err := coordinator.Cancel(t.Context(), control)
	if err != nil || canceled.Status != string(taskregistry.StatusCancelled) || canceled.TerminalSummary == "" {
		t.Fatalf("retained Cancel() = %#v, %v", canceled, err)
	}
	settled, _ := tasks.Get(record.TaskID)
	if settled.DeliveryStatus != taskregistry.DeliveryNotApplicable {
		t.Fatalf("local terminal delivery = %q", settled.DeliveryStatus)
	}
	select {
	case message := <-manager.sent:
		t.Fatalf("local terminal task emitted channel delivery: %#v", message)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestRemoteCodingLocalTaskDoesNotExposeChannelTool(t *testing.T) {
	fixture := newAgentLoopTestFixture(t, &mockProvider{})
	configureRemoteCodingCoordinatorGrant(fixture.Config)
	fixture.Config.Execution.RemoteCodingScopes["mintclaw"] = config.RemoteCodingScope{
		Target: "companion", Scope: "mintclaw", Revision: "project-v1",
		Profiles: []codingtask.TaskMode{codingtask.TaskModeInvestigate},
		Requesters: []config.RemoteCodingRequester{{
			Agent: "other", Channel: "telegram", Sender: "owner-42",
		}},
	}
	if err := fixture.Loop.ConfigureRemoteCodingTaskRuntime(
		func(*config.Config) (RemoteCodingInvoker, error) { return newFakeRemoteCodingInvoker(), nil },
	); err != nil {
		t.Fatal(err)
	}
	coordinator := fixture.Loop.RemoteCodingTaskCoordinator()
	authority := remoteCodingCoordinatorAuthority(fixture.Agent.ID)
	if _, err := coordinator.Start(t.Context(), RemoteCodingTaskStart{
		Authority: authority, TaskID: "coding-local-hidden",
		Scope: "mintclaw", ScopeRevision: "project-v1", Profile: codingscope.ProfileInvestigate,
		Objective: "Inspect without changing files.",
	}); err != nil {
		t.Fatal(err)
	}
	tool, err := fixture.Loop.NewRemoteCodingTaskTool(fixture.Config, fixture.Agent.ID)
	if err != nil || tool != nil {
		t.Fatalf("channel coding tool = %#v, %v; want absent", tool, err)
	}
}

func configureRemoteCodingCoordinatorGrant(cfg *config.Config) {
	configureRemoteCodingTestGrant(cfg)
	cfg.Execution.CodingRemoteGrants = map[string]config.CodingRemoteClientGrant{
		"local-development": {
			Revision: "grant-v1", Agent: "main",
			LocalProfiles: []codingscope.Profile{codingscope.ProfileInvestigate, codingscope.ProfileMutate},
			Tasks: []config.CodingRemoteTaskGrant{{
				Scope: "mintclaw", Profiles: []codingscope.Profile{codingscope.ProfileInvestigate},
			}},
		},
	}
}

func remoteCodingCoordinatorAuthority(agentID string) RemoteCodingTaskAuthority {
	threadID := uuid.NewString()
	sessionKey := "coding:" + threadID
	return RemoteCodingTaskAuthority{
		AgentID: agentID, Grant: "local-development", GrantRevision: "grant-v1",
		DiscoveryRevision: "discovery-v1", ThreadID: threadID, SessionKey: sessionKey,
		ProjectKey:   "git_worktree:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		LocalProfile: codingscope.ProfileMutate, CallID: "task-call-one",
		Principal: runtimecap.Principal{
			Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: agentID,
			SessionID: sessionKey, ExecutionID: "turn-execution-one",
		},
	}
}

func remoteCodingCoordinatorControl(
	authority RemoteCodingTaskAuthority,
	taskID string,
) RemoteCodingTaskControl {
	return RemoteCodingTaskControl{
		Authority: authority, TaskID: taskID,
		Scope: "mintclaw", ScopeRevision: "project-v1", Profile: codingscope.ProfileInvestigate,
	}
}
