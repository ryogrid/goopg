package executor

import "testing"

// Tests for the comma-join / explicit-LATERAL correlation of a FROM-clause
// SRF's argument to a preceding sibling FROM item, e.g.
//
//	FROM tbl, unnest(tbl.arr_col) AS t(m)
//	FROM tbl, LATERAL unnest(tbl.arr_col) AS t(m)
//	FROM tbl, regexp_matches(tbl.col, ...) AS t(m)
//
// Root cause (deferral ledger 2026-07-04, M0122-0002 FROM-clause follow-up):
// planFromUnnest/planFromRegexpMatches resolve a correlated argument against
// the left sibling's schema as a plain *ColumnRef (fromUnnestOp.Open and
// fromRegexpMatchesOp.Open both read ctx.OuterRows directly — the same
// per-outer-row driver joinOp.openLateral uses for the general case), but
// nodeReferencesOuter's switch — which decides whether the wrapping Join
// routes through openLateral at all — had no case for either node type, so
// it fell through to the OuterColumnRef-only general case and never
// detected the correlation. The join then took the "materialise both
// sides once" fast path, ctx.OuterRows stayed empty, and the SRF's arg
// evaluated against a nil row: "XX000: column ref arr/1 on nil slot".
func TestFromSRFLateralCorrelation_UnnestCommaJoin(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE zz_lat (id int, arr int[])`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO zz_lat VALUES (1, ARRAY[10,20]), (2, ARRAY[30])`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	rows := runQuery(t, ctx, `SELECT id, m FROM zz_lat, unnest(zz_lat.arr) AS t(m) ORDER BY id, m`)
	if len(rows) != 3 {
		t.Fatalf("comma-join unnest: got %d rows, want 3 (rows=%v)", len(rows), rows)
	}

	rows2 := runQuery(t, ctx, `SELECT id, m FROM zz_lat, LATERAL unnest(zz_lat.arr) AS t(m) ORDER BY id, m`)
	if len(rows2) != 3 {
		t.Fatalf("explicit LATERAL unnest: got %d rows, want 3 (rows=%v)", len(rows2), rows2)
	}
}

func TestFromSRFLateralCorrelation_RegexpMatchesCommaJoin(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE zz_lat2 (id int, s text)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO zz_lat2 VALUES (1, 'foobarbequebaz')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	rows := runQuery(t, ctx, `SELECT id, m FROM zz_lat2, regexp_matches(zz_lat2.s, '(bar)(beque)') AS t(m)`)
	if len(rows) != 1 {
		t.Fatalf("comma-join regexp_matches: got %d rows, want 1 (rows=%v)", len(rows), rows)
	}
}

// WITH ORDINALITY wraps the FromUnnest/FromRegexpMatches node in an
// OrdinalityWrap; nodeReferencesOuter must unwrap it to still detect the
// correlated argument underneath.
func TestFromSRFLateralCorrelation_UnnestWithOrdinality(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE zz_lat3 (id int, arr int[])`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO zz_lat3 VALUES (1, ARRAY[10,20])`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	rows := runQuery(t, ctx, `SELECT * FROM zz_lat3, unnest(zz_lat3.arr) WITH ORDINALITY AS t(m, n) ORDER BY n`)
	if len(rows) != 2 {
		t.Fatalf("comma-join unnest WITH ORDINALITY: got %d rows, want 2 (rows=%v)", len(rows), rows)
	}
}
