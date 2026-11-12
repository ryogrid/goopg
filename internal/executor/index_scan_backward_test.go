package executor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestIndexScanBackward pins M0148-0001c3a3 slice 2: an IndexScan with
// Backward set walks its probe's bounds from the high end down
// (nbtree.ReverseScanCursor) and returns exactly the forward scan's rows in
// reverse — for a leading range (whose open end stops at the NULL group),
// an equality prefix with duplicates, a DESC index (backward = ascending
// values) and an unbounded scan, with and without NULL-keyed entries. The
// plans are built forward by the planner and flipped by hand: no producer
// emits a backward path yet.
func TestIndexScanBackward(t *testing.T) {
	for _, capable := range []bool{false, true} {
		catalog.SetNullKeyedIndexEntries(capable)
		ctx, cleanup := newVMFixture(t)
		runSQL(t, ctx, "CREATE TABLE bw (a int, b int, c int NOT NULL)")
		runSQL(t, ctx, "INSERT INTO bw SELECT CASE WHEN g % 11 = 0 THEN NULL ELSE g % 700 END, g % 13, g FROM generate_series(1, 9000) g")
		for _, tc := range []struct{ ddl, q string }{
			{"CREATE INDEX bw_i ON bw (a)", "SELECT a, c FROM bw WHERE a > 100"},
			{"CREATE INDEX bw_i ON bw (a)", "SELECT a, c FROM bw WHERE a < 40"},
			{"CREATE INDEX bw_i ON bw (a)", "SELECT a, c FROM bw WHERE a >= 200 AND a < 260"},
			{"CREATE INDEX bw_i ON bw (b, c)", "SELECT a, c FROM bw WHERE b = 5"},
			{"CREATE INDEX bw_i ON bw (b, a)", "SELECT a, c FROM bw WHERE b = 5 AND a > 300"},
			{"CREATE INDEX bw_i ON bw (a DESC)", "SELECT a, c FROM bw WHERE a > 600"},
			{"CREATE INDEX bw_i ON bw (a)", "SELECT a, c FROM bw WHERE a = 77"},
		} {
			runSQL(t, ctx, "DROP INDEX IF EXISTS bw_i")
			runSQL(t, ctx, tc.ddl)
			runSQL(t, ctx, "ANALYZE bw")
			fwd := runIndexScanRows(t, ctx, tc.q, false)
			bwd := runIndexScanRows(t, ctx, tc.q, true)
			if len(fwd) < 2 {
				t.Fatalf("capable=%v %s %s: forward scan returned %d rows; the case tests nothing", capable, tc.ddl, tc.q, len(fwd))
			}
			if len(bwd) != len(fwd) {
				t.Fatalf("capable=%v %s %s: backward %d rows, forward %d", capable, tc.ddl, tc.q, len(bwd), len(fwd))
			}
			for i := range bwd {
				if bwd[i] != fwd[len(fwd)-1-i] {
					t.Fatalf("capable=%v %s %s: backward row %d = %s, want %s", capable, tc.ddl, tc.q, i, bwd[i], fwd[len(fwd)-1-i])
				}
			}
		}
		cleanup()
	}
	catalog.SetNullKeyedIndexEntries(false)
}

// runIndexScanRows plans q with seq and bitmap scans off, requires an
// IndexScan, sets its direction, and returns the rows in output order.
func runIndexScanRows(t *testing.T, ctx *Context, q string, backward bool) []string {
	t.Helper()
	stmts, err := parser.Parse(q)
	if err != nil {
		t.Fatal(err)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableSeqScan, ps.EnableBitmapScan = false, false
	ps.MaxParallelWorkersPerGather = 0
	advanceStmtCounter(ctx)
	plan, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, ps)
	if err != nil {
		t.Fatal(err)
	}
	var is *optimizer.IndexScan
	for n := plan; n != nil && is == nil; {
		switch v := n.(type) {
		case *optimizer.IndexScan:
			is = v
		case *optimizer.Project:
			n = v.Child
		case *optimizer.Filter:
			n = v.Child
		default:
			n = nil
		}
	}
	if is == nil {
		t.Fatalf("%s: no Index Scan (%T)", q, plan)
	}
	is.Backward = backward
	if backward {
		if got := describePlan(is, nil); !strings.HasPrefix(got, "Index Scan Backward using ") {
			t.Fatalf("%s: label %q, want Index Scan Backward", q, got)
		}
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer op.Close()
	var out []string
	for {
		slot, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		r := slot.Row()
		cells := make([]string, len(r))
		for i, d := range r {
			if d.IsNull() {
				cells[i] = "NULL"
			} else {
				cells[i] = fmt.Sprint(d.Int)
			}
		}
		out = append(out, strings.Join(cells, ","))
		slot.Release()
	}
	return out
}
