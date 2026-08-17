package executor

// P9 of docs/design/parallel-query/ — partial/finalize placement.
//
// P5 proved the combine RULES in isolation. This proves the PLACEMENT: that a
// plan the planner actually builds produces what serial execution produces.
//
// The failure this file exists to catch is an N-times overcount. If the
// parallel scan does not reach the scan UNDER the Partial aggregate, every
// worker aggregates the whole relation and the Finalize node combines N
// complete results. The output is a well-formed row set with plausible
// magnitudes and nothing anywhere reports an error — `sum` is simply N times
// too large. Only comparing against serial finds it.

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
)

// pqAggFixture uses values chosen so every exact aggregate is representable
// without rounding: the split must be BIT-identical for those, not merely
// close.
func pqAggFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	if err := runDDL(t, ctx,
		"CREATE TABLE pq_agg (id int, grp int, v int, f float8, s text)"); err != nil {
		cleanup()
		t.Fatalf("create: %v", err)
	}
	for i := 0; i < 400; i++ {
		// grp has 4 distinct values, mirroring Q1's shape: many rows, few
		// groups, so every group necessarily spans several workers.
		v := fmt.Sprintf("%d", i*3)
		if i%61 == 0 {
			v = "NULL" // NULL inputs must be skipped identically on both paths
		}
		if err := runDDL(t, ctx, fmt.Sprintf(
			"INSERT INTO pq_agg VALUES (%d, %d, %s, %d.5, 'r-%d')",
			i, i%4, v, i, i)); err != nil {
			cleanup()
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	return ctx, cleanup
}

func pqAggSettings(workers int) optimizer.ParallelSettings {
	return optimizer.ParallelSettings{
		MaxWorkersPerGather: workers,
		MinTableScanBlocks:  1,
		DebugParallelQuery:  "on", // fixtures are small; force past the size gate
		BlocksForTable:      func(*catalog.Table) (int64, bool) { return 4096, true },
	}
}

// runAggSplit plans sql, lets MaybeAddGather place the split, and returns the
// rows plus whether a Finalize node was actually built.
func runAggSplit(t *testing.T, ctx *Context, sql string, workers int) ([]string, bool) {
	t.Helper()
	// M0129-S8.3: advance the command counter between statements.
	advanceStmtCounter(ctx)
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	node, err := optimizer.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	gathered := optimizer.MaybeAddGather(node, pqAggSettings(workers))
	split := planHasFinalizeAgg(gathered)

	ctx.MaxParallelWorkers = 8
	ctx.ParallelLeaderParticipation = true
	op, err := Build(gathered)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("open: %v", err)
	}
	var out []string
	for {
		slot, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		out = append(out, renderRows([]Row{slot.Row()})...)
	}
	if err := op.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return out, split
}

func planHasFinalizeAgg(n optimizer.Node) bool {
	found := false
	var walk func(optimizer.Node)
	walk = func(cur optimizer.Node) {
		if cur == nil || found {
			return
		}
		if a, ok := cur.(*optimizer.Aggregate); ok && a.Mode == optimizer.AggModeFinal {
			found = true
			return
		}
		for _, c := range optimizer.ParallelChildrenForTest(cur) {
			walk(c)
		}
	}
	walk(n)
	return found
}

// TestPartialFinalizeIdentity is the gate.
//
// Output is compared IN ORDER, not as a set: the aggregate operator sorts by
// group key, so a split that lost or duplicated a group shows up positionally.
func TestPartialFinalizeIdentity(t *testing.T) {
	ctx, cleanup := pqAggFixture(t)
	defer cleanup()

	split := 0
	for _, sql := range []string{
		// Ungrouped — the "__all__" pre-seed path, where an empty partial from
		// some worker is routine.
		"SELECT count(*) FROM pq_agg",
		"SELECT sum(v), count(v), min(v), max(v) FROM pq_agg",
		"SELECT avg(v) FROM pq_agg",
		// Grouped, Q1's shape: many rows, four groups.
		"SELECT grp, count(*), sum(v) FROM pq_agg GROUP BY grp ORDER BY grp",
		"SELECT grp, avg(v), min(v), max(v) FROM pq_agg GROUP BY grp ORDER BY grp",
		"SELECT grp, count(v), count(*) FROM pq_agg GROUP BY grp ORDER BY grp",
		// With a filter below, so the partial subtree is Filter -> Scan.
		"SELECT grp, sum(v) FROM pq_agg WHERE v > 300 GROUP BY grp ORDER BY grp",
		// A filter that eliminates entire groups from some workers.
		"SELECT grp, count(*) FROM pq_agg WHERE id < 20 GROUP BY grp ORDER BY grp",
		// Float lane.
		"SELECT grp, sum(f) FROM pq_agg GROUP BY grp ORDER BY grp",
		// Expression arguments, not bare columns.
		"SELECT grp, sum(v * 2), sum(v + id) FROM pq_agg GROUP BY grp ORDER BY grp",
	} {
		t.Run(sql, func(t *testing.T) {
			serialRows, err := runQueryWithErr(ctx, sql)
			if err != nil {
				t.Fatalf("serial: %v", err)
			}
			want := renderRows(serialRows)

			for _, workers := range []int{1, 2, 4} {
				got, isSplit := runAggSplit(t, ctx, sql, workers)
				if isSplit {
					split++
				}
				if len(got) != len(want) {
					t.Fatalf("workers=%d: got %d rows, want %d\n got=%v\nwant=%v",
						workers, len(got), len(want), got, want)
				}
				for i := range want {
					if got[i] != want[i] {
						t.Fatalf("workers=%d: row %d differs:\n got %q\nwant %q\n"+
							"a value that is ~%d times too large means the parallel "+
							"scan never reached under the Partial aggregate, so every "+
							"worker aggregated the whole relation",
							workers, i, got[i], want[i], workers+1)
					}
				}
			}
		})
	}
	if split == 0 {
		t.Fatal("no query produced a Finalize aggregate; the comparison was " +
			"serial against serial and asserts nothing")
	}
}

