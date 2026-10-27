package executor

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// findBitmapHeapScanOp returns the first bitmap heap scan under op.
func findBitmapHeapScanOp(op Operator) *bitmapHeapScanOp {
	for op != nil {
		switch o := op.(type) {
		case *instrumentedOp:
			op = o.inner
		case *bitmapHeapScanOp:
			return o
		case *aggregateOp:
			op = o.child
		case *projectOp:
			op = o.child
		default:
			return nil
		}
	}
	return nil
}

// TestBitmapHeapScanBorrowMatchesClone pins M0122-0012 C21: under a
// borrowing aggregate (aggregateBorrowsScanRows) the bitmap heap scan hands
// up its reused scanRow instead of a per-row clone, as the seq scan does.
// Its rows are a reused slice whose text values live in a per-page arena, so
// the aggregates must give exactly the cloning path's results — plain and
// grouped, with text min/max (stored after MaterializeArena) — over a scan
// spanning many pages, and the borrow must actually be armed.
func TestBitmapHeapScanBorrowMatchesClone(t *testing.T) {
	ctx := deformW8Fixture(t)
	runSQL(t, ctx, `CREATE TABLE bhb (k int, g int, a int, s text, pad text)`)
	runSQL(t, ctx, `CREATE INDEX bhb_k ON bhb (k)`)
	runSQL(t, ctx, `INSERT INTO bhb SELECT x, x % 7, x * 3, 's' || (x % 113) || repeat('z', x % 9), repeat('p', 60)
		FROM generate_series(1, 6000) x`)
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableSeqScan = false
	ps.EnableIndexScan = false
	run := func(sql string, borrow bool) string {
		t.Helper()
		old := scanBorrowDisabled
		scanBorrowDisabled = !borrow
		defer func() { scanBorrowDisabled = old }()
		advanceStmtCounter(ctx)
		stmts, err := parser.Parse(sql)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, ps)
		if err != nil {
			t.Fatal(err)
		}
		op, err := Build(plan)
		if err != nil {
			t.Fatal(err)
		}
		bh := findBitmapHeapScanOp(op)
		if bh == nil {
			t.Fatalf("%s: no bitmap heap scan under the aggregate (plan %T)", sql, plan)
		}
		if bh.borrowRows != borrow {
			t.Fatalf("%s: borrowRows=%v, want %v", sql, bh.borrowRows, borrow)
		}
		rows, err := Run(op, ctx)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(rows))
		for i, r := range rows {
			out[i] = fmt.Sprint(renderRows([]Row{r}))
		}
		sort.Strings(out)
		return strings.Join(out, "\n")
	}
	for _, sql := range []string{
		`SELECT count(*), sum(a), avg(a), min(s), max(s) FROM bhb WHERE k BETWEEN 100 AND 5200`,
		`SELECT g, count(*), sum(a), min(s), max(s), avg(k) FROM bhb WHERE k BETWEEN 100 AND 5200 GROUP BY g`,
	} {
		if got, want := run(sql, true), run(sql, false); got != want {
			t.Fatalf("%s:\nborrowed:\n%s\ncloned:\n%s", sql, got, want)
		}
	}
}
