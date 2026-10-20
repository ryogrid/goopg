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
	var out []string
	for _, r := range runSQL(t, ctx, sql) {
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
// build side's scan, kept in the hash table, never borrows. With tail poison
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
		if got := borrowedScanTables(op); strings.Join(got, ",") != "bp" {
			t.Fatalf("%s: borrowing scans %v, want only the probe side bp\n%#v", q, got, plan)
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
