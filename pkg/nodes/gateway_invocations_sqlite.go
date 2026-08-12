package nodes

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode"

	_ "modernc.org/sqlite"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

const (
	gatewayInvocationSQLiteSchemaVersion = 1
	gatewayInvocationMigrationVersion    = 2
	gatewayInvocationSQLiteBusyTimeout   = 5 * time.Second
)

const gatewayInvocationSQLiteSchema = `
CREATE TABLE IF NOT EXISTS gateway_invocation_metadata (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    schema_version INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS gateway_invocations (
    invocation_id TEXT PRIMARY KEY,
    idempotency_key TEXT NOT NULL UNIQUE,
    target TEXT NOT NULL,
    tool_call_id TEXT NOT NULL,
    agent_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    execution_id TEXT NOT NULL,
    plan_hash TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('prepared', 'dispatched')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    dispatched_at INTEGER NOT NULL,
    plan_expires_at INTEGER NOT NULL,
    record_json BLOB NOT NULL,
    UNIQUE(agent_id, session_id, actor_id, tool_call_id, workspace_id, execution_id)
);
CREATE INDEX IF NOT EXISTS gateway_invocations_retention
ON gateway_invocations(state, updated_at, plan_expires_at);`

var gatewayInvocationSQLiteSchemaFingerprint = []gatewayInvocationSQLiteSchemaEntry{
	{
		entryType: "index",
		name:      "gateway_invocations_retention",
		table:     "gateway_invocations",
		sql: normalizeGatewayInvocationSQLiteDDL(
			"CREATE INDEX gateway_invocations_retention ON gateway_invocations(state, updated_at, plan_expires_at)",
		),
	},
	{entryType: "index", name: "sqlite_autoindex_gateway_invocations_1", table: "gateway_invocations"},
	{entryType: "index", name: "sqlite_autoindex_gateway_invocations_2", table: "gateway_invocations"},
	{entryType: "index", name: "sqlite_autoindex_gateway_invocations_3", table: "gateway_invocations"},
	{
		entryType: "table",
		name:      "gateway_invocation_metadata",
		table:     "gateway_invocation_metadata",
		sql: normalizeGatewayInvocationSQLiteDDL(
			"CREATE TABLE gateway_invocation_metadata (singleton INTEGER PRIMARY KEY CHECK (singleton = 1), schema_version INTEGER NOT NULL)",
		),
	},
	{
		entryType: "table",
		name:      "gateway_invocations",
		table:     "gateway_invocations",
		sql: normalizeGatewayInvocationSQLiteDDL(
			`CREATE TABLE gateway_invocations (
invocation_id TEXT PRIMARY KEY,
idempotency_key TEXT NOT NULL UNIQUE,
target TEXT NOT NULL,
tool_call_id TEXT NOT NULL,
agent_id TEXT NOT NULL,
session_id TEXT NOT NULL,
actor_id TEXT NOT NULL,
workspace_id TEXT NOT NULL,
execution_id TEXT NOT NULL,
plan_hash TEXT NOT NULL,
state TEXT NOT NULL CHECK (state IN ('prepared', 'dispatched')),
created_at INTEGER NOT NULL,
updated_at INTEGER NOT NULL,
dispatched_at INTEGER NOT NULL,
plan_expires_at INTEGER NOT NULL,
record_json BLOB NOT NULL,
UNIQUE(agent_id, session_id, actor_id, tool_call_id, workspace_id, execution_id))`,
		),
	},
}

type gatewayInvocationMigrationMarker struct {
	Version  int    `json:"version"`
	Backend  string `json:"backend"`
	Database string `json:"database"`
}

type gatewayInvocationSQLiteStore struct {
	path              string
	legacy            string
	maxBytes          int64
	retention         time.Duration
	now               func() time.Time
	db                *sql.DB
	identity          *os.File
	startupValidation func(context.Context) error
	mu                sync.RWMutex
	closed            bool
}

type gatewayInvocationSQLiteSchemaEntry struct {
	entryType string
	name      string
	table     string
	sql       string
}

type gatewayInvocationProjection struct {
	invocationID   string
	idempotencyKey string
	target         string
	toolCallID     string
	agentID        string
	sessionID      string
	actorID        string
	workspaceID    string
	executionID    string
	planHash       string
	state          string
	createdAt      int64
	updatedAt      int64
	dispatchedAt   int64
	planExpiresAt  int64
	recordJSON     []byte
}

func newGatewayInvocationSQLiteStore(
	path string,
	maxBytes int64,
	now func() time.Time,
) (*gatewayInvocationSQLiteStore, error) {
	return newGatewayInvocationSQLiteStoreWithStartupValidation(path, maxBytes, now, nil)
}

func newGatewayInvocationSQLiteStoreWithStartupValidation(
	path string,
	maxBytes int64,
	now func() time.Time,
	startupValidation func(context.Context) error,
) (*gatewayInvocationSQLiteStore, error) {
	path = filepath.Clean(path)
	if filepath.Ext(path) != ".db" || path == string(filepath.Separator) {
		return nil, errors.New("gateway node invocation SQLite path must end in .db")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultGatewayInvocationSQLiteBytes
	}
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create gateway node invocation SQLite directory: %w", err)
	}
	if err := validateGatewayInvocationSQLiteDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}

	legacy := strings.TrimSuffix(path, filepath.Ext(path)) + ".json"
	release, err := acquireRegistryFileLock(legacy + ".lock")
	if err != nil {
		return nil, fmt.Errorf("lock gateway node invocation migration: %w", err)
	}
	defer release()
	legacyKind, document, err := readGatewayInvocationMigrationSource(legacy)
	if err != nil {
		return nil, err
	}
	if legacyKind == "marker" {
		if _, statErr := os.Lstat(path); errors.Is(statErr, os.ErrNotExist) {
			return nil, errors.New("gateway node invocation migration marker has no durable database")
		} else if statErr != nil {
			return nil, fmt.Errorf("inspect gateway node invocation SQLite database behind marker: %w", statErr)
		}
	}

	databaseExisted, databaseIdentity, err := prepareGatewayInvocationSQLiteFile(path)
	if err != nil {
		return nil, err
	}
	if err = validateGatewayInvocationSQLiteSidecars(path); err != nil {
		return nil, err
	}
	databaseURL := (&url.URL{Scheme: "file", Path: path}).String()
	dsn := databaseURL + "?_txlock=immediate&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = databaseIdentity.Close()
		return nil, fmt.Errorf("open gateway node invocation SQLite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err = db.Ping(); err != nil {
		_ = db.Close()
		_ = databaseIdentity.Close()
		return nil, fmt.Errorf("connect gateway node invocation SQLite database: %w", err)
	}
	if err = verifyGatewayInvocationSQLiteIdentity(path, databaseIdentity); err != nil {
		_ = db.Close()
		_ = databaseIdentity.Close()
		return nil, err
	}
	store := &gatewayInvocationSQLiteStore{
		path: path, legacy: legacy, maxBytes: maxBytes,
		retention: DefaultGatewayInvocationRetention, now: now, db: db, identity: databaseIdentity,
		startupValidation: startupValidation,
	}
	if err = store.initialize(databaseExisted, legacyKind, document); err != nil {
		_ = db.Close()
		_ = databaseIdentity.Close()
		return nil, err
	}
	return store, nil
}

func validateGatewayInvocationSQLiteDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect gateway node invocation SQLite directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.New("gateway node invocation SQLite directory must be private and non-symlinked")
	}
	return nil
}

func prepareGatewayInvocationSQLiteFile(path string) (bool, *os.File, error) {
	directory, err := openAnchoredDirectory(filepath.Dir(path))
	if err != nil {
		return false, nil, fmt.Errorf("anchor gateway node invocation SQLite directory: %w", err)
	}
	defer func() { _ = directory.close() }()
	name := filepath.Base(path)
	file, info, err := directory.openRegular(name)
	if err == nil {
		if info.Mode().Perm()&0o077 != 0 {
			_ = file.Close()
			return false, nil, errors.New("gateway node invocation SQLite database permissions are too broad")
		}
		return true, file, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, nil, fmt.Errorf("inspect gateway node invocation SQLite database: %w", err)
	}
	file, err = directory.createRegularExclusive(name, 0o600)
	if err != nil {
		return false, nil, fmt.Errorf("create gateway node invocation SQLite database: %w", err)
	}
	info, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return false, nil, fmt.Errorf("inspect new gateway node invocation SQLite database: %w", statErr)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return false, nil, errors.New("new gateway node invocation SQLite database is unsafe")
	}
	return false, file, nil
}

func validateGatewayInvocationSQLiteSidecars(path string) error {
	for _, candidate := range []string{path + "-wal", path + "-shm"} {
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect gateway node invocation SQLite sidecar: %w", err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return errors.New("gateway node invocation SQLite sidecar must be a private regular file")
		}
	}
	return nil
}

func verifyGatewayInvocationSQLiteIdentity(path string, expected *os.File) error {
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("verify gateway node invocation SQLite database identity: %w", err)
	}
	if !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("gateway node invocation SQLite database path is unsafe")
	}
	if expected == nil {
		return errors.New("gateway node invocation SQLite database identity is unavailable")
	}
	expectedInfo, err := expected.Stat()
	if err != nil {
		return fmt.Errorf("inspect retained gateway node invocation SQLite database identity: %w", err)
	}
	if !os.SameFile(expectedInfo, pathInfo) {
		return errors.New("gateway node invocation SQLite database changed after validation")
	}
	return nil
}

func (store *gatewayInvocationSQLiteStore) initialize(
	databaseExisted bool,
	legacyKind string,
	document gatewayInvocationDocument,
) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if store.startupValidation != nil {
		if err := store.startupValidation(ctx); err != nil {
			return fmt.Errorf("validate gateway node invocation SQLite startup context: %w", err)
		}
	}
	hasSchema, err := store.hasSchema(ctx)
	if err != nil {
		return err
	}
	if legacyKind == "marker" && (!databaseExisted || !hasSchema) {
		return errors.New("gateway node invocation migration marker has no valid durable database")
	}
	if hasSchema {
		if err = store.verifySchema(ctx); err != nil {
			return err
		}
		if err = store.verifyIntegrity(ctx); err != nil {
			return err
		}
	}
	if err = store.configure(ctx); err != nil {
		return err
	}
	if !hasSchema {
		if err = store.createSchema(ctx); err != nil {
			return err
		}
		if err = store.verifySchema(ctx); err != nil {
			return err
		}
		if err = store.verifyIntegrity(ctx); err != nil {
			return err
		}
	}
	if err = store.applyPageLimit(ctx); err != nil {
		return err
	}
	switch legacyKind {
	case "marker":
		// Schema and record integrity were verified before any database mutation.
	case "snapshot":
		if err = store.importOrVerify(ctx, document); err != nil {
			return err
		}
		if err = store.verifyIntegrity(ctx); err != nil {
			return fmt.Errorf("verify imported gateway node invocation SQLite database: %w", err)
		}
		if err = writeGatewayInvocationMigrationMarker(store.legacy, store.path); err != nil {
			return err
		}
	case "missing":
		if databaseExisted && hasSchema {
			var count int
			if err = store.db.QueryRowContext(ctx, "SELECT count(*) FROM gateway_invocations").
				Scan(&count); err != nil {
				return fmt.Errorf("count unmarked gateway node invocation SQLite database: %w", err)
			}
			if count != 0 {
				return errors.New("gateway node invocation SQLite database lacks downgrade marker")
			}
		}
		if err = writeGatewayInvocationMigrationMarker(store.legacy, store.path); err != nil {
			return err
		}
	default:
		return errors.New("unsupported gateway node invocation migration state")
	}
	if err = store.prune(ctx); err != nil {
		return fmt.Errorf("prune gateway node invocation SQLite database: %w", err)
	}
	if err = store.maintain(ctx); err != nil {
		return err
	}
	return chmodGatewayInvocationSQLiteSidecars(store.path)
}

func (store *gatewayInvocationSQLiteStore) configure(ctx context.Context) error {
	for _, statement := range []string{
		"PRAGMA auto_vacuum=INCREMENTAL",
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=FULL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA trusted_schema=OFF",
		"PRAGMA wal_autocheckpoint=1000",
		"PRAGMA journal_size_limit=67108864",
	} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure gateway node invocation SQLite database: %w", err)
		}
	}
	return nil
}

func (store *gatewayInvocationSQLiteStore) applyPageLimit(ctx context.Context) error {
	var pageSize int64
	if err := store.db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return fmt.Errorf("read gateway node invocation SQLite page size: %w", err)
	}
	if pageSize <= 0 || store.maxBytes < pageSize {
		return ErrGatewayInvocationStoreFull
	}
	pages := store.maxBytes / pageSize
	var usedPages int64
	if err := store.db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&usedPages); err != nil {
		return fmt.Errorf("read gateway node invocation SQLite page count: %w", err)
	}
	if usedPages > pages {
		return ErrGatewayInvocationStoreFull
	}
	if _, err := store.db.ExecContext(ctx, fmt.Sprintf("PRAGMA max_page_count=%d", pages)); err != nil {
		return fmt.Errorf("bound gateway node invocation SQLite database: %w", err)
	}
	return nil
}

