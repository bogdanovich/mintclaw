package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

func initializeCensusRoots(t *testing.T, prepared *pdfTrial) {
	t.Helper()
	if _, err := document.NewWriteJournal(
		filepath.Join(prepared.home, "state", "document-writes", "journal"),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := outbox.Open(prepared.workspace); err != nil {
		t.Fatal(err)
	}
}

func censusNormalizedRequest(t *testing.T) document.NormalizedFillRequest {
	t.Helper()
	_, _, schema, _ := reviewedPDFControl(t)
	value := "extra"
	request, err := document.NormalizeFillMap(document.DocumentRef{SHA256: schema.SourceSHA256},
		schema, document.FillMap{
			SchemaVersion: document.FillMapSchemaVersion,
			Assignments: []document.FormFillAssignment{{
				FieldID: schema.Fields[0].ID,
				Value:   document.FormValue{Type: document.FormValueText, Text: &value},
			}},
		})
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestPDFNativeCensusMissingRootsNeverMeanEmptyOrCreateState(t *testing.T) {
	prepared := newPDFTrialFixture(t)
	defer func() { _ = prepared.release(true) }()
	if err := prepared.beginNativeCensus(t.Context()); err == nil || prepared.census.before != nil {
		t.Fatal("missing native roots became an empty observed inventory")
	}
	for _, path := range []string{
		filepath.Join(prepared.home, "state", "document-writes"), filepath.Join(prepared.workspace, "state", "outbox"),
	} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("census initialized a native store")
		}
	}
	trial := Trial{PDF: &PDFTrialEvidence{}, Verdict: Inconclusive}
	(&pdfConversation{prepared: prepared}).concludeNativeCensus(t.Context(), &trial)
	if trial.Verdict != Inconclusive || trial.PDF.Census == nil || trial.PDF.Census.Counts != nil ||
		trial.PDF.Census.BaselineSHA256 != "" {
		t.Fatal("incomplete census invented zero counts or a passing result")
	}
}

