package executor

import (
	"strings"
	"testing"
)

// M0125-0034 — C1: a join whose input is a set operation degenerated to a
// Cartesian product.
//
// Two planner defects stacked, and both are pinned here because each one
// alone is enough to reproduce the `Nested Loop (CROSS)`:
//
//  1. `collectScanOutputNames` (planner/pushdown.go) had no `*SetOp` case,
//     so `allColumnRefNamesInScope` never found the set operation's own
//     output columns and `pushOneConjunct` declined every conjunct that
//     spanned it — the equi-join stayed on the residual Filter and the
//     Join stayed CROSS.
//  2. `pickInnerScanForNLI` (planner/nl_index_join.go) may make the LEFT
//     child the index-probed inner, which emits `outer ++ inner` — the
//     FLIP of `Left ++ Right`. It already declined that flip for a
//     `*Aggregate` / `*Values` outer; a `*SetOp` outer is uncorrectable
//     for the same reason (its columns are not scan-tracked, so
//     remapWithBindings cannot rebind the downstream refs).
//
// Measured on the TPC-DS SF0.5 cluster (:65437) against the git-tracked PG
// 18.3 oracle: Q5 Q8 Q14 Q54 Q71 all went TIMEOUT -> PASS with
// oracle-identical row counts and value checksums (Q71 `580 rows
// ck=521a7af7606d10c1`), and 30 `Nested Loop (CROSS)` nodes disappeared
// from the plan capture. See docs/design/0125-0034-setop-join-promotion.md.

// setopJoinFixture builds the miniature TPC-DS Q71 shape: a dimension
// table joined to a UNION ALL of two fact/date joins, plus a second
// dimension reached through a further equi-join.
func setopJoinFixture(t *testing.T) *Context {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)

	runSQL(t, ctx, "CREATE TABLE sj_item (i_item_sk int, i_brand_id int, i_brand text, i_manager_id int)")
	runSQL(t, ctx, "CREATE TABLE sj_dd (d_date_sk int, d_moy int, d_year int)")
	runSQL(t, ctx, "CREATE TABLE sj_td (t_time_sk int, t_hour int, t_minute int, t_meal_time text)")
	runSQL(t, ctx, "CREATE TABLE sj_ws (ws_ext_sales_price int, ws_sold_date_sk int, ws_item_sk int, ws_sold_time_sk int)")
	runSQL(t, ctx, "CREATE TABLE sj_cs (cs_ext_sales_price int, cs_sold_date_sk int, cs_item_sk int, cs_sold_time_sk int)")
	runSQL(t, ctx, "INSERT INTO sj_item VALUES (1,10,'b1',1),(2,20,'b2',1),(3,30,'b3',2)")
	runSQL(t, ctx, "INSERT INTO sj_dd VALUES (100,12,2002),(101,11,2002)")
	runSQL(t, ctx, "INSERT INTO sj_td VALUES (200,8,30,'breakfast'),(201,19,0,'dinner'),(202,12,0,'lunch')")
	runSQL(t, ctx, "INSERT INTO sj_ws VALUES (5,100,1,200),(6,101,1,200),(7,100,3,201)")
	runSQL(t, ctx, "INSERT INTO sj_cs VALUES (9,100,2,201),(11,100,1,202)")
	return ctx
}

const setopJoinQ71 = `select i_brand_id, i_brand, t_hour, t_minute, sum(ext_price) ext_price
 from sj_item, (select ws_ext_sales_price as ext_price, ws_sold_date_sk as sold_date_sk,
                       ws_item_sk as sold_item_sk, ws_sold_time_sk as time_sk
                from sj_ws, sj_dd where d_date_sk = ws_sold_date_sk and d_moy=12 and d_year=2002
                union all
                select cs_ext_sales_price, cs_sold_date_sk, cs_item_sk, cs_sold_time_sk
                from sj_cs, sj_dd where d_date_sk = cs_sold_date_sk and d_moy=12 and d_year=2002
                ) tmp, sj_td
 where sold_item_sk = i_item_sk and i_manager_id=1 and time_sk = t_time_sk
   and (t_meal_time = 'breakfast' or t_meal_time = 'dinner')
 group by i_brand, i_brand_id, t_hour, t_minute
 order by ext_price desc, i_brand_id`

// TestSetOpJoinPromotesToHashJoin is defect (1): the equi-join across the
// UNION ALL must reach the Join node instead of being demoted to a Filter
// over a Cartesian product.
func TestSetOpJoinPromotesToHashJoin(t *testing.T) {
	ctx := setopJoinFixture(t)
	plan := strings.Join(runExplainRows(t, ctx, "EXPLAIN "+setopJoinQ71), "\n")
	if strings.Contains(plan, "Nested Loop (CROSS)") {
		t.Fatalf("equi-join across the set operation was not promoted:\n%s", plan)
	}
	if !strings.Contains(plan, "Hash Join") {
		t.Fatalf("expected a Hash Join over the Append, got:\n%s", plan)
	}
	// The residual Filter must keep only the genuinely single-sided
	// restrictions; both equi-joins are now join conditions.
	if strings.Contains(plan, "sold_item_sk = i_item_sk") || strings.Contains(plan, "time_sk = t_time_sk") {
		t.Fatalf("an equi-join is still rendered as a Filter conjunct:\n%s", plan)
	}
}

