package outbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
)

func deliveredRetentionFixture(t *testing.T, root, source string, plain, transcript bool) Intent {
	t.Helper()
	coordinator, err := OpenCoordinator(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = coordinator.Close() })
	then := time.Now().Add(-48 * time.Hour).UTC()
	coordinator.now = func() time.Time { return then }
	coordinator.store.now = coordinator.now
	identity := testIdentity()
	identity.SourceID = source
	var native *bus.MessageBus
	var nativeInput bus.InboundMessage
	if plain {
		native, nativeInput = retentionNativeSource(t, root)
		identity.SourceID = nativeInput.SpoolID
	}
	message := bus.OutboundMessage{Content: strings.Repeat("private-body-sentinel ", 1024)}
	if transcript {
		message.Transcript = &bus.OutboundTranscriptProjection{
			AgentID: "main", SessionKey: "session",
			Scope: &bus.OutboundScope{AgentID: "main"}, Content: "canonical history",
		}
	}
	admit := coordinator.AdmitMessage
	if plain {
		admit = coordinator.AdmitNativeFinalMessage
	}
	admission, err := admit("/agents/main", identity, message)
	if err != nil {
		t.Fatal(err)
	}
	commitTestAdmission(t, coordinator, admission.Lease)
	if err = coordinator.BeginAttempt(admission.Intent.ID); err != nil {
		t.Fatal(err)
	}
	if err = coordinator.MarkDelivered(
		admission.Intent.ID,
		Outcome{PlatformMessageIDs: []string{"remote-id"}},
	); err != nil {
		t.Fatal(err)
	}
	if plain {
		proof, ackErr := native.AckInbound(t.Context(), nativeInput)
		if ackErr != nil {
			t.Fatal(ackErr)
		}
		sealed, sealErr := coordinator.SealNativeFinalMessage(admission.Intent.ID, proof)
		if sealErr != nil || !sealed {
			t.Fatalf("fixture source seal=%v,%v", sealed, sealErr)
		}
	}
	intent, err := coordinator.Get(admission.Intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = coordinator.Close(); err != nil {
		t.Fatal(err)
	}
	return intent
}