func (store *gatewayInvocationSQLiteStore) createSchema(ctx context.Context) error {
	return store.createSchemaWithHook(ctx, nil)
}

func (store *gatewayInvocationSQLiteStore) createSchemaWithHook(
	ctx context.Context,
	beforeCommit func() error,
) error {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin gateway node invocation SQLite schema: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if _, err = transaction.ExecContext(ctx, gatewayInvocationSQLiteSchema); err != nil {
		return fmt.Errorf("create gateway node invocation SQLite schema: %w", err)
	}
	if _, err = transaction.ExecContext(
		ctx,
		"INSERT INTO gateway_invocation_metadata(singleton, schema_version) VALUES(1, ?)",
		gatewayInvocationSQLiteSchemaVersion,
	); err != nil {
		return fmt.Errorf("record gateway node invocation SQLite schema: %w", err)
	}
	if beforeCommit != nil {
		if err = beforeCommit(); err != nil {
			return fmt.Errorf("interrupt gateway node invocation SQLite schema: %w", err)
		}
	}
	if err = transaction.Commit(); err != nil {
		return fmt.Errorf("commit gateway node invocation SQLite schema: %w", err)
	}
	return nil
}

func (store *gatewayInvocationSQLiteStore) hasSchema(ctx context.Context) (bool, error) {
	var count int
	if err := store.db.QueryRowContext(
		ctx,
		"SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'",
	).Scan(&count); err != nil {
		return false, fmt.Errorf("inspect gateway node invocation SQLite schema: %w", err)
	}
	return count != 0, nil
}

func (store *gatewayInvocationSQLiteStore) verifySchema(ctx context.Context) error {
	rows, err := store.db.QueryContext(ctx, `SELECT type, name, tbl_name, coalesce(sql, '')
FROM sqlite_master
WHERE type IN ('table', 'index')
ORDER BY type, name`)
	if err != nil {
		return fmt.Errorf("read gateway node invocation SQLite schema fingerprint: %w", err)
	}
	defer func() { _ = rows.Close() }()
	actual := make([]gatewayInvocationSQLiteSchemaEntry, 0, len(gatewayInvocationSQLiteSchemaFingerprint))
	for rows.Next() {
		var entry gatewayInvocationSQLiteSchemaEntry
		if err = rows.Scan(&entry.entryType, &entry.name, &entry.table, &entry.sql); err != nil {
			return fmt.Errorf("scan gateway node invocation SQLite schema fingerprint: %w", err)
		}
		entry.sql = normalizeGatewayInvocationSQLiteDDL(entry.sql)
		actual = append(actual, entry)
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("scan gateway node invocation SQLite schema fingerprint: %w", err)
	}
	if !reflect.DeepEqual(actual, gatewayInvocationSQLiteSchemaFingerprint) {
		return errors.New("gateway node invocation SQLite schema fingerprint mismatch")
	}
	var count, version int
	if err = store.db.QueryRowContext(
		ctx,
		"SELECT count(*), coalesce(max(schema_version), 0) FROM gateway_invocation_metadata WHERE singleton = 1",
	).Scan(&count, &version); err != nil {
		return fmt.Errorf("read gateway node invocation SQLite schema version: %w", err)
	}
	if count != 1 || version != gatewayInvocationSQLiteSchemaVersion {
		return fmt.Errorf("unsupported gateway node invocation SQLite schema version %d", version)
	}
	var metadataRows int
	if err = store.db.QueryRowContext(ctx, "SELECT count(*) FROM gateway_invocation_metadata").
		Scan(&metadataRows); err != nil {
		return fmt.Errorf("count gateway node invocation SQLite schema metadata: %w", err)
	}
	if metadataRows != 1 {
		return errors.New("gateway node invocation SQLite schema metadata mismatch")
	}
	return nil
}

func normalizeGatewayInvocationSQLiteDDL(statement string) string {
	return strings.ToLower(strings.Map(func(character rune) rune {
		if unicode.IsSpace(character) {
			return -1
		}
		return character
	}, statement))
}

func (store *gatewayInvocationSQLiteStore) verifyIntegrity(ctx context.Context) error {
	var result string
	if err := store.db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil {
		return fmt.Errorf("check gateway node invocation SQLite integrity: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("gateway node invocation SQLite integrity check failed: %s", result)
	}
	rows, err := store.db.QueryContext(ctx, gatewayInvocationSelect+" ORDER BY invocation_id")
	if err != nil {
		return fmt.Errorf("scan gateway node invocation SQLite records: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if _, err = scanGatewayInvocationRecord(rows); err != nil {
			return fmt.Errorf("validate gateway node invocation SQLite record: %w", err)
		}
	}
	return rows.Err()
}

func readGatewayInvocationMigrationSource(
	path string,
) (string, gatewayInvocationDocument, error) {
	data, err := readGatewayInvocationMigrationBytes(path, DefaultGatewayInvocationStoreBytes)
	if errors.Is(err, os.ErrNotExist) {
		return "missing", gatewayInvocationDocument{}, nil
	}
	if err != nil {
		return "", gatewayInvocationDocument{}, fmt.Errorf("read gateway node invocation migration source: %w", err)
	}
	var marker gatewayInvocationMigrationMarker
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&marker); err == nil && marker.Version == gatewayInvocationMigrationVersion &&
		marker.Backend == "sqlite" && marker.Database == filepath.Base(strings.TrimSuffix(path, ".json")+".db") {
		if err = requireGatewayInvocationJSONEOF(decoder); err == nil {
			return "marker", gatewayInvocationDocument{}, nil
		}
	}
	var document gatewayInvocationDocument
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&document); err != nil {
		return "", gatewayInvocationDocument{}, fmt.Errorf("decode gateway node invocation migration source: %w", err)
	}
	if err = requireGatewayInvocationJSONEOF(decoder); err != nil {
		return "", gatewayInvocationDocument{}, fmt.Errorf("decode gateway node invocation migration source: %w", err)
	}
	if document.Version != gatewayInvocationStoreVersion || document.Records == nil {
		return "", gatewayInvocationDocument{}, errors.New("unsupported gateway node invocation migration source")
	}
	return "snapshot", document, nil
}

