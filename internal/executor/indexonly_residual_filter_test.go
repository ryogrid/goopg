package executor

import "testing"

// TestIndexOnlyScanResidualFilterColumnRemap is a regression test for
// M0121-0002: a residual Filter surviving IndexOnlyScan promotion
// (tryPromoteIndexOnlyScan, internal/planner/planner.go) kept its
// predicate's ColumnRef.Index pinned to the pre-promotion (full-row)
// schema position. Once the scan narrowed to the covered columns, the
// stale index panicked `Slot.Get` (opnode.go:99) from evalFastExpr's
// ExprColumnRef case via filterOpNext — this crashed the goopg backend
// connection whenever WordPress's wp_set_object_terms ran
// `SELECT term_taxonomy_id FROM wp_term_relationships WHERE object_id = ?
// AND term_taxonomy_id = ?` over the table's composite PK
// (object_id, term_taxonomy_id) and object_id actually matched a row.
//
// Reproduces the same shape with a minimal 3-column table: a composite
// PK (a, b) probed by its leading column (a), leaving a residual filter
// on b — the covered/projected column — after IndexOnlyScan promotion
// narrows the scan's output to just b.
func TestIndexOnlyScanResidualFilterColumnRemap(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, `CREATE TABLE ios_remap_t (a bigint NOT NULL, b bigint NOT NULL, c integer NOT NULL, PRIMARY KEY (a, b))`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	runSQL(t, ctx, `INSERT INTO ios_remap_t VALUES (11, 1, 0)`)
	runSQL(t, ctx, `INSERT INTO ios_remap_t VALUES (22, 2, 0)`)

	// object_id (a) matches an existing row, so the scan actually yields a
	// row and filterOpNext must evaluate the residual `b = 1` predicate
	// against it — this is exactly what panicked pre-fix.
	rows := runSQL(t, ctx, `SELECT b FROM ios_remap_t WHERE a = 11 AND b = 1`)
	if len(rows) != 1 || rows[0][0].Int != 1 {
		t.Fatalf("SELECT b WHERE a=11 AND b=1: want [[1]], got %v", rows)
	}

	// a matches but b doesn't: the residual filter must correctly reject
	// the row (proves the remapped ColumnRef reads the right column,
	// rather than merely avoiding a panic).
	rows = runSQL(t, ctx, `SELECT b FROM ios_remap_t WHERE a = 11 AND b = 2`)
	if len(rows) != 0 {
		t.Fatalf("SELECT b WHERE a=11 AND b=2: want 0 rows, got %v", rows)
	}

	// Sanity check the other row still resolves independently.
	rows = runSQL(t, ctx, `SELECT b FROM ios_remap_t WHERE a = 22 AND b = 2`)
	if len(rows) != 1 || rows[0][0].Int != 2 {
		t.Fatalf("SELECT b WHERE a=22 AND b=2: want [[2]], got %v", rows)
	}
}
