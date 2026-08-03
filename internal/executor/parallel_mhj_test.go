package executor

// Chapter 12 — parallel multi-way hash join.
//
// The correctness property is the same as every parallel operator's: the set
// of rows a parallel MHJ returns must equal what the serial MHJ returns. What
// is specific to MHJ, and what these tests target, is that ONLY the probe scan
// is partitioned — each worker rebuilds its own dimension tables from the full
// build scans. If attachParallelScan reached a build child instead of the
// probe, that worker's dimension table would be missing rows and the join
// would silently drop matches; if it reached nothing, every worker would scan
// the whole fact table and the result would be an N-times multiple.

import (
	"fmt"
	"sort"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// mhjFixture builds a fact table and two dimension tables sized so the planner
// collapses the three-way inner-hash-join into a MultiHashJoin driven off the
// fact table.
func mhjFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	fail := func(err error, what string) {
		cleanup()
		t.Fatalf("%s: %v", what, err)
	}
	for _, ddl := range []string{
		"CREATE TABLE mhj_d1 (d1k int, d1v text)",
		"CREATE TABLE mhj_d2 (d2k int, d2v text)",
		"CREATE TABLE mhj_fact (fid int, k1 int, k2 int, amt int)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			fail(err, ddl)
		}
	}
	// Small dimensions.
	for i := 0; i < 20; i++ {
		if err := runDDL(t, ctx, fmt.Sprintf("INSERT INTO mhj_d1 VALUES (%d, 'a-%d')", i, i)); err != nil {
			fail(err, "insert d1")
		}
		if err := runDDL(t, ctx, fmt.Sprintf("INSERT INTO mhj_d2 VALUES (%d, 'b-%d')", i, i)); err != nil {
			fail(err, "insert d2")
		}
	}
	// A larger fact table, with keys that both hit and miss the dimensions.
	for i := 0; i < 500; i++ {
		k1 := i % 25 // 20..24 have no d1 match
		k2 := i % 22 // 20..21 have no d2 match
		if err := runDDL(t, ctx, fmt.Sprintf(
			"INSERT INTO mhj_fact VALUES (%d, %d, %d, %d)", i, k1, k2, i)); err != nil {
			fail(err, "insert fact")
		}
	}
	return ctx, cleanup
}

// runMHJForced runs sql with Gather insertion forced on or off. Statistics are
// forced present via a size-gate bypass so the Gather is actually inserted;
// the fixtures are too small to clear the real size gate.
func runMHJForced(t *testing.T, ctx *Context, sql string, parallel bool) []string {
	t.Helper()
	prev := planner.ParallelEnabled()
	planner.SetParallelEnabled(parallel)
	defer planner.SetParallelEnabled(prev)
	rows, err := runQueryWithErr(ctx, sql)
	if err != nil {
		t.Fatalf("%s (parallel=%v): %v", sql, parallel, err)
	}
	return renderRows(rows)
}

// mhjQueries are three- and four-table inner-join chains that collapse into a
// MultiHashJoin, with and without residual filters.
func mhjQueries() []string {
	return []string{
		"SELECT f.fid, d1.d1v, d2.d2v FROM mhj_fact f, mhj_d1 d1, mhj_d2 d2 " +
			"WHERE f.k1 = d1.d1k AND f.k2 = d2.d2k",
		"SELECT count(*) FROM mhj_fact f, mhj_d1 d1, mhj_d2 d2 " +
			"WHERE f.k1 = d1.d1k AND f.k2 = d2.d2k",
		// Residual filter over the concatenated output.
		"SELECT f.fid FROM mhj_fact f, mhj_d1 d1, mhj_d2 d2 " +
			"WHERE f.k1 = d1.d1k AND f.k2 = d2.d2k AND f.amt > 200",
		"SELECT f.fid, d1.d1v FROM mhj_fact f, mhj_d1 d1, mhj_d2 d2 " +
			"WHERE f.k1 = d1.d1k AND f.k2 = d2.d2k AND d1.d1k < 10",
	}
}