func readGatewayInvocationMigrationBytes(path string, maxBytes int) ([]byte, error) {
	directory, err := openAnchoredDirectory(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.close() }()
	file, before, err := directory.openRegular(filepath.Base(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	if before.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("gateway node invocation migration source permissions are too broad")
	}
	if before.Size() > int64(maxBytes) {
		return nil, ErrGatewayInvocationStoreFull
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBytes {
		return nil, ErrGatewayInvocationStoreFull
	}
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !sameGatewayInvocationFileInfo(before, after) {
		return nil, errors.New("gateway node invocation migration source changed while reading")
	}
	return data, nil
}

func requireGatewayInvocationJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("unexpected trailing JSON value")
	}
	return err
}

func writeGatewayInvocationMigrationMarker(path string, database string) error {
	data, err := json.Marshal(gatewayInvocationMigrationMarker{
		Version:  gatewayInvocationMigrationVersion,
		Backend:  "sqlite",
		Database: filepath.Base(database),
	})
	if err != nil {
		return fmt.Errorf("encode gateway node invocation migration marker: %w", err)
	}
	data = append(data, '\n')
	if err = fileutil.WriteFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("publish gateway node invocation migration marker: %w", err)
	}
	return nil
}

func chmodGatewayInvocationSQLiteSidecars(path string) error {
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect gateway node invocation SQLite file: %w", err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("gateway node invocation SQLite file is not regular")
		}
		if err = os.Chmod(candidate, 0o600); err != nil {
			return fmt.Errorf("protect gateway node invocation SQLite file: %w", err)
		}
	}
	return nil
}

// InspectGatewayInvocationSQLite validates and reports a redacted view of an
// existing database without running retention or any other mutation.
func InspectGatewayInvocationSQLite(path string) (GatewayInvocationSQLiteReport, error) {
	store, err := openGatewayInvocationSQLiteReadOnly(path)
	if err != nil {
		return GatewayInvocationSQLiteReport{}, err
	}
	defer func() {
		_ = store.db.Close()
		_ = store.identity.Close()
	}()
	ctx := context.Background()
	if err = store.verifySchema(ctx); err != nil {
		return GatewayInvocationSQLiteReport{}, err
	}
	if err = store.verifyIntegrity(ctx); err != nil {
		return GatewayInvocationSQLiteReport{}, err
	}
	report := GatewayInvocationSQLiteReport{
		SchemaVersion:     gatewayInvocationSQLiteSchemaVersion,
		RetentionSeconds:  int64(DefaultGatewayInvocationRetention / time.Second),
		MigrationComplete: true,
	}
	if err = store.db.QueryRowContext(ctx, `SELECT count(*),
coalesce(sum(CASE WHEN state = 'prepared' THEN 1 ELSE 0 END), 0),
coalesce(sum(CASE WHEN state = 'dispatched' THEN 1 ELSE 0 END), 0),
coalesce(min(updated_at), 0) FROM gateway_invocations`).Scan(
		&report.Records,
		&report.Prepared,
		&report.Dispatched,
		&report.OldestUpdatedAt,
	); err != nil {
		return GatewayInvocationSQLiteReport{}, fmt.Errorf("summarize gateway node invocation SQLite database: %w", err)
	}
	var pageSize, pageCount, freePages int64
	for query, destination := range map[string]*int64{
		"PRAGMA page_size":      &pageSize,
		"PRAGMA page_count":     &pageCount,
		"PRAGMA freelist_count": &freePages,
	} {
		if err = store.db.QueryRowContext(ctx, query).Scan(destination); err != nil {
			return GatewayInvocationSQLiteReport{}, fmt.Errorf("inspect gateway node invocation SQLite pages: %w", err)
		}
	}
	report.PageBytes = pageCount * pageSize
	report.FreePageBytes = freePages * pageSize
	report.MaximumBytes = DefaultGatewayInvocationSQLiteBytes
	if err = verifyGatewayInvocationSQLiteIdentity(path, store.identity); err != nil {
		return GatewayInvocationSQLiteReport{}, err
	}
	if report.DatabaseBytes, err = gatewayInvocationSQLiteFileSize(path); err != nil {
		return GatewayInvocationSQLiteReport{}, err
	}
	if report.WALBytes, err = gatewayInvocationSQLiteOptionalFileSize(path + "-wal"); err != nil {
		return GatewayInvocationSQLiteReport{}, err
	}
	if report.SHMBytes, err = gatewayInvocationSQLiteOptionalFileSize(path + "-shm"); err != nil {
		return GatewayInvocationSQLiteReport{}, err
	}
	return report, nil
}

