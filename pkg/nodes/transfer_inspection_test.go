package nodes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func transferInspectionControl(t *testing.T) (*GatewayTransferSpool, TransferArtifactRecord, []byte) {
	t.Helper()
	now := time.Unix(1_800_000_000, 0)
	store := openTestTransferSpool(t, now, 0, 0)
	content := []byte("session,reference\nsynthetic,fixture-code\n")
	spec := testTransferSpec(content, now)
	spec.SourceKind, spec.SourceID, spec.SourceScope, spec.SourceRevision = "browser_download", "prepared_1", "tab_1", 1
	writer, _, created, err := store.Begin(testTransferOwner("actor-a"), spec)
	if err != nil || !created || writer.WriteChunk(1, content) != nil {
		t.Fatal("native transfer control unavailable", err)
	}
	record, err := writer.Commit()
	if err != nil {
		t.Fatal(err)
	}
	record, claimed, err := store.ClaimDelivery(record.Owner, record.Ref, "media://fixture", "delivery_fixture")
	if err != nil || !claimed {
		t.Fatal("native delivery control unavailable", err)
	}
	raw, err := os.ReadFile(filepath.Join(store.root, transferSpoolIndexName))
	if err != nil {
		t.Fatal(err)
	}
	return store, record, raw
}

func TestInspectTransferSpoolSnapshotUsesNativeRecordsWithoutReconciliation(t *testing.T) {
	store, record, raw := transferInspectionControl(t)
	snapshot, err := InspectTransferSpoolSnapshot(raw)
	if err != nil || len(snapshot.Records) != 1 || snapshot.Records[record.ArtifactID] != record {
		t.Fatal("native committed record was not observed", err)
	}
	// Inspection cannot acquire the live store's lease or reconcile corrupt bytes.
	path := filepath.Join(store.root, record.DataName)
	if os.WriteFile(path, []byte("changed bytes"), 0o600) != nil {
		t.Fatal("corruption control unavailable")
	}
	if _, err = InspectTransferSpoolSnapshot(raw); err != nil {
		t.Fatal("metadata inspection claimed to validate file bytes", err)
	}
	after, err := os.ReadFile(filepath.Join(store.root, transferSpoolIndexName))
	data, dataErr := os.ReadFile(path)
	if err != nil || dataErr != nil || !bytes.Equal(after, raw) || string(data) != "changed bytes" {
		t.Fatal("inspection reconciled native evidence")
	}
	snapshot.Records[record.ArtifactID] = TransferArtifactRecord{}
	again, err := InspectTransferSpoolSnapshot(raw)
	if err != nil || again.Records[record.ArtifactID] != record {
		t.Fatal("decoded snapshots alias one another or the input")
	}
}

func TestInspectTransferSpoolSnapshotRetainsStagingAndExpiredMetadata(t *testing.T) {
	store, _, _ := transferInspectionControl(t)
	now := time.Unix(1_800_000_000, 0)
	spec := testTransferSpecWithID([]byte("staging"), now, "staging-transfer")
	writer, staged, created, err := store.Begin(testTransferOwner("actor-b"), spec)
	if err != nil || !created || writer == nil {
		t.Fatal("native staging control unavailable", err)
	}
	t.Cleanup(func() { _ = writer.Abort() })
	raw, err := os.ReadFile(filepath.Join(store.root, transferSpoolIndexName))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := InspectTransferSpoolSnapshot(raw)
	if err != nil || len(snapshot.Records) != 2 ||
		snapshot.Records[staged.ArtifactID].State != TransferArtifactStaging {
		t.Fatal("staging metadata was reconciled or hidden", err)
	}
	for id, record := range snapshot.Records {
		record.Spec.ExpiresAt = 1
		snapshot.Records[id] = record
	}
	encoded, err := json.Marshal(transferSpoolDocument{Version: transferSpoolVersion, Records: snapshot.Records})
	if err != nil {
		t.Fatal(err)
	}
	if expired, inspectErr := InspectTransferSpoolSnapshot(encoded); inspectErr != nil || len(expired.Records) != 2 {
		t.Fatal("inspection silently removed expired metadata", inspectErr)
	}
}

func TestInspectTransferSpoolSnapshotRefusesLossyMalformedOrOversizedInput(t *testing.T) {
	_, record, raw := transferInspectionControl(t)
	for _, mode := range []string{
		"duplicate", "unknown", "trailing", "version", "null", "map-key", "owner", "ref", "source", "claim", "size",
	} {
		t.Run(mode, func(t *testing.T) {
			data := append([]byte(nil), raw...)
			changed := record
			key := record.ArtifactID
			switch mode {
			case "duplicate":
				data = bytes.Replace(data, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1)
			case "unknown":
				data = append([]byte(`{"PRIVATE_INSPECTION_CANARY":true,`), data[1:]...)
			case "trailing":
				data = append(data, []byte(` {}`)...)
			case "version":
				data = []byte(`{"version":2,"records":{}}`)
			case "null":
				data = []byte(`{"version":1,"records":null}`)
			case "map-key":
				key = "foreign_artifact"
			case "owner":
				changed.Owner.ActorID = ""
			case "ref":
				changed.Ref = "transfer-artifact://foreign"
			case "source":
				changed.Spec.SourceRevision = 0
			case "claim":
				changed.MediaRef = ""
			case "size":
				changed.ObservedSize++
			}
			if mode == "map-key" || changed != record {
				var err error
				data, err = json.Marshal(transferSpoolDocument{
					Version: transferSpoolVersion, Records: map[string]TransferArtifactRecord{key: changed},
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			got, err := InspectTransferSpoolSnapshot(data)
			if !errors.Is(err, ErrTransferArtifactConflict) || len(got.Records) != 0 ||
				strings.Contains(err.Error(), "PRIVATE_INSPECTION_CANARY") {
				t.Fatal("invalid or private source reached metadata projection", mode, err)
			}
		})
	}
	if _, err := InspectTransferSpoolSnapshot(
		make([]byte, maxGatewayTransferSpoolIndexBytes+1),
	); !errors.Is(
		err,
		ErrTransferSpoolFull,
	) {
		t.Fatal("byte limit not enforced", err)
	}
	records := make(map[string]TransferArtifactRecord)
	for index := range DefaultGatewayTransferLimit + 1 {
		copy := record
		copy.ArtifactID = fmt.Sprintf("artifact_%d", index)
		copy.Ref, copy.DataName = TransferArtifactRefPrefix+copy.ArtifactID, "artifact_"+copy.ArtifactID+".data"
		records[copy.ArtifactID] = copy
	}
	data, err := json.Marshal(transferSpoolDocument{Version: transferSpoolVersion, Records: records})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = InspectTransferSpoolSnapshot(data); !errors.Is(err, ErrTransferSpoolFull) {
		t.Fatal("record limit not enforced", err)
	}
}