func TestOfflineRetirementDryRunAndCanonicalReplay(t *testing.T) {
	root := t.TempDir()
	original := deliveredRetentionFixture(t, root, "plain-retirement", true, false)
	path := filepath.Join(Path(root), original.ID+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	policy := RetirementPolicy{DeliveryIDs: []string{original.ID}, MinAge: 24 * time.Hour}
	report, err := RetirePayloadsOffline(t.Context(), root, policy)
	if err != nil || !report.DryRun || len(report.Items) != 1 || report.Items[0].Outcome != "eligible" {
		t.Fatalf("dry run = %#v, %v", report, err)
	}
	afterDryRun, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, afterDryRun) {
		t.Fatal("dry run changed canonical payload")
	}
	policy.Apply = true
	report, err = RetirePayloadsOffline(t.Context(), root, policy)
	if err != nil || report.DryRun || report.Items[0].Outcome != "retired" {
		t.Fatalf("apply = %#v, %v", report, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("private-body-sentinel")) || len(data) >= len(before) {
		t.Fatal("retirement retained the body or failed to compact")
	}
	if report.Items[0].AfterBytes != int64(len(data)) {
		t.Fatal("reported compact bytes differ from stored bytes")
	}
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	retired, err := store.Get(original.ID)
	if err != nil || !retired.PayloadRetired || retired.Status != StatusDelivered || retired.Message != nil ||
		retired.Media != nil {
		t.Fatalf("retired receipt = %#v, %v", retired, err)
	}
	if retired.ID != original.ID || retired.Identity != original.Identity || retired.CreatedAt != original.CreatedAt ||
		retired.UpdatedAt != original.UpdatedAt || retired.PlatformMessageIDs[0] != "remote-id" {
		t.Fatal("retirement changed canonical identity or terminal truth")
	}
	observed, err := InspectIntent(data, original.ID)
	if err != nil || !observed.PayloadRetired {
		t.Fatalf("native inspection = %#v, %v", observed, err)
	}
	candidateIdentity := original.Identity
	candidateIdentity.Channel, candidateIdentity.ChatID, candidateIdentity.SessionKey = "web", "changed-route", "changed-session"
	candidate, err := NewMessageIntent(
		"/other-workspace",
		candidateIdentity,
		bus.OutboundMessage{Content: "replacement must not be used"},
		time.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.Create(candidate)
	if err != nil || !replay.PayloadRetired || replay.Message != nil || replay.Identity != original.Identity {
		t.Fatalf("replay = %#v, %v", replay, err)
	}
	recovered, err := store.Recover()
	if err != nil || len(recovered.Intents) != 0 || len(recovered.Issues) != 0 {
		t.Fatalf("retired recovery = %#v, %v", recovered, err)
	}
	again, err := RetirePayloadsOffline(t.Context(), root, policy)
	if err != nil || again.Items[0].Outcome != "already_retired" {
		t.Fatalf("idempotent apply = %#v, %v", again, err)
	}
	coordinator, err := OpenCoordinator(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = coordinator.Close() })
	admission, err := coordinator.AdmitNativeFinalMessage(
		"/other",
		candidateIdentity,
		bus.OutboundMessage{Content: "new candidate"},
	)
	if err != nil || admission.Dispatch || admission.InFlight {
		t.Fatalf("retired admission = %#v, %v", admission, err)
	}
	terminal, err := coordinator.AwaitTerminal(t.Context(), admission)
	if err != nil || terminal.Status != StatusDelivered || !terminal.PayloadRetired {
		t.Fatalf("known terminal = %#v, %v", terminal, err)
	}
}

func TestRetirementProtectsLegacyYoungAndUnsettledPayloads(t *testing.T) {
	for _, tc := range []struct {
		name              string
		plain, transcript bool
		age               time.Duration
		want              string
	}{
		{"legacy_without_provenance", false, false, 24 * time.Hour, "protected_class"},
		{"inside_retention_age", true, false, 7 * 24 * time.Hour, "retention_age"},
		{"pending_transcript", true, true, 24 * time.Hour, "pending_settlement"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			intent := deliveredRetentionFixture(t, root, tc.name, tc.plain, tc.transcript)
			report, err := RetirePayloadsOffline(t.Context(), root,
				RetirementPolicy{DeliveryIDs: []string{intent.ID}, MinAge: tc.age, Apply: true})
			if err != nil || report.Items[0].Outcome != tc.want {
				t.Fatalf("protected = %#v, %v", report, err)
			}
			store, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			current, err := store.Get(intent.ID)
			if err != nil || current.Message == nil || current.PayloadRetired {
				t.Fatalf("protected payload changed: %#v, %v", current, err)
			}
			if tc.transcript {
				if _, err = store.MarkTranscriptProjected(intent.ID); err != nil {
					t.Fatal(err)
				}
				report, err = RetirePayloadsOffline(t.Context(), root,
					RetirementPolicy{DeliveryIDs: []string{intent.ID}, MinAge: time.Nanosecond, Apply: true})
				if err != nil || report.Items[0].Outcome != "retired" {
					t.Fatalf("settled = %#v, %v", report, err)
				}
				// A low-level observer reopens after the offline format cutover.
				store, err = Open(root)
				if err != nil {
					t.Fatal(err)
				}
				current, err = store.Get(intent.ID)
				if err != nil || !current.TranscriptProjected || !current.PayloadRetired {
					t.Fatalf("settlement proof lost: %#v, %v", current, err)
				}
			}
		})
	}
}