// ExportGatewayInvocationSQLite recreates the bounded legacy JSON snapshot
// required before installing a pre-SQLite binary. The caller must first stop
// the gateway so no authority can be committed after this snapshot.
func ExportGatewayInvocationSQLite(path, output string, replace bool) (GatewayInvocationSQLiteReport, error) {
	path = filepath.Clean(path)
	output = filepath.Clean(output)
	if filepath.Dir(path) != filepath.Dir(output) {
		return GatewayInvocationSQLiteReport{}, errors.New(
			"gateway node invocation export must stay in the protected state directory",
		)
	}
	if err := protectGatewayInvocationSQLiteExportTarget(path, output); err != nil {
		return GatewayInvocationSQLiteReport{}, err
	}
	report, err := InspectGatewayInvocationSQLite(path)
	if err != nil {
		return GatewayInvocationSQLiteReport{}, err
	}
	if report.Records > DefaultGatewayInvocationLimit {
		return GatewayInvocationSQLiteReport{}, fmt.Errorf(
			"gateway node invocation downgrade export exceeds legacy record limit: %w",
			ErrGatewayInvocationStoreFull,
		)
	}
	store, err := openGatewayInvocationSQLiteReadOnly(path)
	if err != nil {
		return GatewayInvocationSQLiteReport{}, err
	}
	defer func() {
		_ = store.db.Close()
		_ = store.identity.Close()
	}()
	ctx := context.Background()
	transaction, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return GatewayInvocationSQLiteReport{}, fmt.Errorf("begin gateway node invocation downgrade export: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	rows, err := transaction.QueryContext(ctx, gatewayInvocationSelect+" ORDER BY invocation_id")
	if err != nil {
		return GatewayInvocationSQLiteReport{}, fmt.Errorf("read gateway node invocation downgrade export: %w", err)
	}
	defer func() { _ = rows.Close() }()
	document := gatewayInvocationDocument{
		Version: gatewayInvocationStoreVersion,
		Records: make(map[string]GatewayInvocationRecord, report.Records),
	}
	for rows.Next() {
		record, scanErr := scanGatewayInvocationRecord(rows)
		if scanErr != nil {
			return GatewayInvocationSQLiteReport{}, fmt.Errorf(
				"validate gateway node invocation downgrade export: %w",
				scanErr,
			)
		}
		document.Records[record.Plan.InvocationID] = record
	}
	if err = rows.Err(); err != nil {
		return GatewayInvocationSQLiteReport{}, fmt.Errorf("read gateway node invocation downgrade export: %w", err)
	}
	if err = rows.Close(); err != nil {
		return GatewayInvocationSQLiteReport{}, fmt.Errorf("read gateway node invocation downgrade export: %w", err)
	}
	if int64(len(document.Records)) != report.Records {
		return GatewayInvocationSQLiteReport{}, errors.New(
			"gateway node invocation downgrade export changed while reading",
		)
	}
	data, err := json.Marshal(document)
	if err != nil {
		return GatewayInvocationSQLiteReport{}, fmt.Errorf("encode gateway node invocation downgrade export: %w", err)
	}
	data = append(data, '\n')
	if len(data) > DefaultGatewayInvocationStoreBytes {
		return GatewayInvocationSQLiteReport{}, fmt.Errorf(
			"gateway node invocation downgrade export exceeds legacy byte limit: %w",
			ErrGatewayInvocationStoreFull,
		)
	}
	if err = transaction.Commit(); err != nil {
		return GatewayInvocationSQLiteReport{}, fmt.Errorf("commit gateway node invocation downgrade export: %w", err)
	}
	if err = publishGatewayInvocationSQLiteExport(output, data, replace); err != nil {
		return GatewayInvocationSQLiteReport{}, fmt.Errorf("publish gateway node invocation downgrade export: %w", err)
	}
	kind, exported, err := readGatewayInvocationMigrationSource(output)
	if err != nil || kind != "snapshot" || len(exported.Records) != len(document.Records) {
		return GatewayInvocationSQLiteReport{}, errors.New(
			"validate published gateway node invocation downgrade export",
		)
	}
	return report, nil
}

func protectGatewayInvocationSQLiteExportTarget(path, output string) error {
	protected := []string{
		path,
		strings.TrimSuffix(path, filepath.Ext(path)) + ".json",
		path + "-wal",
		path + "-shm",
	}
	for _, candidate := range protected {
		if strings.EqualFold(output, candidate) {
			return errors.New("gateway node invocation export target is a protected SQLite artifact")
		}
		outputInfo, outputErr := os.Stat(output)
		candidateInfo, candidateErr := os.Stat(candidate)
		if outputErr == nil && candidateErr == nil && os.SameFile(outputInfo, candidateInfo) {
			return errors.New("gateway node invocation export target aliases a protected SQLite artifact")
		}
		if outputErr != nil && !errors.Is(outputErr, os.ErrNotExist) {
			return fmt.Errorf("inspect gateway node invocation export target: %w", outputErr)
		}
		if candidateErr != nil && !errors.Is(candidateErr, os.ErrNotExist) {
			return fmt.Errorf("inspect protected gateway node invocation SQLite artifact: %w", candidateErr)
		}
	}
	return nil
}

func publishGatewayInvocationSQLiteExport(path string, data []byte, replace bool) error {
	if replace {
		return fileutil.WriteFileAtomic(path, data, 0o600)
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".node-invocations-export-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err = temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err = temporary.Write(data); err != nil {
		return err
	}
	if err = temporary.Sync(); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	if err = os.Link(temporaryPath, path); err != nil {
		return err
	}
	if err = os.Remove(temporaryPath); err != nil {
		return &fileutil.CommittedWriteError{Err: err}
	}
	if err = fileutil.SyncDirectory(directory); err != nil {
		return &fileutil.CommittedWriteError{Err: err}
	}
	return nil
}

func openGatewayInvocationSQLiteReadOnly(path string) (*gatewayInvocationSQLiteStore, error) {
	path = filepath.Clean(path)
	if filepath.Ext(path) != ".db" || path == string(filepath.Separator) {
		return nil, errors.New("gateway node invocation SQLite path must end in .db")
	}
	if err := validateGatewayInvocationSQLiteDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := validateGatewayInvocationSQLiteSidecars(path); err != nil {
		return nil, err
	}
	legacy := strings.TrimSuffix(path, filepath.Ext(path)) + ".json"
	kind, _, err := readGatewayInvocationMigrationSource(legacy)
	if err != nil {
		return nil, err
	}
	if kind != "marker" {
		return nil, errors.New("gateway node invocation SQLite database lacks migration marker")
	}
	directory, err := openAnchoredDirectory(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("anchor gateway node invocation SQLite directory: %w", err)
	}
	identity, info, err := directory.openRegular(filepath.Base(path))
	_ = directory.close()
	if err != nil {
		return nil, fmt.Errorf("open gateway node invocation SQLite database: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		_ = identity.Close()
		return nil, errors.New("gateway node invocation SQLite database permissions are too broad")
	}
	databaseURL := (&url.URL{Scheme: "file", Path: path}).String()
	db, err := sql.Open(
		"sqlite",
		databaseURL+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)&_pragma=trusted_schema(OFF)",
	)
	if err != nil {
		_ = identity.Close()
		return nil, fmt.Errorf("open gateway node invocation SQLite database read-only: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err = db.Ping(); err != nil {
		_ = db.Close()
		_ = identity.Close()
		return nil, fmt.Errorf("connect gateway node invocation SQLite database read-only: %w", err)
	}
	if err = verifyGatewayInvocationSQLiteIdentity(path, identity); err != nil {
		_ = db.Close()
		_ = identity.Close()
		return nil, err
	}
	return &gatewayInvocationSQLiteStore{path: path, legacy: legacy, db: db, identity: identity}, nil
}

func gatewayInvocationSQLiteFileSize(path string) (int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, fmt.Errorf("inspect gateway node invocation SQLite file size: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return 0, errors.New("gateway node invocation SQLite file is not regular")
	}
	return info.Size(), nil
}

func gatewayInvocationSQLiteOptionalFileSize(path string) (int64, error) {
	size, err := gatewayInvocationSQLiteFileSize(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	return size, err
}

func (store *gatewayInvocationSQLiteStore) importOrVerify(
	ctx context.Context,
	document gatewayInvocationDocument,
) error {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin gateway node invocation import: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	var count int
	if err = transaction.QueryRowContext(ctx, "SELECT count(*) FROM gateway_invocations").Scan(&count); err != nil {
		return fmt.Errorf("count gateway node invocation import target: %w", err)
	}
	if count == 0 {
		for invocationID, record := range document.Records {
			if invocationID != record.Plan.InvocationID {
				return errors.New("gateway node invocation migration key mismatch")
			}
			if err = insertGatewayInvocationRecord(ctx, transaction, record); err != nil {
				return fmt.Errorf("import gateway node invocation %q: %w", invocationID, err)
			}
		}
		if err = transaction.Commit(); err != nil {
			return classifyGatewayInvocationSQLiteError("commit gateway node invocation import", err)
		}
		return nil
	}
	if count != len(document.Records) {
		return errors.New("gateway node invocation migration count mismatch")
	}
	for invocationID, expected := range document.Records {
		actual, found, lookupErr := lookupGatewayInvocationRow(ctx, transaction, invocationID)
		if lookupErr != nil || !found || !sameGatewayInvocationRecord(actual, expected) {
			return fmt.Errorf("gateway node invocation migration proof mismatch for %q", invocationID)
		}
	}
	return transaction.Commit()
}

func sameGatewayInvocationRecord(left, right GatewayInvocationRecord) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

const gatewayInvocationSelect = `SELECT
invocation_id, idempotency_key, target, tool_call_id, agent_id, session_id,
actor_id, workspace_id, execution_id, plan_hash, state, created_at, updated_at,
dispatched_at, plan_expires_at, record_json FROM gateway_invocations`

type gatewayInvocationScanner interface {
	Scan(dest ...any) error
}

func scanGatewayInvocationRecord(scanner gatewayInvocationScanner) (GatewayInvocationRecord, error) {
	var projection gatewayInvocationProjection
	if err := scanner.Scan(
		&projection.invocationID, &projection.idempotencyKey, &projection.target,
		&projection.toolCallID, &projection.agentID, &projection.sessionID,
		&projection.actorID, &projection.workspaceID, &projection.executionID,
		&projection.planHash, &projection.state, &projection.createdAt,
		&projection.updatedAt, &projection.dispatchedAt, &projection.planExpiresAt,
		&projection.recordJSON,
	); err != nil {
		return GatewayInvocationRecord{}, err
	}
	var record GatewayInvocationRecord
	decoder := json.NewDecoder(bytes.NewReader(projection.recordJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return GatewayInvocationRecord{}, fmt.Errorf("decode canonical invocation record: %w", err)
	}
	if err := requireGatewayInvocationJSONEOF(decoder); err != nil {
		return GatewayInvocationRecord{}, fmt.Errorf("decode canonical invocation record: %w", err)
	}
	if err := record.validate(); err != nil {
		return GatewayInvocationRecord{}, err
	}
	want := projectGatewayInvocationRecord(record)
	want.recordJSON = nil
	projection.recordJSON = nil
	if !reflect.DeepEqual(want, projection) {
		return GatewayInvocationRecord{}, errors.New("gateway node invocation SQLite projection mismatch")
	}
	return record, nil
}

func projectGatewayInvocationRecord(record GatewayInvocationRecord) gatewayInvocationProjection {
	return gatewayInvocationProjection{
		invocationID: record.Plan.InvocationID, idempotencyKey: record.Plan.IdempotencyKey,
		target: record.Target, toolCallID: record.ToolCallID,
		agentID: record.Plan.AgentID, sessionID: record.Plan.SessionID, actorID: record.Plan.ActorID,
		workspaceID: record.WorkspaceID, executionID: record.ExecutionID,
		planHash: record.ExpectedPlanHash, state: string(record.State),
		createdAt: record.CreatedAt, updatedAt: record.UpdatedAt,
		dispatchedAt: record.DispatchedAt, planExpiresAt: record.Plan.ExpiresAt,
	}
}

func insertGatewayInvocationRecord(
	ctx context.Context,
	executor interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
	},
	record GatewayInvocationRecord,
) error {
	if err := record.validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode gateway node invocation record: %w", err)
	}
	projection := projectGatewayInvocationRecord(record)
	_, err = executor.ExecContext(ctx, `INSERT INTO gateway_invocations(
invocation_id, idempotency_key, target, tool_call_id, agent_id, session_id,
actor_id, workspace_id, execution_id, plan_hash, state, created_at, updated_at,
dispatched_at, plan_expires_at, record_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		projection.invocationID, projection.idempotencyKey, projection.target,
		projection.toolCallID, projection.agentID, projection.sessionID,
		projection.actorID, projection.workspaceID, projection.executionID,
		projection.planHash, projection.state, projection.createdAt,
		projection.updatedAt, projection.dispatchedAt, projection.planExpiresAt,
		encoded,
	)
	return classifyGatewayInvocationSQLiteError("insert gateway node invocation", err)
}

func updateGatewayInvocationRecord(
	ctx context.Context,
	transaction *sql.Tx,
	record GatewayInvocationRecord,
) error {
	if err := record.validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode gateway node invocation record: %w", err)
	}
	projection := projectGatewayInvocationRecord(record)
	result, err := transaction.ExecContext(ctx, `UPDATE gateway_invocations SET
target=?, tool_call_id=?, agent_id=?, session_id=?, actor_id=?, workspace_id=?,
execution_id=?, plan_hash=?, state=?, created_at=?, updated_at=?, dispatched_at=?,
plan_expires_at=?, record_json=? WHERE invocation_id=?`,
		projection.target, projection.toolCallID, projection.agentID,
		projection.sessionID, projection.actorID, projection.workspaceID,
		projection.executionID, projection.planHash, projection.state,
		projection.createdAt, projection.updatedAt, projection.dispatchedAt,
		projection.planExpiresAt, encoded, projection.invocationID,
	)
	if err != nil {
		return classifyGatewayInvocationSQLiteError("update gateway node invocation", err)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return ErrGatewayInvocationConflict
	}
	return nil
}

func classifyGatewayInvocationSQLiteError(operation string, err error) error {
	if err == nil {
		return nil
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "database or disk is full") || strings.Contains(lower, "sqlite_full") {
		return fmt.Errorf("%s: %w", operation, ErrGatewayInvocationStoreFull)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func lookupGatewayInvocationRow(
	ctx context.Context,
	queryer interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	},
	invocationID string,
) (GatewayInvocationRecord, bool, error) {
	record, err := scanGatewayInvocationRecord(queryer.QueryRowContext(
		ctx,
		gatewayInvocationSelect+" WHERE invocation_id = ?",
		strings.TrimSpace(invocationID),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return GatewayInvocationRecord{}, false, nil
	}
	if err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	return record, true, nil
}

func (store *gatewayInvocationSQLiteStore) prepareOwned(
	principal GatewayInvocationPrincipal,
	target string,
	toolCallID string,
	plan ExecutionPlan,
	descriptor CommandDescriptor,
) (GatewayInvocationRecord, bool, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	if store.closed {
		return GatewayInvocationRecord{}, false, os.ErrClosed
	}
	if err := verifyGatewayInvocationSQLiteIdentity(store.path, store.identity); err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	principal.AgentID = strings.TrimSpace(principal.AgentID)
	principal.SessionID = strings.TrimSpace(principal.SessionID)
	principal.ActorID = strings.TrimSpace(principal.ActorID)
	principal.WorkspaceID = strings.TrimSpace(principal.WorkspaceID)
	principal.ExecutionID = strings.TrimSpace(principal.ExecutionID)
	if principal.AgentID != plan.AgentID || principal.SessionID != plan.SessionID ||
		principal.ActorID != plan.ActorID ||
		(principal.WorkspaceID == "") != (principal.ExecutionID == "") {
		return GatewayInvocationRecord{}, false, ErrGatewayInvocationConflict
	}
	now := store.now()
	record := GatewayInvocationRecord{
		Target: strings.TrimSpace(target), ToolCallID: strings.TrimSpace(toolCallID),
		Plan: cloneExecutionPlan(plan), Descriptor: cloneCommandDescriptor(descriptor),
		ExpectedPlanHash: plan.PlanHash, State: GatewayInvocationPrepared,
		CreatedAt: now.UnixNano(), UpdatedAt: now.UnixNano(),
		WorkspaceID: principal.WorkspaceID, ExecutionID: principal.ExecutionID,
	}
	if err := record.validate(); err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	if now.Unix() >= plan.ExpiresAt {
		return GatewayInvocationRecord{}, false, fmt.Errorf(
			"%w: execution plan expired before persistence", ErrInvalidInvocation,
		)
	}
	ctx, cancel := context.WithTimeout(context.Background(), gatewayInvocationSQLiteBusyTimeout)
	defer cancel()
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return GatewayInvocationRecord{}, false, classifyGatewayInvocationSQLiteError("begin prepare", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if err = store.pruneTransaction(ctx, transaction, now); err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	retainedRows, err := transaction.QueryContext(ctx, gatewayInvocationSelect+`
 WHERE agent_id=? AND session_id=? AND actor_id=? AND tool_call_id=?`,
		principal.AgentID, principal.SessionID, principal.ActorID, record.ToolCallID,
	)
	if err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	defer func() { _ = retainedRows.Close() }()
	for retainedRows.Next() {
		existing, scanErr := scanGatewayInvocationRecord(retainedRows)
		if scanErr != nil {
			_ = retainedRows.Close()
			return GatewayInvocationRecord{}, false, scanErr
		}
		if !gatewayInvocationScopeMatches(existing, principal) {
			continue
		}
		_ = retainedRows.Close()
		if sameGatewayInvocationBinding(existing, record) {
			if err = transaction.Commit(); err != nil {
				return GatewayInvocationRecord{}, false, classifyGatewayInvocationSQLiteError(
					"commit retained prepare",
					err,
				)
			}
			return existing, false, nil
		}
		return GatewayInvocationRecord{}, false, ErrGatewayInvocationConflict
	}
	if err = retainedRows.Err(); err != nil {
		_ = retainedRows.Close()
		return GatewayInvocationRecord{}, false, err
	}
	if err = retainedRows.Close(); err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	var conflictingID string
	err = transaction.QueryRowContext(
		ctx,
		"SELECT invocation_id FROM gateway_invocations WHERE idempotency_key=?",
		plan.IdempotencyKey,
	).Scan(&conflictingID)
	if err == nil && conflictingID != plan.InvocationID {
		return GatewayInvocationRecord{}, false, ErrGatewayInvocationConflict
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return GatewayInvocationRecord{}, false, err
	}
	now = store.now()
	if now.Unix() >= plan.ExpiresAt {
		return GatewayInvocationRecord{}, false, fmt.Errorf(
			"%w: execution plan expired before persistence", ErrInvalidInvocation,
		)
	}
	record.CreatedAt = now.UnixNano()
	record.UpdatedAt = record.CreatedAt
	if err = insertGatewayInvocationRecord(ctx, transaction, record); err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	if err = transaction.Commit(); err != nil {
		return GatewayInvocationRecord{}, false, classifyGatewayInvocationSQLiteError("commit prepared invocation", err)
	}
	return cloneGatewayInvocationRecord(record), true, nil
}

func (store *gatewayInvocationSQLiteStore) byToolCall(
	principal GatewayInvocationPrincipal,
	toolCallID string,
) (GatewayInvocationRecord, bool, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	if store.closed {
		return GatewayInvocationRecord{}, false, os.ErrClosed
	}
	if err := verifyGatewayInvocationSQLiteIdentity(store.path, store.identity); err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), gatewayInvocationSQLiteBusyTimeout)
	defer cancel()
	now := store.now()
	record, err := scanGatewayInvocationRecord(store.db.QueryRowContext(ctx, gatewayInvocationSelect+`
	 WHERE agent_id=? AND session_id=? AND actor_id=? AND tool_call_id=?
	 AND workspace_id=? AND execution_id=?
	 AND NOT (state='prepared' AND plan_expires_at <= ?)
	 AND NOT (state='dispatched' AND updated_at < ?)`,
		principal.AgentID, principal.SessionID, principal.ActorID, toolCallID,
		principal.WorkspaceID, principal.ExecutionID,
		now.Unix(), now.Add(-store.retention).UnixNano(),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return GatewayInvocationRecord{}, false, nil
	}
	if err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	return record, true, nil
}

func (store *gatewayInvocationSQLiteStore) lookup(
	principal GatewayInvocationPrincipal,
	invocationID string,
) (GatewayInvocationRecord, bool, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	if store.closed {
		return GatewayInvocationRecord{}, false, os.ErrClosed
	}
	if err := verifyGatewayInvocationSQLiteIdentity(store.path, store.identity); err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), gatewayInvocationSQLiteBusyTimeout)
	defer cancel()
	now := store.now()
	record, err := scanGatewayInvocationRecord(store.db.QueryRowContext(
		ctx,
		gatewayInvocationSelect+` WHERE invocation_id=?
		AND NOT (state='prepared' AND plan_expires_at <= ?)
		AND NOT (state='dispatched' AND updated_at < ?)`,
		invocationID,
		now.Unix(),
		now.Add(-store.retention).UnixNano(),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return GatewayInvocationRecord{}, false, nil
	}
	found := err == nil
	if err != nil || !found {
		return record, found, err
	}
	if record.Plan.AgentID != principal.AgentID ||
		record.Plan.SessionID != principal.SessionID ||
		record.Plan.ActorID != principal.ActorID ||
		!gatewayInvocationWorkspaceMatches(record, principal) {
		return GatewayInvocationRecord{}, false, nil
	}
	return record, true, nil
}

func (store *gatewayInvocationSQLiteStore) requestCancellation(
	principal GatewayInvocationPrincipal,
	invocationID string,
) (GatewayInvocationRecord, bool, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	if store.closed {
		return GatewayInvocationRecord{}, false, os.ErrClosed
	}
	if err := verifyGatewayInvocationSQLiteIdentity(store.path, store.identity); err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), gatewayInvocationSQLiteBusyTimeout)
	defer cancel()
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	defer func() { _ = transaction.Rollback() }()
	if err = store.pruneTransaction(ctx, transaction, store.now()); err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	record, found, err := lookupGatewayInvocationRow(ctx, transaction, invocationID)
	if err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	if !found {
		return GatewayInvocationRecord{}, false, ErrGatewayInvocationNotFound
	}
	if record.Plan.AgentID != principal.AgentID ||
		record.Plan.SessionID != principal.SessionID ||
		record.Plan.ActorID != principal.ActorID ||
		!gatewayInvocationScopeMatches(record, principal) {
		return GatewayInvocationRecord{}, false, ErrGatewayInvocationConflict
	}
	if record.State != GatewayInvocationDispatched {
		return GatewayInvocationRecord{}, false, ErrGatewayInvocationNotDispatched
	}
	if record.Cancellation != nil {
		if err = transaction.Commit(); err != nil {
			return GatewayInvocationRecord{}, false, err
		}
		return record, false, nil
	}
	now, err := nextGatewayInvocationTimestamp(store.now(), record.UpdatedAt)
	if err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	record.Cancellation = &GatewayInvocationCancellation{RequestedAt: now}
	record.UpdatedAt = now
	if err = updateGatewayInvocationRecord(ctx, transaction, record); err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	if err = transaction.Commit(); err != nil {
		return GatewayInvocationRecord{}, false, classifyGatewayInvocationSQLiteError(
			"commit invocation cancellation",
			err,
		)
	}
	return record, true, nil
}

func (store *gatewayInvocationSQLiteStore) markDispatched(
	owner GatewayInvocationOwner,
	invocationID string,
	expectedPlanHash string,
) (GatewayInvocationRecord, bool, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	if store.closed {
		return GatewayInvocationRecord{}, false, os.ErrClosed
	}
	if err := verifyGatewayInvocationSQLiteIdentity(store.path, store.identity); err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	if err := owner.validate(); err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), gatewayInvocationSQLiteBusyTimeout)
	defer cancel()
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	defer func() { _ = transaction.Rollback() }()
	if err = store.pruneTransaction(ctx, transaction, store.now()); err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	record, found, err := lookupGatewayInvocationRow(ctx, transaction, invocationID)
	if err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	if !found {
		return GatewayInvocationRecord{}, false, ErrGatewayInvocationNotFound
	}
	if !owner.matches(record) {
		return GatewayInvocationRecord{}, false, ErrGatewayInvocationConflict
	}
	if err = record.Plan.ValidateAgainstHash(expectedPlanHash); err != nil ||
		record.ExpectedPlanHash != expectedPlanHash {
		return GatewayInvocationRecord{}, false, ErrGatewayInvocationConflict
	}
	if record.State == GatewayInvocationDispatched {
		if err = transaction.Commit(); err != nil {
			return GatewayInvocationRecord{}, false, err
		}
		return record, false, nil
	}
	now, err := nextGatewayInvocationTimestamp(store.now(), record.UpdatedAt)
	if err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	record.State = GatewayInvocationDispatched
	record.DispatchedAt = now
	record.UpdatedAt = now
	if err = updateGatewayInvocationRecord(ctx, transaction, record); err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	if err = transaction.Commit(); err != nil {
		return GatewayInvocationRecord{}, false, classifyGatewayInvocationSQLiteError(
			"commit dispatched invocation",
			err,
		)
	}
	return record, true, nil
}

func (store *gatewayInvocationSQLiteStore) prune(ctx context.Context) error {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback() }()
	if err = store.pruneTransaction(ctx, transaction, store.now()); err != nil {
		return err
	}
	return transaction.Commit()
}

func nextGatewayInvocationTimestamp(now time.Time, previous int64) (int64, error) {
	candidate := now.UnixNano()
	if candidate > previous {
		return candidate, nil
	}
	if previous == int64(^uint64(0)>>1) {
		return 0, fmt.Errorf("%w: invocation timestamp exhausted", ErrInvalidInvocation)
	}
	return previous + 1, nil
}

func (store *gatewayInvocationSQLiteStore) maintain(ctx context.Context) error {
	var busy, logFrames, checkpointed int
	if err := store.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)").Scan(
		&busy,
		&logFrames,
		&checkpointed,
	); err != nil {
		return fmt.Errorf("checkpoint gateway node invocation SQLite WAL: %w", err)
	}
	if _, err := store.db.ExecContext(ctx, "PRAGMA incremental_vacuum(1024)"); err != nil {
		return fmt.Errorf("reclaim gateway node invocation SQLite pages: %w", err)
	}
	return nil
}

func (store *gatewayInvocationSQLiteStore) pruneTransaction(
	ctx context.Context,
	transaction *sql.Tx,
	now time.Time,
) error {
	retentionBefore := now.Add(-store.retention).UnixNano()
	_, err := transaction.ExecContext(ctx, `DELETE FROM gateway_invocations
WHERE (state='prepared' AND plan_expires_at <= ?)
   OR (state='dispatched' AND updated_at < ?)`, now.Unix(), retentionBefore)
	return classifyGatewayInvocationSQLiteError("prune gateway node invocations", err)
}

func (store *gatewayInvocationSQLiteStore) close() error {
	if store == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed || store.db == nil {
		return nil
	}
	store.closed = true
	databaseErr := store.db.Close()
	var identityErr error
	if store.identity != nil {
		identityErr = store.identity.Close()
	}
	return errors.Join(databaseErr, identityErr)
}
