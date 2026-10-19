# Architecture Robustness Reproductions

These synthetic probes target `77c79cd50e61ce50d75b8fe0e4b60a63f56b5fda`.
They reproduce A1 and A2 in the
[architecture review](../architecture/architecture-robustness-review-20261019.md).
They use the existing agent fixture, real turn/session/tool/coordinator paths,
a synthetic browser source and a temporary local advisory lock. No real browser
account or OS desktop is involved.

## Run the probes

Use a clean checkout of the pinned snapshot on macOS or Linux. The three files
below are temporary probes, not changes to the production implementation. This
command refuses to replace existing files and removes only the files it creates.
Each test asserts the desired invariant, so the reviewed snapshot is expected
to fail all three assertions. A compile failure or timeout is not a reproduction.

```sh
(
  set -e
  python3 - <<'PYCODE'
from pathlib import Path
import re

record = Path("docs/operations/architecture-robustness-20261019-reproductions.md").read_text()
probes = re.findall(r"<!-- probe: ([a-z_]+\.go) -->\n```go\n(.*?)\n```", record, re.S)
assert len(probes) == 3
paths = [Path("pkg/agent") / name for name, _ in probes]
assert all(not path.exists() for path in paths), "Refusing to replace an existing test file"
for path, (_, source) in zip(paths, probes):
    path.write_text(source + "\n")
PYCODE
  trap 'rm -f pkg/agent/architecture_cleanup_probe_test.go pkg/agent/architecture_cleanup_history_probe_test.go pkg/agent/architecture_sweep_probe_test.go' EXIT
  CGO_ENABLED=1 go test -race -tags=goolm,stdjson ./pkg/agent \
    -run '^TestArchitectureProbe' -count=3 -timeout=3m
)
```

## Expected observations

1. The public browser tool opens one synthetic session. Its source still owns
   that session when inspection and owner cleanup fail. The turn returns
   completed with no error and `Task complete; browser closed.`
2. A confirmed fallback cleanup receipt produces a blocked returned answer,
   but canonical history contains `Hello! How can I help you today?`.
3. While restoration waits for the real interaction-store lock, another caller
   creates `new-live-task` in the published task registry. Restoration marks it
   lost with the previous-owner explanation.

The normal selected suites are separate controls; their passing status does
not discharge these newly asserted postconditions. The registry probe targets
concurrent first access. Configured workspaces are normally initialized eagerly.

## Recorded validation

All three archived probes reached their expected invariant assertion in completed
normal runs with `-count=1 -timeout=2m`. The public-browser probe executed one
successful open and retained ownership through failed inspection and cleanup;
its failure was not a missing identity, schema refusal or compile failure.
The other probes observed the canonical/returned answer mismatch and the new
live task becoming lost.

An earlier probe variant completed three repetitions under the race detector
and failed its logical assertions without a data-race report. After strengthening
the browser probe to open the resource through the public tool, the final
combined race repeat stalled without test output. That repeat was stopped and
is inconclusive. It is not counted as a completed reproduction or a passing
race run. The command above retains the stronger exact probes for follow-up.

## Browser inspection and cleanup failure

