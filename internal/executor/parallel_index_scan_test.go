package executor

// C-19c (P5-03): a plain index scan as a Gather's driving scan. The IOS's
// leaf-block partition (M0134-0189) is reused by indexScanOp; these tests are
// the executor consumer check — the planner must not offer a shape the
// executor cannot run correctly.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// parallelIndexScanFixture: enough rows that the btree spans several leaf
// blocks, or the claim set would hand every entry to one worker and the test
// could not tell a partitioned scan from a serial one.
func parallelIndexScanFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	if err := runDDL(t, ctx, "CREATE TABLE pq_pidx (id int, grp int)"); err != nil {
		cleanup()
		t.Fatalf("create: %v", err)
	}
	const n = 3000
	if err := runDDL(t, ctx, fmt.Sprintf("INSERT INTO pq_pidx SELECT g, g %% 7 FROM generate_series(1, %d) g", n)); err != nil {
		for i := 1; i <= n; i++ {
			if err := runDDL(t, ctx, fmt.Sprintf("INSERT INTO pq_pidx VALUES (%d, %d)", i, i%7)); err != nil {
				cleanup()
				t.Fatalf("insert %d: %v", i, err)
			}
		}
	}
	if err := runDDL(t, ctx, "CREATE INDEX pq_pidx_id ON pq_pidx (id)"); err != nil {
		cleanup()
		t.Fatalf("create index: %v", err)
	}
	return ctx, cleanup
}

// drivingIndexScan finds the plain index scan a Filter/Project spine reads.
func drivingIndexScan(n optimizer.Node) *optimizer.IndexScan {
	switch x := n.(type) {
	case *optimizer.IndexScan:
		return x
	case *optimizer.Filter:
		return drivingIndexScan(x.Child)
	case *optimizer.Project:
		return drivingIndexScan(x.Child)
	}
	return nil
}

// TestParallelIndexScanIdentityWithRealGather drives the Gather operator over
// a real plain index range scan: the union of the workers' output must be the
// serial result exactly once, and the leaf claim set must have been consulted
// for more than one block — proof the partition engaged rather than every
// worker scanning everything (which the row count would catch) or one worker
// scanning everything (which it would not).
func TestParallelIndexScanIdentityWithRealGather(t *testing.T) {
	ctx, cleanup := parallelIndexScanFixture(t)
	defer cleanup()

	const sql = "SELECT id, grp FROM pq_pidx WHERE id >= 100" // grp keeps it off the IOS promotion
	serialRows, err := runQueryWithErr(ctx, sql)
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	want := len(serialRows)
	if want != 2901 {
		t.Fatalf("fixture: got %d rows, want 2901", want)
	}

	for _, workers := range []int{1, 2, 4} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			stmts, err := parser.Parse(sql)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			node, err := optimizer.Plan(stmts[0], ctx.Catalog)
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			scan := drivingIndexScan(node)
			if scan == nil || scan.LowKey == nil || scan.Key != nil || len(scan.Keys) != 0 {
				t.Fatalf("fixture must plan a plain RANGE index scan, got %T (%+v)", node, node)
			}
			gathered := optimizer.NewGather(0, node, workers)

			ctx.MaxParallelWorkers = 8
			ctx.ParallelLeaderParticipation = true
			op, err := Build(gathered)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			g, ok := op.(*gatherOp)
			if !ok {
				t.Fatalf("Build(Gather) returned %T, want *gatherOp", op)
			}
			if err := op.Open(ctx); err != nil {
				t.Fatalf("open: %v", err)
			}
			seen := map[string]int{}
			n := 0
			for {
				slot, err := op.Next()
				if err == EOF {
					break
				}
				if err != nil {
					t.Fatalf("next: %v", err)
				}
				seen[datumTestString(slot.Row()[0])]++
				n++
			}
			claimed := g.pidx.claimedBlocks()
			if err := op.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
			if n != want {
				t.Errorf("got %d rows, want %d — a %.2gx multiple means every worker "+
					"scanned the whole index instead of its leaves", n, want, float64(n)/float64(want))
			}
			for v, c := range seen {
				if c != 1 {
					t.Errorf("row %s returned %d times; each leaf block must be claimed by exactly one worker", v, c)
					break
				}
			}
			if claimed < 2 {
				t.Errorf("leaf claim set handed out %d blocks; the fixture must span several leaves for the partition to be exercised", claimed)
			}
		})
	}
}

// TestExplainParallelIndexScanLabel: the post-pass admits a bare plain index
// scan (drivingScan), stamps it (stampParallelScan), and EXPLAIN renders PG's
// "Parallel Index Scan using ..." — plain and VERBOSE alike.
func TestExplainParallelIndexScanLabel(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := cat.CreateIndex(parser.ObjectName{Name: "t_a_idx"}, tbl, []string{"a"}, false, "btree", false)
	if err != nil {
		t.Fatal(err)
	}
	scan := &optimizer.IndexScan{Table: tbl, Index: idx, LowKey: &optimizer.NumericConst{Value: "1"}}

	serialText := renderPlanText(optimizer.MaybeAddGather(scan, optimizer.ParallelSettings{}))
	if !strings.Contains(serialText, "Index Scan using t_a_idx on t") || strings.Contains(serialText, "Parallel") {
		t.Fatalf("serial plan must be a bare index scan:\n%s", serialText)
	}

	parallelRoot := optimizer.MaybeAddGather(scan, parallelLabelTestSettings())
	if _, ok := parallelRoot.(*optimizer.Gather); !ok {
		t.Fatalf("expected a Gather at the root over a bare range index scan, got %T", parallelRoot)
	}
	for _, verbose := range []bool{false, true} {
		text := renderPlanTextOpts(parallelRoot, parser.ExplainOptions{Verbose: verbose})
		// VERBOSE schema-qualifies the relation (`on public.t`); the prefix
		// must be there in both forms.
		if !strings.Contains(text, "Parallel Index Scan using t_a_idx on ") {
			t.Errorf("verbose=%v: missing 'Parallel Index Scan using t_a_idx on ...':\n%s", verbose, text)
		}
	}
	if scan.Parallel {
		t.Error("the post-pass must not mutate the cached plan node")
	}

	// A point probe is NOT admitted: the post-pass sizes on the table, not on
	// what the probe fetches, so it stays serial until C-19d prices it.
	probe := &optimizer.IndexScan{Table: tbl, Index: idx, Key: &optimizer.NumericConst{Value: "1"}}
	if _, ok := optimizer.MaybeAddGather(probe, parallelLabelTestSettings()).(*optimizer.Gather); ok {
		t.Error("a point-probe index scan must not be gathered by the post-pass")
	}
}
