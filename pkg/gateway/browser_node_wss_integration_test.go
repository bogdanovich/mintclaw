//go:build (linux || darwin) && integration

package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	urlpkg "net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/companion"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

func TestCompanionBrowserLifecycleAndReconnectOverProductionWSS(t *testing.T) {
	workspace := t.TempDir()
	registry, admission, runtimeState := newVerticalSliceNodeRuntime(t, workspace)
	server := httptest.NewTLSServer(admission)
	defer server.Close()
	defer closeVerticalSliceAdmission(t, admission)

	host := &wssBrowserHost{profile: wssBrowserProfile()}
	identity, err := companion.LoadOrCreateIdentity(filepath.Join(t.TempDir(), "identity"))
	if err != nil {
		t.Fatal(err)
	}
	commands := browserRuntimeCommands()
	policy := nodes.LocalCommandPolicy{
		Revision: "browser-wss-policy", AllowedCommands: commands,
		MaximumRisk: nodes.RiskWrite, MaxTimeoutSeconds: nodes.MaxBrowserActionSeconds,
		MaxOutputBytes: nodes.MaxBrowserToolResultBytes,
	}
	ledgerPath := companion.InvocationLedgerPath(filepath.Join(t.TempDir(), "runtime"))
	ledger, err := companion.NewFileInvocationLedger(
		ledgerPath,
		companion.DefaultInvocationLedgerLimit,
		companion.DefaultInvocationLedgerBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	commandRuntime, err := companion.NewRuntime(
		identity.ID, "browser-wss-test", policy, ledger, companion.WithBrowserHost(host),
	)
	if err != nil {
		t.Fatal(err)
	}
	clientConfig := wssBrowserCompanionConfig(t, server, policy)
	client := wssBrowserClient(t, clientConfig, identity, commandRuntime)
	result, err := client.Authenticate(t.Context())
	if err != nil || result.State != nodes.StatePendingPairing {
		t.Fatalf("bootstrap admission = %#v, %v", result, err)
	}
	if _, err = registry.Approve(identity.ID, nodes.PairingApproval{
		Aliases: []nodes.Alias{"ab-local-test"}, AllowedCommands: commands, At: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	run := startWSSBrowserClient(t, client)
	defer run.stop(t)
	waitForVerticalSliceNodeState(t, registry, nodes.StateConnected)
	drop := &wssBrowserDropBeforeAcceptance{
		nodeAdmissionHandler: admission,
		command:              nodes.BrowserCommandAct,
	}
	runtimeState.handler = drop

	cfg := wssBrowserGatewayConfig(t, workspace)
	factory, err := newGatewayBrowserWorkerFactory(cfg, runtimeState)
	if err != nil {
		t.Fatal(err)
	}
	local := &wssBrowserLocalFactory{}
	factory.(*gatewayBrowserWorkerFactory).local = local
	broker, err := browser.NewBroker(cfg, browser.NewMemoryStore(), factory)
	if err != nil {
		t.Fatal(err)
	}
	owner := browser.Owner{
		ActorID: "actor_test", AgentID: browser.OpaqueAgentID("browser"),
		SessionKey: "browser-wss-session", ExecutionID: "browser-wss-execution",
	}

	first, err := broker.Open(t.Context(), browser.OpenRequest{
		Owner: owner, Target: "companion", Profile: "managed",
	})
	if err != nil {
		readiness := factory.(*gatewayBrowserWorkerFactory).PassiveTargetReadiness(
			t.Context(), "companion", "managed",
		)
		t.Fatalf(
			"first browser open: %v; readiness = %#v; host commands = %#v",
			err,
			readiness,
			host.commandSequence(),
		)
	}
	initial, err := broker.Observe(t.Context(), owner, first.ID, first.TabID)
	if err != nil || initial.URL != "about:blank" || initial.SnapshotGeneration != 1 {
		invocationID := browserNodeStableID(
			"browser", first.ID, nodes.BrowserCommandObserve, "observe_1",
		)
		record, found, lookupErr := commandRuntime.Invocation(invocationID)
		t.Fatalf(
			"initial observation = %#v, %v; companion invocation = %#v, found %v, error %v; host commands = %#v",
			initial,
			err,
			record,
			found,
			lookupErr,
			host.commandSequence(),
		)
	}
	prepared, err := broker.PrepareAction(t.Context(), browser.PrepareActionRequest{
		Owner: owner, RequestID: "browser-wss-request", SessionID: first.ID, TabID: first.TabID,
		SnapshotID: initial.SnapshotID, SnapshotGeneration: initial.SnapshotGeneration,
		Action: browser.Action{Kind: browser.ActionNavigate, URL: "https://example.com/"},
	})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := broker.ExecuteAction(t.Context(), owner, prepared.Action.ID, nil)
	if err != nil || invocation.State != browser.InvocationSucceeded {
		t.Fatalf("navigate invocation = %#v, %v", invocation, err)
	}
	final, err := broker.Observe(t.Context(), owner, first.ID, first.TabID)
	if err != nil || final.URL != "https://example.com/" || final.SnapshotGeneration != 2 {
		t.Fatalf("final observation = %#v, %v", final, err)
	}
	refStart := strings.Index(final.Snapshot, "[ref=")
	refEnd := strings.Index(final.Snapshot, "]")
	if refStart < 0 || refEnd <= refStart+5 {
		t.Fatalf("click fixture has no bounded ref: %q", final.Snapshot)
	}
	click, err := broker.PrepareAction(t.Context(), browser.PrepareActionRequest{
		Owner: owner, RequestID: "browser-wss-click", SessionID: first.ID, TabID: first.TabID,
		SnapshotID: final.SnapshotID, SnapshotGeneration: final.SnapshotGeneration,
		Action: browser.Action{Kind: browser.ActionClick, Ref: final.Snapshot[refStart+5 : refEnd]},
	})
	if err != nil || !click.RequiresApproval || click.Action.Effect != browser.EffectExternalCommit {
		t.Fatalf("click preparation = %#v, %v", click, err)
	}
	if _, err = broker.ExecuteAction(
		t.Context(),
		owner,
		click.Action.ID,
		nil,
	); !errors.Is(
		err,
		browser.ErrApprovalRequired,
	) {
		t.Fatalf("unapproved click error = %v, want approval required", err)
	}
	invocation, err = broker.ExecuteAction(t.Context(), owner, click.Action.ID, &click.Approval)
	if err != nil || invocation.State != browser.InvocationSucceeded {
		t.Fatalf("click invocation = %#v, %v", invocation, err)
	}
	afterClick, err := broker.Observe(t.Context(), owner, first.ID, first.TabID)
	if err != nil || afterClick.URL != "https://example.com/" || afterClick.SnapshotGeneration != 3 {
		t.Fatalf("click observation = %#v, %v", afterClick, err)
	}
	selectMarker := `combobox "State" [ref=`
	selectStart := strings.Index(afterClick.Snapshot, selectMarker)
	if selectStart < 0 {
		t.Fatalf("select fixture has no bounded ref: %q", afterClick.Snapshot)
	}
	selectStart += len(selectMarker)
	selectEnd := strings.Index(afterClick.Snapshot[selectStart:], "]")
	if selectEnd < 1 {
		t.Fatalf("select fixture has malformed ref: %q", afterClick.Snapshot)
	}
	selection, err := broker.PrepareAction(t.Context(), browser.PrepareActionRequest{
		Owner: owner, RequestID: "browser-wss-select", SessionID: first.ID, TabID: first.TabID,
		SnapshotID: afterClick.SnapshotID, SnapshotGeneration: afterClick.SnapshotGeneration,
		Action: browser.Action{
			Kind: browser.ActionSelect, Ref: afterClick.Snapshot[selectStart : selectStart+selectEnd], Value: "CA",
		},
	})
	if err != nil || selection.RequiresApproval || selection.Action.Effect != browser.EffectLocalEdit {
		t.Fatalf("select preparation = %#v, %v", selection, err)
	}
	invocation, err = broker.ExecuteAction(t.Context(), owner, selection.Action.ID, nil)
	if err != nil || invocation.State != browser.InvocationSucceeded {
		t.Fatalf("select invocation = %#v, %v", invocation, err)
	}
	for _, retainedPath := range []string{nodes.GatewayInvocationStorePath(workspace), ledgerPath} {
		retained, readErr := os.ReadFile(retainedPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if bytes.Contains(retained, []byte(`"CA"`)) {
			t.Fatalf("durable invocation state exposed select option in %s", retainedPath)
		}
	}
	afterSelect, err := broker.Observe(t.Context(), owner, first.ID, first.TabID)
	if err != nil || afterSelect.SnapshotGeneration != 4 {
		t.Fatalf("select observation = %#v, %v", afterSelect, err)
	}
	press, err := broker.PrepareAction(t.Context(), browser.PrepareActionRequest{
		Owner: owner, RequestID: "browser-wss-press", SessionID: first.ID, TabID: first.TabID,
		SnapshotID: afterSelect.SnapshotID, SnapshotGeneration: afterSelect.SnapshotGeneration,
		Action: browser.Action{Kind: browser.ActionPress, Target: "document", Key: "Tab"},
	})
	if err != nil || !press.RequiresApproval || press.Action.Effect != browser.EffectUnknown {
		t.Fatalf("press preparation = %#v, %v", press, err)
	}
	if _, err = broker.ExecuteAction(
		t.Context(),
		owner,
		press.Action.ID,
		nil,
	); !errors.Is(
		err,
		browser.ErrApprovalRequired,
	) {
		t.Fatalf("unapproved press error = %v, want approval required", err)
	}
	invocation, err = broker.ExecuteAction(t.Context(), owner, press.Action.ID, &press.Approval)
	if err != nil || invocation.State != browser.InvocationSucceeded {
		t.Fatalf("press invocation = %#v, %v", invocation, err)
	}
	afterPress, err := broker.Observe(t.Context(), owner, first.ID, first.TabID)
	if err != nil || afterPress.SnapshotGeneration != 5 {
		t.Fatalf("press observation = %#v, %v", afterPress, err)
	}
	scroll, err := broker.PrepareAction(t.Context(), browser.PrepareActionRequest{
		Owner: owner, RequestID: "browser-wss-scroll", SessionID: first.ID, TabID: first.TabID,
		SnapshotID: afterPress.SnapshotID, SnapshotGeneration: afterPress.SnapshotGeneration,
		Action: browser.Action{Kind: browser.ActionScroll, Direction: "down", Amount: 2},
	})
	if err != nil || scroll.RequiresApproval || scroll.Action.Effect != browser.EffectRead {
		t.Fatalf("scroll preparation = %#v, %v", scroll, err)
	}
	invocation, err = broker.ExecuteAction(t.Context(), owner, scroll.Action.ID, nil)
	if err != nil || invocation.State != browser.InvocationSucceeded {
		t.Fatalf("scroll invocation = %#v, %v", invocation, err)
	}
	afterScroll, err := broker.Observe(t.Context(), owner, first.ID, first.TabID)
	if err != nil || afterScroll.URL != "https://example.com/" || afterScroll.SnapshotGeneration != 6 {
		t.Fatalf("scroll observation = %#v, %v", afterScroll, err)
	}
	closed, err := broker.Close(t.Context(), owner, first.ID)
	if err != nil || closed.State != browser.SessionClosed {
		t.Fatalf("first close = %#v, %v", closed, err)
	}

	second := openWSSBrowserSession(t, broker, owner)
	run.stop(t)
	waitForVerticalSliceNodeState(t, registry, nodes.StateDisconnected)
	if _, err = broker.Observe(t.Context(), owner, second.ID, second.TabID); err == nil {
		t.Fatal("node-placed observe unexpectedly succeeded while companion was disconnected")
	}
	if local.opens != 0 {
		t.Fatalf("disconnected node placement opened local workers %d times", local.opens)
	}

	client = wssBrowserClient(t, clientConfig, identity, commandRuntime)
	reconnectedRun := startWSSBrowserClient(t, client)
	defer reconnectedRun.stop(t)
	waitForVerticalSliceNodeState(t, registry, nodes.StateConnected)
	status, err := broker.Status(t.Context(), owner, second.ID)
	if err != nil || status.State != browser.SessionReady {
		t.Fatalf("reconnected status = %#v, %v", status, err)
	}
	closed, err = broker.Close(t.Context(), owner, second.ID)
	if err != nil || closed.State != browser.SessionClosed {
		t.Fatalf("reconnected close = %#v, %v", closed, err)
	}
	if local.opens != 0 {
		t.Fatalf("reconnected node placement opened local workers %d times", local.opens)
	}

	drop.arm()
	third := openWSSBrowserSession(t, broker, owner)
	thirdInitial, err := broker.Observe(t.Context(), owner, third.ID, third.TabID)
	if err != nil {
		t.Fatal(err)
	}
	thirdPrepared := prepareWSSBrowserNavigate(t, broker, owner, third, thirdInitial, "preaccept")
	thirdInvocation, err := broker.ExecuteAction(t.Context(), owner, thirdPrepared.Action.ID, nil)
	if err != nil || thirdInvocation.State != browser.InvocationSucceeded {
		t.Fatalf("pre-acceptance recovery invocation = %#v, %v", thirdInvocation, err)
	}
	if !drop.exactRedispatch() {
		t.Fatalf("pre-acceptance recovery plans = %#v", drop.invocationPlans())
	}
	thirdFinal, err := broker.Observe(t.Context(), owner, third.ID, third.TabID)
	if err != nil || thirdFinal.URL != "https://example.com/" {
		t.Fatalf("pre-acceptance recovery observation = %#v, %v", thirdFinal, err)
	}
	if _, err = broker.Close(t.Context(), owner, third.ID); err != nil {
		t.Fatal(err)
	}
	fourth := openWSSBrowserSession(t, broker, owner)
	fourthInitial, err := broker.Observe(t.Context(), owner, fourth.ID, fourth.TabID)
	if err != nil {
		t.Fatal(err)
	}
	fourthPrepared := prepareWSSBrowserNavigate(t, broker, owner, fourth, fourthInitial, "postaccept")
	accepted, release := host.blockNextNavigate()
	executeDone := make(chan wssBrowserExecuteResult, 1)
	go func() {
		invocation, executeErr := broker.ExecuteAction(
			context.Background(), owner, fourthPrepared.Action.ID, nil,
		)
		executeDone <- wssBrowserExecuteResult{invocation: invocation, err: executeErr}
	}()
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("companion did not accept the blocked browser action")
	}
	reconnectedRun.cancel()
	waitForVerticalSliceNodeState(t, registry, nodes.StateDisconnected)
	replacement := wssBrowserClient(t, clientConfig, identity, commandRuntime)
	replacementRun := startWSSBrowserClient(t, replacement)
	defer replacementRun.stop(t)
	waitForVerticalSliceNodeState(t, registry, nodes.StateConnected)
	close(release)
	var recovered wssBrowserExecuteResult
	select {
	case recovered = <-executeDone:
	case <-time.After(10 * time.Second):
		t.Fatal("gateway did not recover the accepted browser action")
	}
	reconnectedRun.stop(t)
	if recovered.err != nil || recovered.invocation.State != browser.InvocationSucceeded {
		t.Fatalf("post-acceptance recovery invocation = %#v, %v", recovered.invocation, recovered.err)
	}
	fourthFinal, err := broker.Observe(t.Context(), owner, fourth.ID, fourth.TabID)
	if err != nil || fourthFinal.URL != "https://example.com/" {
		t.Fatalf("post-acceptance recovery observation = %#v, %v", fourthFinal, err)
	}
	if _, err = broker.Close(t.Context(), owner, fourth.ID); err != nil {
		t.Fatal(err)
	}

	host.failNextNavigate()
	fifth := openWSSBrowserSession(t, broker, owner)
	fifthInitial, err := broker.Observe(t.Context(), owner, fifth.ID, fifth.TabID)
	if err != nil {
		t.Fatal(err)
	}
	fifthPrepared := prepareWSSBrowserNavigate(t, broker, owner, fifth, fifthInitial, "unknown")
	fifthInvocation, err := broker.ExecuteAction(t.Context(), owner, fifthPrepared.Action.ID, nil)
	if err != nil || fifthInvocation.State != browser.InvocationUnknown ||
		fifthInvocation.SafeFailure != "outcome_unknown" {
		t.Fatalf("unknown-outcome invocation = %#v, %v", fifthInvocation, err)
	}
	fifthStatus, err := broker.Status(t.Context(), owner, fifth.ID)
	if err != nil || fifthStatus.State != browser.SessionLost ||
		fifthStatus.SafeFailure != "outcome_unknown" {
		t.Fatalf("unknown-outcome session = %#v, %v", fifthStatus, err)
	}
	availability, err := broker.ProfileAvailability(t.Context(), "companion", "managed")
	if err != nil || availability.Status != "ready" {
		t.Fatalf("profile availability after unknown outcome = %#v, %v", availability, err)
	}
	sixth := openWSSBrowserSession(t, broker, owner)
	if _, err = broker.Close(t.Context(), owner, sixth.ID); err != nil {
		t.Fatal(err)
	}

	replacementRun.stop(t)
	waitForVerticalSliceNodeState(t, registry, nodes.StateDisconnected)
	restartPlan, restartDescriptor, restartPrincipal := prepareWSSBrowserRestartPlan(
		t, commandRuntime, identity.ID, policy, owner,
	)
	if _, _, err = ledger.Accept(restartPlan); err != nil {
		t.Fatal(err)
	}
	if _, err = ledger.MarkRunning(restartPlan.InvocationID); err != nil {
		t.Fatal(err)
	}
	ledger.Close()
	recoveredLedger, err := companion.NewFileInvocationLedger(
		ledgerPath,
		companion.DefaultInvocationLedgerLimit,
		companion.DefaultInvocationLedgerBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer recoveredLedger.Close()
	recoveredRuntime, err := companion.NewRuntime(
		identity.ID, "browser-wss-test", policy, recoveredLedger, companion.WithBrowserHost(host),
	)
	if err != nil {
		t.Fatal(err)
	}
	recoveredClient := wssBrowserClient(t, clientConfig, identity, recoveredRuntime)
	recoveredRun := startWSSBrowserClient(t, recoveredClient)
	defer recoveredRun.stop(t)
	waitForVerticalSliceNodeState(t, registry, nodes.StateConnected)
	restartToolCallID := "call_browser_restart_unknown"
	gatewayRecord, created, err := factory.(*gatewayBrowserWorkerFactory).node.source.PrepareInvocation(
		"ab-local-test", "ab-local-test", restartToolCallID, restartPrincipal,
		restartPlan, restartDescriptor, true,
		func(current tools.NodeDiscoveryRecord) error {
			if !current.Connected || current.Snapshot.CatalogHash != restartPlan.CatalogHash {
				return errors.New("restarted companion authority changed")
			}
			return nil
		},
	)
	if err != nil || !created {
		t.Fatalf("prepare restarted gateway record = %#v, created %v, error %v", gatewayRecord, created, err)
	}
	restartOwner := nodes.GatewayInvocationOwner{
		Target: "ab-local-test", AgentID: restartPrincipal.AgentID,
		SessionID: restartPrincipal.SessionID, ActorID: restartPrincipal.ActorID,
		ToolCallID: restartToolCallID, WorkspaceID: restartPrincipal.WorkspaceID,
		ExecutionID: restartPrincipal.ExecutionID,
	}
	if _, transitioned, markErr := factory.(*gatewayBrowserWorkerFactory).node.source.store.MarkDispatched(
		restartOwner, gatewayRecord.Plan.InvocationID, gatewayRecord.ExpectedPlanHash,
	); markErr != nil || !transitioned {
		t.Fatalf("mark restarted gateway record dispatched = %v, %v", transitioned, markErr)
	}
	restartedRecord, err := factory.(*gatewayBrowserWorkerFactory).node.source.QueryInvocation(
		t.Context(), restartPrincipal, "ab-local-test", identity.ID, restartPlan.InvocationID,
	)
	if err != nil || restartedRecord.State != nodes.InvocationUnknown ||
		len(restartedRecord.Result) != 0 || restartedRecord.Failure != nil {
		t.Fatalf("restarted browser invocation = %#v, %v", restartedRecord, err)
	}
	if got, want := host.commandSequence(), []string{
		"open", "observe", "navigate", "click", "select", "observe", "press", "observe", "scroll", "close",
		"open", "status", "close",
		"open", "observe", "navigate", "close",
		"open", "observe", "navigate", "close",
		"open", "observe", "navigate", "close",
		"open", "close",
	}; !slices.Equal(got, want) {
		t.Fatalf("browser host sequence = %#v, want %#v", got, want)
	}
}

func TestCompanionBrowserClickDryRunDeniedOverProductionWSS(t *testing.T) {
	workspace := t.TempDir()
	registry, admission, runtimeState := newVerticalSliceNodeRuntime(t, workspace)
	server := httptest.NewTLSServer(admission)
	defer server.Close()
	defer closeVerticalSliceAdmission(t, admission)

	profile := wssBrowserProfile()
	profile.DryRun = true
	profile.AllowApprovedActions = false
	host := &wssBrowserHost{profile: profile}
	identity, err := companion.LoadOrCreateIdentity(filepath.Join(t.TempDir(), "identity"))
	if err != nil {
		t.Fatal(err)
	}
	commands := browserRuntimeCommands()
	policy := nodes.LocalCommandPolicy{
		Revision: "browser-wss-dry-run-policy", AllowedCommands: commands,
		MaximumRisk: nodes.RiskWrite, MaxTimeoutSeconds: nodes.MaxBrowserActionSeconds,
		MaxOutputBytes: nodes.MaxBrowserToolResultBytes,
	}
	ledger, err := companion.NewFileInvocationLedger(
		companion.InvocationLedgerPath(filepath.Join(t.TempDir(), "runtime")),
		companion.DefaultInvocationLedgerLimit,
		companion.DefaultInvocationLedgerBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	runtime, err := companion.NewRuntime(
		identity.ID, "browser-wss-test", policy, ledger, companion.WithBrowserHost(host),
	)
	if err != nil {
		t.Fatal(err)
	}
	clientConfig := wssBrowserCompanionConfig(t, server, policy)
	client := wssBrowserClient(t, clientConfig, identity, runtime)
	result, err := client.Authenticate(t.Context())
	if err != nil || result.State != nodes.StatePendingPairing {
		t.Fatalf("bootstrap admission = %#v, %v", result, err)
	}
	if _, err = registry.Approve(identity.ID, nodes.PairingApproval{
		Aliases: []nodes.Alias{"ab-local-test"}, AllowedCommands: commands, At: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	run := startWSSBrowserClient(t, client)
	defer run.stop(t)
	waitForVerticalSliceNodeState(t, registry, nodes.StateConnected)

	cfg := wssBrowserGatewayConfig(t, workspace)
	target := cfg.Tools.Browser.Targets["companion"]
	localProfile := target.Profiles["managed"]
	localProfile.DryRun = true
	localProfile.AllowApprovedActions = false
	target.Profiles["managed"] = localProfile
	cfg.Tools.Browser.Targets["companion"] = target
	factory, err := newGatewayBrowserWorkerFactory(cfg, runtimeState)
	if err != nil {
		t.Fatal(err)
	}
	broker, err := browser.NewBroker(cfg, browser.NewMemoryStore(), factory)
	if err != nil {
		t.Fatal(err)
	}
	owner := browser.Owner{
		ActorID: "actor_test", AgentID: browser.OpaqueAgentID("browser"),
		SessionKey: "browser-wss-dry-run", ExecutionID: "browser-wss-dry-run-execution",
	}
	session := openWSSBrowserSession(t, broker, owner)
	initial, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	navigate := prepareWSSBrowserNavigate(t, broker, owner, session, initial, "dry-run-navigate")
	if _, err = broker.ExecuteAction(t.Context(), owner, navigate.Action.ID, nil); err != nil {
		t.Fatal(err)
	}
	page, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	refStart := strings.Index(page.Snapshot, "[ref=")
	refEnd := strings.Index(page.Snapshot, "]")
	if refStart < 0 || refEnd <= refStart+5 {
		t.Fatalf("click fixture has no bounded ref: %q", page.Snapshot)
	}
	click, err := broker.PrepareAction(t.Context(), browser.PrepareActionRequest{
		Owner: owner, RequestID: "browser-wss-dry-run-click", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: page.SnapshotID, SnapshotGeneration: page.SnapshotGeneration,
		Action: browser.Action{Kind: browser.ActionClick, Ref: page.Snapshot[refStart+5 : refEnd]},
	})
	if err != nil || !click.RequiresApproval {
		t.Fatalf("dry-run click preparation = %#v, %v", click, err)
	}
	invocation, err := broker.ExecuteAction(t.Context(), owner, click.Action.ID, &click.Approval)
	if !errors.Is(err, browser.ErrDenied) || invocation.State != browser.InvocationCanceled ||
		invocation.SafeFailure != "dry_run_denied" {
		t.Fatalf("dry-run click invocation = %#v, %v", invocation, err)
	}
	if slices.Contains(host.commandSequence(), "click") {
		t.Fatalf("dry-run click reached companion host: %#v", host.commandSequence())
	}
}

func prepareWSSBrowserRestartPlan(
	t *testing.T,
	runtime *companion.Runtime,
	nodeID nodes.ID,
	policy nodes.LocalCommandPolicy,
	owner browser.Owner,
) (nodes.ExecutionPlan, nodes.CommandDescriptor, nodes.GatewayInvocationPrincipal) {
	t.Helper()
	var descriptor nodes.CommandDescriptor
	for _, candidate := range runtime.Catalog().Commands {
		if candidate.Name == nodes.BrowserCommandAct {
			descriptor = candidate
			break
		}
	}
	if descriptor.Name == "" {
		t.Fatal("browser action descriptor is unavailable")
	}
	catalogHash, err := runtime.Catalog().Hash()
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(nodes.BrowserActInput{
		SessionID: "browser_restart_session", TabID: "tab_primary", SnapshotGeneration: 1,
		ActionInvocationID: "browser_restart_action",
		Action:             nodes.BrowserAction{Kind: "navigate", URL: "https://example.com/"},
		Effect:             "navigation", CurrentOrigin: "about:blank",
		PreparedActionHash:    strings.Repeat("a", 64),
		BrowserPolicyRevision: strings.Repeat("b", 64), ProfileRevision: "managed-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	principal := nodes.GatewayInvocationPrincipal{
		AgentID: owner.AgentID, SessionID: "browser_restart_session",
		ActorID: owner.ActorID, WorkspaceID: "browser_restart_workspace",
		ExecutionID: "browser_restart_execution",
	}
	plan, err := nodes.PrepareExecutionPlan(nodes.InvocationRequest{
		InvocationID: "browser_restart_invocation", IdempotencyKey: "browser_restart_idempotency",
		NodeID: nodeID, CatalogHash: catalogHash, Command: descriptor.Name, Input: input,
		AgentID: principal.AgentID, SessionID: principal.SessionID, ActorID: principal.ActorID,
		TimeoutSeconds: nodes.MaxBrowserActionSeconds, OutputLimitBytes: nodes.MaxBrowserToolResultBytes,
	}, descriptor, companion.LocalExecutor, policy.Revision, time.Now(), nodes.MaxExecutionPlanTTL)
	if err != nil {
		t.Fatal(err)
	}
	return plan, descriptor, principal
}

type wssBrowserHost struct {
	mu              sync.Mutex
	profile         nodes.BrowserProfileDescriptor
	commands        []string
	urls            map[string]string
	navigateEntered chan struct{}
	navigateRelease chan struct{}
	navigateFailure bool
}

func (host *wssBrowserHost) BrowserProfiles() []nodes.BrowserProfileDescriptor {
	return []nodes.BrowserProfileDescriptor{host.profile}
}

func (host *wssBrowserHost) Open(
	_ context.Context,
	request nodes.BrowserHostOpenRequest,
) (nodes.BrowserSessionResult, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	host.commands = append(host.commands, "open")
	if host.urls == nil {
		host.urls = make(map[string]string)
	}
	host.urls[request.SessionID] = "about:blank"
	return wssBrowserSessionResult(request.SessionID, "ready"), nil
}

func (host *wssBrowserHost) Status(
	_ context.Context,
	request nodes.BrowserHostStatusRequest,
) (nodes.BrowserSessionResult, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	host.commands = append(host.commands, "status")
	if _, found := host.urls[request.SessionID]; !found {
		return nodes.BrowserSessionResult{}, nodes.ErrBrowserHostNotFound
	}
	return wssBrowserSessionResult(request.SessionID, "ready"), nil
}

func (host *wssBrowserHost) Observe(
	_ context.Context,
	request nodes.BrowserHostObserveRequest,
) (nodes.BrowserObservationResult, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	host.commands = append(host.commands, "observe")
	url, found := host.urls[request.SessionID]
	if !found {
		return nodes.BrowserObservationResult{}, nodes.ErrBrowserHostNotFound
	}
	return wssBrowserObservation(request, url), nil
}

func (host *wssBrowserHost) Navigate(
	_ context.Context,
	request nodes.BrowserHostActRequest,
) (nodes.BrowserObservationResult, error) {
	host.mu.Lock()
	host.commands = append(host.commands, "navigate")
	current, found := host.urls[request.SessionID]
	if !found {
		host.mu.Unlock()
		return nodes.BrowserObservationResult{}, nodes.ErrBrowserHostNotFound
	}
	if current != request.CurrentOrigin && request.CurrentOrigin != "about:blank" {
		host.mu.Unlock()
		return nodes.BrowserObservationResult{}, nodes.ErrBrowserHostStale
	}
	if host.navigateFailure {
		host.navigateFailure = false
		host.mu.Unlock()
		return nodes.BrowserObservationResult{}, nodes.ErrBrowserHostLost
	}
	entered, release := host.navigateEntered, host.navigateRelease
	host.navigateEntered, host.navigateRelease = nil, nil
	host.mu.Unlock()
	if entered != nil {
		close(entered)
		<-release
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	host.urls[request.SessionID] = request.Action.URL
	return wssBrowserObservation(nodes.BrowserHostObserveRequest{
		SessionID: request.SessionID, TabID: request.TabID,
		SnapshotGeneration: request.SnapshotGeneration + 1,
	}, request.Action.URL), nil
}

func (host *wssBrowserHost) Scroll(
	_ context.Context,
	request nodes.BrowserHostActRequest,
) (nodes.BrowserObservationResult, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	host.commands = append(host.commands, "scroll")
	url, found := host.urls[request.SessionID]
	if !found {
		return nodes.BrowserObservationResult{}, nodes.ErrBrowserHostNotFound
	}
	return wssBrowserObservation(nodes.BrowserHostObserveRequest{
		SessionID: request.SessionID, TabID: request.TabID,
		SnapshotGeneration: request.SnapshotGeneration + 1,
	}, url), nil
}

func (host *wssBrowserHost) Click(
	_ context.Context,
	request nodes.BrowserHostActRequest,
) (nodes.BrowserObservationResult, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	host.commands = append(host.commands, "click")
	url, found := host.urls[request.SessionID]
	if !found {
		return nodes.BrowserObservationResult{}, nodes.ErrBrowserHostNotFound
	}
	input := nodes.BrowserActInput{
		SessionID: request.SessionID, TabID: request.TabID,
		SnapshotGeneration: request.SnapshotGeneration,
		ActionInvocationID: request.ActionInvocationID, Action: request.Action,
		Effect: request.Effect, CurrentOrigin: request.CurrentOrigin,
		PreparedActionHash:    request.PreparedActionHash,
		BrowserPolicyRevision: request.BrowserPolicyRevision, ProfileRevision: request.ProfileRevision,
		ExpectedRole: request.ExpectedRole, ExpectedName: request.ExpectedName,
		ApprovalDigest: request.ApprovalDigest,
	}
	if request.Action.Ref != "host_ref_1" || request.ExpectedRole != "button" ||
		request.ExpectedName != "Save" || !nodes.BrowserApprovalDigestMatches(input) {
		return nodes.BrowserObservationResult{}, nodes.ErrBrowserHostDenied
	}
	return wssBrowserObservation(nodes.BrowserHostObserveRequest{
		SessionID: request.SessionID, TabID: request.TabID,
		SnapshotGeneration: request.SnapshotGeneration + 1,
	}, url), nil
}

func (host *wssBrowserHost) Select(
	_ context.Context,
	request nodes.BrowserHostActRequest,
) (nodes.BrowserObservationResult, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	host.commands = append(host.commands, "select")
	url, found := host.urls[request.SessionID]
	if !found {
		return nodes.BrowserObservationResult{}, nodes.ErrBrowserHostNotFound
	}
	if request.Action.Ref != "host_ref_2" || request.Action.Value != "CA" ||
		request.ExpectedRole != "combobox" || request.ExpectedName != "State" ||
		request.Effect != "local_edit" || request.ApprovalDigest != "" {
		return nodes.BrowserObservationResult{}, nodes.ErrBrowserHostDenied
	}
	result := wssBrowserObservation(nodes.BrowserHostObserveRequest{
		SessionID: request.SessionID, TabID: request.TabID,
		SnapshotGeneration: request.SnapshotGeneration + 1,
	}, url)
	result.Snapshot += `
- text "CA"`
	return result, nil
}

func (host *wssBrowserHost) Press(
	_ context.Context,
	request nodes.BrowserHostActRequest,
) (nodes.BrowserObservationResult, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	host.commands = append(host.commands, "press")
	url, found := host.urls[request.SessionID]
	if !found {
		return nodes.BrowserObservationResult{}, nodes.ErrBrowserHostNotFound
	}
	input := nodes.BrowserActInput{
		SessionID: request.SessionID, TabID: request.TabID,
		SnapshotGeneration: request.SnapshotGeneration,
		ActionInvocationID: request.ActionInvocationID, Action: request.Action,
		Effect: request.Effect, CurrentOrigin: request.CurrentOrigin,
		PreparedActionHash:    request.PreparedActionHash,
		BrowserPolicyRevision: request.BrowserPolicyRevision, ProfileRevision: request.ProfileRevision,
		ApprovalDigest: request.ApprovalDigest,
	}
	if request.Action.Target != "document" || request.Action.Key != "Tab" ||
		request.Effect != "unknown" || !nodes.BrowserApprovalDigestMatches(input) {
		return nodes.BrowserObservationResult{}, nodes.ErrBrowserHostDenied
	}
	return wssBrowserObservation(nodes.BrowserHostObserveRequest{
		SessionID: request.SessionID, TabID: request.TabID,
		SnapshotGeneration: request.SnapshotGeneration + 1,
	}, url), nil
}

func (host *wssBrowserHost) failNextNavigate() {
	host.mu.Lock()
	defer host.mu.Unlock()
	host.navigateFailure = true
}

func (host *wssBrowserHost) blockNextNavigate() (<-chan struct{}, chan struct{}) {
	host.mu.Lock()
	defer host.mu.Unlock()
	entered := make(chan struct{})
	release := make(chan struct{})
	host.navigateEntered = entered
	host.navigateRelease = release
	return entered, release
}

func (host *wssBrowserHost) Close(
	_ context.Context,
	request nodes.BrowserHostStatusRequest,
) (nodes.BrowserSessionResult, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	host.commands = append(host.commands, "close")
	delete(host.urls, request.SessionID)
	return wssBrowserSessionResult(request.SessionID, "closed"), nil
}

func (host *wssBrowserHost) commandSequence() []string {
	host.mu.Lock()
	defer host.mu.Unlock()
	return append([]string(nil), host.commands...)
}

func wssBrowserSessionResult(sessionID, state string) nodes.BrowserSessionResult {
	return nodes.BrowserSessionResult{
		SessionID: sessionID, State: state, TabID: "tab_primary", Controller: "agent",
		Features:  nodes.BrowserHostFeatures{Observe: true, Navigate: true},
		ExpiresAt: time.Now().Add(time.Hour).Unix(), IdleExpiresAt: time.Now().Add(time.Minute).Unix(),
	}
}

func wssBrowserObservation(
	request nodes.BrowserHostObserveRequest,
	url string,
) nodes.BrowserObservationResult {
	origin, title, snapshot := url, "Example Domain", "- button \"Save\" [ref=host_ref_1]\n- combobox \"State\" [ref=host_ref_2]"
	elements := []nodes.BrowserElement{
		{Ref: "host_ref_1", Role: "button", Name: "Save"},
		{Ref: "host_ref_2", Role: "combobox", Name: "State"},
	}
	if url == "about:blank" {
		title, snapshot = "", ""
		elements = []nodes.BrowserElement{}
	} else if parsed, err := urlpkg.Parse(url); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		origin = parsed.Scheme + "://" + parsed.Host
	}
	return nodes.BrowserObservationResult{
		SessionID: request.SessionID, TabID: request.TabID,
		SnapshotGeneration: request.SnapshotGeneration,
		URL:                url, Origin: origin, Title: title, Snapshot: snapshot,
		Elements: elements,
	}
}

func wssBrowserProfile() nodes.BrowserProfileDescriptor {
	return nodes.BrowserProfileDescriptor{
		Alias: "managed", Revision: "managed-v1", Driver: nodes.BrowserDriverPlaywrightMCP,
		Mode: nodes.BrowserProfileManaged, NetworkMode: nodes.BrowserNetworkAnyHTTP,
		AllowApprovedActions: true, Actions: []string{"click", "navigate", "press", "scroll", "select"},
		Limits: nodes.BrowserLimits{}.Effective(),
	}
}

func browserRuntimeCommands() []string {
	return []string{
		nodes.BrowserCommandSessionOpen,
		nodes.BrowserCommandSessionStatus,
		nodes.BrowserCommandObserve,
		nodes.BrowserCommandAct,
		nodes.BrowserCommandSessionClose,
	}
}

func wssBrowserCompanionConfig(
	t *testing.T,
	server *httptest.Server,
	policy nodes.LocalCommandPolicy,
) companion.Config {
	t.Helper()
	fingerprint := sha256.Sum256(server.Certificate().Raw)
	cfg, err := (companion.Config{
		GatewayURL: "wss://" + server.Listener.Addr().String() + companion.GatewayPath,
		StateDir:   filepath.Join(t.TempDir(), "client"),
		TLS:        companion.TLSConfig{CertificateSHA256: hex.EncodeToString(fingerprint[:])},
		Reconnect: companion.ReconnectConfig{
			MinDelaySeconds: 1, MaxDelaySeconds: 1, PendingDelaySeconds: 1,
		},
		Policy: policy,
	}).Normalize(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func wssBrowserClient(
	t *testing.T,
	cfg companion.Config,
	identity companion.Identity,
	runtime *companion.Runtime,
) *companion.Client {
	t.Helper()
	client, err := companion.NewClientWithRuntime(
		cfg, identity, "browser-wss-test", runtime, slog.New(slog.DiscardHandler),
	)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

type wssBrowserClientRun struct {
	cancel context.CancelFunc
	done   chan error
	once   sync.Once
}

func startWSSBrowserClient(t *testing.T, client *companion.Client) *wssBrowserClientRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	run := &wssBrowserClientRun{cancel: cancel, done: make(chan error, 1)}
	go func() { run.done <- client.Run(ctx) }()
	return run
}

func (run *wssBrowserClientRun) stop(t *testing.T) {
	t.Helper()
	run.once.Do(func() {
		run.cancel()
		select {
		case err := <-run.done:
			if err != nil {
				t.Errorf("companion client stopped with error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("companion client did not stop")
		}
	})
}

func wssBrowserGatewayConfig(t *testing.T, workspace string) *config.Config {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Nodes.Enabled = true
	cfg.Execution.Targets = map[string]config.ExecutionTarget{
		"ab-local-test": {Type: "node", Node: "ab-local-test", Executor: companion.LocalExecutor},
	}
	cfg.Tools.Browser = config.BrowserToolsConfig{
		Enabled: true, Agents: []string{"browser"},
		Targets: map[string]config.BrowserTargetConfig{
			"companion": {
				Enabled: true, Placement: config.BrowserPlacementNode, NodeTarget: "ab-local-test",
				Profiles: map[string]config.BrowserProfileConfig{
					"managed": {
						Enabled: true, Mode: config.BrowserProfileManaged,
						NetworkMode: config.BrowserNetworkAnyHTTP, AllowApprovedActions: true,
					},
				},
			},
		},
	}
	if err := cfg.ValidateBrowserConfig(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func openWSSBrowserSession(
	t *testing.T,
	broker *browser.Broker,
	owner browser.Owner,
) browser.Session {
	t.Helper()
	session, err := broker.Open(t.Context(), browser.OpenRequest{
		Owner: owner, Target: "companion", Profile: "managed",
	})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func prepareWSSBrowserNavigate(
	t *testing.T,
	broker *browser.Broker,
	owner browser.Owner,
	session browser.Session,
	observation browser.Observation,
	requestSuffix string,
) browser.Preparation {
	t.Helper()
	prepared, err := broker.PrepareAction(t.Context(), browser.PrepareActionRequest{
		Owner: owner, RequestID: "browser-wss-" + requestSuffix,
		SessionID: session.ID, TabID: session.TabID,
		SnapshotID:         observation.SnapshotID,
		SnapshotGeneration: observation.SnapshotGeneration,
		Action:             browser.Action{Kind: browser.ActionNavigate, URL: "https://example.com/"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

type wssBrowserExecuteResult struct {
	invocation browser.Invocation
	err        error
}

// wssBrowserDropBeforeAcceptance models a transport that committed dispatch
// but lost the first action frame before the companion accepted it. Queries
// and the exact redispatch still cross the production AdmissionHandler/WSS
// boundary.
type wssBrowserDropBeforeAcceptance struct {
	nodeAdmissionHandler
	command string

	mu      sync.Mutex
	armed   bool
	dropped bool
	plans   []nodes.ExecutionPlan
}

func (handler *wssBrowserDropBeforeAcceptance) Invoke(
	ctx context.Context,
	nodeID nodes.ID,
	plan nodes.ExecutionPlan,
	ephemeralInput json.RawMessage,
	commit func() error,
) (json.RawMessage, bool, error) {
	if plan.Command != handler.command {
		return handler.nodeAdmissionHandler.Invoke(ctx, nodeID, plan, ephemeralInput, commit)
	}
	handler.mu.Lock()
	if !handler.armed {
		handler.mu.Unlock()
		return handler.nodeAdmissionHandler.Invoke(ctx, nodeID, plan, ephemeralInput, commit)
	}
	handler.plans = append(handler.plans, plan)
	drop := !handler.dropped
	if drop {
		handler.dropped = true
	}
	handler.mu.Unlock()
	if !drop {
		return handler.nodeAdmissionHandler.Invoke(ctx, nodeID, plan, ephemeralInput, commit)
	}
	if commit != nil {
		if err := commit(); err != nil {
			return nil, false, err
		}
	}
	return nil, true, errors.New("simulated pre-acceptance transport loss")
}

func (handler *wssBrowserDropBeforeAcceptance) arm() {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	handler.armed = true
}

func (handler *wssBrowserDropBeforeAcceptance) invocationPlans() []nodes.ExecutionPlan {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	return append([]nodes.ExecutionPlan(nil), handler.plans...)
}

func (handler *wssBrowserDropBeforeAcceptance) exactRedispatch() bool {
	plans := handler.invocationPlans()
	return len(plans) == 2 && reflect.DeepEqual(plans[0], plans[1])
}

type wssBrowserLocalFactory struct {
	opens int
}

func (factory *wssBrowserLocalFactory) Open(
	context.Context,
	browser.WorkerOpenRequest,
) (browser.WorkerOpenResult, error) {
	factory.opens++
	return browser.WorkerOpenResult{}, errors.New("local browser fallback must not run")
}