func TestRetirementRefusesLiveCoordinatorInvalidBatchAndCancellation(t *testing.T) {
	root := t.TempDir()
	intent := deliveredRetentionFixture(t, root, "owner-exclusion", true, false)
	coordinator, err := OpenCoordinator(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = coordinator.Close() })
	policy := RetirementPolicy{DeliveryIDs: []string{intent.ID}, MinAge: time.Hour, Apply: true}
	if _, err = RetirePayloadsOffline(t.Context(), root, policy); !errors.Is(err, ErrCoordinatorOwned) {
		t.Fatalf("live owner error = %v", err)
	}
	if err = coordinator.Close(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []RetirementPolicy{
		{},
		{DeliveryIDs: []string{intent.ID}},
		{DeliveryIDs: []string{intent.ID, intent.ID}, MinAge: time.Hour},
		{DeliveryIDs: []string{"../invalid"}, MinAge: time.Hour},
		{DeliveryIDs: make([]string, MaxRetirementBatch+1), MinAge: time.Hour},
	} {
		if _, err = RetirePayloadsOffline(t.Context(), root, bad); err == nil {
			t.Fatal("invalid batch accepted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = RetirePayloadsOffline(ctx, root, policy); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled cleanup = %v", err)
	}
	missing := filepath.Join(t.TempDir(), "not-created")
	if _, err = RetirePayloadsOffline(t.Context(), missing, policy); err == nil {
		t.Fatal("missing outbox accepted")
	}
	if _, err = os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid dry run created store")
	}
}

func TestRetirementWriteFailuresNeverRecreateIdentity(t *testing.T) {
	for _, stage := range []string{"before_receipt", "after_receipt"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			original := deliveredRetentionFixture(t, root, stage, true, false)
			store, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			realWrite := store.writeAtomic
			injected := errors.New("synthetic persistence interruption")
			store.writeAtomic = func(path string, data []byte, mode os.FileMode) error {
				if strings.HasPrefix(stage, "before") {
					return injected
				}
				if err := realWrite(path, data, mode); err != nil {
					return err
				}
				return injected
			}
			if _, err = store.retirePlainPayload(
				original.ID,
				time.Now().Add(-time.Hour),
				time.Now(),
			); !errors.Is(
				err,
				injected,
			) {
				t.Fatalf("injected write = %v", err)
			}
			reopened, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			current, err := reopened.Get(original.ID)
			if err != nil || current.ID != original.ID || current.Status != StatusDelivered {
				t.Fatalf("interrupted receipt = %#v, %v", current, err)
			}
			if (stage == "after_receipt") != current.PayloadRetired {
				t.Fatalf("unexpected replacement boundary: %#v", current)
			}
			replay, err := reopened.Create(originalPendingCandidate(t, original.Identity))
			if err != nil || replay.Status != StatusDelivered || replay.PayloadRetired != current.PayloadRetired {
				t.Fatalf("interrupted replay = %#v, %v", replay, err)
			}
			if _, err = reopened.retirePlainPayload(original.ID, time.Now().Add(-time.Hour), time.Now()); err != nil {
				t.Fatalf("resume interrupted cleanup: %v", err)
			}
		})
	}
}

