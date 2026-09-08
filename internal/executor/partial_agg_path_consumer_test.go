package executor

// C-19g / P5-07's executor consumer check.
//
// The planner slice is `internal/optimizer/partialaggpaths.go`
// (`create_partial_grouping_paths`'s economics as a two-candidate path
// tournament) and its design is
// docs/design/planner-c19g-partial-agg/DESIGN.md. Its unit tests prove that the
// split candidate WINS by cost on a Q1-shaped aggregate and LOSES once worker
// duplication saturates the boundary.
//
// A planner-only pin is explicitly not enough. "An unwinnable path is an
// untested path" has fired four times in this workstream: C-19f's consumer
// check found two `createPlan` bugs unreachable until a Gather could win, and
// E-10 found a Gather Merge over a partial index path returning `(workers+1)x`
// every row IN THE CORRECT ORDER — which no ordering test could have caught and
// only a VALUES test did.
//
// So this file asserts, with the priced verdict live (`GOOPG_PARTIAL_AGG_PATHS=on`):
//
//	(1) an aggregate the tournament PRICES as a win actually executes as
//	    Finalize -> Gather -> Partial;
//	(2) its values are identical to the serial answer — the failure mode of a
//	    partial aggregation is a plausible, error-free, N-times-too-large sum;
//	(3) an aggregate the tournament prices as a LOSS is not split, so the
//	    verdict is doing work rather than returning true;
//	(4) the decomposability whitelist still refuses what it refused, with the
//	    priced model in charge of the profitability half.

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// runAggSplitPriced is `runAggSplit` with the C-19g verdict live. The mode is
// process-global by design (read once at process start, so a plan cannot change
// shape mid-statement), so every caller must restore it.
func runAggSplitPriced(t *testing.T, ctx *Context, sql string, workers int) ([]string, bool) {
	t.Helper()
	restore := optimizer.SetPartialAggPathsMode("on")
	defer restore()
	return runAggSplit(t, ctx, sql, workers)
}

// pqAggPricedFixture is pqAggFixture with ANALYZE-visible statistics, because
// the priced model needs ABSOLUTE row and distinct counts where the retired
// ratio model needed only a fraction. Two tables, deliberately:
//
//   - `pq_priced_low` is Q1's shape (many rows, four groups) — the tournament
//     must price the split as a win;
//   - `pq_priced_uniq` gives every row its own group — the tournament must
//     price it as a loss, which is the Q18-inner shape the gate exists for.
func pqAggPricedFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	fail := func(err error) {
		cleanup()
		t.Fatalf("fixture: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE pq_priced_low (id int, grp int, v int)"); err != nil {
		fail(err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE pq_priced_uniq (id int, grp int, v int)"); err != nil {
		fail(err)
	}
	for i := 0; i < 400; i++ {
		if err := runDDL(t, ctx, fmt.Sprintf(
			"INSERT INTO pq_priced_low VALUES (%d, %d, %d)", i, i%4, i*3)); err != nil {
			fail(err)
		}
		if err := runDDL(t, ctx, fmt.Sprintf(
			"INSERT INTO pq_priced_uniq VALUES (%d, %d, %d)", i, i, i*3)); err != nil {
			fail(err)
		}
	}
	// The priced verdict reads EstimateRows / estimateNumGroups, and the blind
	// arm reads NDistinctFrac; both come from the catalog's statistics. Stamped
	// directly rather than through ANALYZE because ANALYZE leaves `RowCount` at
	// zero on this fixture path (the same startup gap the deferral ledger
	// records as pq-P6 — `internal/initdb/open.go` restores column statistics
	// but not the row count), and a test that ran blind would pin the FALLBACK
	// arm while claiming to pin the priced one.
	//
	// The two shapes are deliberate: `low` has four groups over 400 rows, so
	// what crosses the boundary shrinks a hundredfold; `uniq` has one group per
	// row, so every worker ships as many states as it read.
	stamp := func(name string, ndistinctGrp int64) {
		tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: name})
		if !ok {
			fail(fmt.Errorf("table %s vanished", name))
		}
		tbl.Stats = &catalog.TableStats{
			RowCount: 400,
			Columns: []catalog.ColumnStats{
				{NDistinct: 400, NDistinctFrac: 1},
				{NDistinct: ndistinctGrp, NDistinctFrac: float64(ndistinctGrp) / 400},
				{NDistinct: 400, NDistinctFrac: 1},
			},
		}
	}
	stamp("pq_priced_low", 4)
	stamp("pq_priced_uniq", 400)
	return ctx, cleanup
}

