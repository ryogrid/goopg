package executor

import "testing"

// M0125-0040 ("C6"): the grouping-sets expansion gives every generated
// UNION ALL branch its own FROM+WHERE, so an N-set construct used to execute
// the whole join subtree N times (TPC-DS Q18: 5 full catalog_sales scans;
// Q67: 9 full store_sales scans). The planner now hoists FROM+WHERE into one
// synthetic materialized CTE that all branches read
// (internal/planner/groupingsets_share.go).
//
// The compat tests in grouping_sets_compat_test.go already pin ROLLUP/CUBE/
// GROUPING SETS answers and now run through the hoisted path. What these add
// is what only an end-to-end run can state: that the source is executed ONCE,
// that a multi-table shape with the statement's own ORDER BY still names its
// output columns the way it did, and that the shapes the rewrite must decline
// still answer correctly.

// TestGroupingSetsShareSourceExecutesSourceOnce is the acceptance evidence for
// the item's "ONE scan" claim, stated so that it cannot pass by accident: a
// sequence advanced once per source row counts executions of the source. With
// the hoist, three ROLLUP branches consume one execution (3 rows read => 3
// nextval calls). Without it, each branch re-runs the scan and the counter
// triples.
func TestGroupingSetsShareSourceExecutesSourceOnce(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, ddl := range []string{
		"CREATE TABLE gs_src (dept text, region text, amt int)",
		"CREATE SEQUENCE gs_scan_counter",
		"INSERT INTO gs_src VALUES ('a','x',10), ('a','y',20), ('b','x',5)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}

	// nextval() sits in WHERE, i.e. inside the hoisted source, and is called
	// once per row the source produces.
	rows := runQuery(t, ctx,
		`SELECT dept, region, sum(amt) FROM gs_src
		 WHERE nextval('gs_scan_counter') > 0
		 GROUP BY ROLLUP(dept, region)`)
	if len(rows) != 6 {
		t.Fatalf("rows=%d want 6 (3 groups + 2 subtotals + grand total)", len(rows))
	}

	got := runQuery(t, ctx, "SELECT last_value FROM gs_scan_counter")
	if len(got) != 1 {
		t.Fatalf("sequence probe returned %d rows", len(got))
	}
	// 3 rows x 1 execution. The pre-M0125-0040 expansion read 9 (3 x 3).
	if n := got[0][0].Int; n != 3 {
		t.Fatalf("source rows read = %d, want 3 — the grouping-set branches are not sharing one execution", n)
	}
}

// TestGroupingSetsShareSourceJoinedRollupKeepsNamesAndAnswers is the TPC-DS
// Q18 shape in miniature: two tables joined in a comma-FROM, rolled up over
// columns from both, with the statement's own ORDER BY naming those columns.
// The ORDER BY is the part that would break silently — the hoist renames
// every projected column to a generated __gs_cN, so a target without an AS
// clause has to be given its original name back or the sort key stops
// resolving.
func TestGroupingSetsShareSourceJoinedRollupKeepsNamesAndAnswers(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, ddl := range []string{
		"CREATE TABLE gs_fact (item_sk int, qty int)",
		"CREATE TABLE gs_dim (i_item_sk int, i_class text, i_brand text)",
		"INSERT INTO gs_dim VALUES (1,'c1','b1'), (2,'c1','b2'), (3,'c2','b3')",
		"INSERT INTO gs_fact VALUES (1,10), (1,5), (2,20), (3,7)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}

	rows := runQuery(t, ctx,
		`SELECT i_class, i_brand, sum(qty)
		 FROM gs_fact, gs_dim
		 WHERE item_sk = i_item_sk
		 GROUP BY ROLLUP(i_class, i_brand)
		 ORDER BY i_class NULLS LAST, i_brand NULLS LAST`)

	// Cross-checked against PostgreSQL 18.3 on the same data.
	want := []struct {
		class, brand string
		nullClass    bool
		nullBrand    bool
		sum          int64
	}{
		{class: "c1", brand: "b1", sum: 15},
		{class: "c1", brand: "b2", sum: 20},
		{class: "c1", nullBrand: true, sum: 35},
		{class: "c2", brand: "b3", sum: 7},
		{class: "c2", nullBrand: true, sum: 7},
		{nullClass: true, nullBrand: true, sum: 42},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows=%d want %d: %+v", len(rows), len(want), rows)
	}
	for i, w := range want {
		r := rows[i]
		if w.nullClass != r[0].IsNull() || (!w.nullClass && r[0].StringValue() != w.class) {
			t.Fatalf("row[%d] i_class=%+v want %q (null=%v)", i, r[0], w.class, w.nullClass)
		}
		if w.nullBrand != r[1].IsNull() || (!w.nullBrand && r[1].StringValue() != w.brand) {
			t.Fatalf("row[%d] i_brand=%+v want %q (null=%v)", i, r[1], w.brand, w.nullBrand)
		}
		if r[2].IsNull() || r[2].Int != w.sum {
			t.Fatalf("row[%d] sum=%+v want %d", i, r[2], w.sum)
		}
	}
}

// TestGroupingSetsShareSourceDeclinedShapesStillAnswer covers the fail-closed
// side end to end. Each of these is refused by the hoist (a correlated WHERE
// cannot move into a CTE body; explicit JOIN syntax is not modelled; a
// sublink brings its own scope) and must therefore still produce exactly the
// answers the un-hoisted expansion always did.
func TestGroupingSetsShareSourceDeclinedShapesStillAnswer(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, ddl := range []string{
		"CREATE TABLE gs_f (k int, g text, v int)",
		"CREATE TABLE gs_o (k int)",
		"INSERT INTO gs_f VALUES (1,'a',10), (1,'b',20), (2,'a',5)",
		"INSERT INTO gs_o VALUES (1), (2)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("correlated where", func(t *testing.T) {
		// The inner grouping-sets query's WHERE references the outer row.
		// Hoisting that WHERE into a CTE body would make the reference
		// unresolvable and turn a working query into a 42703 plan error, so
		// the rewrite must decline. Both outer rows have matching facts, so
		// both survive the EXISTS.
		rows := runQuery(t, ctx,
			`SELECT k FROM gs_o WHERE EXISTS (
			     SELECT g, sum(v) FROM gs_f WHERE gs_f.k = gs_o.k GROUP BY ROLLUP(g))
			 ORDER BY k`)
		if len(rows) != 2 {
			t.Fatalf("rows=%d want 2: %+v", len(rows), rows)
		}
		if rows[0][0].Int != 1 || rows[1][0].Int != 2 {
			t.Fatalf("k = %d,%d want 1,2", rows[0][0].Int, rows[1][0].Int)
		}
	})

	t.Run("explicit join", func(t *testing.T) {
		rows := runQuery(t, ctx,
			`SELECT g, sum(v) FROM gs_f JOIN gs_o ON gs_f.k = gs_o.k
			 GROUP BY ROLLUP(g) ORDER BY g NULLS LAST`)
		if len(rows) != 3 {
			t.Fatalf("rows=%d want 3: %+v", len(rows), rows)
		}
		// gs_o holds both k values, so the join keeps all three fact rows:
		// g='a' => 10+5, g='b' => 20, grand total 35.
		if rows[0][1].Int != 15 || rows[1][1].Int != 20 || rows[2][1].Int != 35 {
			t.Fatalf("sums = %d,%d,%d want 15,20,35", rows[0][1].Int, rows[1][1].Int, rows[2][1].Int)
		}
	})

	t.Run("sublink in target list", func(t *testing.T) {
		rows := runQuery(t, ctx,
			`SELECT g, (SELECT count(*) FROM gs_o), sum(v) FROM gs_f
			 GROUP BY ROLLUP(g) ORDER BY g NULLS LAST`)
		if len(rows) != 3 {
			t.Fatalf("rows=%d want 3: %+v", len(rows), rows)
		}
		for i, r := range rows {
			if r[1].Int != 2 {
				t.Fatalf("row[%d] sublink=%d want 2", i, r[1].Int)
			}
		}
		if rows[0][2].Int != 15 || rows[1][2].Int != 20 || rows[2][2].Int != 35 {
			t.Fatalf("sums = %d,%d,%d want 15,20,35", rows[0][2].Int, rows[1][2].Int, rows[2][2].Int)
		}
	})
}
