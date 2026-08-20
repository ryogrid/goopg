package executor

import "testing"

// TestOrdinalityCompositeSRFSelectStar pins the composite/multi-column
// SETOF-function + WITH ORDINALITY slot-width bug from
// postgres/src/test/regress/sql/rangefuncs.sql (the rngfunct fixture,
// M0134-0059). Before the fix, evalSQLFunctionSetof
// (internal/executor/plpgsql_runtime.go) collapsed every returned row of a
// `RETURNS SETOF <composite-table-type>` SQL-language function down to
// row[0], silently dropping all columns past the first. userSrfScanOp.Next
// then saw a single-column (KindInt) datum instead of the expected
// composite text, so `WITH ORDINALITY` — which appends its ordinal column
// on top of the (mis-sized) child schema — produced a 2-wide slot even
// though the planner's schema (a, b, ord) expected 3, and any reference to
// the ordinality column raised "column ref ord/2 out of Slot range 2"
// (internal/executor/expr.go). A companion WHERE-filtered query silently
// returned 0 rows instead of erroring, for the same root cause.
//
// PG oracle: postgres/src/backend/executor/nodeFunctionscan.c
// ExecInitFunctionScan appends the ordinality column after the function's
// own (possibly multi-column/composite) output columns.
func TestOrdinalityCompositeSRFSelectStar(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, ddl := range []string{
		`CREATE TABLE rngfunc2(rngfuncid int, f2 int)`,
		`INSERT INTO rngfunc2 VALUES(1, 11)`,
		`INSERT INTO rngfunc2 VALUES(2, 22)`,
		`INSERT INTO rngfunc2 VALUES(1, 111)`,
		`CREATE FUNCTION rngfunct(int) returns setof rngfunc2 as 'SELECT * FROM rngfunc2 WHERE rngfuncid = $1 ORDER BY f2;' LANGUAGE SQL`,
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("DDL %q: %v", ddl, err)
		}
	}

	// datumStr formats a,b (returned as composite-decomposed KindString
	// datums — the established convention for OUT-param/composite SETOF
	// decomposition, see decomposeCompositeText) and ord (a genuine KindInt
	// ordinality counter) uniformly for comparison.
	datumStr := func(d Datum) string {
		if d.Kind == KindString {
			return d.StringValue()
		}
		return d.Format()
	}

	// select * from rngfunct(1) with ordinality as z(a,b,ord);
	rows := runQuery(t, ctx, `select * from rngfunct(1) with ordinality as z(a,b,ord)`)
	wantRows := [][3]string{{"1", "11", "1"}, {"1", "111", "2"}}
	if len(rows) != len(wantRows) {
		t.Fatalf("select *: got %d rows %v, want %d rows", len(rows), rows, len(wantRows))
	}
	for i, want := range wantRows {
		got := rows[i]
		if len(got) != 3 {
			t.Fatalf("row %d: got %d columns %v, want 3", i, len(got), got)
		}
		gotStrs := [3]string{datumStr(got[0]), datumStr(got[1]), datumStr(got[2])}
		if gotStrs != want {
			t.Fatalf("row %d: got (a=%s,b=%s,ord=%s), want (a=%s,b=%s,ord=%s)",
				i, gotStrs[0], gotStrs[1], gotStrs[2], want[0], want[1], want[2])
		}
	}

	// select * from rngfunct(1) with ordinality as z(a,b,ord) where b > 100;
	// -- ordinal 2, not 1 (matches rangefuncs.sql's own comment).
	filtered := runQuery(t, ctx, `select * from rngfunct(1) with ordinality as z(a,b,ord) where b > 100`)
	if len(filtered) != 1 {
		t.Fatalf("WHERE-filtered: got %d rows %v, want 1", len(filtered), filtered)
	}
	gotFiltered := [3]string{datumStr(filtered[0][0]), datumStr(filtered[0][1]), datumStr(filtered[0][2])}
	wantFiltered := [3]string{"1", "111", "2"}
	if gotFiltered != wantFiltered {
		t.Fatalf("WHERE-filtered row: got %v, want (a=1,b=111,ord=2)", gotFiltered)
	}
}