<!-- probe: architecture_cleanup_probe_test.go -->
```go
package agent

import (
 "context"
 "errors"
 "testing"
 "time"

 "github.com/bogdanovich/mintclaw/pkg/browser"
 "github.com/bogdanovich/mintclaw/pkg/config"
 "github.com/bogdanovich/mintclaw/pkg/providers"
 "github.com/bogdanovich/mintclaw/pkg/taskresult"
 "github.com/bogdanovich/mintclaw/pkg/tools"
)

type architectureUnavailableBrowser struct {
 tools.BrowserToolSource
 inspected, cleaned, opened int
 owned bool
}
func (*architectureUnavailableBrowser) Available() bool { return true }
func (*architectureUnavailableBrowser) ScreenshotAvailable() bool { return false }
func (*architectureUnavailableBrowser) ArtifactTransferAvailable() bool { return false }
func (*architectureUnavailableBrowser) DownloadAvailable() bool { return false }
func (*architectureUnavailableBrowser) HandoffAvailable() bool { return false }
func (source *architectureUnavailableBrowser) Open(_ context.Context, request browser.OpenRequest) (browser.Session,error) {
 now := time.Now().UnixMilli()
 session := browser.Session{ID:"browser_session_probe",Owner:request.Owner,Target:request.Target,Profile:request.Profile,
  State:browser.SessionReady,PolicyRevision:"policy_probe",ControllerGeneration:1,Controller:browser.ControllerAgent,
  TabID:"tab_probe",Revision:1,CreatedAt:now,UpdatedAt:now,LastActivityAt:now,ExpiresAt:now+60000}
 if err := session.Validate(); err != nil { return browser.Session{},err }
 source.opened++; source.owned=true
 return session,nil
}
func (source *architectureUnavailableBrowser) ListOwnerSessions(context.Context,browser.Owner) ([]browser.Session,error) {
 source.inspected++
 return nil,errors.New("synthetic browser owner unavailable")
}
func (source *architectureUnavailableBrowser) CloseOwner(context.Context,browser.Owner) ([]browser.Session,error) {
 source.cleaned++
 return nil,errors.New("synthetic browser owner unavailable")
}

func TestArchitectureProbeUnconfirmedCleanupCannotReturnSuccessfulTurn(t *testing.T) {
 provider := &sequenceProvider{responses:[]*providers.LLMResponse{
  {ToolCalls:[]providers.ToolCall{{ID:"open_probe",Name:"browser_session",Arguments:map[string]any{
   "operation":"open","target":"gateway","profile":"managed","interaction_language":"en",
  }}},FinishReason:"tool_calls"},
  {Content:"Task complete; browser closed.",FinishReason:"stop"},
 }}
 loop, agent, closeFixture := newTurnCoordTestLoop(t,provider)
 defer closeFixture()
 source := &architectureUnavailableBrowser{}
 agent.Tools.Register(tools.NewBrowserSessionTool(tools.NewBrowserToolOptions(config.BrowserToolsConfig{
  Enabled:true,Agents:[]string{agent.ID},Targets:map[string]config.BrowserTargetConfig{
   "gateway":{Enabled:true,Profiles:map[string]config.BrowserProfileConfig{"managed":{Enabled:true,Mode:config.BrowserProfileManaged}}},
  },
 }),source))
 spec := makeTestTurnSpec("cleanup-failure-probe")
 spec.Dispatch.InboundContext.SenderID = "fixture-owner"
 spec.Dispatch.InboundContext.ActorID = "cli:fixture-owner"
 state := newTurnState(agent,spec,turnEventScope{
  turnID:"cleanup-failure-probe", context:newTurnContext(nil,nil,nil),
 })
 result,err := runTestTurn(loop,t.Context(),state,newTestPipeline(loop))
 if source.opened != 1 || !source.owned || source.inspected == 0 || source.cleaned != 1 { t.Fatalf("probe did not reach real browser lifecycle: opened=%d owned=%t inspected=%d cleaned=%d",source.opened,source.owned,source.inspected,source.cleaned) }
 blocked := result.deliverable != nil && result.deliverable.ObjectiveOutcome != nil && result.deliverable.ObjectiveOutcome.Status == taskresult.OutcomeBlocked
 if err == nil && result.status == TurnEndStatusCompleted && !blocked {
  t.Fatalf("unresolved browser inspection and cleanup returned completed turn with no error: final=%q",result.finalContent)
 }
}
```

## Cleanup outcome and canonical history

