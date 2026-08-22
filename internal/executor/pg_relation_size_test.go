package executor

import (
	"fmt"
	"strings"
	"testing"
)

// TestPgRelationSizeReflectsActualStorage exercises the M0122-0002 fix:
// pg_relation_size/pg_table_size/pg_indexes_size/pg_total_relation_size
// previously always returned a hardcoded 8kB regardless of real size.
func TestPgRelationSizeReflectsActualStorage(t *testing.T) {
	ctx, _, cleanup := newHOTFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE rs_t (id int, v text)"); err != nil {
		t.Fatal(err)
	}
	// Insert enough rows to force at least one real heap block.
	for i := range 200 {
		stmt := fmt.Sprintf("INSERT INTO rs_t VALUES (%d, 'row-%d')", i, i)
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	if err := runDDL(t, ctx, "CREATE INDEX rs_t_idx ON rs_t (id)"); err != nil {
		t.Fatal(err)
	}

	relSize := scalarInt(t, ctx, "SELECT pg_relation_size('rs_t'::regclass)")
	if relSize <= 0 || relSize%8192 != 0 {
		t.Fatalf("pg_relation_size: want a positive multiple of 8192, got %d", relSize)
	}

	idxSize := scalarInt(t, ctx, "SELECT pg_indexes_size('rs_t'::regclass)")
	if idxSize <= 0 || idxSize%8192 != 0 {
		t.Fatalf("pg_indexes_size: want a positive multiple of 8192, got %d", idxSize)
	}

	tableSize := scalarInt(t, ctx, "SELECT pg_table_size('rs_t'::regclass)")
	if tableSize != relSize {
		t.Fatalf("pg_table_size: want %d (no TOAST relation), got %d", relSize, tableSize)
	}

	totalSize := scalarInt(t, ctx, "SELECT pg_total_relation_size('rs_t'::regclass)")
	if totalSize != tableSize+idxSize {
		t.Fatalf("pg_total_relation_size: want %d (table+indexes), got %d", tableSize+idxSize, totalSize)
	}

	// A never-created fork (fsm/vm) must report 0, not silently create the
	// fork file as a side effect (smgr O_CREATE gotcha).
	fsmSize := scalarInt(t, ctx, "SELECT pg_relation_size('rs_t'::regclass, 'fsm')")
	if fsmSize != 0 {
		t.Fatalf("pg_relation_size(..., 'fsm'): want 0, got %d", fsmSize)
	}

	// An invalid fork name is a 22023 error, matching PG.
	if _, err := runQueryWithErr(ctx, "SELECT pg_relation_size('rs_t'::regclass, 'bogus')"); err == nil {
		t.Fatal("pg_relation_size with an invalid fork name: want an error, got nil")
	}
}

// TestPgTableSizeIncludesToastRelation verifies pg_table_size counts the
// TOAST relation's own storage, while pg_relation_size (the main heap only)
// does not.
func TestPgTableSizeIncludesToastRelation(t *testing.T) {
	ctx, _, cleanup := newHOTFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE rs_toast (id int, big text)"); err != nil {
		t.Fatal(err)
	}
	bigValue := strings.Repeat("X", 1<<20)
	if err := runDDL(t, ctx, fmt.Sprintf("INSERT INTO rs_toast VALUES (1, '%s')", bigValue)); err != nil {
		t.Fatal(err)
	}

	relSize := scalarInt(t, ctx, "SELECT pg_relation_size('rs_toast'::regclass)")
	tableSize := scalarInt(t, ctx, "SELECT pg_table_size('rs_toast'::regclass)")
	if tableSize <= relSize {
		t.Fatalf("pg_table_size (%d) should exceed pg_relation_size (%d) once a TOASTed value exists", tableSize, relSize)
	}
}

// TestPgRelationSizeResolvesToastRelid verifies M0134-0070 (J1, design
// m0134-0070-toast-pg-relation-size.md): pg_relation_size(reltoastrelid)
// resolves a synthetic TOAST relation OID to its real main-fork RelFileNode
// instead of returning NULL. Previously relationFileNodeForOID only knew
// LookupTableByOID/LookupIndexByOID, so a synthetic TOAST OID (parent OID +
// 100M, a virtual pg_class row with no table/index registry entry) matched
// neither and evalPgRelationSize returned NullDatum — `NULL = 0` printed blank
// in the regress toasttest bucket. Mirrors PG, where reltoastrelid is a real
// relkind='t' relation that try_relation_open opens (dbsize.c:371-381) and
// calculate_relation_size (dbsize.c:326) sizes from the live fork file.
func TestPgRelationSizeResolvesToastRelid(t *testing.T) {
	ctx, _, cleanup := newHOTFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE toasttest (id int, big text)"); err != nil {
		t.Fatal(err)
	}
	// STORAGE EXTERNAL forbids compression (toast.go:233), so the 3000-byte
	// value is stored raw out-of-line and the toast heap grows on disk.
	if err := runDDL(t, ctx, "ALTER TABLE toasttest ALTER COLUMN big SET STORAGE EXTERNAL"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO toasttest VALUES (1, repeat('1234567890', 300))"); err != nil {
		t.Fatal(err)
	}

	// Case 1: the raw reltoastrelid OID column feeds pg_relation_size directly —
	// the exact shape the regress toasttest bucket uses.
	rows := runQuery(t, ctx,
		"SELECT pg_relation_size(reltoastrelid) FROM pg_class WHERE relname = 'toasttest'")
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("pg_relation_size(reltoastrelid): want 1x1 result, got %d rows", len(rows))
	}
	if rows[0][0].IsNull() {
		t.Fatal("pg_relation_size(reltoastrelid): NULL (toast OID did not resolve), want > 0")
	}
	if rows[0][0].Kind != KindInt || rows[0][0].Int <= 0 {
		t.Fatalf("pg_relation_size(reltoastrelid) = %v, want > 0", rows[0][0])
	}

	// Case 2: the toast relation OID routed through an explicit ::regclass cast.
	toastRows := runQuery(t, ctx, "SELECT reltoastrelid FROM pg_class WHERE relname = 'toasttest'")
	if len(toastRows) != 1 || len(toastRows[0]) != 1 {
		t.Fatalf("reltoastrelid: want 1x1 result, got %d rows", len(toastRows))
	}
	toastOID := toastRows[0][0].Int
	rows = runQuery(t, ctx, fmt.Sprintf("SELECT pg_relation_size('%d'::regclass)", toastOID))
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("pg_relation_size('<toast relid>'::regclass): want 1x1 result, got %d rows", len(rows))
	}
	if rows[0][0].IsNull() || rows[0][0].Kind != KindInt || rows[0][0].Int <= 0 {
		t.Fatalf("pg_relation_size('%d'::regclass) = %v, want > 0", toastOID, rows[0][0])
	}
}

func scalarInt(t *testing.T, ctx *Context, sql string) int64 {
	t.Helper()
	rows := runQuery(t, ctx, sql)
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("scalarInt(%q): want 1x1 result, got %d rows", sql, len(rows))
	}
	if rows[0][0].Kind != KindInt {
		t.Fatalf("scalarInt(%q): want KindInt, got kind %d", sql, rows[0][0].Kind)
	}
	return rows[0][0].Int
}
