package browser

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestInspectStateSnapshotUsesNativeStoreWithoutLockOrMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "browser.json")
	store, err := NewFileStore(path, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	broker, _, session := openActionTestBrokerWithConfig(t, admittedBrowserConfig(), store)
	observation, err := broker.Observe(t.Context(), testOwner(), session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	preparation, err := broker.PrepareAction(t.Context(), PrepareActionRequest{
		Owner: testOwner(), RequestID: "inspect_control", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionFill, Ref: onlyVisibleRef(t, observation.Snapshot), Value: "synthetic"},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	before := bytes.Clone(raw)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := InspectStateSnapshot(raw)
	if err != nil || len(snapshot.Sessions) != 1 || len(snapshot.PreparedActions) != 1 ||
		len(snapshot.Invocations) != 1 || !bytes.Equal(raw, before) {
		t.Fatal("native state observation failed or changed caller bytes", err)
	}
	stored, err := store.GetSession(t.Context(), session.ID)
	invocations, invocationErr := store.ListInvocations(t.Context(), session.ID)
	if err != nil || !reflect.DeepEqual(snapshot.Sessions[session.ID], stored) ||
		!reflect.DeepEqual(snapshot.PreparedActions[preparation.Action.ID], preparation.Action) ||
		invocationErr != nil || len(invocations) != 1 ||
		!reflect.DeepEqual(snapshot.Invocations[invocations[0].ID], invocations[0]) {
		t.Fatal("inspection invented a different native record", err)
	}
	after, err := os.ReadFile(path)
	afterInfo, infoErr := os.Stat(path)
	if err != nil || infoErr != nil || !bytes.Equal(after, before) || !info.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("observation changed the persisted store")
	}
	if second, openErr := NewFileStore(path, 0, 0); !errors.Is(openErr, ErrStoreOwned) {
		if second != nil {
			second.Close()
		}
		t.Fatal("observation released or replaced the active store lock", openErr)
	}
	delete(snapshot.Sessions, session.ID)
	if again, inspectErr := InspectStateSnapshot(before); inspectErr != nil || len(again.Sessions) != 1 {
		t.Fatal("returned snapshot shared mutable state with another observation", inspectErr)
	}
	// A later filesystem replacement cannot substitute bytes after a caller has
	// already scanned and captured its snapshot.
	if os.WriteFile(path, []byte("replacement"), 0o600) != nil {
		t.Fatal("cannot replace control state")
	}
	if again, inspectErr := InspectStateSnapshot(before); inspectErr != nil || len(again.Sessions) != 1 {
		t.Fatal("inspection reopened the path instead of captured bytes", inspectErr)
	}
}

func TestInspectStateSnapshotRefusesInvalidStateWithoutEchoOrPartialRecords(t *testing.T) {
	session := testOpeningSession(testOwner())
	control := fileStoreDocument{
		Version: fileStoreVersion, Sessions: map[string]Session{session.ID: session},
		PreparedActions: map[string]PreparedAction{}, Invocations: map[string]Invocation{},
	}
	raw, err := json.Marshal(control)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{
		"missing", "malformed", "unknown", "duplicate", "trailing", "version", "nil_collection",
		"state", "identity", "record_bound", "byte_bound",
	} {
		t.Run(mode, func(t *testing.T) {
			input := bytes.Clone(raw)
			var changed fileStoreDocument
			if json.Unmarshal(raw, &changed) != nil {
				t.Fatal("invalid control")
			}
			switch mode {
			case "missing":
				input = nil
			case "malformed":
				input = []byte(`{"private-canary":`)
			case "unknown":
				input = append([]byte(`{"private-canary":true,`), raw[1:]...)
			case "duplicate":
				input = append([]byte(`{"version":2,`), raw[1:]...)
			case "trailing":
				input = append(input, []byte(` {"private-canary":true}`)...)
			case "version":
				changed.Version++
			case "nil_collection":
				changed.PreparedActions = nil
			case "state":
				next := session
				next.State = SessionState("private-canary")
				changed.Sessions[session.ID] = next
			case "identity":
				delete(changed.Sessions, session.ID)
				changed.Sessions["private-canary"] = session
			case "record_bound":
				for index := 0; index <= DefaultFileStoreRecords; index++ {
					next := session
					next.ID = "session_" + strconv.Itoa(index)
					changed.Sessions[next.ID] = next
				}
			case "byte_bound":
				input = append(input, bytes.Repeat([]byte(" "), DefaultFileStoreBytes)...)
			}
			if mode == "version" || mode == "nil_collection" || mode == "state" ||
				mode == "identity" || mode == "record_bound" {
				input, err = json.Marshal(changed)
				if err != nil {
					t.Fatal(err)
				}
			}
			snapshot, inspectErr := InspectStateSnapshot(input)
			if inspectErr == nil || strings.Contains(inspectErr.Error(), "private-canary") ||
				snapshot.Sessions != nil || snapshot.PreparedActions != nil || snapshot.Invocations != nil {
				t.Fatal("invalid state was echoed or admitted partially", inspectErr)
			}
		})
	}
}