// pqAggMultiset normalises a rendered array_agg/string_agg value for
// order-insensitive comparison: strip a surrounding array-literal `{`/`}`
// if present, split on `,`, sort the elements, rejoin. Safe here only
// because the pq_agg fixture's `s` ("r-%d") and `v` (bare integers) values
// contain no embedded commas or braces to be confused with delimiters.
func pqAggMultiset(s string) string {
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")
	parts := strings.Split(s, ",")
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// TestPartialAggregateRefusals pins the whitelist at the PLACEMENT level. P5
// tested the predicate; this tests that the planner honours it.
func TestPartialAggregateRefusals(t *testing.T) {
	ctx, cleanup := pqAggFixture(t)
	defer cleanup()

	cases := []struct {
		name string
		sql  string
		// orderSensitive: after M0134-0001 S16, a Gather may sit below the
		// plain (unsplit) Aggregate, so the element order this aggregate's
		// input arrives in is genuinely nondeterministic — and PG does not
		// guarantee it either (postgres/doc/src/sgml/parallel.sgml:101-104:
		// Gather "reads tuples from the workers in whatever order is
		// convenient, destroying any sort order that may have existed").
		// Asserting positional equality would pin a property PostgreSQL
		// itself does not offer, so these two compare as sorted multisets
		// instead — which still catches a dropped or duplicated row, the
		// real parallel-execution risk this test exists to guard against.
		orderSensitive bool
	}{
		// Each worker's distinct map sees only its own share. Result is
		// order-independent (a scalar count), so nothing here became
		// nondeterministic — keeps its exact positional comparison.
		{name: "distinct-agg", sql: "SELECT count(DISTINCT v) FROM pq_agg"},
		{name: "string_agg", sql: "SELECT string_agg(s, ',') FROM pq_agg", orderSensitive: true},
		{name: "array_agg", sql: "SELECT array_agg(v) FROM pq_agg", orderSensitive: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Correctness first: whatever the planner decides, the answer must
			// match serial.
			serialRows, err := runQueryWithErr(ctx, tc.sql)
			if err != nil {
				t.Skipf("serial execution unsupported for this shape: %v", err)
			}
			want := renderRows(serialRows)
			got, isSplit := runAggSplit(t, ctx, tc.sql, 4)
			if isSplit {
				t.Errorf("planner split an aggregate the whitelist refuses")
			}
			if len(got) != len(want) {
				t.Fatalf("got %d rows, want %d", len(got), len(want))
			}
			for i := range want {
				g, w := got[i], want[i]
				if tc.orderSensitive {
					g, w = pqAggMultiset(g), pqAggMultiset(w)
				}
				if g != w {
					t.Errorf("row %d: got %q, want %q", i, got[i], want[i])
				}
			}
		})
	}
}

// TestPartialAggregateAccumulatorRetracted: the accumulator is keyed by plan
// node on a session-lived context, so a leak would let a second execution of
// the same cached plan adopt the first one's states — doubling every value.
func TestPartialAggregateAccumulatorRetracted(t *testing.T) {
	ctx, cleanup := pqAggFixture(t)
	defer cleanup()

	sql := "SELECT grp, sum(v) FROM pq_agg GROUP BY grp ORDER BY grp"
	first, isSplit := runAggSplit(t, ctx, sql, 4)
	if !isSplit {
		t.Skip("planner declined to split this shape")
	}
	if n := len(ctx.PartialAggStates); n != 0 {
		t.Errorf("accumulator still registered after Open returned: %d entries", n)
	}
	// Running the SAME query again must give the same answer, not double it.
	second, _ := runAggSplit(t, ctx, sql, 4)
	if len(first) != len(second) {
		t.Fatalf("row counts differ between runs: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("row %d differs between two runs of the same query: %q vs %q\n"+
				"a stale accumulator would combine this run's states into the "+
				"previous run's",
				i, first[i], second[i])
		}
	}
}
