package seahorse

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"slices"
	"strings"
)

const (
	SnapshotMaxBytes      = 8 * 1024 * 1024
	SnapshotMaxRows       = 8192
	SnapshotMaxValueBytes = 64 * 1024
	maxSnapshotSchema     = 256
)

var ErrSnapshotUnavailable = errors.New("seahorse: bounded native snapshot unavailable")

type SnapshotValueKind uint8

const (
	SnapshotText SnapshotValueKind = iota
	SnapshotIndexBlob
)

type SnapshotInspection struct {
	Rows, TextValues, BinaryValues, ValueBytes int
}

type snapshotSchemaRow struct {
	Kind, Name, Table, SQL string
}

// InspectSnapshot reads a caller-owned image through an in-memory SQLite
// connection, without opening, migrating or checkpointing its original store.
// The caller must establish image/sidecar completeness separately and inspect
// original bytes: current rows do not cover deleted or encoded residual data.
// visit receives ephemeral values and must not retain them. Its errors pass
// through unchanged; SQLite errors and schema contents are never exported.
func InspectSnapshot(
	ctx context.Context, image []byte, visit func([]byte, SnapshotValueKind) error,
) (SnapshotInspection, error) {
	result := SnapshotInspection{}
	if ctx == nil || ctx.Err() != nil || visit == nil || !validSnapshotImage(image) {
		return result, ErrSnapshotUnavailable
	}
	expectedDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return result, ErrSnapshotUnavailable
	}
	defer func() { _ = expectedDB.Close() }()
	expectedDB.SetMaxOpenConns(1)
	if _, err = expectedDB.ExecContext(ctx, "PRAGMA temp_store=MEMORY"); err != nil {
		return result, ErrSnapshotUnavailable
	}
	if runSchemaContext(ctx, expectedDB) != nil {
		return result, ErrSnapshotUnavailable
	}
	expected, err := readSnapshotSchema(ctx, expectedDB)
	if err != nil {
		return result, ErrSnapshotUnavailable
	}
	shadows, err := snapshotShadowTables(ctx, expectedDB)
	if err != nil {
		return result, ErrSnapshotUnavailable
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return result, ErrSnapshotUnavailable
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		return result, ErrSnapshotUnavailable
	}
	defer func() { _ = conn.Close() }()
	copyImage := bytes.Clone(image)
	defer clear(copyImage)
	// Deserialize does not support WAL-format images. Only this private decoding
	// copy changes; the caller still owns the exact original and its raw scan.
	copyImage[18], copyImage[19] = 1, 1
	err = conn.Raw(func(raw any) error {
		decoder, ok := raw.(interface{ Deserialize([]byte) error })
		if !ok {
			return ErrSnapshotUnavailable
		}
		return decoder.Deserialize(copyImage)
	})
	if err != nil {
		return result, ErrSnapshotUnavailable
	}
	if _, err = conn.ExecContext(
		ctx,
		"PRAGMA temp_store=MEMORY; PRAGMA query_only=ON; PRAGMA trusted_schema=OFF",
	); err != nil {
		return result, ErrSnapshotUnavailable
	}
	actual, err := readSnapshotSchema(ctx, conn)
	if err != nil {
		return result, ErrSnapshotUnavailable
	}
	for _, row := range actual {
		for _, text := range []string{row.Kind, row.Name, row.Table, row.SQL} {
			if err = result.inspectValue([]byte(text), SnapshotText, visit); err != nil {
				return result, err
			}
		}
	}
	if !slices.Equal(expected, actual) {
		return result, ErrSnapshotUnavailable
	}
	var integrity string
	if conn.QueryRowContext(ctx, "PRAGMA quick_check(1)").Scan(&integrity) != nil || integrity != "ok" {
		return result, ErrSnapshotUnavailable
	}
	for _, row := range actual {
		if row.Kind == "table" {
			if err = result.inspectTable(ctx, conn, row.Name, shadows[row.Name], visit); err != nil {
				return result, err
			}
		}
	}
	return result, nil
}

