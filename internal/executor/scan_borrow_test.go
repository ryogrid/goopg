package executor

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestAggregateBorrowsScanRowsWhitelist pins which aggregates may take a seq
// scan's reused row (M0145-0008u): only built-in count/sum/avg/min/max with no
// DISTINCT, ORDER BY, WITHIN GROUP or passthrough, hashed, non-Finalize.
func TestAggregateBorrowsScanRowsWhitelist(t *testing.T) {
	call := func(name string) optimizer.AggregateCall { return optimizer.AggregateCall{Name: name} }
	for _, tc := range []struct {
		name string
		agg  optimizer.Aggregate
		want bool
	}{
		{"count-sum-avg-min-max", optimizer.Aggregate{Aggs: []optimizer.AggregateCall{call("count"), call("SUM"), call("avg"), call("min"), call("max")}}, true},
		{"grouped", optimizer.Aggregate{GroupExprs: []optimizer.Expr{deformCol(1)}, Aggs: []optimizer.AggregateCall{call("sum")}}, true},
		{"string_agg", optimizer.Aggregate{Aggs: []optimizer.AggregateCall{call("string_agg")}}, false},
		{"distinct", optimizer.Aggregate{Aggs: []optimizer.AggregateCall{{Name: "count", Distinct: true}}}, false},
		{"order-by", optimizer.Aggregate{Aggs: []optimizer.AggregateCall{{Name: "max", OrderBy: []optimizer.SortKey{{}}}}}, false},
		{"within-group", optimizer.Aggregate{Aggs: []optimizer.AggregateCall{{Name: "max", WithinGroup: true}}}, false},
		{"passthrough", optimizer.Aggregate{Passthrough: []optimizer.Expr{deformCol(2)}, Aggs: []optimizer.AggregateCall{call("sum")}}, false},
		{"sorted", optimizer.Aggregate{Strategy: optimizer.AggStrategySorted, Aggs: []optimizer.AggregateCall{call("sum")}}, false},
		{"finalize", optimizer.Aggregate{Mode: optimizer.AggModeFinal, Aggs: []optimizer.AggregateCall{call("sum")}}, false},
	} {
		if got := aggregateBorrowsScanRows(&tc.agg); got != tc.want {
			t.Errorf("%s: aggregateBorrowsScanRows = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// sortedRowValues runs sql and renders every cell as its text value at once,
// before the statement's arena can be reused (sortedRowStrings renders only
// Datum.Int, which is meaningless for text and numeric cells).
func sortedRowValues(t *testing.T, ctx *Context, sql string) []string {
	t.Helper()
	return sortedCells(runSQL(t, ctx, sql))
}

// sortedCells renders rows as comma-joined text cells, sorted.
func sortedCells(rows []Row) []string {
	var out []string
	for _, r := range rows {
		cells := make([]string, len(r))
		for i, d := range r {
			if d.IsNull() {
				cells[i] = "NULL"
			} else {
				cells[i] = string(d.AppendValueText(nil))
			}
		}
		out = append(out, strings.Join(cells, ","))
	}
	sort.Strings(out)
	return out
}

// TestScanBorrowMatchesClonedResults runs whitelisted aggregates over a
// multi-page table whose text and big-numeric values live in the scan's
// per-page arena, with borrowing on and forced off, tail poison armed: group
// keys and min/max text must survive every page's arena reset, and every
// result must equal the cloning path's.
func TestScanBorrowMatchesClonedResults(t *testing.T) {
	ctx := deformW8Fixture(t)
	runSQL(t, ctx, "CREATE TABLE bt (g text, s text, n numeric, i int)")
	runSQL(t, ctx, "INSERT INTO bt SELECT 'grp-' || (x % 7), repeat(chr(65 + x % 26), 20 + x % 40) || x, (x::numeric * 123456789012345678901234567890), x FROM generate_series(1, 6000) x")
	queries := []string{
		"SELECT count(*), sum(i), avg(i), min(i), max(i) FROM bt",
		"SELECT g, count(*), min(s), max(s), sum(n) FROM bt GROUP BY g",
		"SELECT min(s), max(s), max(n), min(n) FROM bt",
		"SELECT avg(n), count(*), max(s) FROM bt WHERE i % 3 = 0",
	}
	for _, q := range queries {
		plan, err := testPlanDeform(t, ctx, q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		op := mustBuildDeform(t, plan)
		borrowed := false
		for cur := Operator(op); cur != nil; {
			if so := unwrapSeqScanOp(cur); so != nil {
				borrowed = so.borrowRows
				break
			}
			switch o := cur.(type) {
			case *aggregateOp:
				cur = o.child
			case *projectOp:
				cur = o.child
			case *sortOp:
				cur = o.child
			case *instrumentedOp:
				cur = o.inner
			default:
				cur = nil
			}
		}
		if !borrowed {
			t.Fatalf("%s: the seq scan does not borrow; nothing is exercised", q)
		}

		old := seqScanDeformPoison
		seqScanDeformPoison = true
		got := sortedRowValues(t, ctx, q)
		seqScanDeformPoison = old
		scanBorrowDisabled = true
		want := sortedRowValues(t, ctx, q)
		scanBorrowDisabled = false
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("%s:\nborrowed %v\ncloned   %v", q, got, want)
		}
		t.Log(fmt.Sprintf("%s -> %d rows", q, len(got)))
	}
}

// borrowedScanTables walks a built tree through the operators that carry
// rows and returns the tables whose seq scan borrows.
func borrowedScanTables(op Operator) []string {
	var out []string
	var walk func(Operator)
	walk = func(cur Operator) {
		switch o := cur.(type) {
		case nil:
		case *instrumentedOp:
			walk(o.inner)
		case *seqScanOp:
			if o.borrowRows && o.tbl != nil {
				out = append(out, o.tbl.Name)
			}
		case *aggregateOp:
			walk(o.child)
		case *projectOp:
			walk(o.child)
		case *sortOp:
			walk(o.child)
		case *joinOp:
			walk(o.left)
			walk(o.right)
		}
	}
	walk(op)
	sort.Strings(out)
	return out
}

// TestScanBorrowThroughHashJoinProbe extends the borrow to TPC-H Q17's shape,
// Aggregate → Hash Join → Project → Seq Scan (markScanBorrowChain): the probe
// side's scan hands up its reused row through the join and the Projects; the
// build side's scan borrows too, as a root of its own: the build loop copies
// every row it keeps (joinBorrowBuildChild, slice 23). With tail poison
// armed, group keys and min/max taken from probe rows must survive every
// page's arena reset, the join filter must read the right row, and every
// result must equal the cloning path's.
func TestScanBorrowThroughHashJoinProbe(t *testing.T) {
	ctx := deformW8Fixture(t)
	runSQL(t, ctx, "CREATE TABLE bp (k int, s text, n numeric, i int)")
	runSQL(t, ctx, "INSERT INTO bp SELECT x % 50, repeat(chr(65 + x % 26), 20 + x % 40) || x, (x::numeric * 123456789012345678901234567890), x FROM generate_series(1, 6000) x")
	runSQL(t, ctx, "CREATE TABLE bd (k int, label text, lim int)")
	runSQL(t, ctx, "INSERT INTO bd SELECT x, 'dim-' || x, x * 100 FROM generate_series(0, 49) x")
	runSQL(t, ctx, "ANALYZE bp")
	runSQL(t, ctx, "ANALYZE bd")
	queries := []string{
		"SELECT sum(bp.n), count(*), max(bp.s) FROM bp JOIN bd ON bp.k = bd.k",
		"SELECT bd.label, min(bp.s), max(bp.s), sum(bp.i) FROM bp JOIN bd ON bp.k = bd.k GROUP BY bd.label",
		"SELECT bp.k, count(*), min(bp.s) FROM bp JOIN bd ON bp.k = bd.k WHERE bp.i < bd.lim GROUP BY bp.k",
		"SELECT count(*), max(bp.s) FROM bp WHERE EXISTS (SELECT 1 FROM bd WHERE bd.k = bp.k AND bd.lim > 2000)",
	}
	for _, q := range queries {
		plan, err := testPlanDeform(t, ctx, q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		op := mustBuildDeform(t, plan)
		if got := borrowedScanTables(op); strings.Join(got, ",") != "bd,bp" {
			t.Fatalf("%s: borrowing scans %v, want the probe side bp and the build side bd\n%#v", q, got, plan)
		}
		old := seqScanDeformPoison
		seqScanDeformPoison = true
		got := sortedRowValues(t, ctx, q)
		seqScanDeformPoison = old
		scanBorrowDisabled = true
		want := sortedRowValues(t, ctx, q)
		scanBorrowDisabled = false
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("%s:\nborrowed %v\ncloned   %v", q, got, want)
		}
		if len(got) == 0 {
			t.Fatalf("%s: no rows", q)
		}
	}
}

// hashJoins lists the hash joins in a built tree.
func hashJoins(op Operator) []*joinOp {
	var out []*joinOp
	var walk func(Operator)
	walk = func(cur Operator) {
		switch o := cur.(type) {
		case *instrumentedOp:
			walk(o.inner)
		case *aggregateOp:
			walk(o.child)
		case *projectOp:
			walk(o.child)
		case *sortOp:
			walk(o.child)
		case *joinOp:
			if o.plan != nil && o.plan.Algo == optimizer.JoinAlgoHash {
				out = append(out, o)
			}
			walk(o.left)
			walk(o.right)
		}
	}
	walk(op)
	return out
}

// TestScanBorrowThroughOuterHashJoinProbe extends the borrow to TPC-H Q13's
// shape, Aggregate → Hash Right Join → Seq Scan: a RIGHT or FULL hash join
// streams its probe rows like an inner one, and the sweep after the
// last probe row emits build rows (null-extended on the probe side) only. bd
// has keys bp lacks, so the sweep emits rows; bp has keys bd lacks, so the
// FULL join null-extends probe rows too. (RIGHT SEMI / RIGHT ANTI never emit
// a probe row at all.) The build side bd borrows as its own root. goopg never plans a FULL hash join
// (deferral ledger M0134-0037), so the FULL case retypes the planned RIGHT
// join; both orientations are the same runtime (join_outer_fill.go). With
// tail poison armed every result must equal the cloning path's.
func TestScanBorrowThroughOuterHashJoinProbe(t *testing.T) {
	ctx := deformW8Fixture(t)
	runSQL(t, ctx, "CREATE TABLE bp (k int, s text, n numeric, i int)")
	runSQL(t, ctx, "INSERT INTO bp SELECT x % 50, repeat(chr(65 + x % 26), 20 + x % 40) || x, (x::numeric * 123456789012345678901234567890), x FROM generate_series(1, 6000) x")
	runSQL(t, ctx, "CREATE TABLE bd (k int, label text)")
	runSQL(t, ctx, "INSERT INTO bd SELECT x, 'dim-' || x FROM generate_series(10, 69) x")
	runSQL(t, ctx, "ANALYZE bp")
	runSQL(t, ctx, "ANALYZE bd")
	const rightSQL = "SELECT bd.label, bp.k, count(bp.i), max(bp.s), sum(bp.n) FROM bd LEFT JOIN bp ON bp.k = bd.k AND bp.s NOT LIKE '%B%' GROUP BY bd.label, bp.k"
	run := func(plan optimizer.Node) []string {
		t.Helper()
		op, err := Build(plan)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := Run(op, ctx)
		if err != nil {
			t.Fatal(err)
		}
		return sortedCells(rows)
	}
	for _, c := range []struct {
		name, sql string
		planned   optimizer.JoinType
		runAs     optimizer.JoinType
	}{
		{"right", rightSQL, optimizer.JoinTypeRight, optimizer.JoinTypeRight},
		{"full", rightSQL, optimizer.JoinTypeRight, optimizer.JoinTypeFull},
	} {
		t.Run(c.name, func(t *testing.T) {
			plan, err := testPlanDeform(t, ctx, c.sql)
			if err != nil {
				t.Fatalf("%s: %v", c.sql, err)
			}
			op := mustBuildDeform(t, plan)
			joins := hashJoins(op)
			if len(joins) != 1 || joins[0].plan.Type != c.planned {
				t.Fatalf("%s: %d hash joins, want one %v", c.sql, len(joins), c.planned)
			}
			joins[0].plan.Type = c.runAs // shared with plan: later builds see it
			if got := borrowedScanTables(op); strings.Join(got, ",") != "bd,bp" {
				t.Fatalf("%s: borrowing scans %v, want the probe side bp and the build side bd", c.sql, got)
			}
			old := seqScanDeformPoison
			seqScanDeformPoison = true
			got := run(plan)
			seqScanDeformPoison = old
			scanBorrowDisabled = true
			want := run(plan)
			scanBorrowDisabled = false
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Fatalf("%s:\nborrowed %v\ncloned   %v", c.sql, got, want)
			}
			if len(got) == 0 {
				t.Fatalf("%s: no rows", c.sql)
			}
			if c.runAs == optimizer.JoinTypeFull && !strings.Contains(strings.Join(got, "|"), "NULL,") {
				t.Fatalf("%s as FULL: no null-extended probe row in %v", c.sql, got)
			}
		})
	}
}

// TestScanBorrowBuildSideAndGatherWorkers pins the two borrow roots that do
// not need a borrowing aggregate above them (slice 23):
//   - a hash join's build side (joinBorrowBuildChild): the build loop copies
//     every row it keeps, so its scan borrows even under a Sort, which keeps
//     rows and stops every other descent;
//   - a Gather worker's tree (gatherOp.runWorker): each row is copied by
//     transferRowForQueue before the next is pulled.
//
// Both tables span many pages with text in the per-page arena, so a row kept
// past its page's reset would read stale bytes; tail poison is armed. Every
// result must equal the cloning path's. Making retainBuildRow keep its input
// uncopied fails the serial case. The leader-tree rule (gatherOp.borrowLeader:
// the leader's rows pass through Gather uncopied, so its tree borrows only
// under a borrowing chain) is defensive and not witnessed here: the Sort above
// this Gather materialises every row it keeps.
func TestScanBorrowBuildSideAndGatherWorkers(t *testing.T) {
	ctx := deformW8Fixture(t)
	runSQL(t, ctx, "CREATE TABLE bp (k int, s text, n numeric, i int)")
	runSQL(t, ctx, "INSERT INTO bp SELECT x % 700, repeat(chr(65 + x % 26), 20 + x % 40) || x, (x::numeric * 123456789012345678901234567890), x FROM generate_series(1, 6000) x")
	runSQL(t, ctx, "CREATE TABLE bd (k int, label text)")
	runSQL(t, ctx, "INSERT INTO bd SELECT x, repeat(chr(97 + x % 26), 30 + x % 50) || '-' || x FROM generate_series(0, 1999) x")
	runSQL(t, ctx, "ANALYZE bp")
	runSQL(t, ctx, "ANALYZE bd")
	const q = "SELECT bd.label, bp.s, bp.n FROM bp JOIN bd ON bp.k = bd.k ORDER BY 1, 2"
	// Keep the serial build loop: the cooperative parallel build rebuilds the
	// build subtree in its own producers (parallelBuildEligible), so the
	// borrowing scan of this tree would never run.
	ctx.MinParallelTableScanBlocks = 1 << 40

	plan, err := testPlanDeform(t, ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	op := mustBuildDeform(t, plan)
	joins := hashJoins(op)
	if len(joins) != 1 {
		t.Fatalf("want one hash join, got %d", len(joins))
	}
	build := "bd"
	if joins[0].hashBuildIsLeft() {
		build = "bp"
	}
	if got := borrowedScanTables(op); strings.Join(got, ",") != build {
		t.Fatalf("borrowing scans %v, want only the build side %s under the Sort", got, build)
	}

	run := func(gathered bool) []string {
		t.Helper()
		old := seqScanDeformPoison
		seqScanDeformPoison = true
		defer func() { seqScanDeformPoison = old }()
		if gathered {
			rows, ok := runJoinGathered(t, ctx, q, 3)
			if !ok {
				t.Fatal("no Gather in the plan")
			}
			return rows
		}
		return renderRows(runSQL(t, ctx, q))
	}
	for _, gathered := range []bool{false, true} {
		got := run(gathered)
		scanBorrowDisabled = true
		want := run(gathered)
		scanBorrowDisabled = false
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("gathered=%v: borrowed and cloned results differ (%d vs %d rows)", gathered, len(got), len(want))
		}
		if len(got) == 0 {
			t.Fatalf("gathered=%v: no rows", gathered)
		}
	}
}
