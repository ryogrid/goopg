package executor

import "testing"

// TestArrayIndexQuotedBraceElement pins review/260831-2 EC-3: the
// multidimensional-array refusal in encodeArrayBTreeKey scanned the RAW
// literal for a `{`, ignoring quoting, so an ordinary 1-D text[] value whose
// element merely contains a brace (`{"a{b"}`) could not be probed —
// `0A000 btree v0 cannot index multidimensional array column "a"` — while PG
// 18.3 indexes it and the equality probe returns the row. A genuinely nested
// literal must still be refused.
func TestArrayIndexQuotedBraceElement(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE zbrace (a text[])`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE INDEX zbrace_idx ON zbrace (a)`); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	runSQL(t, ctx, `INSERT INTO zbrace VALUES ('{"a{b"}'), ('{x}')`)

	rows, err := runSQLCtxErr(t, ctx, `SELECT count(*) FROM zbrace WHERE a = '{"a{b"}'`)
	if err != nil {
		t.Fatalf("equality probe on a quoted-brace element: %v", err)
	}
	if len(rows) != 1 || rows[0][0].Format() != "1" {
		t.Errorf("count = %v, want 1 (PG 18.3 returns 1)", rows)
	}

	// A real nested (2-D) literal: PG 18.3 accepts it as a 2-D text[] and
	// array_eq finds no 1-D row equal to it — count 0, no error. Since the
	// unknown literal is cast to the column's array type (M0149-0046k) the
	// one-row table is not probed through the index at all; the previous
	// assertion pinned goopg's 0A000 refusal, not PG's answer.
	rows, err = runSQLCtxErr(t, ctx, `SELECT count(*) FROM zbrace WHERE a = '{{1,2},{3,4}}'`)
	if err != nil {
		t.Fatalf("nested literal: %v (PG 18.3 returns count 0)", err)
	}
	if len(rows) != 1 || rows[0][0].Format() != "0" {
		t.Errorf("nested literal count = %v, want 0 (PG 18.3)", rows)
	}
}
