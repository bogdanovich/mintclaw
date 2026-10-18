package eval

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestPDFPostStopOutputRequiresTheOriginallyAdmittedNativeReceipts(t *testing.T) {
	for _, mode := range []string{
		"unchanged", "write", "delivery", "write-whitespace", "delivery-whitespace",
		"missing-write", "missing-delivery", "missing-snapshot", "wrong-operation", "wrong-delivery",
	} {
		t.Run(mode, func(t *testing.T) {
			domain, settled := settledPDFControl(t)
			prepared := domain.prepared
			prepared.census = &pdfNativeCensus{before: &pdfNativeInventory{}}
			trial := Trial{
				ID: uuid.NewString(), SessionID: prepared.sessionID, SessionKey: domain.owner.RouteSessionKey,
				Verdict: Pass, ExecutionVerdict: Pass,
				Checks: gradeConversation(prepared.item.Case.Checks, nil, Event{}),
			}
			domain.conclude(t.Context(), &trial)
			if trial.PDF.Settlement == nil || trial.ExecutionVerdict != Pass {
				t.Fatal("native output fixture did not settle", trial.Code)
			}
			pending := pdfRunFinalization{}
			pending.retain(domain, trial)
			defer pending.close()
			entry := pending.trials[trial.ID]
			writePath := filepath.Join(prepared.home, "state", "document-writes", "journal",
				settled.operation.OperationID+".json")
			deliveryPath := filepath.Join(prepared.workspace, "state", "outbox", settled.delivery.ID+".json")
			switch mode {
			case "write":
				settled.operation.Artifact.SHA256 = digest([]byte("changed-output-receipt"))
				writeAdmissionJSON(t, writePath, settled.operation)
			case "delivery":
				settled.delivery.PlatformMessageIDs = []string{"another-delivered-message"}
				writeAdmissionJSON(t, deliveryPath, settled.delivery)
			case "write-whitespace", "delivery-whitespace":
				path := writePath
				if mode == "delivery-whitespace" {
					path = deliveryPath
				}
				data, err := os.ReadFile(path)
				if err != nil || os.WriteFile(path, append(data, '\n'), 0o600) != nil {
					t.Fatal("cannot prepare a changed raw receipt")
				}
			case "missing-write", "missing-delivery":
				path := writePath
				if mode == "missing-delivery" {
					path = deliveryPath
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "missing-snapshot":
				entry.outputSnapshot = pdfOutputSnapshot{}
			case "wrong-operation":
				trial.PDF.Settlement.OperationID = "another-operation"
			case "wrong-delivery":
				trial.PDF.Settlement.OutboxDeliveryID = "another-delivery"
			}
			inventory, err := prepared.nativeInventory(t.Context())
			if err != nil {
				t.Fatal("changed native receipt was not independently readable", err)
			}
			defer inventory.close()
			entry.conclude(t.Context(), &trial, inventory, nil, nil, cleanPDFLifecycle(),
				&PrivateSurfaceObservation{Code: "observed"})
			delete(pending.trials, trial.ID)
			want := mode == "unchanged"
			proof := trial.PDF.PostStop
			if proof.LogicalSettled != want || proof.FixtureRemoved != want || !proof.RuntimeStopped ||
				trial.Verdict == Pass || trial.EvidenceEligible ||
				!want && proof.Code != "logical_settlement_unavailable" {
				t.Fatal("post-stop cleanup trusted a missing/changed output receipt or qualified metadata alone", proof)
			}
			_, sourceErr := os.Stat(filepath.Join(prepared.workspace, ".eval-trials", prepared.sessionID, "source.pdf"))
			if want != errors.Is(sourceErr, os.ErrNotExist) {
				t.Fatal("source retention disagreed with observed output settlement", sourceErr)
			}
		})
	}
}

func TestPDFOutputSnapshotFingerprintsTheValidatedRawBytes(t *testing.T) {
	domain, settled := settledPDFControl(t)
	defer func() { _ = domain.prepared.release(true) }()
	for _, item := range []struct {
		path string
		sha  string
	}{
		{filepath.Join(domain.prepared.home, "state", "document-writes", "journal",
			settled.operation.OperationID+".json"), settled.snapshot.operationSHA256},
		{
			filepath.Join(domain.prepared.workspace, "state", "outbox", settled.delivery.ID+".json"),
			settled.snapshot.deliverySHA256,
		},
	} {
		data, err := os.ReadFile(item.path)
		if err != nil || !validDigest(item.sha) || digest(data) != item.sha {
			t.Fatal("settlement fingerprint did not match the exact admitted native bytes")
		}
	}
}

func TestPDFCohortCannotAdoptAWriteChangedAfterSettlementObservation(t *testing.T) {
	domain, settled := settledPDFControl(t)
	domain.prepared.census = &pdfNativeCensus{before: &pdfNativeInventory{}}
	domain.outputSnapshot = settled.snapshot
	trial := Trial{
		ID: uuid.NewString(), ExecutionVerdict: Pass, SessionKey: domain.owner.RouteSessionKey,
		PDF: &PDFTrialEvidence{Settlement: &PDFSettlementEvidence{OperationID: settled.operation.OperationID}},
	}
	path := filepath.Join(domain.prepared.home, "state", "document-writes", "journal",
		settled.operation.OperationID+".json")
	data, err := os.ReadFile(path)
	if err != nil || os.WriteFile(path, append(data, '\n'), 0o600) != nil {
		t.Fatal("cannot prepare a receipt change before the conversation census")
	}
	domain.concludeNativeCensus(t.Context(), &trial)
	pending := pdfRunFinalization{}
	pending.retain(domain, trial)
	defer pending.close()
	if len(pending.trials[trial.ID].expectedWrites) != 0 {
		t.Fatal("a later census laundered a changed terminal write into the cohort")
	}
}
