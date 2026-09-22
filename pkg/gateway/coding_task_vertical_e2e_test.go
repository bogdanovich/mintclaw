//go:build (linux || darwin) && integration

package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	codingscope "github.com/bogdanovich/mintclaw/pkg/coding/scope"
	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/companion"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/routing"
	"github.com/bogdanovich/mintclaw/pkg/session"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const (
	remoteCodingVerticalAlias   = "mintclaw"
	remoteCodingMachineAlias    = "operator-machine"
	remoteCodingVerticalTarget  = "developer"
	remoteCodingVerticalNode    = "developer-node"
	remoteCodingVerticalModel   = "remote-coding-e2e-model"
	remoteCodingVerticalChatID  = "remote-coding-e2e-chat"
	remoteCodingVerticalSender  = "remote-coding-e2e-owner"
	remoteCodingVerticalTimeout = 30 * time.Second
)

func TestRemoteCodingTaskTelegramToNativeCompanionVerticalSlice(t *testing.T) {
	workerBinary := remoteCodingVerticalWorkerBinary(t)
	gatewayWorkspace := t.TempDir()
	companionRoot := t.TempDir()
	projectRoot := filepath.Join(companionRoot, "source", "mintclaw")
	machineRoot := filepath.Join(companionRoot, "machine-scopes", "operator")
	remoteRoot := filepath.Join(companionRoot, "remote.git")
	workerHome := filepath.Join(companionRoot, "mintclaw-home")
	worktreeParent := filepath.Join(companionRoot, "worktrees")
	for _, path := range []string{projectRoot, machineRoot, workerHome, worktreeParent} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeRemoteCodingVerticalRepository(t, projectRoot, remoteRoot)
	installRemoteCodingVerticalPublicationCLIs(t, companionRoot)
	provider := newRemoteCodingVerticalProvider(t)
	writeRemoteCodingVerticalWorkerConfig(t, workerHome, provider.server.URL)

	registry, admission, runtimeState := newNodeJobVerticalSliceRuntime(t, gatewayWorkspace)
	server := httptest.NewTLSServer(admission)
	defer server.Close()
	defer closeNodeJobVerticalSliceRuntime(t, runtimeState)

	companionConfig, descriptors := remoteCodingVerticalCompanionConfig(
		t,
		server,
		companionRoot,
		projectRoot,
		machineRoot,
		workerHome,
		worktreeParent,
		workerBinary,
	)
	gatewayConfig := remoteCodingVerticalGatewayConfig(
		gatewayWorkspace,
		descriptors[remoteCodingVerticalAlias].Revision,
		descriptors[remoteCodingMachineAlias].Revision,
	)
	if err := gatewayConfig.ValidateExecutionTargets(); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(companionRoot, "node-config.json")
	writeVerticalSliceConfig(t, configPath, companionConfig)
	companionBinary := buildVerticalSliceCompanion(t, companionRoot)
	process := startVerticalSliceCompanion(t, companionBinary, configPath)
	defer process.stop(t)

	pending := waitForVerticalSliceNodeState(t, registry, nodes.StatePendingPairing)
	if _, err := registry.Approve(pending.ID, nodes.PairingApproval{
		Aliases: []nodes.Alias{remoteCodingVerticalNode},
		AllowedCommands: []string{
			nodes.CodingCommandScopes,
			nodes.CodingCommandTaskStart,
			nodes.CodingCommandTaskStatus,
			nodes.CodingCommandTaskSteer,
			nodes.CodingCommandTaskCancel,
		},
		At: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	waitForVerticalSliceNodeState(t, registry, nodes.StateConnected)

	harness := newRemoteCodingVerticalHarness(t, gatewayConfig, runtimeState)
	defer harness.close(t)
	agentInstance := harness.loop.GetRegistry().GetDefaultAgent()
	agentWorkspace := agentInstance.Workspace
	tool, ok := agentInstance.Tools.Get("coding_task")
	if !ok {
		t.Fatal("coding_task was not registered for the explicit Telegram requester")
	}

	investigationID := startRemoteCodingVerticalTask(
		t,
		tool,
		agentWorkspace,
		"start-investigation",
		"turn-investigation",
		codingtask.TaskModeInvestigate,
		"Inspect the fixture and ask which area to summarize without changing files.",
	)
	first := provider.next(t)
	first.requireStreaming(t)
	first.requireText(t, "without changing files")
	first.respond(t, remoteCodingOpenAIToolCallResponse(
		"I need one bounded choice.",
		"request-area",
		"request_user_input",
		`{"questions":[{"id":"area","header":"Area","question":"Which area should I summarize?",`+
			`"options":[{"label":"Runtime","description":"Summarize runtime code."},`+
			`{"label":"Tests","description":"Summarize test code."}]}]}`,
	))
	waitRemoteCodingVerticalState(t, tool, agentWorkspace, investigationID, "waiting_for_input")
	question := harness.nextMessage(t, remoteCodingVerticalTimeout)
	if question.Metadata.InteractionKind != bus.OutboundInteractionQuestion ||
		question.Context.SenderID != remoteCodingVerticalSender ||
		question.Metadata.InteractionShortID == "" ||
		!strings.Contains(question.Content, "Which area should I summarize?") {
		t.Fatalf("remote coding question = %#v", question)
	}
	harness.answer(t, question, "Runtime", "answer-investigation")
	continuation := provider.next(t)
	continuation.requireText(t, "Runtime")
	continuation.respond(t, remoteCodingOpenAITextResponse("runtime investigation complete"))
	waitRemoteCodingVerticalState(t, tool, agentWorkspace, investigationID, "completed")
	investigationFinal := harness.nextMessage(t, remoteCodingVerticalTimeout)
	assertRemoteCodingVerticalFinal(
		t,
		investigationFinal,
		investigationID,
		"runtime investigation complete",
	)

	mutationID := startRemoteCodingVerticalTask(
		t,
		tool,
		agentWorkspace,
		"start-mutation",
		"turn-mutation",
		codingtask.TaskModeMutate,
		"Create one isolated marker file and report the retained handoff.",
	)
	mutation := provider.next(t)
	mutation.requireText(t, "isolated marker file")
	waitRemoteCodingVerticalState(t, tool, agentWorkspace, mutationID, "running")
	steer := tool.Execute(
		remoteCodingVerticalToolContext(agentWorkspace, "steer-mutation", "turn-mutation-steer"),
		map[string]any{
			"action": "steer", "task_id": mutationID,
			"text": "Name the marker remote-marker.txt and keep the content bounded.",
		},
	)
	if steer == nil || steer.IsError {
		t.Fatalf("remote coding steer = %#v", steer)
	}
	mutation.respond(t, remoteCodingOpenAIToolCallResponse(
		"I will create the requested marker inside the isolated worktree.",
		"write-remote-marker",
		"write_file",
		`{"path":"remote-marker.txt","content":"remote coding vertical proof\n"}`,
	))
	mutationContinuation := provider.next(t)
	mutationContinuation.requireText(t, "Name the marker remote-marker.txt")
	mutationContinuation.requireText(t, "File written")
	mutationContinuation.respond(t, remoteCodingOpenAITextResponse("isolated mutation complete"))
	waitRemoteCodingVerticalState(t, tool, agentWorkspace, mutationID, "completed")
	mutationFinal := harness.nextMessage(t, remoteCodingVerticalTimeout)
	assertRemoteCodingVerticalFinal(t, mutationFinal, mutationID, "isolated mutation complete")
	if !strings.Contains(mutationFinal.Content, "remote-marker.txt") ||
		!strings.Contains(mutationFinal.Content, "Cleanup: retained") {
		t.Fatalf("mutation handoff report = %q", mutationFinal.Content)
	}
	assertRemoteCodingVerticalMutation(t, projectRoot, worktreeParent)

	projectYoloID := startRemoteCodingVerticalTask(
		t,
		tool,
		agentWorkspace,
		"start-project-yolo",
		"turn-project-yolo",
		codingtask.TaskModeProjectYolo,
		"Create and commit project-yolo.txt, push the isolated branch, open a pull request, and deploy it.",
	)
	projectYolo := provider.next(t)
	projectYolo.requireText(t, "Execution profile: project-yolo")
	projectYolo.respond(t, remoteCodingOpenAIToolCallResponse(
		"I will commit and push from the admitted isolated worktree.",
		"commit-and-push-project-yolo",
		"exec",
		`{"action":"run","command":"printf 'project yolo proof\\n' > project-yolo.txt && `+
			`git add project-yolo.txt && git commit -m 'project yolo proof' && git push -u origin HEAD"}`,
	))
	projectYoloPublication := provider.next(t)
	projectYoloPublication.requireText(t, "project yolo proof")
	projectYoloPublication.respond(t, remoteCodingOpenAIToolCallResponse(
		"The branch is pushed; I will open the requested pull request.",
		"open-project-yolo-pr",
		"exec",
		`{"action":"run","command":"gh pr create --fill"}`,
	))
	projectYoloDeploy := provider.next(t)
	projectYoloDeploy.requireText(t, "https://github.com/example/mintclaw/pull/42")
	projectYoloDeploy.respond(t, remoteCodingOpenAIToolCallResponse(
		"The pull request exists; I will run the requested deployment.",
		"deploy-project-yolo",
		"exec",
		`{"action":"run","command":"fake-deploy production"}`,
	))
	projectYoloFinalCall := provider.next(t)
	projectYoloFinalCall.requireText(t, "https://deploy.example/runs/17")
	projectYoloFinalCall.respond(t, remoteCodingOpenAITextResponse("project yolo publication complete"))
	waitRemoteCodingVerticalState(t, tool, agentWorkspace, projectYoloID, "completed")
	projectYoloFinal := harness.nextMessage(t, remoteCodingVerticalTimeout)
	assertRemoteCodingVerticalFinal(t, projectYoloFinal, projectYoloID, "project yolo publication complete")
	for _, expected := range []string{
		"External effects:",
		"commit:",
		"push:",
		"https://github.com/example/mintclaw/pull/42",
		"https://deploy.example/runs/17",
		"(verified)",
	} {
		if !strings.Contains(projectYoloFinal.Content, expected) {
			t.Fatalf("project-yolo final report missing %q: %s", expected, projectYoloFinal.Content)
		}
	}
	assertRemoteCodingVerticalProjectYolo(t, projectRoot, remoteRoot, worktreeParent)

	machineID := startRemoteCodingVerticalTaskInScope(
		t,
		tool,
		agentWorkspace,
		"start-machine-yolo",
		"turn-machine-yolo",
		remoteCodingMachineAlias,
		codingtask.TaskModeMachineYolo,
		"Create a non-Git project, initialize its repository, install one user tool, and start one user service.",
	)
	machine := provider.nextBeforeDelivery(t, harness)
	for _, expected := range []string{
		"Execution profile: machine-yolo",
		"under the companion service account",
		"not a filesystem sandbox",
		"ambient authority already available to the companion account is not sandboxed",
		"not rolled back automatically",
	} {
		machine.requireText(t, expected)
	}
	waitRemoteCodingVerticalState(t, tool, agentWorkspace, machineID, "running")
	machineSteer := tool.Execute(
		remoteCodingVerticalToolContext(agentWorkspace, "steer-machine", "turn-machine-steer"),
		map[string]any{
			"action": "steer", "task_id": machineID,
			"text": "Name the child project app and keep all canary effects harmless and local.",
		},
	)
	if machineSteer == nil || machineSteer.IsError {
		t.Fatalf("machine-yolo steer = %#v", machineSteer)
	}
	machine.respond(t, remoteCodingOpenAIToolCallResponse(
		"I will create the project and exercise the admitted user-level machine operations.",
		"run-machine-yolo-canary",
		"exec",
		`{"action":"run","command":"mkdir app && git init app && `+
			`pipx install demo-tool && systemctl --user start demo.service"}`,
	))
	machineFinalCall := provider.next(t)
	machineFinalCall.requireText(t, "Name the child project app")
	machineFinalCall.requireText(t, "installed demo-tool")
	machineFinalCall.requireText(t, "started demo.service")
	machineFinalCall.respond(t, remoteCodingOpenAITextResponse("machine yolo canary complete"))
	waitRemoteCodingVerticalState(t, tool, agentWorkspace, machineID, "completed")
	machineFinal := harness.nextMessage(t, remoteCodingVerticalTimeout)
	assertRemoteCodingVerticalFinal(t, machineFinal, machineID, "machine yolo canary complete")
	for _, expected := range []string{
		"repository: repository (verified)",
		"package: package (verified)",
		"service: service (verified)",
		"Rollback: unavailable",
	} {
		if !strings.Contains(machineFinal.Content, expected) {
			t.Fatalf("machine-yolo final report missing %q: %s", expected, machineFinal.Content)
		}
	}
	assertRemoteCodingVerticalMachine(t, machineRoot)

	machineCancelID := startRemoteCodingVerticalTaskInScope(
		t,
		tool,
		agentWorkspace,
		"start-machine-cancel",
		"turn-machine-cancel",
		remoteCodingMachineAlias,
		codingtask.TaskModeMachineYolo,
		"Run the harmless process canary until the requester cancels it.",
	)
	machineProcess := provider.next(t)
	machineProcess.respond(t, remoteCodingOpenAIToolCallResponse(
		"I will run the foreground process canary and wait.",
		"run-machine-process-canary",
		"exec",
		`{"action":"run","command":"mintclaw-process-canary"}`,
	))
	processID := waitRemoteCodingVerticalProcessID(t, filepath.Join(machineRoot, "process.pid"))
	machineCanceled := tool.Execute(
		remoteCodingVerticalToolContext(agentWorkspace, "cancel-machine", "turn-machine-cancel-command"),
		map[string]any{"action": "cancel", "task_id": machineCancelID},
	)
	if machineCanceled == nil || machineCanceled.IsError {
		t.Fatalf("machine-yolo cancel = %#v", machineCanceled)
	}
	machineCancelFinal := harness.nextMessage(t, remoteCodingVerticalTimeout)
	assertRemoteCodingVerticalFinal(t, machineCancelFinal, machineCancelID, "canceled")
	for _, expected := range []string{"process: process (uncertain)", "Rollback: unavailable"} {
		if !strings.Contains(machineCancelFinal.Content, expected) {
			t.Fatalf("machine cancellation omitted %q: %s", expected, machineCancelFinal.Content)
		}
	}
	assertRemoteCodingVerticalProcessExited(t, processID)

	cancelID := startRemoteCodingVerticalTask(
		t,
		tool,
		agentWorkspace,
		"start-cancel",
		"turn-cancel",
		codingtask.TaskModeInvestigate,
		"Wait until the requester cancels this investigation.",
	)
	blocked := provider.next(t)
	blocked.requireText(t, "requester cancels")
	canceled := tool.Execute(
		remoteCodingVerticalToolContext(
			agentWorkspace,
			"cancel-investigation",
			"turn-cancel-command",
		),
		map[string]any{"action": "cancel", "task_id": cancelID},
	)
	if canceled == nil || canceled.IsError {
		t.Fatalf("remote coding cancel = %#v", canceled)
	}
	blocked.waitDone(t)
	cancelFinal := harness.nextMessage(t, remoteCodingVerticalTimeout)
	assertRemoteCodingVerticalFinal(t, cancelFinal, cancelID, "canceled")

	provider.requireCallCount(t, 12)
	provider.requireHealthy(t)
	select {
	case duplicate := <-harness.channel.messages:
		t.Fatalf("unexpected duplicate remote coding delivery: %#v", duplicate)
	case <-time.After(500 * time.Millisecond):
	}
	for _, message := range []bus.OutboundMessage{
		question, investigationFinal, mutationFinal, projectYoloFinal, machineFinal, machineCancelFinal, cancelFinal,
	} {
		for _, privatePath := range []string{projectRoot, machineRoot, workerHome, worktreeParent, workerBinary} {
			if strings.Contains(message.Content, privatePath) {
				t.Fatalf("channel message disclosed private path %q: %q", privatePath, message.Content)
			}
		}
	}
}

func remoteCodingVerticalWorkerBinary(t *testing.T) string {
	t.Helper()
	binary := strings.TrimSpace(os.Getenv("MINTCLAW_CODING_WORKER_TEST_BINARY"))
	if binary == "" {
		if os.Getenv("MINTCLAW_REQUIRE_CODING_WORKER_E2E") == "1" {
			t.Fatal("MINTCLAW_CODING_WORKER_TEST_BINARY is required")
		}
		t.Skip("set MINTCLAW_CODING_WORKER_TEST_BINARY to a built mintclaw binary")
	}
	absolute, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(absolute); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("coding worker binary = %q, %v", absolute, err)
	}
	return absolute
}

func remoteCodingVerticalCompanionConfig(
	t *testing.T,
	server *httptest.Server,
	root string,
	projectRoot string,
	machineRoot string,
	workerHome string,
	worktreeParent string,
	workerBinary string,
) (companion.Config, map[string]companion.CodingScopeDescriptor) {
	t.Helper()
	fingerprint := sha256.Sum256(server.Certificate().Raw)
	cfg := companion.Config{
		GatewayURL: strings.Replace(server.URL, "https://", "wss://", 1) + companion.GatewayPath,
		StateDir:   filepath.Join(root, "node-state"),
		TLS: companion.TLSConfig{
			CertificateSHA256: hex.EncodeToString(fingerprint[:]),
		},
		Reconnect: companion.ReconnectConfig{
			MinDelaySeconds: 1, MaxDelaySeconds: 1, PendingDelaySeconds: 1,
		},
		Policy: nodes.LocalCommandPolicy{
			Revision: "remote-coding-e2e-policy",
			AllowedCommands: []string{
				nodes.CodingCommandScopes,
				nodes.CodingCommandTaskStart,
				nodes.CodingCommandTaskStatus,
				nodes.CodingCommandTaskSteer,
				nodes.CodingCommandTaskCancel,
			},
			MaximumRisk:       nodes.RiskWrite,
			MaxTimeoutSeconds: 60,
			MaxOutputBytes:    256 << 10,
		},
		CodingScopes: map[string]companion.CodingScopePolicy{
			remoteCodingVerticalAlias: {
				Revision: "remote-coding-project-v1", Kind: codingscope.KindGitProject,
				SourceParent: filepath.Dir(projectRoot),
				Root:         projectRoot,
				AllowedProfiles: []codingtask.TaskMode{
					codingtask.TaskModeInvestigate,
					codingtask.TaskModeMutate,
					codingtask.TaskModeProjectYolo,
				},
				WorkerExecutable: workerBinary, WorkerProtocolVersion: companion.CodingWorkerProtocolV5,
				MintClawHome: workerHome, CredentialSource: companion.CodingCredentialSourceNative,
				ProviderProfile:    companion.CodingProviderProfileDefault,
				Model:              remoteCodingVerticalModel,
				Provider:           "openai",
				WorktreeParent:     worktreeParent,
				BranchPrefix:       companion.CodingBranchPrefix,
				TaskTimeoutSeconds: 30, MaxConcurrentTasks: 1,
				RetentionSeconds: 300, CleanupPolicy: companion.CodingCleanupRetain,
			},
			remoteCodingMachineAlias: {
				Revision: "remote-coding-machine-v1", Kind: codingscope.KindMachine,
				SourceParent: filepath.Dir(machineRoot), Root: machineRoot,
				AllowedProfiles:  []codingtask.TaskMode{codingtask.TaskModeMachineYolo},
				WorkerExecutable: workerBinary, WorkerProtocolVersion: companion.CodingWorkerProtocolV5,
				MintClawHome: workerHome, CredentialSource: companion.CodingCredentialSourceNative,
				ProviderProfile:    companion.CodingProviderProfileDefault,
				Model:              remoteCodingVerticalModel,
				Provider:           "openai",
				TaskTimeoutSeconds: 30, MaxConcurrentTasks: 1,
				RetentionSeconds: 300, CleanupPolicy: companion.CodingCleanupRetain,
			},
		},
	}
	normalized, err := cfg.Normalize(root)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := companion.NewCodingScopeCatalog(normalized.CodingScopes)
	if err != nil {
		t.Fatal(err)
	}
	descriptors := catalog.List()
	if len(descriptors) != 2 {
		t.Fatalf("coding scope descriptors = %#v", descriptors)
	}
	byAlias := make(map[string]companion.CodingScopeDescriptor, len(descriptors))
	for _, descriptor := range descriptors {
		byAlias[descriptor.Alias] = descriptor
	}
	return normalized, byAlias
}

func remoteCodingVerticalGatewayConfig(
	workspace string,
	projectRevision string,
	machineRevision string,
) *config.Config {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.Defaults.ResponseFooter.Enabled = false
	cfg.Nodes.Enabled = true
	cfg.Execution.Targets = map[string]config.ExecutionTarget{
		remoteCodingVerticalTarget: {Type: "node", Node: remoteCodingVerticalNode},
	}
	cfg.Agents.Defaults.TargetPolicy = &config.TargetPolicy{
		DefaultTarget: remoteCodingVerticalTarget,
		AllowedTargets: []string{
			remoteCodingVerticalTarget,
		},
	}
	cfg.Execution.RemoteCodingScopes = map[string]config.RemoteCodingScope{
		remoteCodingVerticalAlias: {
			Target: remoteCodingVerticalTarget, Scope: remoteCodingVerticalAlias, Revision: projectRevision,
			Profiles: []codingtask.TaskMode{
				codingtask.TaskModeInvestigate,
				codingtask.TaskModeMutate,
				codingtask.TaskModeProjectYolo,
			},
			Requesters: []config.RemoteCodingRequester{{
				Agent: "main", Channel: "telegram", Sender: remoteCodingVerticalSender,
			}},
		},
		remoteCodingMachineAlias: {
			Target: remoteCodingVerticalTarget, Scope: remoteCodingMachineAlias, Revision: machineRevision,
			Profiles: []codingtask.TaskMode{codingtask.TaskModeMachineYolo},
			Requesters: []config.RemoteCodingRequester{{
				Agent: "main", Channel: "telegram", Sender: remoteCodingVerticalSender,
			}},
		},
	}
	return cfg
}

func writeRemoteCodingVerticalRepository(t *testing.T, root string, remote string) {
	t.Helper()
	if output, err := exec.Command("git", "init", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, output)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "mintclaw@example.invalid"},
		{"config", "user.name", "MintClaw Test"},
	} {
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("remote coding fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "README.md"}, {"commit", "-m", "fixture"}} {
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
	for _, args := range [][]string{{"remote", "add", "origin", remote}, {"push", "-u", "origin", "main"}} {
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
}

func installRemoteCodingVerticalPublicationCLIs(t *testing.T, root string) {
	t.Helper()
	bin := filepath.Join(root, "fake-bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"gh":          "#!/bin/sh\nprintf '%s\\n' 'https://github.com/example/mintclaw/pull/42'\n",
		"fake-deploy": "#!/bin/sh\nprintf '%s\\n' 'https://deploy.example/runs/17'\n",
		"pipx": "#!/bin/sh\nprintf '%s\\n' installed > \"$PWD/package-installed.txt\"\n" +
			"printf '%s\\n' 'installed demo-tool'\n",
		"systemctl": "#!/bin/sh\nprintf '%s\\n' active > \"$PWD/service-active.txt\"\n" +
			"printf '%s\\n' 'started demo.service'\n",
		"mintclaw-process-canary": "#!/bin/sh\nprintf '%s\\n' \"$$\" > \"$PWD/process.pid\"\n" +
			"exec sleep 300\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(content), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func writeRemoteCodingVerticalWorkerConfig(t *testing.T, home string, providerURL string) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = remoteCodingVerticalModel
	cfg.Agents.Defaults.Provider = "openai"
	cfg.Agents.Defaults.ModelFallbacks = nil
	cfg.Agents.Defaults.Routing = nil
	cfg.Agents.Defaults.Workspace = filepath.Join(home, "workspace")
	cfg.ModelList = config.SecureModelList{&config.ModelConfig{
		ModelName: remoteCodingVerticalModel,
		Provider:  "openai",
		Model:     "fixture-model",
		APIBase:   providerURL,
		Enabled:   true,
	}}
	if _, err := config.NewRepository(filepath.Join(home, "config.json")).Save(cfg); err != nil {
		t.Fatal(err)
	}
}

type remoteCodingVerticalHarness struct {
	loop       *agent.AgentLoop
	messageBus *bus.MessageBus
	channel    *nodeVerticalSliceChannel
	manager    *channels.Manager
	cancel     context.CancelFunc
	done       chan error
}

func newRemoteCodingVerticalHarness(
	t *testing.T,
	cfg *config.Config,
	runtimeState *nodeAdmissionRuntime,
) *remoteCodingVerticalHarness {
	t.Helper()
	messageBus := bus.NewMessageBus()
	loop := agent.NewAgentLoop(
		cfg,
		messageBus,
		&startupBlockedProvider{reason: "live agent provider is not used by the direct tool boundary"},
		agent.WithIsolatedToolBootstrap(),
	)
	if err := setupNodeTools(cfg, loop, runtimeState); err != nil {
		t.Fatal(err)
	}
	mediaStore := media.NewFileMediaStore()
	loop.SetMediaStore(mediaStore)
	outboundOutbox, err := outbox.OpenCoordinator(cfg.WorkspacePath())
	if err != nil {
		t.Fatal(err)
	}
	loop.SetOutboundOutbox(outboundOutbox)
	manager, err := channels.NewManager(
		cfg,
		messageBus,
		mediaStore,
		channels.WithOutboundOutbox(outboundOutbox),
	)
	if err != nil {
		t.Fatal(err)
	}
	channel := newNodeVerticalSliceChannel()
	manager.RegisterChannel("telegram", channel)
	if err = manager.StartAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	loop.SetChannelManager(manager)
	runCtx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- loop.Run(runCtx) }()
	startupCtx, startupCancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer startupCancel()
	if err = loop.WaitStartup(startupCtx); err != nil {
		t.Fatal(err)
	}
	return &remoteCodingVerticalHarness{
		loop: loop, messageBus: messageBus, channel: channel, manager: manager,
		cancel: cancel, done: done,
	}
}

func (harness *remoteCodingVerticalHarness) answer(
	t *testing.T,
	question bus.OutboundMessage,
	content string,
	messageID string,
) {
	t.Helper()
	optionIndex := 0
	if err := harness.messageBus.PublishInbound(t.Context(), bus.InboundMessage{
		Context: bus.InboundContext{
			Channel: "telegram", ChatID: remoteCodingVerticalChatID, ChatType: "direct",
			SenderID: remoteCodingVerticalSender, ActorID: remoteCodingVerticalSender,
			MessageID: messageID, ReplyToMessageID: "node-e2e-message",
			Interaction: bus.InboundInteractionProjection{
				Response: content, ShortID: question.Metadata.InteractionShortID,
				OptionIndex: &optionIndex, ResponseMessageID: "node-e2e-message",
			},
		},
		Content: content, SessionKey: remoteCodingVerticalSessionKey(),
	}); err != nil {
		t.Fatal(err)
	}
}

func (harness *remoteCodingVerticalHarness) nextMessage(
	t *testing.T,
	timeout time.Duration,
) bus.OutboundMessage {
	t.Helper()
	select {
	case message := <-harness.channel.messages:
		return message
	case <-time.After(timeout):
		t.Fatal("timeout waiting for remote coding channel message")
		return bus.OutboundMessage{}
	}
}

func (harness *remoteCodingVerticalHarness) close(t *testing.T) {
	t.Helper()
	harness.cancel()
	select {
	case err := <-harness.done:
		if err != nil {
			t.Errorf("agent loop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("agent loop did not stop")
	}
	if err := harness.manager.StopAll(context.Background()); err != nil {
		t.Errorf("stop channels: %v", err)
	}
	harness.loop.Close()
	harness.messageBus.Close()
}

func remoteCodingVerticalToolContext(workspace string, callID string, turnID string) context.Context {
	inbound := bus.InboundContext{
		Channel: "telegram", ChatID: remoteCodingVerticalChatID, ChatType: "direct",
		SenderID: remoteCodingVerticalSender, ActorID: remoteCodingVerticalSender,
		MessageID: "origin-" + callID,
	}
	ctx := toolshared.WithToolInboundMetadata(context.Background(), inbound)
	ctx = toolshared.WithToolContext(ctx, inbound.Channel, inbound.ChatID)
	ctx = toolshared.WithToolSessionContext(ctx, "main", remoteCodingVerticalSessionKey(), nil)
	ctx = toolshared.WithToolRouteSessionKey(ctx, remoteCodingVerticalSessionKey())
	ctx = toolshared.WithToolExecutionIdentity(ctx, workspace, turnID)
	return toolshared.WithToolCallID(ctx, callID)
}

func remoteCodingVerticalSessionKey() string {
	allocation := session.AllocateRouteSession(session.AllocationInput{
		AgentID: "main",
		Context: bus.InboundContext{
			Channel: "telegram", ChatID: remoteCodingVerticalChatID, ChatType: "direct",
			SenderID: remoteCodingVerticalSender, ActorID: remoteCodingVerticalSender,
		},
		SessionPolicy: routing.SessionPolicy{Dimensions: []string{"chat"}},
	})
	return allocation.RouteScopeKey
}

func startRemoteCodingVerticalTask(
	t *testing.T,
	tool toolshared.Tool,
	workspace string,
	callID string,
	turnID string,
	profile codingtask.TaskMode,
	objective string,
) string {
	return startRemoteCodingVerticalTaskInScope(
		t, tool, workspace, callID, turnID, remoteCodingVerticalAlias, profile, objective,
	)
}

func startRemoteCodingVerticalTaskInScope(
	t *testing.T,
	tool toolshared.Tool,
	workspace string,
	callID string,
	turnID string,
	scopeAlias string,
	profile codingtask.TaskMode,
	objective string,
) string {
	t.Helper()
	result := tool.Execute(remoteCodingVerticalToolContext(workspace, callID, turnID), map[string]any{
		"action": "start", "scope": scopeAlias, "profile": string(profile),
		"objective": objective, "done_criteria": "Return one bounded, evidence-based summary.",
	})
	if result == nil || result.IsError {
		t.Fatalf("start remote coding task = %#v", result)
	}
	projection := decodeRemoteCodingVerticalProjection(t, result.ContentForLLM())
	taskID, _ := projection["task_id"].(string)
	if taskID == "" || projection["status"] != "queued" {
		t.Fatalf("initial remote coding projection = %#v", projection)
	}
	return taskID
}

func waitRemoteCodingVerticalState(
	t *testing.T,
	tool toolshared.Tool,
	workspace string,
	taskID string,
	want string,
) map[string]any {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last map[string]any
	var lastError string
	for attempt := 1; time.Now().Before(deadline); attempt++ {
		result := tool.Execute(
			remoteCodingVerticalToolContext(
				workspace,
				fmt.Sprintf("status-%s-%d", taskID, attempt),
				fmt.Sprintf("turn-status-%s-%d", taskID, attempt),
			),
			map[string]any{"action": "status", "task_id": taskID},
		)
		if result != nil && !result.IsError {
			last = decodeRemoteCodingVerticalProjection(t, result.ContentForLLM())
			if last["node_state"] == want {
				return last
			}
		} else if result != nil {
			lastError = result.ContentForLLM()
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("remote coding state = %#v, error %q; want %q", last, lastError, want)
	return nil
}

func decodeRemoteCodingVerticalProjection(t *testing.T, content string) map[string]any {
	t.Helper()
	var projection map[string]any
	if err := json.Unmarshal([]byte(content), &projection); err != nil {
		t.Fatalf("decode remote coding projection: %v: %s", err, content)
	}
	return projection
}

func assertRemoteCodingVerticalFinal(
	t *testing.T,
	message bus.OutboundMessage,
	taskID string,
	want string,
) {
	t.Helper()
	if message.Metadata.MessageKind != bus.OutboundMessageKindFinalReply ||
		message.Context.SenderID != remoteCodingVerticalSender ||
		!strings.Contains(message.Content, taskID) ||
		!strings.Contains(strings.ToLower(message.Content), strings.ToLower(want)) {
		t.Fatalf("remote coding final delivery = %#v", message)
	}
}

func assertRemoteCodingVerticalMutation(t *testing.T, sourceRoot string, worktreeParent string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(sourceRoot, "remote-marker.txt")); !os.IsNotExist(err) {
		t.Fatalf("investigated source checkout was modified: %v", err)
	}
	var matches []string
	err := filepath.WalkDir(worktreeParent, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && entry.Name() == "remote-marker.txt" {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("retained mutation markers = %v", matches)
	}
	content, err := os.ReadFile(matches[0])
	if err != nil || string(content) != "remote coding vertical proof\n" {
		t.Fatalf("retained mutation = %q, %v", content, err)
	}
}

func assertRemoteCodingVerticalProjectYolo(
	t *testing.T,
	sourceRoot string,
	remoteRoot string,
	worktreeParent string,
) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(sourceRoot, "project-yolo.txt")); !os.IsNotExist(err) {
		t.Fatalf("project-yolo modified the source checkout: %v", err)
	}
	output, err := exec.Command(
		"git",
		"--git-dir="+remoteRoot,
		"for-each-ref",
		"--format=%(refname:short)",
		"refs/heads/mintclaw/",
	).CombinedOutput()
	if err != nil || !strings.Contains(string(output), "mintclaw/") {
		t.Fatalf("project-yolo remote refs = %q, %v", output, err)
	}
	var matches []string
	err = filepath.WalkDir(worktreeParent, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && entry.Name() == "project-yolo.txt" {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil || len(matches) != 1 {
		t.Fatalf("project-yolo retained worktree paths = %v, %v", matches, err)
	}
}

func assertRemoteCodingVerticalMachine(t *testing.T, machineRoot string) {
	t.Helper()
	for path, want := range map[string]string{
		filepath.Join(machineRoot, "package-installed.txt"): "installed\n",
		filepath.Join(machineRoot, "service-active.txt"):    "active\n",
	} {
		content, err := os.ReadFile(path)
		if err != nil || string(content) != want {
			t.Fatalf("machine canary artifact %q = %q, %v", path, content, err)
		}
	}
	if info, err := os.Stat(filepath.Join(machineRoot, "app", ".git")); err != nil || !info.IsDir() {
		t.Fatalf("machine canary repository = %#v, %v", info, err)
	}
}

func waitRemoteCodingVerticalProcessID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		content, err := os.ReadFile(path)
		if err == nil {
			processID, parseErr := strconv.Atoi(strings.TrimSpace(string(content)))
			if parseErr == nil && processID > 1 {
				return processID
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("machine process canary did not publish a PID at %s", path)
	return 0
}

func assertRemoteCodingVerticalProcessExited(t *testing.T, processID int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(processID, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			t.Fatalf("inspect machine process %d: %v", processID, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("machine process %d remained alive after task cancellation", processID)
}

type remoteCodingVerticalProvider struct {
	server   *httptest.Server
	calls    chan *remoteCodingVerticalProviderCall
	shutdown chan struct{}
	once     sync.Once

	mu     sync.Mutex
	count  int
	errors []error
}

type remoteCodingVerticalProviderCall struct {
	body     []byte
	stream   bool
	response chan string
	done     chan struct{}
}

func newRemoteCodingVerticalProvider(t *testing.T) *remoteCodingVerticalProvider {
	t.Helper()
	provider := &remoteCodingVerticalProvider{
		calls: make(chan *remoteCodingVerticalProviderCall, 8), shutdown: make(chan struct{}),
	}
	provider.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer func() { _ = request.Body.Close() }()
		body, err := io.ReadAll(io.LimitReader(request.Body, 8<<20))
		if err != nil {
			provider.recordError(err)
			http.Error(writer, "read request", http.StatusBadRequest)
			return
		}
		var requestOptions struct {
			Stream bool `json:"stream"`
		}
		if err = json.Unmarshal(body, &requestOptions); err != nil {
			provider.recordError(err)
			http.Error(writer, "decode request", http.StatusBadRequest)
			return
		}
		call := &remoteCodingVerticalProviderCall{
			body: body, stream: requestOptions.Stream,
			response: make(chan string, 1), done: make(chan struct{}),
		}
		provider.mu.Lock()
		provider.count++
		provider.mu.Unlock()
		select {
		case provider.calls <- call:
		case <-request.Context().Done():
			close(call.done)
			return
		case <-provider.shutdown:
			close(call.done)
			return
		}
		defer close(call.done)
		select {
		case response := <-call.response:
			if err = writeRemoteCodingProviderResponse(writer, response, call.stream); err != nil {
				provider.recordError(err)
			}
		case <-request.Context().Done():
		case <-provider.shutdown:
		}
	}))
	t.Cleanup(func() {
		provider.once.Do(func() { close(provider.shutdown) })
		provider.server.CloseClientConnections()
		provider.server.Close()
	})
	return provider
}

func writeRemoteCodingProviderResponse(writer http.ResponseWriter, response string, stream bool) error {
	if !stream {
		writer.Header().Set("Content-Type", "application/json")
		_, err := io.WriteString(writer, response)
		return err
	}
	chunk, err := remoteCodingOpenAIStreamingChunk(response)
	if err != nil {
		return err
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	if _, err = io.WriteString(writer, "data: "+string(chunk)+"\n\n"); err != nil {
		return err
	}
	_, err = io.WriteString(writer, "data: [DONE]\n\n")
	return err
}

func remoteCodingOpenAIStreamingChunk(response string) ([]byte, error) {
	var completion struct {
		Choices []struct {
			Message      map[string]any `json:"message"`
			FinishReason string         `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(response), &completion); err != nil {
		return nil, err
	}
	if len(completion.Choices) == 0 {
		return nil, errors.New("scripted provider response has no choices")
	}
	if toolCalls, ok := completion.Choices[0].Message["tool_calls"].([]any); ok {
		for index, rawCall := range toolCalls {
			if call, ok := rawCall.(map[string]any); ok {
				call["index"] = index
			}
		}
	}
	return json.Marshal(map[string]any{"choices": []any{map[string]any{
		"index": 0, "delta": completion.Choices[0].Message,
		"finish_reason": completion.Choices[0].FinishReason,
	}}})
}

func (provider *remoteCodingVerticalProvider) next(t *testing.T) *remoteCodingVerticalProviderCall {
	t.Helper()
	select {
	case call := <-provider.calls:
		return call
	case <-time.After(remoteCodingVerticalTimeout):
		t.Fatal("timeout waiting for native coding provider request")
		return nil
	}
}

func (provider *remoteCodingVerticalProvider) nextBeforeDelivery(
	t *testing.T,
	harness *remoteCodingVerticalHarness,
) *remoteCodingVerticalProviderCall {
	t.Helper()
	select {
	case call := <-provider.calls:
		return call
	case message := <-harness.channel.messages:
		t.Fatalf("coding task delivered before native provider request: %#v", message)
	case <-time.After(remoteCodingVerticalTimeout):
		t.Fatal("timeout waiting for native coding provider request")
	}
	return nil
}

func (provider *remoteCodingVerticalProvider) recordError(err error) {
	provider.mu.Lock()
	provider.errors = append(provider.errors, err)
	provider.mu.Unlock()
}

func (provider *remoteCodingVerticalProvider) requireCallCount(t *testing.T, want int) {
	t.Helper()
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.count != want {
		t.Fatalf("native coding provider calls = %d, want %d", provider.count, want)
	}
}

func (provider *remoteCodingVerticalProvider) requireHealthy(t *testing.T) {
	t.Helper()
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.errors) != 0 {
		t.Fatalf("native coding provider errors = %v", provider.errors)
	}
}

func (call *remoteCodingVerticalProviderCall) respond(t *testing.T, response string) {
	t.Helper()
	select {
	case call.response <- response:
	case <-call.done:
		t.Fatal("native coding provider request ended before its scripted response")
	}
}

func (call *remoteCodingVerticalProviderCall) waitDone(t *testing.T) {
	t.Helper()
	select {
	case <-call.done:
	case <-time.After(10 * time.Second):
		t.Fatal("native coding provider request remained active after cancellation")
	}
}

func (call *remoteCodingVerticalProviderCall) requireStreaming(t *testing.T) {
	t.Helper()
	if !call.stream {
		t.Fatalf("native coding provider request did not enable streaming: %s", call.body)
	}
}

func (call *remoteCodingVerticalProviderCall) requireText(t *testing.T, text string) {
	t.Helper()
	if !strings.Contains(string(call.body), text) {
		t.Fatalf("native coding provider request does not contain %q: %s", text, call.body)
	}
}

func remoteCodingOpenAITextResponse(content string) string {
	encoded, _ := json.Marshal(content)
	return `{"choices":[{"message":{"role":"assistant","content":` + string(encoded) +
		`},"finish_reason":"stop"}]}`
}

func remoteCodingOpenAIToolCallResponse(content string, id string, name string, arguments string) string {
	encodedContent, _ := json.Marshal(content)
	encodedID, _ := json.Marshal(id)
	encodedName, _ := json.Marshal(name)
	encodedArguments, _ := json.Marshal(arguments)
	return `{"choices":[{"message":{"role":"assistant","content":` + string(encodedContent) +
		`,"tool_calls":[{"id":` + string(encodedID) +
		`,"type":"function","function":{"name":` + string(encodedName) +
		`,"arguments":` + string(encodedArguments) + `}}]},"finish_reason":"tool_calls"}]}`
}