func originalPendingCandidate(t *testing.T, identity Identity) Intent {
	t.Helper()
	intent, err := NewMessageIntent(
		"/different-owner",
		identity,
		bus.OutboundMessage{Content: "different candidate"},
		time.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	intent.RetentionClass = retentionSourcePending
	intent.Version = retentionRecordVersion
	return intent
}

func TestRetiredReaderRejectsMalformedReceiptAndUnknownVersion(t *testing.T) {
	root := t.TempDir()
	original := deliveredRetentionFixture(t, root, "strict-retired-reader", true, false)
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	retired, err := store.retirePlainPayload(original.ID, time.Now().Add(-time.Hour), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []func(*Intent){
		func(i *Intent) { i.Status = StatusPending },
		func(i *Intent) { i.Message = original.Message },
		func(i *Intent) { i.PayloadRetired = false },
		func(i *Intent) { i.PayloadRetiredAt = time.Time{} },
		func(i *Intent) { i.RetentionClass = "unknown" },
		func(i *Intent) { i.RecoverySettled = true },
		func(i *Intent) { i.Version = recordVersion },
		func(i *Intent) { i.Version = retentionRecordVersion + 1 },
	} {
		changed := retired
		mutation(&changed)
		data, err := json.Marshal(changed)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = InspectIntent(data, retired.ID); err == nil {
			t.Fatal("malformed retired receipt accepted")
		}
	}
}

func TestRetirementProtectsEveryNonDeliveredOutcome(t *testing.T) {
	for _, outcome := range []string{"pending", "attempting", "definitely_failed", "exhausted", "ambiguous", "abandoned"} {
		t.Run(outcome, func(t *testing.T) {
			root := t.TempDir()
			native, input := retentionNativeSource(t, root)
			identity := testIdentity()
			identity.SourceID = input.SpoolID
			coordinator, err := OpenCoordinator(root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = coordinator.Close() })
			then := time.Now().Add(-48 * time.Hour).UTC()
			coordinator.now = func() time.Time { return then }
			coordinator.store.now = coordinator.now
			admission, err := coordinator.AdmitNativeFinalMessage(
				"/agent",
				identity,
				bus.OutboundMessage{Content: "protected payload"},
			)
			if err != nil {
				t.Fatal(err)
			}
			proof, ackErr := native.AckInbound(t.Context(), input)
			if ackErr != nil {
				t.Fatal(ackErr)
			}
			sealed, sealErr := coordinator.SealNativeFinalMessage(admission.Intent.ID, proof)
			if sealErr != nil || !sealed {
				t.Fatalf("fixture source seal=%v,%v", sealed, sealErr)
			}
			if outcome == "abandoned" {
				if _, err = coordinator.Abandon(admission.Intent.ID, Outcome{}); err != nil {
					t.Fatal(err)
				}
			} else if outcome != "pending" {
				commitTestAdmission(t, coordinator, admission.Lease)
				if err = coordinator.BeginAttempt(admission.Intent.ID); err != nil {
					t.Fatal(err)
				}
				switch outcome {
				case "definitely_failed", "exhausted":
					if err = coordinator.MarkDefinitelyFailed(admission.Intent.ID, Outcome{}); err != nil {
						t.Fatal(err)
					}
					if outcome == "exhausted" {
						for attempt := 1; attempt < MaxDeliveryAttempts; attempt++ {
							admission, err = coordinator.AdmitNativeFinalMessage(
								"/agent",
								identity,
								bus.OutboundMessage{Content: "same source"},
							)
							if err != nil {
								t.Fatal(err)
							}
							commitTestAdmission(t, coordinator, admission.Lease)
							if err = coordinator.BeginAttempt(admission.Intent.ID); err != nil {
								t.Fatal(err)
							}
							if err = coordinator.MarkDefinitelyFailed(admission.Intent.ID, Outcome{}); err != nil {
								t.Fatal(err)
							}
						}
					}
				case "ambiguous":
					if err = coordinator.MarkAmbiguous(admission.Intent.ID, Outcome{}); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err = coordinator.Close(); err != nil {
				t.Fatal(err)
			}
			report, err := RetirePayloadsOffline(t.Context(), root,
				RetirementPolicy{DeliveryIDs: []string{admission.Intent.ID}, MinAge: time.Hour, Apply: true})
			if err != nil || report.Items[0].Outcome != "protected_outcome" {
				t.Fatalf("%s cleanup = %#v, %v", outcome, report, err)
			}
			store, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			current, err := store.Get(admission.Intent.ID)
			if err != nil || current.Message == nil || current.PayloadRetired {
				t.Fatalf("lost protected outcome: %#v, %v", current, err)
			}
		})
	}
}

func TestPlainRetirementAdmissionRejectsDomainAndIntermediateMetadata(t *testing.T) {
	for _, message := range []bus.OutboundMessage{
		{Content: "thought", Metadata: bus.OutboundMetadata{MessageKind: bus.OutboundMessageKindThought}},
		{Content: "interim", Metadata: bus.OutboundMetadata{OutboundKind: bus.OutboundKindInterim}},
		{Content: "interaction", Metadata: bus.OutboundMetadata{InteractionID: "private-interaction"}},
		{Content: "choices", Metadata: bus.OutboundMetadata{Choices: []string{"yes", "no"}}},
		{Content: "request", Metadata: bus.OutboundMetadata{RequestID: "private-request"}},
	} {
		root := t.TempDir()
		coordinator, err := OpenCoordinator(root)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = coordinator.Close() })
		if admission, err := coordinator.AdmitNativeFinalMessage(
			"/agent",
			testIdentity(),
			message,
		); err == nil ||
			admission.Dispatch {
			t.Fatalf("domain-bearing plain admission = %#v, %v", admission, err)
		}
		if err = coordinator.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRetirementPreservesOversizedAndNonregularRecords(t *testing.T) {
	t.Run("oversized", func(t *testing.T) {
		root := t.TempDir()
		intent := deliveredRetentionFixture(t, root, "oversized", true, false)
		path := filepath.Join(Path(root), intent.ID+".json")
		original := bytes.Repeat([]byte("sensitive-large-record"), MaxInspectionBytes/10)
		if err := os.WriteFile(path, original, 0o600); err != nil {
			t.Fatal(err)
		}
		report, err := RetirePayloadsOffline(t.Context(), root,
			RetirementPolicy{DeliveryIDs: []string{intent.ID}, MinAge: time.Hour, Apply: true})
		if err != nil || report.Items[0].Outcome != "protected_large_record" {
			t.Fatalf("large cleanup = %#v, %v", report, err)
		}
		current, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(original, current) {
			t.Fatal("oversized evidence changed")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		intent := deliveredRetentionFixture(t, root, "linked", true, false)
		path := filepath.Join(Path(root), intent.ID+".json")
		saved := filepath.Join(root, "saved-private-payload")
		if err := os.Rename(path, saved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(saved, path); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		report, err := RetirePayloadsOffline(t.Context(), root,
			RetirementPolicy{DeliveryIDs: []string{intent.ID}, MinAge: time.Hour, Apply: true})
		if err != nil || report.Items[0].Outcome != "protected_nonregular_record" {
			t.Fatalf("linked cleanup = %#v, %v", report, err)
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatal("cleanup replaced a symlink")
		}
	})
}

func TestDuplicateProtectionIsDurableAndCannotBePromoted(t *testing.T) {
	root := t.TempDir()
	original := deliveredRetentionFixture(t, root, "revoke-eligibility", true, false)
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	candidate := originalPendingCandidate(t, original.Identity)
	candidate.RetentionClass = ""
	realWrite := store.writeAtomic
	interrupted := errors.New("revocation did not persist")
	store.writeAtomic = func(string, []byte, os.FileMode) error { return interrupted }
	if _, err = store.Create(candidate); !errors.Is(err, interrupted) {
		t.Fatalf("unconfirmed protection admitted callback: %v", err)
	}
	current, err := store.Get(original.ID)
	if err != nil || current.RetentionClass != retentionPlainMessage {
		t.Fatalf("failed write changed eligibility: %#v, %v", current, err)
	}
	store.writeAtomic = realWrite
	current, err = store.Create(candidate)
	if err != nil || current.RetentionClass != "" {
		t.Fatalf("durable protection = %#v, %v", current, err)
	}
	candidate.RetentionClass = retentionPlainMessage
	current, err = store.Create(candidate)
	if err != nil || current.RetentionClass != "" {
		t.Fatalf("replay promoted protected payload: %#v, %v", current, err)
	}
}

// A classified record is also its own old-reader fence. Admission never
// separates eligibility proof from the schema version that protects it.
func TestClassifiedRecordIsAtomicAdmissionFence(t *testing.T) {
	for _, afterWrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_record", true: "after_record"}[afterWrite], func(t *testing.T) {
			root := t.TempDir()
			store, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			candidate := originalPendingCandidate(t, testIdentity())
			injected := errors.New("classified admission interrupted")
			write := store.writeAtomic
			store.writeAtomic = func(path string, data []byte, mode os.FileMode) error {
				if path != store.recordPath(candidate.ID) {
					t.Fatal("classification created a second authority")
				}
				if afterWrite {
					if err := write(path, data, mode); err != nil {
						return err
					}
				}
				return injected
			}
			if _, err = store.Create(candidate); !errors.Is(err, injected) {
				t.Fatalf("classified admission = %v", err)
			}
			reopened, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			observed, err := reopened.Get(candidate.ID)
			if afterWrite {
				if err != nil || observed.Version != retentionRecordVersion ||
					observed.RetentionClass != retentionSourcePending {
					t.Fatalf("committed classification lost its version fence: %#v, %v", observed, err)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("uncommitted record exists: %v", err)
			}
			confirmed, err := reopened.Create(candidate)
			if err != nil || confirmed.ID != candidate.ID || confirmed.Version != retentionRecordVersion {
				t.Fatalf("resumed classification = %#v, %v", confirmed, err)
			}
		})
	}
}
