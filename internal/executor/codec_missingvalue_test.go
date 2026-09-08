package executor

import (
	"fmt"
	"testing"
)

// TestDecodeMissingValueAfterAddColumn covers the `i >= storedNatts` arm of
// decodeRowRangeInfo — the path that reads catalog.Column.MissingValue.
//
// Why this test exists: part 4 changed `c := cols[i]` from a 312-byte struct
// COPY to a pointer, and MissingValue is the only field read on that arm.
// Neither TPC-H nor TPC-DS exercises it at all — every row in those corpora
// was written with the full column count — so the corpus gates are blind to a
// regression here however green they look.
//
// The shape: ALTER TABLE ... ADD COLUMN ... DEFAULT writes no new heap rows,
// so pre-existing tuples have fewer stored attributes than the descriptor has
// columns. Reading them back must synthesise the default rather than NULL.
func TestDecodeMissingValueAfterAddColumn(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE mv_t (id int, a text)`,
		`INSERT INTO mv_t VALUES (1, 'one')`,
		`INSERT INTO mv_t VALUES (2, 'two')`,
		`INSERT INTO mv_t VALUES (3, 'three')`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	commitTx(t, ctx)
	beginTx(t, ctx)

	// Rows already on disk have storedNatts == 2; the descriptor now has 3.
	if err := runDDL(t, ctx, `ALTER TABLE mv_t ADD COLUMN b int DEFAULT 42`); err != nil {
		t.Fatalf("ADD COLUMN: %v", err)
	}
	commitTx(t, ctx)
	beginTx(t, ctx)

	rows := runQuery(t, ctx, `SELECT id, a, b FROM mv_t ORDER BY id`)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	for i, r := range rows {
		if len(r) != 3 {
			t.Fatalf("row %d has %d columns, want 3", i, len(r))
		}
		if r[2].IsNull() {
			t.Errorf("row %d: column b is NULL, want the DEFAULT 42 "+
				"(the missing-value path returned nothing)", i)
		} else if got := r[2].Int; got != 42 {
			t.Errorf("row %d: column b = %d, want 42", i, got)
		}
	}
	t.Logf("missing-value path: %d pre-ALTER rows all read back b=42", len(rows))

	// A row written AFTER the ALTER stores all three attributes, so it takes
	// the ordinary path; both must agree.
	if err := runDDL(t, ctx, fmt.Sprintf(`INSERT INTO mv_t VALUES (4, 'four', %d)`, 42)); err != nil {
		t.Fatalf("post-ALTER insert: %v", err)
	}
	rows = runQuery(t, ctx, `SELECT b FROM mv_t ORDER BY id`)
	for i, r := range rows {
		if r[0].IsNull() || r[0].Int != 42 {
			t.Errorf("row %d: b=%v, want 42 (stored and missing-value paths disagree)", i, r[0])
		}
	}
}
