package executor

import "testing"

// TestLeftJoinInnerOnlyInPushdown is the executor-level regression for the
// M0097-0023 virtual-table LEFT JOIN crash.
//
// The planner pushes an inner-only ON conjunct of a LEFT JOIN to a Filter on
// the inner plan and rebases its ColumnRef indices by -leftWidth. The side
// classifier recurses into the literal-list form of `IN`, so a conjunct like
// `inner_t.kind IN ('p','u','x')` is correctly classified inner-only and
// pushed down. Before the fix, shiftColumnRefsBy had no InExpr case, so the
// pushed-down `kind` ref kept its outer-cumulative index (leftWidth+ordinal).
// At execution the inner Filter row is only as wide as inner_t, so the
// unshifted index addressed one past the end and Slot.Get panicked
// "index out of range".
//
// This is the same shape psql `\d` uses against the system catalogs
// (pg_index LEFT JOIN pg_constraint ON ... AND con.contype IN ('p','u','x')),
// which is why populating pg_constraint formerly crashed \d. Using plain user
// tables here keeps the regression deterministic and free of catalog-stub
// confounds while exercising the identical pushdown+shift code path.
func TestLeftJoinInnerOnlyInPushdown(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, ddl := range []string{
		"CREATE TABLE outer_t (id int)",
		// inner_t has 2 columns so `kind` sits at inner-ordinal 1; the merged
		// outer index is leftWidth(1)+1 = 2, equal to the inner row width — the
		// exact off-by-end that panicked.
		"CREATE TABLE inner_t (oid int, kind text)",
		"INSERT INTO outer_t VALUES (1), (2), (3)",
		"INSERT INTO inner_t VALUES (1, 'p'), (2, 'z'), (3, 'x')",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("DDL %q: %v", ddl, err)
		}
	}

	// LEFT JOIN with an equi-join key plus an inner-only IN conjunct. The IN
	// must be pushed to a Filter on inner_t. Outer rows with no surviving
	// inner match keep NULLs (LEFT JOIN semantics): id=2's inner row has
	// kind='z' which fails the IN, so its join partner is dropped.
	const q = `SELECT outer_t.id, inner_t.kind
	             FROM outer_t
	             LEFT JOIN inner_t
	               ON inner_t.oid = outer_t.id
	              AND inner_t.kind IN ('p', 'u', 'x')
	            ORDER BY outer_t.id`

	rows := runQuery(t, ctx, q) // panicked here before the fix
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}

	// Expected: (1,'p'), (2,NULL), (3,'x').
	wantKind := []struct {
		id   int64
		null bool
		kind string
	}{
		{1, false, "p"},
		{2, true, ""},
		{3, false, "x"},
	}
	for i, w := range wantKind {
		if got := rows[i][0].Int; got != w.id {
			t.Errorf("row %d: id = %d, want %d", i, got, w.id)
		}
		gotNull := rows[i][1].Kind == KindNull
		if gotNull != w.null {
			t.Errorf("row %d: kind null = %v, want %v", i, gotNull, w.null)
		}
		if !w.null && rows[i][1].StringValue() != w.kind {
			t.Errorf("row %d: kind = %q, want %q", i, rows[i][1].StringValue(), w.kind)
		}
	}
}
