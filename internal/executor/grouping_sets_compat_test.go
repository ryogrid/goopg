package executor

import "testing"

// TestCompatGroupByRollupGeneratesSubtotalsAndGrandTotal pins ROLLUP's
// hierarchical-subtotal semantics: GROUP BY ROLLUP(dept, region) must
// produce one row per (dept, region) group, one per-dept subtotal row
// (region rolled up to NULL), and a single grand-total row (both rolled
// up to NULL) — cross-checked against upstream PostgreSQL 18.3's ROLLUP
// output shape. M0122-0004.
func TestCompatGroupByRollupGeneratesSubtotalsAndGrandTotal(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (dept text, region text, amt int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES ('a','x',10), ('a','y',20), ('b','x',5)"); err != nil {
		t.Fatal(err)
	}

	rows := runQuery(t, ctx,
		`SELECT dept, region, SUM(amt) FROM t GROUP BY ROLLUP(dept, region)
		 ORDER BY dept NULLS LAST, region NULLS LAST`)

	type want struct {
		dept, region string
		nullDept     bool
		nullRegion   bool
		sum          int64
	}
	wants := []want{
		{dept: "a", region: "x", sum: 10},
		{dept: "a", region: "y", sum: 20},
		{dept: "a", nullRegion: true, sum: 30},
		{dept: "b", region: "x", sum: 5},
		{dept: "b", nullRegion: true, sum: 5},
		{nullDept: true, nullRegion: true, sum: 35},
	}
	if len(rows) != len(wants) {
		t.Fatalf("rows=%d want %d: %+v", len(rows), len(wants), rows)
	}
	for i, w := range wants {
		r := rows[i]
		if w.nullDept {
			if !r[0].IsNull() {
				t.Fatalf("row[%d] dept=%+v want NULL", i, r[0])
			}
		} else if r[0].IsNull() || r[0].StringValue() != w.dept {
			t.Fatalf("row[%d] dept=%+v want %q", i, r[0], w.dept)
		}
		if w.nullRegion {
			if !r[1].IsNull() {
				t.Fatalf("row[%d] region=%+v want NULL", i, r[1])
			}
		} else if r[1].IsNull() || r[1].StringValue() != w.region {
			t.Fatalf("row[%d] region=%+v want %q", i, r[1], w.region)
		}
		if r[2].IsNull() || r[2].Int != w.sum {
			t.Fatalf("row[%d] sum=%+v want %d", i, r[2], w.sum)
		}
	}
}

// TestCompatGroupByCubeGeneratesAllSubsetTotals pins CUBE's full-power-set
// semantics: GROUP BY CUBE(dept, region) must generate every one of the
// 2^2 = 4 grouping combinations, including the region-only subtotal that
// ROLLUP(dept, region) does NOT produce (ROLLUP's set list is a strict
// prefix chain; CUBE's is the full subset lattice). M0122-0004.
func TestCompatGroupByCubeGeneratesAllSubsetTotals(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (dept text, region text, amt int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES ('a','x',10), ('a','y',20), ('b','x',5)"); err != nil {
		t.Fatal(err)
	}

	rows := runQuery(t, ctx,
		`SELECT dept, region, SUM(amt) FROM t GROUP BY CUBE(dept, region)`)
	if len(rows) != 8 {
		t.Fatalf("rows=%d want 8 (2x2 detail + 2 dept subtotals + 2 region subtotals + 1 grand total = 8): %+v", len(rows), rows)
	}

	var regionOnlyX, regionOnlyY, grandTotal int
	for _, r := range rows {
		deptNull := r[0].IsNull()
		regionNull := r[1].IsNull()
		switch {
		case deptNull && !regionNull && r[1].StringValue() == "x":
			regionOnlyX++
			if r[2].Int != 15 {
				t.Fatalf("region=x subtotal=%+v want 15", r[2])
			}
		case deptNull && !regionNull && r[1].StringValue() == "y":
			regionOnlyY++
			if r[2].Int != 20 {
				t.Fatalf("region=y subtotal=%+v want 20", r[2])
			}
		case deptNull && regionNull:
			grandTotal++
			if r[2].Int != 35 {
				t.Fatalf("grand total=%+v want 35", r[2])
			}
		}
	}
	if regionOnlyX != 1 || regionOnlyY != 1 || grandTotal != 1 {
		t.Fatalf("regionOnlyX=%d regionOnlyY=%d grandTotal=%d (each want 1): rows=%+v", regionOnlyX, regionOnlyY, grandTotal, rows)
	}
}

// TestCompatGroupByExplicitGroupingSets pins the explicit
// `GROUPING SETS ((a), (b), ())` form — an arbitrary caller-specified set
// list rather than ROLLUP/CUBE's generated hierarchy/lattice.
func TestCompatGroupByExplicitGroupingSets(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (dept text, region text, amt int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES ('a','x',10), ('a','y',20), ('b','x',5)"); err != nil {
		t.Fatal(err)
	}

	rows := runQuery(t, ctx,
		`SELECT dept, region, SUM(amt) FROM t GROUP BY GROUPING SETS ((dept), (region), ())`)
	if len(rows) != 5 {
		t.Fatalf("rows=%d want 5 (2 dept + 2 region + 1 grand total): %+v", len(rows), rows)
	}

	var deptOnly, regionOnly, grand int
	for _, r := range rows {
		switch {
		case !r[0].IsNull() && r[1].IsNull():
			deptOnly++
		case r[0].IsNull() && !r[1].IsNull():
			regionOnly++
		case r[0].IsNull() && r[1].IsNull():
			grand++
		default:
			t.Fatalf("unexpected fully-detailed row (dept and region both non-NULL): %+v", r)
		}
	}
	if deptOnly != 2 || regionOnly != 2 || grand != 1 {
		t.Fatalf("deptOnly=%d regionOnly=%d grand=%d (want 2/2/1): rows=%+v", deptOnly, regionOnly, grand, rows)
	}
}

// TestCompatGroupingFuncReportsRolledUpColumns pins the GROUPING(...)
// pseudo-function: bit i (rightmost arg = LSB) is 1 iff that argument was
// rolled up away (excluded from) the current output row's grouping set —
// upstream's documented bitmask semantics for disambiguating a real NULL
// value from a rollup-induced one.
func TestCompatGroupingFuncReportsRolledUpColumns(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (dept text, region text, amt int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES ('a','x',10), ('a','y',20), ('b','x',5)"); err != nil {
		t.Fatal(err)
	}

	rows := runQuery(t, ctx,
		`SELECT dept, region, SUM(amt), GROUPING(dept, region) AS g FROM t
		 GROUP BY ROLLUP(dept, region) ORDER BY dept NULLS LAST, region NULLS LAST`)
	wantG := []int64{0, 0, 1, 0, 1, 3}
	if len(rows) != len(wantG) {
		t.Fatalf("rows=%d want %d: %+v", len(rows), len(wantG), rows)
	}
	for i, w := range wantG {
		if rows[i][3].IsNull() || rows[i][3].Int != w {
			t.Fatalf("row[%d] grouping=%+v want %d", i, rows[i][3], w)
		}
	}
}
