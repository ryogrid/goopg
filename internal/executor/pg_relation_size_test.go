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
