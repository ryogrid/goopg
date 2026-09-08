package executor

// e07_worker_dispatch_bench_test.go — E-07 (EX5-01 slab parity for `Gather`)
// adjudication witness.
//
// E-07's only surviving justification is the independent claim that slab
// dispatch (opOpen/opNext over an opTreeSlab) beats the legacy `Operator`
// interface on WORKER trees. Workers build via BuildWorker -> buildNode, i.e.
// the legacy tree; the leader's live path is BuildFastIterator. The claim has
// never been measured on either path in isolation, and E-04's lesson is that a
// sub-1% predicted effect cannot be read off a suite total.
//
// This benchmark measures the delta directly, on the node shape a TPC-H worker
// subtree actually has (scan -> filter -> project over a real heap), by driving
// the SAME plan through both builders in the same process. It is a
// measurement apparatus, not a production path: it asserts only that both arms
// return the same row count.

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
)

// e07SeedRows inserts n rows into items in batches, so the scan below has a
// realistic number of pages and the per-row dispatch cost is what varies.
func e07SeedRows(b *testing.B, ctx *Context, tbl *catalog.Table, n int) {
	b.Helper()
	const batch = 500
	for base := 0; base < n; base += batch {
		advanceStmtCounter(ctx)
		rows := make([][]optimizer.Expr, 0, batch)
		for i := base; i < base+batch && i < n; i++ {
			rows = append(rows, []optimizer.Expr{
				&optimizer.IntegerConst{Value: int64(i)},
				&optimizer.StringConst{Value: fmt.Sprintf("label-%06d", i)},
			})
		}
		in := &optimizer.Insert{Table: tbl, Source: &optimizer.Values{Rows: rows}, ColumnIndex: []int{0, 1}}
		op, err := Build(in)
		if err != nil {
			b.Fatal(err)
		}
		if err := op.Open(ctx); err != nil {
			b.Fatal(err)
		}
		if _, err := op.Next(); err != EOF {
			b.Fatalf("seed insert: %v", err)
		}
		_ = op.Close()
	}
	advanceStmtCounter(ctx)
}

func e07PlanOne(b *testing.B, sql string, cat catalog.Catalog) optimizer.Node {
	b.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		b.Fatal(err)
	}
	plan, err := optimizer.Plan(stmts[0], cat)
	if err != nil {
		b.Fatal(err)
	}
	return plan
}

const e07Rows = 50000

// BenchmarkE07WorkerDispatchLegacy drains the witness plan through
// Build + Operator.Next — the path a Gather worker takes today.
func BenchmarkE07WorkerDispatchLegacy(b *testing.B) {
	ctx, cat, cleanup := newStorageFixture(b)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	e07SeedRows(b, ctx, tbl, e07Rows)
	plan := e07PlanOne(b, "SELECT id + 1, label FROM items WHERE id > 0", cat)

	b.ReportAllocs()
	b.ResetTimer()
	total := 0
	for i := 0; i < b.N; i++ {
		op, err := Build(plan)
		if err != nil {
			b.Fatal(err)
		}
		if err := op.Open(ctx); err != nil {
			b.Fatal(err)
		}
		for {
			_, err := op.Next()
			if err == EOF {
				break
			}
			if err != nil {
				b.Fatal(err)
			}
			total++
		}
		_ = op.Close()
	}
	b.StopTimer()
	if b.N > 0 && total/b.N != e07Rows-1 {
		b.Fatalf("legacy arm produced %d rows/iter, want %d", total/b.N, e07Rows-1)
	}
	b.ReportMetric(float64(total)/float64(b.N), "rows/op")
}

// BenchmarkE07WorkerDispatchSlab drains the same plan through
// BuildFast + opNext — the dispatch E-07 proposes to give workers.
func BenchmarkE07WorkerDispatchSlab(b *testing.B) {
	ctx, cat, cleanup := newStorageFixture(b)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	e07SeedRows(b, ctx, tbl, e07Rows)
	plan := e07PlanOne(b, "SELECT id + 1, label FROM items WHERE id > 0", cat)

	b.ReportAllocs()
	b.ResetTimer()
	total := 0
	var dst Slot
	for i := 0; i < b.N; i++ {
		tree, rootIdx, err := BuildFast(plan)
		if err != nil {
			b.Fatal(err)
		}
		if err := opOpen(tree, rootIdx, ctx); err != nil {
			b.Fatal(err)
		}
		for {
			dst.Reset()
			err := opNext(tree, rootIdx, &dst)
			if err == EOF {
				break
			}
			if err != nil {
				b.Fatal(err)
			}
			total++
		}
		_ = opClose(tree, rootIdx)
	}
	b.StopTimer()
	if b.N > 0 && total/b.N != e07Rows-1 {
		b.Fatalf("slab arm produced %d rows/iter, want %d", total/b.N, e07Rows-1)
	}
	b.ReportMetric(float64(total)/float64(b.N), "rows/op")
}