// TestMHJSerialParallelIdentity is the gate.
func TestMHJSerialParallelIdentity(t *testing.T) {
	// M0126-0011 retired MHJ packing as the default; these tests exercise
	// the MHJ executor and must opt back in (e85e5347 updated the three
	// planner-side MHJ tests but missed this file).
	planner.SetMHJPackingEnabled(true)
	defer planner.SetMHJPackingEnabled(false)
	ctx, cleanup := mhjFixture(t)
	defer cleanup()

	for _, sql := range mhjQueries() {
		t.Run(sql, func(t *testing.T) {
			serial := runMHJForced(t, ctx, sql, false)
			parallel := runMHJForced(t, ctx, sql, true)
			sort.Strings(serial)
			sort.Strings(parallel)
			if len(serial) != len(parallel) {
				t.Fatalf("row count differs: serial %d, parallel %d",
					len(serial), len(parallel))
			}
			for i := range serial {
				if serial[i] != parallel[i] {
					t.Fatalf("row %d differs: serial %q, parallel %q",
						i, serial[i], parallel[i])
				}
			}
		})
	}
}

// TestMHJParallelNoDuplicates drives the MHJ under a Gather directly, so a
// Gather is definitely present, and checks the union of workers' output is the
// join result ONCE. This is the shape that would reproduce the duplicate-rows
// bug if attachParallelScan failed to reach the probe scan under the MHJ.
func TestMHJParallelNoDuplicates(t *testing.T) {
	planner.SetMHJPackingEnabled(true)
	defer planner.SetMHJPackingEnabled(false)
	ctx, cleanup := mhjFixture(t)
	defer cleanup()

	sql := "SELECT f.fid, d1.d1v, d2.d2v FROM mhj_fact f, mhj_d1 d1, mhj_d2 d2 " +
		"WHERE f.k1 = d1.d1k AND f.k2 = d2.d2k"

	serialRows, err := runQueryWithErr(ctx, sql)
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	want := len(serialRows)
	if want == 0 {
		t.Fatal("fixture produced no join rows")
	}

	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	node, err := planner.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	mhj := findPlanMHJ(node)
	if mhj == nil {
		t.Fatalf("planner did not build a MultiHashJoin for %q; the test would "+
			"prove nothing", sql)
	}

	for _, workers := range []int{1, 2, 4} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			gathered := spliceGatherAboveMHJ(node, workers)
			ctx.MaxParallelWorkers = 8
			ctx.ParallelLeaderParticipation = true
			op, err := Build(gathered)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if err := op.Open(ctx); err != nil {
				t.Fatalf("open: %v", err)
			}
			n := 0
			for {
				_, err := op.Next()
				if err == EOF {
					break
				}
				if err != nil {
					t.Fatalf("next: %v", err)
				}
				n++
			}
			if err := op.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
			if n != want {
				t.Errorf("got %d rows, want %d — a %.2gx multiple means the "+
					"parallel scan did not reach the probe scan under the MHJ, "+
					"so every worker scanned the whole fact table",
					n, want, float64(n)/float64(want))
			}
		})
	}
}

func findPlanMHJ(n planner.Node) *planner.MultiHashJoin {
	found := (*planner.MultiHashJoin)(nil)
	var walk func(planner.Node)
	walk = func(cur planner.Node) {
		if cur == nil || found != nil {
			return
		}
		if m, ok := cur.(*planner.MultiHashJoin); ok {
			found = m
			return
		}
		for _, c := range planner.ParallelChildrenForTest(cur) {
			walk(c)
		}
	}
	walk(n)
	return found
}

// spliceGatherAboveMHJ wraps the MultiHashJoin in a Gather, leaving any nodes
// above it (Aggregate, Project) in place.
func spliceGatherAboveMHJ(n planner.Node, workers int) planner.Node {
	switch x := n.(type) {
	case *planner.MultiHashJoin:
		return planner.NewGather(0, x, workers)
	case *planner.Aggregate:
		c := *x
		c.Child = spliceGatherAboveMHJ(x.Child, workers)
		return &c
	case *planner.Project:
		c := *x
		c.Child = spliceGatherAboveMHJ(x.Child, workers)
		return &c
	case *planner.Filter:
		c := *x
		c.Child = spliceGatherAboveMHJ(x.Child, workers)
		return &c
	}
	return n
}