func validSnapshotImage(image []byte) bool {
	if len(image) < 100 || len(image) > SnapshotMaxBytes || !bytes.HasPrefix(image, []byte("SQLite format 3\x00")) ||
		image[18] != image[19] || image[18] != 1 && image[18] != 2 || binary.BigEndian.Uint32(image[56:60]) != 1 {
		return false
	}
	pageSize := int(binary.BigEndian.Uint16(image[16:18]))
	if pageSize == 1 {
		pageSize = 65536
	}
	return pageSize >= 512 && pageSize <= 65536 && pageSize&(pageSize-1) == 0 &&
		len(image)%pageSize == 0 && uint64(binary.BigEndian.Uint32(image[28:32])) == uint64(len(image)/pageSize)
}

type snapshotQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readSnapshotSchema(ctx context.Context, db snapshotQueryer) ([]snapshotSchemaRow, error) {
	rows, err := db.QueryContext(
		ctx,
		`SELECT type,name,tbl_name,coalesce(sql,'') FROM sqlite_schema ORDER BY type,name`,
	)
	if err != nil {
		return nil, ErrSnapshotUnavailable
	}
	defer func() { _ = rows.Close() }()
	result := []snapshotSchemaRow{}
	total := 0
	for rows.Next() {
		var row snapshotSchemaRow
		if rows.Scan(&row.Kind, &row.Name, &row.Table, &row.SQL) != nil || len(result) >= maxSnapshotSchema {
			return nil, ErrSnapshotUnavailable
		}
		total += len(row.Kind) + len(row.Name) + len(row.Table) + len(row.SQL)
		if total > 256*1024 {
			return nil, ErrSnapshotUnavailable
		}
		result = append(result, row)
	}
	if rows.Err() != nil {
		return nil, ErrSnapshotUnavailable
	}
	return result, nil
}

func snapshotShadowTables(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_list")
	if err != nil {
		return nil, ErrSnapshotUnavailable
	}
	defer func() { _ = rows.Close() }()
	result := map[string]bool{}
	count := 0
	for rows.Next() {
		var schema, name, kind string
		var columns, withoutRowID, strict int
		if rows.Scan(&schema, &name, &kind, &columns, &withoutRowID, &strict) != nil || count >= maxSnapshotSchema {
			return nil, ErrSnapshotUnavailable
		}
		count++
		if schema == "main" && kind == "shadow" {
			result[name] = true
		}
	}
	if rows.Err() != nil {
		return nil, ErrSnapshotUnavailable
	}
	return result, nil
}

func (result *SnapshotInspection) inspectValue(
	value []byte, kind SnapshotValueKind, visit func([]byte, SnapshotValueKind) error,
) error {
	if len(value) > SnapshotMaxValueBytes || len(value) > SnapshotMaxBytes-result.ValueBytes {
		return ErrSnapshotUnavailable
	}
	result.ValueBytes += len(value)
	if kind == SnapshotText {
		result.TextValues++
	} else {
		result.BinaryValues++
	}
	return visit(value, kind)
}

func (result *SnapshotInspection) inspectTable(
	ctx context.Context, conn *sql.Conn, name string, shadow bool, visit func([]byte, SnapshotValueKind) error,
) error {
	identifier := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	rows, err := conn.QueryContext(ctx, "SELECT * FROM "+identifier)
	if err != nil {
		return ErrSnapshotUnavailable
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil || len(columns) > 64 {
		return ErrSnapshotUnavailable
	}
	values := make([]any, len(columns))
	pointers := make([]any, len(columns))
	for index := range values {
		pointers[index] = &values[index]
	}
	for rows.Next() {
		if ctx.Err() != nil || result.Rows >= SnapshotMaxRows || rows.Scan(pointers...) != nil {
			return ErrSnapshotUnavailable
		}
		result.Rows++
		for _, value := range values {
			switch typed := value.(type) {
			case string:
				if err = result.inspectValue([]byte(typed), SnapshotText, visit); err != nil {
					return err
				}
			case []byte:
				if !shadow {
					return ErrSnapshotUnavailable
				}
				if err = result.inspectValue(typed, SnapshotIndexBlob, visit); err != nil {
					return err
				}
			case nil, int64, float64:
			default:
				return ErrSnapshotUnavailable
			}
		}
	}
	if rows.Err() != nil {
		return ErrSnapshotUnavailable
	}
	return nil
}