// TestPricedPartialAggExecutesAsPartialAndFinalize is the gate: a plan the
// PRICE model chose, executed, compared to serial by VALUE.
func TestPricedPartialAggExecutesAsPartialAndFinalize(t *testing.T) {
	ctx, cleanup := pqAggPricedFixture(t)
	defer cleanup()

	splits := 0
	for _, sql := range []string{
		"SELECT grp, count(*), sum(v) FROM pq_priced_low GROUP BY grp ORDER BY grp",
		"SELECT grp, avg(v), min(v), max(v) FROM pq_priced_low GROUP BY grp ORDER BY grp",
		"SELECT count(*), sum(v) FROM pq_priced_low",
		"SELECT grp, sum(v) FROM pq_priced_low WHERE v > 300 GROUP BY grp ORDER BY grp",
	} {
		t.Run(sql, func(t *testing.T) {
			serialRows, err := runQueryWithErr(ctx, sql)
			if err != nil {
				t.Fatalf("serial: %v", err)
			}
			want := renderRows(serialRows)
			for _, workers := range []int{1, 2, 4} {
				got, isSplit := runAggSplitPriced(t, ctx, sql, workers)
				if isSplit {
					splits++
				}
				if len(got) != len(want) {
					t.Fatalf("workers=%d: got %d rows, want %d\n got=%v\nwant=%v",
						workers, len(got), len(want), got, want)
				}
				for i := range want {
					if got[i] != want[i] {
						t.Fatalf("workers=%d: row %d differs:\n got %q\nwant %q\n"+
							"a value ~%dx too large means the parallel scan never "+
							"reached under the Partial aggregate, so every worker "+
							"aggregated the whole relation",
							workers, i, got[i], want[i], workers+1)
					}
				}
			}
		})
	}
	if splits == 0 {
		t.Fatal("the priced verdict never produced a Finalize aggregate on a " +
			"Q1-shaped fixture — this file compared serial against serial and " +
			"asserts nothing about the path model")
	}
}

// TestPricedPartialAggDeclinesASaturatedBoundary is the other direction: the
// verdict must be capable of saying no, or (1) above is satisfied by a
// predicate that returns true.
func TestPricedPartialAggDeclinesASaturatedBoundary(t *testing.T) {
	ctx, cleanup := pqAggPricedFixture(t)
	defer cleanup()

	const sql = "SELECT grp, sum(v) FROM pq_priced_uniq GROUP BY grp ORDER BY grp"
	serialRows, err := runQueryWithErr(ctx, sql)
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	want := renderRows(serialRows)

	for _, workers := range []int{2, 4} {
		got, isSplit := runAggSplitPriced(t, ctx, sql, workers)
		if isSplit {
			t.Errorf("workers=%d: the priced verdict split an aggregate whose "+
				"group count equals its input row count — every worker ships as "+
				"many states as it read and the split saves nothing", workers)
		}
		// Correctness holds either way: refusing the split leaves the Gather
		// BELOW the aggregate, which must still answer identically.
		if len(got) != len(want) {
			t.Fatalf("workers=%d: got %d rows, want %d", workers, len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("workers=%d: row %d differs:\n got %q\nwant %q",
					workers, i, got[i], want[i])
			}
		}
	}
}

// TestPricedPartialAggKeepsTheDecomposabilityRefusals pins that the priced
// verdict changed only the PROFITABILITY half. `aggregateSplitIsSafe` is a
// whitelist because the executor's `applyAgg` ends in a `default:` catch-all
// that would silently return garbage for a name added later, so a hole here is
// a wrong answer, not a missed plan.
func TestPricedPartialAggKeepsTheDecomposabilityRefusals(t *testing.T) {
	ctx, cleanup := pqAggPricedFixture(t)
	defer cleanup()

	for _, sql := range []string{
		"SELECT count(DISTINCT v) FROM pq_priced_low",
		"SELECT array_agg(v) FROM pq_priced_low",
		"SELECT string_agg(v::text, ',') FROM pq_priced_low",
	} {
		t.Run(sql, func(t *testing.T) {
			if _, isSplit := runAggSplitPriced(t, ctx, sql, 4); isSplit {
				t.Error("the priced verdict split an aggregate the whitelist refuses")
			}
		})
	}
}
