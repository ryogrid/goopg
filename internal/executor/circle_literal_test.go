package executor

import "testing"

// TestCircleLiteralParseValidation pins parseCircleLiteral (M0134-0098)
// against the circle_in accept/reject verdicts exercised by
// postgres/src/test/regress/sql/circle.sql's CIRCLE_TBL fixture: the
// canonical "<(x,y),r>" form, an un-bracketed "(x,y),r" form (with optional
// doubled wrapper paren), and whitespace-padded variants are all accepted;
// zero radius is valid, NaN radius is valid (NaN < 0 is false), and each of
// PG's five documented "bad values" is rejected.
func TestCircleLiteralParseValidation(t *testing.T) {
	cases := []struct {
		in         string
		wantOK     bool
		x, y, r    float64
		wantNaNRad bool
	}{
		{"<(5,1),3>", true, 5, 1, 3, false},
		{"((1,2),100)", true, 1, 2, 100, false},
		{" 1 , 3 , 5 ", true, 1, 3, 5, false},
		{" ( ( 1 , 2 ) , 3 ) ", true, 1, 2, 3, false},
		{" ( 100 , 200 ) , 10 ", true, 100, 200, 10, false},
		{" < ( 100 , 1 ) , 115 > ", true, 100, 1, 115, false},
		{"<(3,5),0>", true, 3, 5, 0, false},
		{"<(3,5),NaN>", true, 3, 5, 0, true},
		// PG's five documented "bad values".
		{"<(-100,0),-100>", false, 0, 0, 0, false},  // negative radius
		{"<(100,200),10", false, 0, 0, 0, false},    // unterminated
		{"<(100,200),10> x", false, 0, 0, 0, false}, // trailing garbage
		{"1abc,3,5", false, 0, 0, 0, false},         // not a number
		{"(3,(1,2),3)", false, 0, 0, 0, false},      // malformed pair
	}
	for _, c := range cases {
		x, y, r, ok := parseCircleLiteral(c.in)
		if ok != c.wantOK {
			t.Errorf("parseCircleLiteral(%q) ok = %v, want %v", c.in, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if c.wantNaNRad {
			if r == r { // r==r is false only for NaN
				t.Errorf("parseCircleLiteral(%q) radius = %v, want NaN", c.in, r)
			}
			continue
		}
		if x != c.x || y != c.y || r != c.r {
			t.Errorf("parseCircleLiteral(%q) = (%v,%v,%v), want (%v,%v,%v)",
				c.in, x, y, r, c.x, c.y, c.r)
		}
	}
}

// TestCircleColumnCoercionCanonicalizes pins the coerceTextLikeDatum circle
// arm: an INSERT of a valid-but-not-canonical circle literal into a
// circle(n) column is stored in circle_out's canonical "<(x,y),r>" form, and
// a malformed literal is rejected with PG's exact SQLSTATE instead of being
// stored as raw, unvalidated text (the previous behavior — circle was a
// raw-varlena pass-through with zero validation).
func TestCircleColumnCoercionCanonicalizes(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE circle_tbl(f1 circle)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO circle_tbl VALUES ('((1,2),100)')"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO circle_tbl VALUES (' < ( 100 , 1 ) , 115 > ')"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	rows := runDMLRows(t, ctx, "SELECT f1 FROM circle_tbl ORDER BY f1::text")
	want := []string{"<(1,2),100>", "<(100,1),115>"}
	if len(rows) != len(want) {
		t.Fatalf("SELECT f1 FROM circle_tbl = %d rows, want %d", len(rows), len(want))
	}
	for i, w := range want {
		if got := rows[i][0].StringValue(); got != w {
			t.Errorf("row %d = %q, want %q", i, got, w)
		}
	}

	err := runDDL(t, ctx, "INSERT INTO circle_tbl VALUES ('<(100,200),10')")
	if err == nil {
		t.Fatalf("INSERT of malformed circle literal did not error")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "22P02" {
		t.Fatalf("INSERT malformed circle error = %v, want SQLSTATE 22P02", err)
	}
}
