package outbox

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	outboxpkg "github.com/bogdanovich/mintclaw/pkg/outbox"
)

func nativeCLIFixture(t *testing.T) (string, string, outboxpkg.Identity) {
	t.Helper()
	root := t.TempDir()
	spool, err := bus.NewInboundSpool(bus.InboundSpoolPath(root))
	if err != nil {
		t.Fatal(err)
	}
	input, err := spool.Prepare(t.Context(), bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  "telegram",
			ChatID:   "PRIVATE_CLI_ROUTE",
			SenderID: "owner",
		},
		Content: "PRIVATE_CLI_INPUT",
	})
	if err != nil {
		t.Fatal(err)
	}
	native := bus.NewMessageBus()
	native.SetInboundSpool(spool)
	defer native.Close()
	c, err := outboxpkg.OpenCoordinator(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := c.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	identity := outboxpkg.Identity{
		SourceID: input.SpoolID,
		Kind:     outboxpkg.KindMessage,
		Channel:  "telegram",
		ChatID:   "PRIVATE_CLI_ROUTE",
	}
	a, err := c.AdmitNativeFinalMessage(
		"/PRIVATE_OWNER_WORKSPACE",
		identity,
		bus.OutboundMessage{Content: strings.Repeat("PRIVATE_CLI_BODY ", 1024)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.PrepareAdmission(a.Lease); err != nil {
		t.Fatal(err)
	}
	if err = c.CommitAdmission(a.Lease); err != nil {
		t.Fatal(err)
	}
	if err = c.BeginAttempt(a.Intent.ID); err != nil {
		t.Fatal(err)
	}
	if err = c.MarkDelivered(a.Intent.ID, outboxpkg.Outcome{}); err != nil {
		t.Fatal(err)
	}
	proof, err := native.AckInbound(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if sealed, sealErr := c.SealNativeFinalMessage(a.Intent.ID, proof); sealErr != nil || !sealed {
		t.Fatalf("native seal=%v,%v", sealed, sealErr)
	}
	return root, a.Intent.ID, identity
}

func executeOutbox(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	var output bytes.Buffer
	cmd := NewOutboxCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(t.Context())
	return output.Bytes(), err
}

func TestOutboxCommandDryRunApplyAndPermanentDuplicatePrevention(t *testing.T) {
	root, id, identity := nativeCLIFixture(t)
	path := filepath.Join(outboxpkg.Path(root), id+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"retire", "--instance-root", root, "--min-age", "1ns", "--delivery", id, "--json"}
	output, err := executeOutbox(t, args...)
	var dry outboxpkg.RetirementReport
	if err != nil || json.Unmarshal(output, &dry) != nil || !dry.DryRun || len(dry.Items) != 1 ||
		dry.Items[0].Outcome != "eligible" ||
		dry.CapacityBefore == nil ||
		dry.CapacityAfter != nil {
		t.Fatalf("CLI dry run=%s,%v", output, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("default CLI invocation changed payload")
	}
	output, err = executeOutbox(t, append(args, "--apply")...)
	var applied outboxpkg.RetirementReport
	if err != nil || json.Unmarshal(output, &applied) != nil || applied.DryRun ||
		applied.Items[0].Outcome != "retired" ||
		applied.CapacityAfter == nil ||
		applied.CapacityAfter.Receipts.Count != 1 {
		t.Fatalf("CLI apply=%s,%v", output, err)
	}
	if bytes.Contains(output, []byte("PRIVATE_")) {
		t.Fatal("CLI report exposed content/route/workspace")
	}
	c, err := outboxpkg.OpenCoordinator(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := c.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	identity.Channel, identity.ChatID = "changed", "changed-route"
	replay, err := c.AdmitMessage("/changed", identity, bus.OutboundMessage{Content: "candidate must never dispatch"})
	if err != nil || replay.Dispatch || !replay.Intent.PayloadRetired ||
		replay.Intent.Status != outboxpkg.StatusDelivered ||
		replay.Intent.Message != nil {
		t.Fatalf("CLI erased duplicate prevention=%#v,%v", replay, err)
	}
}

func TestOutboxBudgetAlertsRemainNonzeroWithoutDiscardingReceipts(t *testing.T) {
	root, id, _ := nativeCLIFixture(t)
	output, err := executeOutbox(
		t,
		"inspect",
		"--instance-root",
		root,
		"--min-age",
		"1ns",
		"--max-bytes",
		"1",
		"--json",
	)
	var capacity outboxpkg.CapacityReport
	if err == nil || json.Unmarshal(output, &capacity) != nil || !capacity.OperatorActionRequired ||
		!capacity.BudgetExceeded ||
		capacity.TotalRecords != 1 {
		t.Fatalf("budget inspection=%s,%v", output, err)
	}
	output, err = executeOutbox(
		t,
		"retire",
		"--instance-root",
		root,
		"--min-age",
		"1ns",
		"--delivery",
		id,
		"--max-bytes",
		"1",
		"--apply",
		"--json",
	)
	var result outboxpkg.RetirementReport
	if err == nil || json.Unmarshal(output, &result) != nil || result.Items[0].Outcome != "retired" ||
		result.CapacityAfter == nil ||
		result.CapacityAfter.Receipts.Count != 1 ||
		!result.CapacityAfter.OperatorActionRequired {
		t.Fatalf("post-retirement budget=%s,%v", output, err)
	}
	if _, err = os.Stat(filepath.Join(outboxpkg.Path(root), id+".json")); err != nil {
		t.Fatal("budget alert removed delivery identity")
	}
}

func TestOutboxCommandRejectsMissingPolicyAndLiveWriters(t *testing.T) {
	root, id, _ := nativeCLIFixture(t)
	path := filepath.Join(outboxpkg.Path(root), id+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"retire", "--instance-root", root, "--delivery", id, "--apply"},
		{"retire", "--instance-root", root, "--min-age", "1h", "--apply"},
		{"retire", "--instance-root", root, "--min-age", "0", "--delivery", id, "--apply"},
		{"retire", "--instance-root", root, "--min-age", "1h", "--delivery", id, "--delivery", id, "--apply"},
		{"inspect", "--instance-root", root, "--min-age", "1h", "--max-records", "-1"},
	} {
		if _, err = executeOutbox(t, args...); err == nil {
			t.Fatalf("invalid CLI request accepted: %v", args)
		}
	}
	c, err := outboxpkg.OpenCoordinator(root)
	if err != nil {
		t.Fatal(err)
	}
	output, err := executeOutbox(
		t,
		"retire",
		"--instance-root",
		root,
		"--min-age",
		"1ns",
		"--delivery",
		id,
		"--apply",
		"--json",
	)
	if err == nil || !strings.Contains(err.Error(), "another writer") {
		t.Fatalf("live CLI=%s,%v", output, err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("invalid or live-writer CLI changed payload")
	}
	missing := filepath.Join(t.TempDir(), "missing-instance")
	if _, err = executeOutbox(t, "inspect", "--instance-root", missing, "--min-age", "1h"); err == nil {
		t.Fatal("CLI accepted missing store")
	}
	if _, err = os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("CLI bootstrapped missing instance")
	}
}
