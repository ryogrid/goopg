package executor

import "testing"

// TestNotInUnnest_NormalCase verifies the common case (no NULLs
// anywhere) after M0122-0011 wired `x NOT IN (subquery)` through
// the planner's non-correlated unnest path (JoinTypeAnti,
// NullAware=true) instead of only the slower runtime-cache
// fallback. Verified against real PostgreSQL 18.3 semantics.
func TestNotInUnnest_NormalCase(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE niu_outer (x int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE niu_inner (y int)"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"INSERT INTO niu_outer VALUES (1), (2), (3), (4)",
		"INSERT INTO niu_inner VALUES (2), (4)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	rows := runQueryRows(t, ctx, "SELECT x FROM niu_outer WHERE x NOT IN (SELECT y FROM niu_inner) ORDER BY x")
	want := []int64{1, 3}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d; rows=%v", len(rows), len(want), rows)
	}
	for i, r := range rows {
		if r[0].Int != want[i] {
			t.Errorf("row %d: got x=%d, want %d", i, r[0].Int, want[i])
		}
	}
}

// TestNotInUnnest_SubqueryNullPoisonsAllRows verifies PostgreSQL's
// well-known NOT IN NULL trap: if the subquery produces ANY NULL,
// `x NOT IN (subquery)` is NULL/false for every outer row (even
// rows whose x doesn't equal any subquery value), so the whole
// query returns zero rows. A naive anti-join (correct for NOT
// EXISTS but not NOT IN) would incorrectly keep x=1/x=3 here.
func TestNotInUnnest_SubqueryNullPoisonsAllRows(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE niu2_outer (x int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE niu2_inner (y int)"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"INSERT INTO niu2_outer VALUES (1), (2), (3), (4)",
		"INSERT INTO niu2_inner VALUES (2), (4), (NULL)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	rows := runQueryRows(t, ctx, "SELECT x FROM niu2_outer WHERE x NOT IN (SELECT y FROM niu2_inner) ORDER BY x")
	if len(rows) != 0 {
		t.Errorf("got %d rows, want 0 (NULL in subquery must poison NOT IN to empty): rows=%v", len(rows), rows)
	}
}

// TestNotInUnnest_EmptySubqueryReturnsAllRows verifies `x NOT IN
// ()` is TRUE for every outer row, including a NULL x, when the
// subquery produces zero rows.
func TestNotInUnnest_EmptySubqueryReturnsAllRows(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE niu3_outer (x int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE niu3_inner (y int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO niu3_outer VALUES (1), (NULL), (3)"); err != nil {
		t.Fatal(err)
	}
	// niu3_inner stays empty.

	rows := runQueryRows(t, ctx, "SELECT x FROM niu3_outer WHERE x NOT IN (SELECT y FROM niu3_inner) ORDER BY x")
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3 (NOT IN () is true for every row, including NULL x): rows=%v", len(rows), rows)
	}
	nullCount := 0
	for _, r := range rows {
		if r[0].IsNull() {
			nullCount++
		}
	}
	if nullCount != 1 {
		t.Errorf("expected the NULL x row to survive NOT IN (), got %d NULL rows in result", nullCount)
	}
}

// TestNotInUnnest_NullProbeExcluded verifies a NULL outer value is
// excluded from `x NOT IN (subquery)` whenever the subquery is
// non-empty (and NULL-free) — `NULL IN (nonempty)` is NULL, so
// `NOT (NULL)` is NULL too, never TRUE.
func TestNotInUnnest_NullProbeExcluded(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE niu4_outer (x int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE niu4_inner (y int)"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"INSERT INTO niu4_outer VALUES (1), (NULL), (5)",
		"INSERT INTO niu4_inner VALUES (2), (4)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	rows := runQueryRows(t, ctx, "SELECT x FROM niu4_outer WHERE x NOT IN (SELECT y FROM niu4_inner) ORDER BY x")
	want := []int64{1, 5}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d (NULL x must be excluded): rows=%v", len(rows), len(want), rows)
	}
	for i, r := range rows {
		if r[0].Int != want[i] {
			t.Errorf("row %d: got x=%d, want %d", i, r[0].Int, want[i])
		}
	}
}
