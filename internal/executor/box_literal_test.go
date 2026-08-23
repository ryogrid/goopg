package executor

import "testing"

// TestBoxLiteralParseValidation pins parseBoxLiteral (M0134-0094) against the
// box_in accept/reject verdicts and corner-reordering exercised by
// postgres/src/test/regress/sql/box.sql's BOX_TBL fixture: well-formed
// two-point forms (with or without an outer wrapper paren, with or without
// per-point parens) parse and normalize to (high, low); each of PG's five
// "badly formatted" box inputs is rejected.
func TestBoxLiteralParseValidation(t *testing.T) {
	cases := []struct {
		in             string
		wantOK         bool
		hx, hy, lx, ly float64
	}{
		// Well-formed, no wrapper paren: "x1,y1,x2,y2" wrapped in one pair
		// of parens — box.sql's canonical INSERT shape.
		{"(2.0,2.0,0.0,0.0)", true, 2, 2, 0, 0},
		{"(1.0,1.0,3.0,3.0)", true, 3, 3, 1, 1},
		// Well-formed, doubled wrapper paren + per-point parens, and the
		// two points given out of (high,low) order — box_in reorders per
		// axis, it does not just swap the two points.
		{"((-8, 2), (-2, -10))", true, -2, 2, -8, -10},
		// Degenerate (zero-area) boxes are still valid.
		{"(2.5, 2.5, 2.5,3.5)", true, 2.5, 3.5, 2.5, 2.5},
		{"(3.0, 3.0,3.0,3.0)", true, 3, 3, 3, 3},
		// PG's five documented "badly formatted" rejects.
		{"(2.3, 4.5)", false, 0, 0, 0, 0},     // only one coordinate pair
		{"[1, 2, 3, 4)", false, 0, 0, 0, 0},   // '[' is not a valid delimiter
		{"(1, 2, 3, 4]", false, 0, 0, 0, 0},   // mismatched closing delimiter
		{"(1, 2, 3, 4) x", false, 0, 0, 0, 0}, // trailing garbage
		{"asdfasdf(ad", false, 0, 0, 0, 0},    // not a number at all
	}
	for _, c := range cases {
		hx, hy, lx, ly, ok := parseBoxLiteral(c.in)
		if ok != c.wantOK {
			t.Errorf("parseBoxLiteral(%q) ok = %v, want %v", c.in, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if hx != c.hx || hy != c.hy || lx != c.lx || ly != c.ly {
			t.Errorf("parseBoxLiteral(%q) = (%v,%v,%v,%v), want (%v,%v,%v,%v)",
				c.in, hx, hy, lx, ly, c.hx, c.hy, c.lx, c.ly)
		}
	}
}

// TestBoxColumnCoercionCanonicalizes pins the coerceTextLikeDatum box arm: an
// INSERT of a valid-but-not-canonical box literal into a box(n) column is
// stored in box_out's canonical "(hx,hy),(lx,ly)" form, and a malformed
// literal is rejected with PG's exact SQLSTATE/message instead of being
// stored as raw, unvalidated text (the previous behavior — box was a
// raw-varlena pass-through with zero validation).
func TestBoxColumnCoercionCanonicalizes(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE box_tbl(f1 box)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO box_tbl VALUES ('(2.0,2.0,0.0,0.0)')"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO box_tbl VALUES ('((-8, 2), (-2, -10))')"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	rows := runDMLRows(t, ctx, "SELECT f1 FROM box_tbl ORDER BY f1::text DESC")
	want := []string{"(2,2),(0,0)", "(-2,2),(-8,-10)"}
	if len(rows) != len(want) {
		t.Fatalf("SELECT f1 FROM box_tbl = %d rows, want %d", len(rows), len(want))
	}
	for i, w := range want {
		if got := rows[i][0].StringValue(); got != w {
			t.Errorf("row %d = %q, want %q", i, got, w)
		}
	}

	err := runDDL(t, ctx, "INSERT INTO box_tbl VALUES ('(2.3, 4.5)')")
	if err == nil {
		t.Fatalf("INSERT of malformed box literal did not error")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "22P02" {
		t.Fatalf("INSERT malformed box error = %v, want SQLSTATE 22P02", err)
	}
}
