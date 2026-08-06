package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// M0125-0003's FOURTH consumer of the estimate_rel_size block-count fallback.
//
// Stage 2 gave the bushy DP seed a cold-start row count, but
// `reorderCommaFromByCardinality` — which permutes the comma-FROM list *before*
// any of that runs — kept its own `Stats.RowCount <= 0 ⇒ decline` guard. Since
// `TableStats.RowCount` does not survive a restart (ledger pq-P6), that guard
// fired for EVERY base table on an S-cold server: the greedy reorder never ran
// at all, and the source order reached `planFromClause` untouched. The tests
// below pin the tier that replaces the guard, in both flag directions and in
// the one failure direction that must still decline.
//
// The fixture deliberately makes the fallback and the source order disagree:
// `big` is first in FROM and has the most blocks, so a reorder can only happen
// if a row count was actually derived — "the pass rewrote something" is not a
// strong enough assertion on its own.

// newColdReorderFixture builds a three-relation comma-FROM list whose tables
// have NO stored statistics (the S-cold state) and a live sizer that reports a
// different block count for each. Source order is big, mid, small; every pair
// is joined by an equality, so connectivity mode is not involved and the
// objective really is cardinality.
func newColdReorderFixture(t *testing.T, sizer func(storage.RelFileNode) (int64, bool)) (*parser.SelectStmt, catalog.Catalog) {
	t.Helper()
	c := catalog.NewInMemory()
	if sizer != nil {
		c.SetRelationSizer(sizer)
	}
	mk := func(name string, cols ...string) {
		defs := make([]catalog.Column, len(cols))
		for i, col := range cols {
			defs[i] = catalog.Column{Name: col, Type: catalog.Type{Name: "int4"}}
		}
		if _, err := c.CreateTable(parser.ObjectName{Name: name}, defs); err != nil {
			t.Fatalf("CreateTable %s: %v", name, err)
		}
		// No SetTableStats: this is the never-ANALYZEd / post-restart state.
	}
	mk("big", "big_id", "big_mid")
	mk("mid", "mid_id", "mid_small")
	mk("small", "small_id")

	sql := `select 1 from big, mid, small
	         where big_mid = mid_id and mid_small = small_id`
	stmt := parseOne(t, sql).(*parser.SelectStmt)
	return stmt, c
}

// blockSizer maps a relation's file node to a block count by CreateTable order:
// the fixture creates big, mid, small in that order, so their relfilenodes are
// increasing and the first gets the most blocks.
func blockSizer(t *testing.T, c catalog.Catalog, blocks map[string]int64) func(storage.RelFileNode) (int64, bool) {
	t.Helper()
	byNode := make(map[uint32]int64, len(blocks))
	for name, n := range blocks {
		tbl, ok := c.LookupTable(parser.ObjectName{Name: name})
		if !ok || tbl == nil {
			t.Fatalf("fixture table %q missing", name)
		}
		byNode[tbl.OID] = n
	}
	return func(rn storage.RelFileNode) (int64, bool) {
		if n, ok := byNode[rn.RelOid]; ok {
			return n, true
		}
		return 0, false
	}
}

// TestCommaFromReorderColdUsesRelSizeFallback is the landing invariant: at
// stage 2 an S-cold comma-FROM list is ordered by its block-derived row counts,
// smallest first. Before this change the pass returned `rewrote == false` here
// and `big, mid, small` — three fact tables joined largest-first — survived
// into the plan.
func TestCommaFromReorderColdUsesRelSizeFallback(t *testing.T) {
	defer SetRelSizeFallbackStage(SetRelSizeFallbackStage(0))

	stmt, c := newColdReorderFixture(t, nil)
	c.(*catalog.InMemory).SetRelationSizer(blockSizer(t, c, map[string]int64{
		"big": 5000, "mid": 500, "small": 50,
	}))

	SetRelSizeFallbackStage(2)
	_, newFR, rewrote := reorderCommaFromByCardinality(stmt, c)
	if !rewrote {
		t.Fatalf("stage 2: an S-cold comma-FROM list must be reordered from its block counts")
	}
	assertOrder(t, fromNames(t, newFR), []string{"small", "mid", "big"})
}

