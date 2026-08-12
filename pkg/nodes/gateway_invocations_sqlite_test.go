package nodes

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestGatewayInvocationSQLiteMigratesLegacySnapshotAndRejectsDowngrade(t *testing.T) {
	workspace := t.TempDir()
	legacyPath := GatewayInvocationLegacyStorePath(workspace)
	legacy, err := NewGatewayInvocationStore(legacyPath, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	plan := gatewayTestPlan(t, "inv_sqlite_migrate", "idem_sqlite_migrate", time.Now())
	if _, _, err = legacy.Prepare("vpn", "call-sqlite-migrate", plan, gatewayTestDescriptor()); err != nil {
		t.Fatal(err)
	}
	if err = legacy.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := NewGatewayInvocationSQLiteStore(GatewayInvocationStorePath(workspace), 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	defer closeGatewayInvocationSQLiteTestStore(t, store)()
	record, found, err := store.Lookup(gatewayTestPrincipal(plan), plan.InvocationID)
	if err != nil || !found || record.Plan.PlanHash != plan.PlanHash {
		t.Fatalf("migrated record = (%#v, %v, %v)", record, found, err)
	}
	data, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	var marker gatewayInvocationMigrationMarker
	if err = json.Unmarshal(data, &marker); err != nil {
		t.Fatal(err)
	}
	if marker.Version != gatewayInvocationMigrationVersion || marker.Backend != "sqlite" ||
		marker.Database != filepath.Base(GatewayInvocationStorePath(workspace)) {
		t.Fatalf("migration marker = %#v", marker)
	}
	if _, err = NewGatewayInvocationStore(legacyPath, 8, 1024*1024); err == nil {
		t.Fatal("legacy store accepted SQLite downgrade marker")
	}
}

func TestGatewayInvocationSQLiteMigratesProductionShapedSnapshot(t *testing.T) {
	workspace := t.TempDir()
	legacyPath := GatewayInvocationLegacyStorePath(workspace)
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	legacy := newGatewayInvocationStore("", 512, DefaultGatewayInvocationStoreBytes, func() time.Time { return clock })
	for index := 0; index < 256; index++ {
		plan := gatewayTestPlan(
			t,
			fmt.Sprintf("inv_sqlite_production_%03d", index),
			fmt.Sprintf("idem_sqlite_production_%03d", index),
			clock,
		)
		if _, created, err := legacy.Prepare(
			"vpn",
			fmt.Sprintf("call-sqlite-production-%03d", index),
			plan,
			gatewayTestDescriptor(),
		); err != nil || !created {
			t.Fatalf("prepare production-shaped record %d = (%v, %v)", index, created, err)
		}
	}
	data, err := json.Marshal(gatewayInvocationDocument{
		Version: gatewayInvocationStoreVersion,
		Records: legacy.records,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(legacyPath, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewGatewayInvocationSQLiteStore(GatewayInvocationStorePath(workspace), 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	defer closeGatewayInvocationSQLiteTestStore(t, store)()
	var count int
	if err = store.sqlite.db.QueryRow("SELECT count(*) FROM gateway_invocations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 256 {
		t.Fatalf("migrated production-shaped records = %d, want 256", count)
	}
}

func TestGatewayInvocationSQLiteLifecycleSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "node_invocations.db")
	store, err := NewGatewayInvocationSQLiteStore(path, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	plan := gatewayTestPlan(t, "inv_sqlite_restart", "idem_sqlite_restart", time.Now())
	record, created, err := store.Prepare("vpn", "call-sqlite-restart", plan, gatewayTestDescriptor())
	if err != nil || !created {
		t.Fatalf("prepare = (%#v, %v, %v)", record, created, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = NewGatewayInvocationSQLiteStore(path, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	defer closeGatewayInvocationSQLiteTestStore(t, store)()
	record, transitioned, err := store.MarkDispatched(
		gatewayTestOwner("vpn", "call-sqlite-restart", plan),
		plan.InvocationID,
		plan.PlanHash,
	)
	if err != nil || !transitioned || record.State != GatewayInvocationDispatched {
		t.Fatalf("dispatch = (%#v, %v, %v)", record, transitioned, err)
	}
	record, requested, err := store.RequestCancellation(gatewayTestPrincipal(plan), plan.InvocationID)
	if err != nil || !requested || record.Cancellation == nil {
		t.Fatalf("cancel = (%#v, %v, %v)", record, requested, err)
	}
}

func TestGatewayInvocationSQLitePreservesActorAndWorkspaceIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "node_invocations.db")
	store, err := NewGatewayInvocationSQLiteStore(path, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	defer closeGatewayInvocationSQLiteTestStore(t, store)()
	plan := gatewayTestPlan(t, "inv_sqlite_scope", "idem_sqlite_scope", time.Now())
	principal := gatewayTestPrincipal(plan)
	principal.WorkspaceID = "workspace-a"
	principal.ExecutionID = "execution-a"
	if _, created, prepareErr := store.PrepareOwned(
		principal,
		"vpn",
		"call-sqlite-scope",
		plan,
		gatewayTestDescriptor(),
	); prepareErr != nil || !created {
		t.Fatalf("scoped prepare = (%v, %v)", created, prepareErr)
	}

	wrongActor := principal
	wrongActor.ActorID = "different-actor"
	if _, found, lookupErr := store.Lookup(wrongActor, plan.InvocationID); lookupErr != nil || found {
		t.Fatalf("wrong-actor lookup = (%v, %v)", found, lookupErr)
	}
	wrongWorkspace := principal
	wrongWorkspace.WorkspaceID = "workspace-b"
	wrongWorkspace.ExecutionID = "execution-b"
	if _, found, lookupErr := store.ByToolCall(wrongWorkspace, "call-sqlite-scope"); lookupErr != nil || found {
		t.Fatalf("wrong-workspace lookup = (%v, %v)", found, lookupErr)
	}
	owner := gatewayTestOwner("vpn", "call-sqlite-scope", plan)
	owner.WorkspaceID = principal.WorkspaceID
	owner.ExecutionID = principal.ExecutionID
	if _, transitioned, dispatchErr := store.MarkDispatched(
		owner,
		plan.InvocationID,
		plan.PlanHash,
	); dispatchErr != nil ||
		!transitioned {
		t.Fatalf("scoped dispatch = (%v, %v)", transitioned, dispatchErr)
	}
	if _, _, cancelErr := store.RequestCancellation(wrongActor, plan.InvocationID); !errors.Is(
		cancelErr,
		ErrGatewayInvocationConflict,
	) {
		t.Fatalf("wrong-actor cancellation error = %v", cancelErr)
	}
}

func TestGatewayInvocationSQLiteConcurrentPrepareHasOneWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "node_invocations.db")
	first, err := NewGatewayInvocationSQLiteStore(path, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	defer closeGatewayInvocationSQLiteTestStore(t, first)()
	second, err := NewGatewayInvocationSQLiteStore(path, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	defer closeGatewayInvocationSQLiteTestStore(t, second)()
	plan := gatewayTestPlan(t, "inv_sqlite_race", "idem_sqlite_race", time.Now())
	stores := []*GatewayInvocationStore{first, second}
	created := make(chan bool, len(stores))
	errorsChannel := make(chan error, len(stores))
	var start sync.WaitGroup
	start.Add(1)
	var workers sync.WaitGroup
	for _, store := range stores {
		workers.Add(1)
		go func(store *GatewayInvocationStore) {
			defer workers.Done()
			start.Wait()
			_, won, prepareErr := store.Prepare("vpn", "call-sqlite-race", plan, gatewayTestDescriptor())
			created <- won
			errorsChannel <- prepareErr
		}(store)
	}
	start.Done()
	workers.Wait()
	close(created)
	close(errorsChannel)
	winners := 0
	for won := range created {
		if won {
			winners++
		}
	}
	for prepareErr := range errorsChannel {
		if prepareErr != nil {
			t.Fatal(prepareErr)
		}
	}
	if winners != 1 {
		t.Fatalf("prepare winners = %d, want 1", winners)
	}
}

func TestGatewayInvocationSQLiteConcurrentDispatchHasOneWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "node_invocations.db")
	first, err := NewGatewayInvocationSQLiteStore(path, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	defer closeGatewayInvocationSQLiteTestStore(t, first)()
	second, err := NewGatewayInvocationSQLiteStore(path, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	defer closeGatewayInvocationSQLiteTestStore(t, second)()
	plan := gatewayTestPlan(t, "inv_sqlite_dispatch_race", "idem_sqlite_dispatch_race", time.Now())
	if _, _, err = first.Prepare("vpn", "call-sqlite-dispatch-race", plan, gatewayTestDescriptor()); err != nil {
		t.Fatal(err)
	}
	owner := gatewayTestOwner("vpn", "call-sqlite-dispatch-race", plan)
	stores := []*GatewayInvocationStore{first, second}
	transitioned := make(chan bool, len(stores))
	errorsChannel := make(chan error, len(stores))
	var start sync.WaitGroup
	start.Add(1)
	var workers sync.WaitGroup
	for _, store := range stores {
		workers.Add(1)
		go func(store *GatewayInvocationStore) {
			defer workers.Done()
			start.Wait()
			_, won, dispatchErr := store.MarkDispatched(owner, plan.InvocationID, plan.PlanHash)
			transitioned <- won
			errorsChannel <- dispatchErr
		}(store)
	}
	start.Done()
	workers.Wait()
	close(transitioned)
	close(errorsChannel)
	winners := 0
	for won := range transitioned {
		if won {
			winners++
		}
	}
	for dispatchErr := range errorsChannel {
		if dispatchErr != nil {
			t.Fatal(dispatchErr)
		}
	}
	if winners != 1 {
		t.Fatalf("dispatch winners = %d, want 1", winners)
	}
}

func TestGatewayInvocationSQLitePrunesExpiredAuthority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "node_invocations.db")
	clock := time.Now().UTC().Truncate(time.Second)
	backend, err := newGatewayInvocationSQLiteStore(path, 16*1024*1024, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	store := &GatewayInvocationStore{sqlite: backend}
	defer closeGatewayInvocationSQLiteTestStore(t, store)()

	prepared := gatewayTestPlan(t, "inv_sqlite_expired", "idem_sqlite_expired", clock)
	if _, _, err = store.Prepare("vpn", "call-sqlite-expired", prepared, gatewayTestDescriptor()); err != nil {
		t.Fatal(err)
	}
	dispatched := gatewayTestPlan(t, "inv_sqlite_retained", "idem_sqlite_retained", clock)
	if _, _, err = store.Prepare("vpn", "call-sqlite-retained", dispatched, gatewayTestDescriptor()); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.MarkDispatched(
		gatewayTestOwner("vpn", "call-sqlite-retained", dispatched),
		dispatched.InvocationID,
		dispatched.PlanHash,
	); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(DefaultGatewayInvocationRetention + time.Second)
	if _, found, lookupErr := store.Lookup(
		gatewayTestPrincipal(prepared),
		prepared.InvocationID,
	); lookupErr != nil ||
		found {
		t.Fatalf("expired prepared lookup = (%v, %v)", found, lookupErr)
	}
	if _, found, lookupErr := store.Lookup(
		gatewayTestPrincipal(dispatched),
		dispatched.InvocationID,
	); lookupErr != nil ||
		found {
		t.Fatalf("expired dispatched lookup = (%v, %v)", found, lookupErr)
	}
	fresh := gatewayTestPlan(t, "inv_sqlite_fresh", "idem_sqlite_fresh", clock)
	if _, _, err = store.Prepare("vpn", "call-sqlite-fresh", fresh, gatewayTestDescriptor()); err != nil {
		t.Fatal(err)
	}
	for _, invocationID := range []string{prepared.InvocationID, dispatched.InvocationID} {
		var count int
		if err = backend.db.QueryRow(
			"SELECT count(*) FROM gateway_invocations WHERE invocation_id=?",
			invocationID,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("expired invocation %q retained", invocationID)
		}
	}
	var count int
	if err = backend.db.QueryRow("SELECT count(*) FROM gateway_invocations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("retained invocation count = %d, want 1", count)
	}
}

func TestGatewayInvocationSQLiteRejectsProjectionCorruptionOnRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "node_invocations.db")
	store, err := NewGatewayInvocationSQLiteStore(path, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	plan := gatewayTestPlan(t, "inv_sqlite_corrupt", "idem_sqlite_corrupt", time.Now())
	if _, _, err = store.Prepare("vpn", "call-sqlite-corrupt", plan, gatewayTestDescriptor()); err != nil {
		t.Fatal(err)
	}
	if _, err = store.sqlite.db.Exec(
		"UPDATE gateway_invocations SET target='changed' WHERE invocation_id=?",
		plan.InvocationID,
	); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = NewGatewayInvocationSQLiteStore(path, 16*1024*1024); err == nil {
		t.Fatal("projection corruption was accepted on restart")
	}
}

func TestGatewayInvocationSQLiteRejectsDatabasePathReplacement(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(directory, "node_invocations.db")
	store, err := NewGatewayInvocationSQLiteStore(path, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	defer closeGatewayInvocationSQLiteTestStore(t, store)()
	retainedPath := filepath.Join(directory, "retained.db")
	if err = os.Rename(path, retainedPath); err != nil {
		t.Fatal(err)
	}
	replacement, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err = replacement.Close(); err != nil {
		t.Fatal(err)
	}
	plan := gatewayTestPlan(t, "inv_sqlite_replaced", "idem_sqlite_replaced", time.Now())
	if _, _, err = store.Prepare("vpn", "call-sqlite-replaced", plan, gatewayTestDescriptor()); err == nil {
		t.Fatal("replacement SQLite database path was accepted")
	}
}

func TestGatewayInvocationSQLiteRejectsDuplicateLegacyOwnership(t *testing.T) {
	workspace := t.TempDir()
	legacyPath := GatewayInvocationLegacyStorePath(workspace)
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	records := make(map[string]GatewayInvocationRecord)
	for index, identity := range []struct {
		invocationID   string
		idempotencyKey string
	}{
		{invocationID: "inv_sqlite_duplicate_one", idempotencyKey: "idem_sqlite_duplicate_one"},
		{invocationID: "inv_sqlite_duplicate_two", idempotencyKey: "idem_sqlite_duplicate_two"},
	} {
		legacy := newGatewayInvocationStore("", 8, 1024*1024, func() time.Time { return clock })
		plan := gatewayTestPlan(t, identity.invocationID, identity.idempotencyKey, clock)
		record, created, err := legacy.Prepare(
			"vpn",
			"call-sqlite-duplicate",
			plan,
			gatewayTestDescriptor(),
		)
		if err != nil || !created {
			t.Fatalf("prepare duplicate fixture %d = (%v, %v)", index, created, err)
		}
		records[plan.InvocationID] = record
	}
	data, err := json.Marshal(gatewayInvocationDocument{Version: gatewayInvocationStoreVersion, Records: records})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(legacyPath, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = NewGatewayInvocationSQLiteStore(GatewayInvocationStorePath(workspace), 16*1024*1024); err == nil {
		t.Fatal("duplicate legacy tool-call ownership was imported")
	}
}

func TestGatewayInvocationSQLiteFailsClosedForMarkerWithoutDatabase(t *testing.T) {
	workspace := t.TempDir()
	legacyPath := GatewayInvocationLegacyStorePath(workspace)
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeGatewayInvocationMigrationMarker(legacyPath, GatewayInvocationStorePath(workspace)); err != nil {
		t.Fatal(err)
	}
	_, err := NewGatewayInvocationSQLiteStore(GatewayInvocationStorePath(workspace), 16*1024*1024)
	if err == nil {
		t.Fatal("marker without durable database was accepted")
	}
	if _, statErr := os.Stat(GatewayInvocationStorePath(workspace)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("marker-only startup created database: %v", statErr)
	}
}

func TestGatewayInvocationSQLiteFailsClosedForMarkerWithTruncatedDatabase(t *testing.T) {
	workspace := t.TempDir()
	path := GatewayInvocationStorePath(workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if err = writeGatewayInvocationMigrationMarker(GatewayInvocationLegacyStorePath(workspace), path); err != nil {
		t.Fatal(err)
	}
	if _, err = NewGatewayInvocationSQLiteStore(path, 16*1024*1024); err == nil {
		t.Fatal("marker-backed truncated database was accepted")
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	var schemaObjects int
	if err = database.QueryRow("SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(
		&schemaObjects,
	); err != nil {
		t.Fatal(err)
	}
	if schemaObjects != 0 {
		t.Fatalf("failed startup mutated truncated database with %d schema objects", schemaObjects)
	}
}

func TestGatewayInvocationSQLiteFailsClosedForConstraintlessSchema(t *testing.T) {
	workspace := t.TempDir()
	path := GatewayInvocationStorePath(workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`
CREATE TABLE gateway_invocation_metadata(singleton INTEGER, schema_version INTEGER);
INSERT INTO gateway_invocation_metadata(singleton, schema_version) VALUES(1, 1);
CREATE TABLE gateway_invocations(
invocation_id TEXT, idempotency_key TEXT, target TEXT, tool_call_id TEXT,
agent_id TEXT, session_id TEXT, actor_id TEXT, workspace_id TEXT,
execution_id TEXT, plan_hash TEXT, state TEXT, created_at INTEGER,
updated_at INTEGER, dispatched_at INTEGER, plan_expires_at INTEGER,
record_json BLOB);
CREATE INDEX gateway_invocations_retention
ON gateway_invocations(state, updated_at, plan_expires_at);`)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err = database.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = writeGatewayInvocationMigrationMarker(GatewayInvocationLegacyStorePath(workspace), path); err != nil {
		t.Fatal(err)
	}
	if _, err = NewGatewayInvocationSQLiteStore(path, 16*1024*1024); err == nil {
		t.Fatal("marker-backed schema without authority constraints was accepted")
	}
}

func TestGatewayInvocationSQLiteRejectsSymlinkMigrationSource(t *testing.T) {
	workspace := t.TempDir()
	legacyPath := GatewayInvocationLegacyStorePath(workspace)
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "marker.json")
	if err := writeGatewayInvocationMigrationMarker(target, GatewayInvocationStorePath(workspace)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, legacyPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := NewGatewayInvocationSQLiteStore(GatewayInvocationStorePath(workspace), 16*1024*1024)
	if err == nil {
		t.Fatal("symlink migration source was accepted")
	}
}

func TestGatewayInvocationSQLiteRecoversEmptyDatabaseBeforeMarker(t *testing.T) {
	workspace := t.TempDir()
	path := GatewayInvocationStorePath(workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := NewGatewayInvocationSQLiteStore(path, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	defer closeGatewayInvocationSQLiteTestStore(t, store)()
	if _, err = os.Stat(GatewayInvocationLegacyStorePath(workspace)); err != nil {
		t.Fatalf("recovered startup did not publish marker: %v", err)
	}
}

func TestGatewayInvocationSQLiteCapacityIsByteBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "node_invocations.db")
	clock := time.Now().UTC().Truncate(time.Second)
	backend, err := newGatewayInvocationSQLiteStore(path, 128*1024, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	store := &GatewayInvocationStore{sqlite: backend}
	defer closeGatewayInvocationSQLiteTestStore(t, store)()
	full := false
	for index := 0; index < 1000; index++ {
		plan := gatewayTestPlan(
			t,
			"inv_sqlite_capacity_"+time.Unix(int64(index), 0).Format("150405.000000000"),
			"idem_sqlite_capacity_"+time.Unix(int64(index), 0).Format("150405.000000000"),
			clock,
		)
		_, _, prepareErr := store.Prepare(
			"vpn",
			"call-sqlite-capacity-"+time.Unix(int64(index), 0).Format("150405.000000000"),
			plan,
			gatewayTestDescriptor(),
		)
		if errors.Is(prepareErr, ErrGatewayInvocationStoreFull) {
			full = true
			break
		}
		if prepareErr != nil {
			t.Fatal(prepareErr)
		}
	}
	if !full {
		t.Fatal("bounded SQLite store did not report capacity exhaustion")
	}
	clock = clock.Add(2 * time.Minute)
	recoveryPlan := gatewayTestPlan(t, "inv_sqlite_after_prune", "idem_sqlite_after_prune", clock)
	if _, created, prepareErr := store.Prepare(
		"vpn",
		"call-sqlite-after-prune",
		recoveryPlan,
		gatewayTestDescriptor(),
	); prepareErr != nil || !created {
		t.Fatalf("prepare after retention cleanup = (%v, %v)", created, prepareErr)
	}
}

func closeGatewayInvocationSQLiteTestStore(t *testing.T, store *GatewayInvocationStore) func() {
	t.Helper()
	return func() {
		if err := store.Close(); err != nil {
			t.Errorf("close gateway invocation SQLite test store: %v", err)
		}
	}
}
