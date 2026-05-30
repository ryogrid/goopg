package executor

import (
	"testing"
)

// TestRowConstructorInSubquery verifies that (col1, col2) IN (SELECT x, y FROM ...)
// performs element-wise tuple comparison. M0097-0020.
func TestRowConstructorInSubquery(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE rci_tbl (f1 int, f2 int, f3 int)"); err != nil {
		t.Fatal(err)
	}
	for _, row := range []string{
		"INSERT INTO rci_tbl VALUES (1,2,3)",
		"INSERT INTO rci_tbl VALUES (2,3,4)",
		"INSERT INTO rci_tbl VALUES (3,4,5)",
		"INSERT INTO rci_tbl VALUES (1,1,1)",
		"INSERT INTO rci_tbl VALUES (2,2,2)",
		"INSERT INTO rci_tbl VALUES (3,3,3)",
		"INSERT INTO rci_tbl VALUES (6,7,8)",
		"INSERT INTO rci_tbl VALUES (8,9,0)",
	} {
		if err := runDDL(t, ctx, row); err != nil {
			t.Fatal(err)
		}
	}

	// (f1, f2) IN (SELECT f2, f3 FROM rci_tbl WHERE f3 != 0)
	// Right side (f2,f3): (2,3),(3,4),(4,5),(1,1),(2,2),(3,3),(7,8)
	// Matches: (2,3),(3,4),(1,1),(2,2),(3,3) → f1 in {1,2,2,3,3}
	rows := runQueryRows(t, ctx, "SELECT f1 FROM rci_tbl WHERE (f1, f2) IN (SELECT f2, f3 FROM rci_tbl WHERE f3 != 0) ORDER BY f1")
	wantF1 := []int64{1, 2, 2, 3, 3}
	if len(rows) != len(wantF1) {
		t.Fatalf("got %d rows, want %d; rows=%v", len(rows), len(wantF1), rows)
	}
	for i, r := range rows {
		if r[0].Int != wantF1[i] {
			t.Errorf("row %d: got f1=%d, want %d", i, r[0].Int, wantF1[i])
		}
	}
}

// TestRowConstructorNotInSubquery verifies (col1, col2) NOT IN (SELECT ...) semantics.
func TestRowConstructorNotInSubquery(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE rci_not (f1 int, f2 int, f3 int)"); err != nil {
		t.Fatal(err)
	}
	for _, row := range []string{
		"INSERT INTO rci_not VALUES (1,2,3)",
		"INSERT INTO rci_not VALUES (2,3,4)",
		"INSERT INTO rci_not VALUES (3,4,5)",
		"INSERT INTO rci_not VALUES (1,1,1)",
		"INSERT INTO rci_not VALUES (2,2,2)",
		"INSERT INTO rci_not VALUES (3,3,3)",
		"INSERT INTO rci_not VALUES (6,7,8)",
		"INSERT INTO rci_not VALUES (8,9,0)",
	} {
		if err := runDDL(t, ctx, row); err != nil {
			t.Fatal(err)
		}
	}

	// NOT IN: rows where (f1,f2) is not in right side
	// Right side (f2,f3 where f3!=0): (2,3),(3,4),(4,5),(1,1),(2,2),(3,3),(7,8)
	// Non-matching left tuples: (1,2),(6,7),(8,9)
	rows := runQueryRows(t, ctx, "SELECT f1, f2 FROM rci_not WHERE (f1, f2) NOT IN (SELECT f2, f3 FROM rci_not WHERE f3 != 0) ORDER BY f1, f2")
	wantPairs := [][2]int64{{1, 2}, {6, 7}, {8, 9}}
	if len(rows) != len(wantPairs) {
		t.Fatalf("got %d rows, want %d; rows=%v", len(rows), len(wantPairs), rows)
	}
	for i, r := range rows {
		if r[0].Int != wantPairs[i][0] || r[1].Int != wantPairs[i][1] {
			t.Errorf("row %d: got (%d,%d), want (%d,%d)", i, r[0].Int, r[1].Int, wantPairs[i][0], wantPairs[i][1])
		}
	}
}

// TestArrayUpperLowerLength verifies array_upper, array_lower, and array_length.
func TestArrayUpperLowerLength(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	tests := []struct {
		sql  string
		want int64
	}{
		{"SELECT array_upper(ARRAY[1,2,3], 1)", 3},
		{"SELECT array_upper(ARRAY[10], 1)", 1},
		{"SELECT array_lower(ARRAY[1,2,3], 1)", 1},
		{"SELECT array_lower(ARRAY[5], 1)", 1},
		{"SELECT array_length(ARRAY[1,2,3], 1)", 3},
		{"SELECT array_length(ARRAY[10,20], 1)", 2},
	}
	for _, tc := range tests {
		rows := runQueryRows(t, ctx, tc.sql)
		if len(rows) != 1 || len(rows[0]) != 1 {
			t.Errorf("%s: got %v rows, want 1 row with 1 col", tc.sql, rows)
			continue
		}
		got := rows[0][0].Int
		if got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.sql, got, tc.want)
		}
	}
}

// TestRowEqSubqueryConstant verifies ROW(a,b) = (SELECT x,y) constant comparison.
func TestRowEqSubqueryConstant(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE rsst (f1 int, f2 int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO rsst VALUES (1,2),(3,4)"); err != nil {
		t.Fatal(err)
	}

	// ROW(1,2) = (SELECT 3,4) → always FALSE (1≠3)
	rows := runQueryRows(t, ctx, "SELECT ROW(1,2) = (SELECT 3,4) AS eq FROM rsst ORDER BY f1")
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	for i, r := range rows {
		if r[0].Kind != KindBool || r[0].BoolValue() {
			t.Errorf("row %d: expected false, got %v", i, r[0])
		}
	}

	// ROW(1,2) = (SELECT 1,2) → always TRUE
	rows2 := runQueryRows(t, ctx, "SELECT ROW(1,2) = (SELECT 1,2) AS eq FROM rsst ORDER BY f1")
	if len(rows2) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows2))
	}
	for i, r := range rows2 {
		if r[0].Kind != KindBool || !r[0].BoolValue() {
			t.Errorf("row %d: expected true, got %v", i, r[0])
		}
	}
}

// TestArrayUpperLowerNullForDimNotOne verifies NULL returned for dim != 1.
func TestArrayUpperLowerNullForDimNotOne(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	nullCases := []string{
		"SELECT array_upper(ARRAY[1,2,3], 2)",
		"SELECT array_lower(ARRAY[1,2,3], 2)",
		"SELECT array_length(ARRAY[1,2,3], 2)",
	}
	for _, sql := range nullCases {
		rows := runQueryRows(t, ctx, sql)
		if len(rows) == 1 && !rows[0][0].IsNull() {
			t.Errorf("%s: expected NULL, got %v", sql, rows[0][0])
		}
	}
}
