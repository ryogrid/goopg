package executor

// ctid_function_arg_test.go — M0134-0073: `ctid` is lost when used as a
// FUNCTION ARGUMENT. evalExprSlot's FuncCall case reduced the scan slot to a
// bare Row via slotToRow (slot.go:210-239), dropping the ctid side-channel, so
// CTIDExpr inside a function argument fell through to NullDatum:
//
//	length(ctid::text)                    → NULL   (should be 5)
//	substr(ctid::text, 2, 3)              → NULL   (should be '0,1')
//	substring(ctid::text FROM ',(\\d+)\\)') → NULL (should be '1')
//	DELETE ... WHERE substring(ctid::text FROM ',(\d+)\)')::integer > 2
//	                                     → deletes 0 rows (PG deletes all but two)
//
// PG oracle: system columns resolve from the scan slot via ExecEvalSysVar →
// slot_getsysattr(slot, SelfItemPointerAttributeNumber), so the slot must stay
// alive through argument evaluation. goopg now threads SlotView through
// evalFuncCall and its function-handler family (expr.go:1186 passes `slot`
// directly; handlers re-evaluate args via evalExprSlot).

import (
	"testing"
)

// TestCTIDFunctionArgEval pins the bug fix end to end: a fixture table, a
// handful of rows all on block 0 (offsets 1..N), then the three failing
// function-argument forms assert non-NULL ctid-derived values, and a DELETE
// with the tidrangescan substring-predicate prunes to the expected surviving
// count. Before the fix every assertion here returned NULL / deleted 0 rows.
func TestCTIDFunctionArgEval(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (id int, data text)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	// Five small rows all land on block 0 at offsets 1..5 (single page).
	for i := 1; i <= 5; i++ {
		if err := runDDL(t, ctx, "INSERT INTO t VALUES ("+itoa(i)+", 'row"+itoa(i)+"')"); err != nil {
			t.Fatalf("INSERT row %d: %v", i, err)
		}
	}

	// ctid::text must resolve to "(0,N)" — the raw slot TID. This is the
	// baseline that every function-argument assertion below is derived from.
	ctids := runQuery(t, ctx, "SELECT ctid::text FROM t")
	if len(ctids) != 5 {
		t.Fatalf("SELECT ctid::text: %d rows, want 5", len(ctids))
	}
	wantCTID := []string{"(0,1)", "(0,2)", "(0,3)", "(0,4)", "(0,5)"}
	for i, r := range ctids {
		if r[0].IsNull() {
			t.Fatalf("ctid::text row %d = NULL (scan slot lost its TID?)", i)
		}
		if got := r[0].StringValue(); got != wantCTID[i] {
			t.Errorf("ctid::text row %d = %q, want %q", i, got, wantCTID[i])
		}
	}

	// length(ctid::text): "(0,1)" is 5 chars; before the fix this was NULL.
	rows := runQuery(t, ctx, "SELECT length(ctid::text) FROM t")
	if len(rows) != 5 {
		t.Fatalf("SELECT length(ctid::text): %d rows, want 5", len(rows))
	}
	for i, r := range rows {
		if r[0].IsNull() {
			t.Fatalf("length(ctid::text) row %d = NULL: ctid lost as function argument", i)
		}
		if r[0].Kind != KindInt || r[0].Int != 5 {
			t.Errorf("length(ctid::text) row %d = %#v, want int 5", i, r[0])
		}
	}

	// substr(ctid::text, 2, 3): chars 2..4 of "(0,1)" = "0,1".
	rows = runQuery(t, ctx, "SELECT substr(ctid::text, 2, 3) FROM t")
	if len(rows) != 5 {
		t.Fatalf("SELECT substr(ctid::text, 2, 3): %d rows, want 5", len(rows))
	}
	for i, r := range rows {
		if r[0].IsNull() {
			t.Fatalf("substr(ctid::text, 2, 3) row %d = NULL: ctid lost as function argument", i)
		}
		if got := r[0].StringValue(); got != wantCTID[i][1:4] {
			t.Errorf("substr(ctid::text, 2, 3) row %d = %q, want %q", i, got, wantCTID[i][1:4])
		}
	}

	// substring(ctid::text FROM ',(\d+)\)'): captures the offset digit. The
	// pattern is single-backslash (standard_conforming_strings=on keeps `\d`
	// literal in the string literal), exactly as tidrangescan.sql writes it.
	rows = runQuery(t, ctx, `SELECT substring(ctid::text FROM ',(\d+)\)') FROM t`)
	if len(rows) != 5 {
		t.Fatalf("SELECT substring(ctid::text FROM ...): %d rows, want 5", len(rows))
	}
	for i, r := range rows {
		if r[0].IsNull() {
			t.Fatalf("substring(ctid::text FROM ...) row %d = NULL: ctid lost as function argument", i)
		}
		if got := r[0].StringValue(); got != wantCTID[i][3:4] {
			t.Errorf("substring(ctid::text FROM ...) row %d = %q, want %q (offset of %s)", i, got, wantCTID[i][3:4], wantCTID[i])
		}
	}

	// DELETE with the tidrangescan substring-predicate: rows with offset > 2
	// (on any block) or block > 2 are removed. All five rows sit on block 0,
	// so offsets 3,4,5 die and offsets 1,2 survive — exactly the brief's
	// example where PG "deletes all but two".
	if err := runDDL(t, ctx, `DELETE FROM t WHERE substring(ctid::text FROM ',(\d+)\)')::integer > 2 OR substring(ctid::text FROM '\((\d+),')::integer > 2`); err != nil {
		t.Fatalf("DELETE with substring predicate: %v", err)
	}
	survivors := runQuery(t, ctx, "SELECT ctid::text FROM t")
	if len(survivors) != 2 {
		t.Fatalf("after DELETE: %d surviving rows, want 2 (offsets 1,2 remain)", len(survivors))
	}
	for i, r := range survivors {
		if got := r[0].StringValue(); got != wantCTID[i] {
			t.Errorf("survivor %d = %q, want %q", i, got, wantCTID[i])
		}
	}

	// No-regression probes: these paths already worked (BinaryOp/IsNullExpr
	// evaluate operands via evalExprSlot) and must stay unchanged.
	eqRows := runQuery(t, ctx, "SELECT ctid::text = '(0,1)'::text FROM t")
	if len(eqRows) != 2 {
		t.Fatalf("SELECT ctid::text = '(0,1)': %d rows, want 2", len(eqRows))
	}
	if eqRows[0][0].IsNull() || !eqRows[0][0].BoolValue() {
		t.Errorf("ctid::text = '(0,1)' on first survivor = %#v, want true", eqRows[0][0])
	}
	if eqRows[1][0].IsNull() || eqRows[1][0].BoolValue() {
		t.Errorf("ctid::text = '(0,1)' on second survivor = %#v, want false", eqRows[1][0])
	}

	byteaRows := runQuery(t, ctx, "SELECT ctid::text::bytea FROM t")
	if len(byteaRows) != 2 || byteaRows[0][0].IsNull() {
		t.Errorf("ctid::text::bytea should stay non-NULL, got %d rows (first %#v)", len(byteaRows), byteaRows[0][0])
	}

	concatRows := runQuery(t, ctx, "SELECT ctid::text || 'x' FROM t")
	if len(concatRows) != 2 || concatRows[0][0].IsNull() {
		t.Errorf("ctid::text || 'x' should stay non-NULL, got %d rows (first %#v)", len(concatRows), concatRows[0][0])
	} else if got := concatRows[0][0].StringValue(); got != "(0,1)x" {
		t.Errorf("ctid::text || 'x' first = %q, want \"(0,1)x\"", got)
	}

	isnullRows := runQuery(t, ctx, "SELECT ctid IS NULL FROM t")
	if len(isnullRows) != 2 {
		t.Fatalf("SELECT ctid IS NULL: %d rows, want 2", len(isnullRows))
	}
	for i, r := range isnullRows {
		if r[0].IsNull() || r[0].BoolValue() {
			t.Errorf("ctid IS NULL row %d = %#v, want false", i, r[0])
		}
	}
}
