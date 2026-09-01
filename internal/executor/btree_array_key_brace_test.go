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

	// A real nested literal is still out of scope for the v0 key encoding.
	if _, err := runSQLCtxErr(t, ctx, `SELECT count(*) FROM zbrace WHERE a = '{{1,2},{3,4}}'`); err == nil {
		t.Error("a genuinely multidimensional literal should still be refused")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "0A000" {
		t.Errorf("nested literal: err = %v, want *ExecError{Code: 0A000}", err)
	}
}