// TestCommaFromReorderColdStageGating is the inertness half. Below stage 2 the
// pass must behave exactly as it did before M0125-0003 — decline outright, so
// the source order survives. Stage 1 is asserted explicitly because stage 1's
// consumer (the SeqScan probe side) is shape-neutral by construction and must
// not leak into the join order; that separation is the whole reason the flag is
// staged (design §D4).
func TestCommaFromReorderColdStageGating(t *testing.T) {
	defer SetRelSizeFallbackStage(SetRelSizeFallbackStage(0))

	for _, stage := range []int{0, 1} {
		stmt, c := newColdReorderFixture(t, nil)
		c.(*catalog.InMemory).SetRelationSizer(blockSizer(t, c, map[string]int64{
			"big": 5000, "mid": 500, "small": 50,
		}))
		SetRelSizeFallbackStage(stage)
		_, _, rewrote := reorderCommaFromByCardinality(stmt, c)
		if rewrote {
			t.Errorf("stage %d: the fallback must not reach the comma-FROM reorder", stage)
		}
	}
}

// TestCommaFromReorderNoSizerStillDeclines is the failure direction. With no
// live block count available — every planner unit test with a bare
// `catalog.NewInMemory()`, and any embedded caller with no storage behind it —
// the fallback answers "no estimate" and the pass must decline rather than
// order three relations by a row count of 0, which would be a silent
// source-order-dependent permutation.
func TestCommaFromReorderNoSizerStillDeclines(t *testing.T) {
	defer SetRelSizeFallbackStage(SetRelSizeFallbackStage(0))
	SetRelSizeFallbackStage(2)

	stmt, c := newColdReorderFixture(t, nil)
	if _, _, rewrote := reorderCommaFromByCardinality(stmt, c); rewrote {
		t.Fatalf("with no live block count the pass must decline")
	}
}

// TestCommaFromReorderStoredCountOutranksFallback pins the tier ORDER at this
// site, matching `bushySeedRowCounts`. An ANALYZEd relation must be ordered by
// its stored count even when a block count is also available: design §D3's
// "flag-on == flag-off when ANALYZEd" invariant is what makes the measured
// W-arms a genuine control, and it has to hold at every consumer, not just the
// DP seed. Here the stored counts invert the block-count ranking, so a
// fallback leak would show up as the opposite permutation.
func TestCommaFromReorderStoredCountOutranksFallback(t *testing.T) {
	defer SetRelSizeFallbackStage(SetRelSizeFallbackStage(0))
	SetRelSizeFallbackStage(2)

	stmt, c := newColdReorderFixture(t, nil)
	im := c.(*catalog.InMemory)
	im.SetRelationSizer(blockSizer(t, c, map[string]int64{
		"big": 5000, "mid": 500, "small": 50,
	}))
	// Stored counts disagree with the block counts: `mid` is now the smallest
	// relation, so the walk must start there and take `small` second. The
	// fallback ranking would have produced small, mid, big.
	for name, rows := range map[string]int64{"mid": 10, "small": 100, "big": 100000} {
		tbl, ok := im.LookupTable(parser.ObjectName{Name: name})
		if !ok {
			t.Fatalf("fixture table %q missing", name)
		}
		im.SetTableStats(tbl, &catalog.TableStats{RowCount: rows, Analyzed: true})
	}

	_, newFR, rewrote := reorderCommaFromByCardinality(stmt, c)
	if !rewrote {
		t.Fatalf("expected the ANALYZEd list to be reordered")
	}
	assertOrder(t, fromNames(t, newFR), []string{"mid", "small", "big"})
}
