package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestBuildUserPGAttributeRowsForIndexMatchesPG pins AI-20260810-011258-003
// blocker #11: an index relation needs its OWN pg_attribute rows.
//
// goopg never wrote them because its own catalog reads catalog.Index directly,
// but PG rebuilds an index's TupleDesc from pg_attribute exactly like a table's
// (RelationBuildTupleDesc), and errors with
//
//	pg_attribute catalog is missing N attribute(s) for relation OID <index>
//
// the first time it opens a goopg-created index. The per-attribute values must
// follow upstream ConstructTupleDescriptor (src/backend/catalog/index.c): the
// heap column's physical type description is copied verbatim while every
// relation-level flag is reset, because an index attribute carries none of the
// heap column's constraints.
func TestBuildUserPGAttributeRowsForIndexMatchesPG(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE ai_t (id integer NOT NULL, val text NOT NULL DEFAULT 'x', pay integer)`); err != nil {
		t.Fatalf("CREATE TABLE ai_t: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE INDEX ai_t_val_idx ON ai_t (val) INCLUDE (pay)`); err != nil {
		t.Fatalf("CREATE INDEX ai_t_val_idx: %v", err)
	}
	idx, ok := cat.LookupIndex(parser.ObjectName{Name: "ai_t_val_idx"})
	if !ok {
		t.Fatal("ai_t_val_idx not found in catalog")
	}

	rows, names := buildUserPGAttributeRowsForIndex(cat, idx)
	// One row per index attribute: the key column, then the INCLUDE column.
	if len(rows) != 2 || len(names) != 2 {
		t.Fatalf("rows=%d names=%v, want 2 attributes (val, pay)", len(rows), names)
	}
	if names[0] != "val" || names[1] != "pay" {
		t.Fatalf("attnames = %v, want [val pay]", names)
	}

	col := func(row Row, name string) Datum {
		t.Helper()
		for i, c := range pgAttributeColumnsPG18() {
			if c.Name == name {
				return row[i]
			}
		}
		t.Fatalf("pg_attribute has no column %q", name)
		return NullDatum
	}

	for i, row := range rows {
		if len(row) != len(pgAttributeColumnsPG18()) {
			t.Fatalf("attr %d: row has %d datums, want %d", i, len(row), len(pgAttributeColumnsPG18()))
		}
		// attrelid is the INDEX's oid, not the heap's — this is the field that
		// makes RelationBuildTupleDesc find the rows at all.
		if got := col(row, "attrelid").Int; got != int64(idx.OID) {
			t.Errorf("attr %d: attrelid = %d, want index oid %d", i, got, idx.OID)
		}
		if got := col(row, "attnum").Int; got != int64(i+1) {
			t.Errorf("attr %d: attnum = %d, want %d", i, got, i+1)
		}
		// ConstructTupleDescriptor resets every relation-level flag.
		if col(row, "attnotnull").BoolValue() {
			t.Errorf("attr %d: attnotnull = true, want false (index attrs carry no constraints)", i)
		}
		if col(row, "atthasdef").BoolValue() {
			t.Errorf("attr %d: atthasdef = true, want false", i)
		}
		if !col(row, "attislocal").BoolValue() {
			t.Errorf("attr %d: attislocal = false, want true", i)
		}
		if got := col(row, "attinhcount").Int; got != 0 {
			t.Errorf("attr %d: attinhcount = %d, want 0", i, got)
		}
		if col(row, "attisdropped").BoolValue() {
			t.Errorf("attr %d: attisdropped = true, want false", i)
		}
	}

	// The physical type description is copied from the heap column: `val` is
	// text (varlena, typlen -1), `pay` is integer (typlen 4, by-value). A
	// mismatch here would make PG deform index tuples with the wrong widths.
	if got := col(rows[0], "atttypid").Int; got != int64(catalog.OIDText) {
		t.Errorf("val atttypid = %d, want text (%d)", got, catalog.OIDText)
	}
	if got := col(rows[0], "attlen").Int; got != -1 {
		t.Errorf("val attlen = %d, want -1 (varlena)", got)
	}
	if got := col(rows[1], "atttypid").Int; got != int64(catalog.OIDInt4) {
		t.Errorf("pay atttypid = %d, want int4 (%d)", got, catalog.OIDInt4)
	}
	if got := col(rows[1], "attlen").Int; got != 4 {
		t.Errorf("pay attlen = %d, want 4", got)
	}
	if !col(rows[1], "attbyval").BoolValue() {
		t.Error("pay attbyval = false, want true (int4 is pass-by-value)")
	}
}

// TestBuildUserPGAttributeRowsForIndexNamesExpressionKeys pins the
// expression-key naming half of the same blocker: upstream
// ConstructTupleDescriptor names an expressional index column
// `pg_expression_<attnum>` (there is no heap column to take a name from).
// The attribute's TYPE is the documented `text` fallback — goopg has no
// expression type resolver reachable from the row builder — which is why the
// deferral ledger carries a row for expression-index type fidelity.
func TestBuildUserPGAttributeRowsForIndexNamesExpressionKeys(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE aix_t (id integer, val text)`); err != nil {
		t.Fatalf("CREATE TABLE aix_t: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE INDEX aix_t_lower_idx ON aix_t (lower(val))`); err != nil {
		t.Fatalf("CREATE INDEX aix_t_lower_idx: %v", err)
	}
	idx, ok := cat.LookupIndex(parser.ObjectName{Name: "aix_t_lower_idx"})
	if !ok {
		t.Fatal("aix_t_lower_idx not found in catalog")
	}
	rows, names := buildUserPGAttributeRowsForIndex(cat, idx)
	if len(rows) != 1 || len(names) != 1 {
		t.Fatalf("rows=%d names=%v, want 1 expression attribute", len(rows), names)
	}
	if names[0] != "pg_expression_1" {
		t.Errorf("expression attname = %q, want pg_expression_1", names[0])
	}
}
