package executor

import (
	"testing"
)

// TestIndexAMHasPropertyFunctions verifies pg_indexam_has_property,
// pg_index_has_property and pg_index_column_has_property
// (postgres/src/backend/utils/adt/amutils.c) — previously entirely
// unimplemented ("function ... does not exist", every case in amutils.sql).
// Assertions mirror the exact values captured from a live PG 18.3 oracle run
// of amutils.sql (M0134-0090): btree onek_hundred, hash hash_i4_index, gin
// botharrayidx.
func TestIndexAMHasPropertyFunctions(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, `CREATE TABLE amp_t (a int4, b int4, c int4)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE INDEX amp_btree ON amp_t USING btree (a)`); err != nil {
		t.Fatalf("CREATE INDEX btree: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE INDEX amp_hash ON amp_t USING hash (b)`); err != nil {
		t.Fatalf("CREATE INDEX hash: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE INDEX amp_desc ON amp_t (a desc, b asc nulls first)`); err != nil {
		t.Fatalf("CREATE INDEX (a desc, b asc nulls first): %v", err)
	}

	scalarBool := func(sql string) Datum {
		rows := runQueryRows(t, ctx, sql)
		if len(rows) != 1 || len(rows[0]) != 1 {
			t.Fatalf("query %q: want 1x1 result, got %v", sql, rows)
		}
		return rows[0][0]
	}
	wantBool := func(sql string, want bool) {
		t.Helper()
		d := scalarBool(sql)
		if d.IsNull() || d.Kind != KindBool || d.BoolValue() != want {
			t.Errorf("%s = %v, want %v", sql, d, want)
		}
	}
	wantNull := func(sql string) {
		t.Helper()
		d := scalarBool(sql)
		if !d.IsNull() {
			t.Errorf("%s = %v, want NULL", sql, d)
		}
	}

	// pg_indexam_has_property: AM-wide capability flags, btree vs hash.
	// 403/405 are pg_am.oid for the btree/hash AMs (catalog.AccessMethodOIDByName).
	wantBool(`SELECT pg_indexam_has_property(403, 'can_order')`, true)
	wantBool(`SELECT pg_indexam_has_property(403, 'can_unique')`, true)
	wantBool(`SELECT pg_indexam_has_property(405, 'can_order')`, false)
	wantBool(`SELECT pg_indexam_has_property(405, 'can_exclude')`, true)
	wantNull(`SELECT pg_indexam_has_property(403, 'bogus')`)

	// pg_index_has_property: index-wide, btree vs hash.
	wantBool(`SELECT pg_index_has_property('amp_btree'::regclass, 'clusterable')`, true)
	wantBool(`SELECT pg_index_has_property('amp_hash'::regclass, 'clusterable')`, false)
	wantBool(`SELECT pg_index_has_property('amp_hash'::regclass, 'backward_scan')`, true)

	// pg_index_column_has_property: per-column indoption bits + AM flags.
	wantBool(`SELECT pg_index_column_has_property('amp_desc'::regclass, 1, 'desc')`, true)
	// DESC defaults to NULLS FIRST (no explicit NULLS clause given for column
	// "a"), mirroring ruleutils.c's pg_get_indexdef_worker convention.
	wantBool(`SELECT pg_index_column_has_property('amp_desc'::regclass, 1, 'nulls_first')`, true)
	wantBool(`SELECT pg_index_column_has_property('amp_desc'::regclass, 2, 'asc')`, true)
	wantBool(`SELECT pg_index_column_has_property('amp_desc'::regclass, 2, 'nulls_first')`, true)
	wantBool(`SELECT pg_index_column_has_property('amp_hash'::regclass, 1, 'orderable')`, false)
	wantNull(`SELECT pg_index_column_has_property('amp_btree'::regclass, 1, 'bogus')`)
	wantNull(`SELECT pg_index_column_has_property('amp_btree'::regclass, 0, 'asc')`) // attno 0 rejected
}