// TestSetOpJoinAnswerUnchanged is the correctness half: the promoted plan
// must return exactly what the Cartesian-product plan returned.
//
// Hand-derived expectation. Only manager 1 survives (items 1 and 2); only
// d_date_sk 100 is moy=12/2002; only breakfast/dinner times qualify. So
// web row (5,100,1,200) -> brand 10 at 08:30, catalog row (9,100,2,201) ->
// brand 20 at 19:00; the catalog row at time 202 (lunch) and the web row
// at date 101 both drop, as does item 3 (manager 2).
func TestSetOpJoinAnswerUnchanged(t *testing.T) {
	ctx := setopJoinFixture(t)
	rows := runQuery(t, ctx, setopJoinQ71)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %v", len(rows), rows)
	}
	// Ordered by ext_price DESC: brand 20 (9) before brand 10 (5).
	want := [][2]int64{{20, 9}, {10, 5}}
	for i, w := range want {
		if got := rows[i][0].Int; got != w[0] {
			t.Errorf("row %d: i_brand_id = %d, want %d", i, got, w[0])
		}
		if got := rows[i][4].Int; got != w[1] {
			t.Errorf("row %d: sum(ext_price) = %d, want %d", i, got, w[1])
		}
	}
}

// TestSetOpJoinNLIFlipDeclined is defect (2). Without the *SetOp decline in
// pickInnerScanForNLI this query planned `Append ++ sj_item` while the
// aggregate and the four group keys stayed bound to `sj_item ++ Append`,
// and TPC-DS Q71 failed with "aggregate sum requires numeric argument in
// v0" — sum() had been handed the brand NAME. Asserting the answer above
// covers the symptom; this asserts the shape, so a future NLI change that
// re-enables the flip fails here with a readable reason rather than as a
// type error.
func TestSetOpJoinNLIFlipDeclined(t *testing.T) {
	ctx := setopJoinFixture(t)
	lines := runExplainRows(t, ctx, "EXPLAIN "+setopJoinQ71)
	for i, l := range lines {
		if !strings.Contains(l, "Append") {
			continue
		}
		// The Append must not be the driving (first) child of a
		// nested-loop index join — that is the flipped layout.
		if i > 0 && strings.Contains(lines[i-1], "Nested Loop") &&
			!strings.Contains(lines[i-1], "CROSS") {
			t.Fatalf("set operation became the NLI outer (flipped schema):\n%s",
				strings.Join(lines, "\n"))
		}
	}
}

// TestIntersectJoinPromotesToHashJoin covers the TPC-DS Q8 arm: the set
// operation is INTERSECT and sits one level deeper, behind a *Project,
// which is the shape where M0097-0058's `containsSetOp` bailout used to
// fire on its own (collectScanOutputNames already saw the Project's
// output names, so the name check passed and the bailout was reached).
func TestIntersectJoinPromotesToHashJoin(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	runSQL(t, ctx, "CREATE TABLE ij_ca (ca_zip text, ca_state text)")
	runSQL(t, ctx, "CREATE TABLE ij_store (s_zip text, s_name text)")
	runSQL(t, ctx, "CREATE TABLE ij_cb (b_zip text, b_n int)")
	runSQL(t, ctx, "CREATE TABLE ij_ss (ss_name text, ss_amt int)")
	runSQL(t, ctx, "INSERT INTO ij_ca VALUES ('111','CA'),('222','NY')")
	runSQL(t, ctx, "INSERT INTO ij_store VALUES ('111','s1'),('333','s3')")
	runSQL(t, ctx, "INSERT INTO ij_cb VALUES ('111',1),('222',2)")
	runSQL(t, ctx, "INSERT INTO ij_ss VALUES ('s1',5),('s3',7)")

	q := `select s_name, ss_amt from ij_store, ij_ss,
	  (select zip from
	     (select ca_zip as zip from ij_ca
	      intersect
	      select b_zip from ij_cb) x) v
	 where s_zip = zip and ss_name = s_name order by s_name`

	plan := strings.Join(runExplainRows(t, ctx, "EXPLAIN "+q), "\n")
	if strings.Contains(plan, "Nested Loop (CROSS)") {
		t.Fatalf("equi-join across the INTERSECT was not promoted:\n%s", plan)
	}
	rows := runQuery(t, ctx, q)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1: %v", len(rows), rows)
	}
	if got := string(rows[0][0].Buf); got != "s1" {
		t.Errorf("s_name = %q, want \"s1\"", got)
	}
	if got := rows[0][1].Int; got != 5 {
		t.Errorf("ss_amt = %d, want 5", got)
	}
}