func TestPDFNativeInventoryRefusesUnsafeOrIncompleteSurfaces(t *testing.T) {
	for _, mode := range []string{
		"unknown-file", "directory", "symlink", "ancestor-alias", "public-file", "public-directory", "oversize",
		"trailing", "duplicate-key", "oversize-inventory", "canceled", "lock-content", "credential", "private-field",
	} {
		t.Run(mode, func(t *testing.T) {
			prepared := newPDFTrialFixture(t)
			defer func() { _ = prepared.release(true) }()
			initializeCensusRoots(t, prepared)
			directory := filepath.Join(prepared.home, "state", "document-writes", "journal")
			name := "document_write_" + strings.ReplaceAll(uuid.NewString(), "-", "") + ".json"
			path := filepath.Join(directory, name)
			data := []byte(`{"placeholder":true}`)
			ctx := t.Context()
			wanted := errNativeEvidence
			switch mode {
			case "unknown-file":
				path = filepath.Join(directory, "unexpected.txt")
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				data = nil
			case "symlink":
				if err := os.Symlink(filepath.Join(prepared.directory, "source.pdf"), path); err != nil {
					t.Fatal(err)
				}
				data = nil
			case "ancestor-alias":
				original := filepath.Join(prepared.home, "state", "document-writes")
				moved := original + "-moved"
				if err := os.Rename(original, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, original); err != nil {
					t.Fatal(err)
				}
				data = nil
			case "public-directory":
				if err := os.Chmod(directory, 0o755); err != nil {
					t.Fatal(err)
				}
				data = nil
			case "oversize":
				data = bytes.Repeat([]byte("x"), document.DefaultMaxWriteJournalRecord+1)
			case "trailing":
				data = []byte(`{} {}`)
			case "duplicate-key":
				data = []byte(`{"id":1,"id":2}`)
			case "oversize-inventory":
				for index := range maxPDFCensusEntries + 1 {
					entry := filepath.Join(directory, fmt.Sprintf("entry-%05d", index))
					if err := os.WriteFile(entry, []byte("{}"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				data = nil
			case "canceled":
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx, data = canceled, nil
			case "lock-content":
				path = filepath.Join(directory, ".write-journal.lock")
			case "credential":
				prepared.filter = strings.NewReplacer("synthetic-credential", "<redacted>")
				data = []byte(`{"ignored":"synthetic-credential"}`)
				wanted = ErrPrivateEvidence
			case "private-field":
				data, _ = json.Marshal(
					map[string]string{"ignored": prepared.item.Case.Driver.Facts["contact-reference"]},
				)
				wanted = ErrPrivateEvidence
			}
			if data != nil {
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				if mode == "public-file" {
					if err := os.Chmod(path, 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			inventory, err := prepared.nativeInventory(ctx)
			defer inventory.close()
			if inventory != nil || !errors.Is(err, wanted) || strings.Contains(err.Error(), "synthetic-credential") {
				t.Fatal("unsafe census surface was ignored, partially admitted, or echoed", err)
			}
		})
	}
}

func TestPDFNativeCensusObservesRealMetadataWithoutGrantingQualification(t *testing.T) {
	domain, settled := settledPDFControl(t)
	defer func() { _ = domain.prepared.release(true) }()
	owner, err := pdfWriteOwner(domain.prepared.workspace, settled.approval)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := domain.prepared.nativeInventory(t.Context())
	if err != nil {
		t.Fatal("native fixture inventory unavailable", err)
	}
	defer inventory.close()
	projection, err := domain.prepared.projectInventory(t.Context(), inventory, owner, domain.owner.RouteSessionKey)
	if err != nil || len(projection.writes) != 1 || len(projection.media) != 1 || projection.wrongSource {
		t.Fatal("native operation and logical media intent were not counted", err)
	}
	// An empty synthetic baseline exercises reduction, not a live execution.
	domain.prepared.census = &pdfNativeCensus{before: &pdfNativeInventory{}}
	trial := Trial{PDF: &PDFTrialEvidence{}, SessionKey: domain.owner.RouteSessionKey, Verdict: Inconclusive}
	domain.concludeNativeCensus(t.Context(), &trial)
	if trial.Verdict != Inconclusive || trial.PDF.Census == nil || trial.PDF.Census.Counts == nil ||
		trial.PDF.Census.Counts.WriteOperations != 1 || trial.PDF.Census.Counts.MediaIntents != 1 ||
		trial.PDF.Census.SourceUnchanged == nil || !*trial.PDF.Census.SourceUnchanged || trial.EvidenceEligible {
		t.Fatal("bounded metadata became delivery, idle, privacy or artifact qualification")
	}
	domain.prepared.census.before.close()
	domain.prepared.census.before = inventory
	raw := domain.prepared.census.before.files[0].data
	if domain.prepared.release(false) != nil || !bytes.Equal(raw, make([]byte, len(raw))) {
		t.Fatal("census retained private raw baseline bytes after release")
	}
}

func TestPDFNativeCensusRejectsMalformedForeignWriteAndObservedExtraEffects(t *testing.T) {
	for _, mode := range []string{
		"malformed-foreign", "extra-write", "forbidden-write", "source-changed", "preexisting", "extra-delivery",
		"missing-source", "extra-write-missing-source", "forbidden-write-missing-source",
		"preexisting-missing-source", "extra-delivery-missing-source",
	} {
		t.Run(mode, func(t *testing.T) {
			domain, settled := settledPDFControl(t)
			defer func() { _ = domain.prepared.release(true) }()
			owner, err := pdfWriteOwner(domain.prepared.workspace, settled.approval)
			if err != nil {
				t.Fatal(err)
			}
			domain.prepared.census = &pdfNativeCensus{before: &pdfNativeInventory{}}
			path := filepath.Join(domain.prepared.home, "state", "document-writes", "journal",
				settled.operation.OperationID+".json")
			wanted := Fail
			switch mode {
			case "malformed-foreign":
				record := settled.operation
				record.OwnerSHA256 = strings.Repeat("a", 64)
				record.SourceSHA256 = "invalid"
				data, _ := json.Marshal(record)
				if err = os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				wanted = Inconclusive
			case "extra-write", "extra-write-missing-source":
				record := settled.operation
				record.OperationID = "document_write_" + strings.ReplaceAll(uuid.NewString(), "-", "")
				// Accept an independent native operation, not a hand-written transition.
				journal, openErr := document.NewWriteJournal(filepath.Dir(path))
				if openErr != nil {
					t.Fatal(openErr)
				}
				if _, _, err = journal.Accept(
					t.Context(),
					record.OperationID,
					owner,
					censusNormalizedRequest(t),
				); err != nil {
					t.Fatal(err)
				}
			case "forbidden-write", "forbidden-write-missing-source":
				pdf := *domain.prepared.item.Case.PDF
				pdf.Output = "forbidden"
				domain.prepared.item.Case.PDF = &pdf
			case "extra-delivery", "extra-delivery-missing-source":
				intent, makeErr := outbox.NewMediaIntent(domain.prepared.workspace, outbox.Identity{
					SourceID:   "extra-native-receipt",
					Channel:    "mintclaw",
					ChatID:     "mintclaw:" + domain.prepared.sessionID,
					SessionKey: domain.owner.RouteSessionKey,
				}, *settled.delivery.Media, time.Now())
				if makeErr != nil {
					t.Fatal(makeErr)
				}
				box, openErr := outbox.Open(domain.prepared.workspace)
				if openErr != nil {
					t.Fatal(openErr)
				}
				if _, err = box.Create(intent); err != nil {
					t.Fatal(err)
				}
				if _, err = box.BeginAttempt(intent.ID); err != nil {
					t.Fatal(err)
				}
				if _, err = box.MarkDelivered(
					intent.ID,
					outbox.Outcome{PlatformMessageIDs: []string{"second-native-message"}},
				); err != nil {
					t.Fatal(err)
				}
			case "source-changed":
				file, openErr := domain.prepared.scratch.OpenFile("source.pdf", os.O_WRONLY, 0)
				if openErr != nil {
					t.Fatal(openErr)
				}
				_, err = file.WriteAt([]byte("changed"), 0)
				_ = file.Close()
				if err != nil {
					t.Fatal(err)
				}
			case "preexisting", "preexisting-missing-source":
				domain.prepared.census = nil
				if err = domain.prepared.beginNativeCensus(t.Context()); err != nil {
					t.Fatal(err)
				}
			case "missing-source":
				wanted = Inconclusive
			}
			if strings.HasSuffix(mode, "missing-source") {
				if err = domain.prepared.scratch.Remove("source.pdf"); err != nil {
					t.Fatal(err)
				}
			}
			trial := Trial{PDF: &PDFTrialEvidence{}, SessionKey: domain.owner.RouteSessionKey, Verdict: Inconclusive}
			domain.concludeNativeCensus(t.Context(), &trial)
			if trial.Verdict != wanted || trial.EvidenceEligible || trial.PDF.Census == nil ||
				mode == "malformed-foreign" && trial.PDF.Census.Counts != nil {
				t.Fatal("invalid foreign evidence or extra native effects were qualified", trial.PDF.Census)
			}
			missingSourceCodes := map[string]string{
				"missing-source":                 "census_source_unavailable",
				"extra-write-missing-source":     "multiple_document_writes",
				"forbidden-write-missing-source": "unexpected_document_write",
				"preexisting-missing-source":     "preexisting_trial_effects",
				"extra-delivery-missing-source":  "multiple_document_deliveries",
			}
			if code, missing := missingSourceCodes[mode]; missing &&
				(trial.PDF.Census.Code != code || trial.PDF.Census.SourceUnchanged != nil ||
					trial.PDF.Census.Counts == nil) {
				t.Fatal("unknown source hid native violations or became invented source evidence", trial.PDF.Census)
			}
		})
	}
}

func TestPDFNativeCensusKeepsForeignHistoryAndDetectsChanges(t *testing.T) {
	domain, settled := settledPDFControl(t)
	owner, err := pdfWriteOwner(domain.prepared.workspace, settled.approval)
	if err != nil {
		t.Fatal(err)
	}
	owner.SessionID = "foreign-census-scope"
	directory := filepath.Join(domain.prepared.home, "state", "document-writes", "journal")
	if err = os.Remove(filepath.Join(directory, settled.operation.OperationID+".json")); err != nil {
		t.Fatal(err)
	}
	journal, err := document.NewWriteJournal(directory)
	if err != nil {
		t.Fatal(err)
	}
	id := "document_write_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, _, err = journal.Accept(t.Context(), id, owner, censusNormalizedRequest(t)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, id+".json")
	if err = domain.prepared.beginNativeCensus(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := domain.prepared.census.before
	if err = domain.prepared.beginNativeCensus(t.Context()); err == nil || domain.prepared.census.before != before {
		t.Fatal("native baseline was recaptured after task effects")
	}
	// Only the foreign record remains in this synthetic baseline; native owned
	// prompts/media were fixture setup, not part of this isolated comparison.
	retained := before.files[:0]
	for _, file := range before.files {
		if file.kind == "write" {
			retained = append(retained, file)
		} else {
			clear(file.data)
		}
	}
	before.files = retained
	trial := Trial{PDF: &PDFTrialEvidence{}, SessionKey: domain.owner.RouteSessionKey, Verdict: Inconclusive}
	domain.concludeNativeCensus(t.Context(), &trial)
	if trial.PDF.Census.Counts == nil || trial.PDF.Census.Counts.WriteOperations != 0 ||
		trial.PDF.Census.Counts.ForeignChanges != 0 || trial.Verdict != Inconclusive {
		t.Fatal("stable validated foreign history was treated as owned or passing")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	domain.concludeNativeCensus(t.Context(), &trial)
	if trial.PDF.Census.Counts == nil || trial.PDF.Census.Counts.ForeignChanges != 1 ||
		trial.PDF.Census.Code != "census_scope_changed" || trial.Verdict != Inconclusive {
		t.Fatal("foreign disappearance was hidden by ownership filtering")
	}
}

func TestCensusJSONNullAdmissionDoesNotRelaxManifestContracts(t *testing.T) {
	decoder := json.NewDecoder(strings.NewReader(`{"native_optional":null}`))
	if _, err := decodeJSONValue(decoder, 0, true); err != nil {
		t.Fatal("native optional null could not be validated")
	}
	if _, err := decodeValue(json.NewDecoder(strings.NewReader(`{"manifest_optional":null}`)), 0); err == nil {
		t.Fatal("manifest null refusal was weakened by census parsing")
	}
	if _, err := decodeJSONValue(json.NewDecoder(strings.NewReader(`{"id":null,"id":1}`)), 0, true); err == nil {
		t.Fatal("nullable census parser accepted duplicate keys")
	}
}