<!-- probe: architecture_cleanup_history_probe_test.go -->
```go
package agent

import (
 "testing"

 "github.com/bogdanovich/mintclaw/pkg/taskresult"
 "github.com/bogdanovich/mintclaw/pkg/tools"
)

func TestArchitectureProbeCleanupDispositionMatchesCanonicalHistory(t *testing.T) {
 loop, agent, closeFixture := newTurnCoordTestLoop(t,&simpleConvProvider{})
 defer closeFixture()
 cleanup := &turnCleanupTestTool{countingTestTool:&countingTestTool{name:"turn-cleanup"},result:tools.TurnCleanupResult{Receipts:[]taskresult.Receipt{{
  ID:"browser_cleanup_receipt",Kind:taskresult.ReceiptKindResourceCleanup,Target:"browser:gateway/managed",Action:"close",Tool:"browser_session",Summary:"Browser cleanup confirmed.",Metadata:map[string]string{"state":"closed"},
 }}}}
 agent.Tools.Register(cleanup)
 state := newTurnState(agent,makeTestTurnSpec("cleanup-history-probe"),turnEventScope{turnID:"cleanup-history-probe",context:newTurnContext(nil,nil,nil)})
 result,err := runTestTurn(loop,t.Context(),state,newTestPipeline(loop))
 if err != nil { t.Fatal(err) }
 if result.deliverable == nil || result.deliverable.ObjectiveOutcome == nil || result.deliverable.ObjectiveOutcome.Status != taskresult.OutcomeBlocked { t.Fatal("probe did not reach receipt-backed blocked outcome") }
 history := agent.Sessions.GetHistory(state.sessionKey)
 if len(history)==0 { t.Fatal("canonical history absent") }
 last := history[len(history)-1]
 if last.Role != "assistant" || last.Content != result.finalContent {
  t.Fatalf("canonical final=%q; returned final=%q",last.Content,result.finalContent)
 }
}
```

## Task restoration and concurrent first access

<!-- probe: architecture_sweep_probe_test.go -->
```go
//go:build !windows

package agent

import (
 "os"
 "syscall"
 "testing"
 "time"

 "github.com/bogdanovich/mintclaw/pkg/interactions"
 "github.com/bogdanovich/mintclaw/pkg/tasks"
)

// A contended interaction-store lock pauses restoration after the task registry
// is published. A second caller then creates work in the same live registry.
func TestArchitectureProbeTaskRegistryPublishedBeforeRestoreCompletes(t *testing.T) {
 workspace := normalizeRuntimeWorkspace(t.TempDir())
 store := tasks.WorkspaceStorePath(workspace)
 initial := tasks.NewRegistry(store)
 if err := initial.Create(tasks.Record{TaskID:"old-runtime-task", Status:tasks.StatusRunning}); err != nil { t.Fatal(err) }
 lock, err := os.OpenFile(interactions.WorkspaceStorePath(workspace)+".lock", os.O_CREATE|os.O_RDWR, 0600)
 if err != nil { t.Fatal(err) }
 defer lock.Close()
 if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil { t.Fatal(err) }
 held := true
 defer func(){ if held { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) } }()
 owner := newInteractionCoordinator(t.TempDir())
 coordinator := newTaskCoordinator(nil,nil,&owner)
 done := make(chan *tasks.Registry,1)
 go func(){ done <- coordinator.configuredRegistry(workspace) }()
 deadline := time.NewTimer(3*time.Second)
 defer deadline.Stop()
 tick := time.NewTicker(time.Millisecond)
 defer tick.Stop()
 var published *tasks.Registry
 for published == nil {
  published = coordinator.registry(workspace)
  if published != nil { break }
  select { case <-deadline.C: t.Fatal("registry was not published"); case <-tick.C: }
 }
 live := coordinator.configuredRegistry(workspace)
 if live != published { t.Fatal("caller did not receive the published registry") }
 if err := live.Create(tasks.Record{TaskID:"new-live-task", Status:tasks.StatusRunning}); err != nil { t.Fatal(err) }
 if err := syscall.Flock(int(lock.Fd()),syscall.LOCK_UN); err != nil { t.Fatal(err) }
 held = false
 select {
 case restored := <-done:
  if restored != published { t.Fatal("registry identity changed") }
 case <-time.After(3*time.Second): t.Fatal("restore did not finish")
 }
 record, ok := published.Get("new-live-task")
 if !ok { t.Fatal("new live task disappeared") }
 if record.Status != tasks.StatusRunning {
  t.Fatalf("new live task was reconciled as %s: %s", record.Status, record.Error)
 }
}
```
