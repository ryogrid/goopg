package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// M0125-0043 — the small-dimension tag after its extinction as a name lookup.
//
// The two production writers that set `catalog.Table.SmallDimension` for a
// relation literally named `region` or `nation` are gone; `smallDimensionTag`
// derives the property from the relation's SIZE. These tests pin the three
// things that can silently go wrong:
//
//   - the two TPC-H tables the name tag used to cover must still be tagged, in
//     the state the benchmark actually runs in (S-cold, no ANALYZE) — if they
//     stop being tagged, `shouldAttachBeforeMHJ` disarms and Q8 / Q21 go back
//     to the M0077-0001 PASS→CANCEL regression;
//   - a fact table must NOT be tagged, in either state;
//   - a catalog with no storage behind it (every planner unit test) must see
//     "no estimate", never "tiny".
//
// Design: docs/design/0125-0043-smalldimension-name-tag-extinction.md

// tpchRegion is TPC-H `region`'s real schema: the widths feed the
// estimate_rel_size density, so a test that made the columns up would pin
// nothing about the benchmark this tag exists for.
func tpchRegion() *catalog.Table {
	return &catalog.Table{
		Name: "region",
		Columns: []catalog.Column{
			{Name: "r_regionkey", Type: catalog.Type{Name: "int4"}},
			{Name: "r_name", Type: catalog.Type{Name: "char", Args: []int64{25}}},
			{Name: "r_comment", Type: catalog.Type{Name: "varchar", Args: []int64{152}}},
		},
	}
}

// tpchLineitem is the fact table's schema, trimmed to the column set that
// determines its width class.
func tpchLineitem() *catalog.Table {
	return &catalog.Table{
		Name: "lineitem",
		Columns: []catalog.Column{
			{Name: "l_orderkey", Type: catalog.Type{Name: "int8"}},
			{Name: "l_partkey", Type: catalog.Type{Name: "int4"}},
			{Name: "l_suppkey", Type: catalog.Type{Name: "int4"}},
			{Name: "l_quantity", Type: catalog.Type{Name: "numeric", Args: []int64{15, 2}}},
			{Name: "l_shipdate", Type: catalog.Type{Name: "date"}},
			{Name: "l_shipinstruct", Type: catalog.Type{Name: "char", Args: []int64{25}}},
			{Name: "l_comment", Type: catalog.Type{Name: "varchar", Args: []int64{44}}},
		},
	}
}

func catWithBlocks(blocks int64) *catalog.InMemory {
	cat := catalog.NewInMemory()
	cat.SetRelationSizer(func(storage.RelFileNode) (int64, bool) { return blocks, true })
	return cat
}

// TestSmallDimensionTagCoversTPCHTinyDimensionsWhenCold is the acceptance
// property of the whole change: with no ANALYZE anywhere — the state
// `scripts/tpch-spotcheck.sh` and the 22-query stream run in — the relation
// that used to be tagged by NAME is still tagged, now by size.
//
// The mechanism is upstream's never-analyzed 10-page floor: `region` occupies
// one block at SF=1, so estimate_rel_size credits it with 10 pages' worth of
// rows rather than believing a fresh relation stays tiny. That is far below
// smallDimensionMaxRows, which is the point — the threshold does not need to
// resolve 5 rows from 25, it needs to separate "fits in a handful of blocks"
// from "is a fact table".
func TestSmallDimensionTagCoversTPCHTinyDimensionsWhenCold(t *testing.T) {
	cat := catWithBlocks(1)
	region := tpchRegion()

	rows := smallDimensionRows(cat, region)
	if rows <= 0 || rows > smallDimensionMaxRows {
		t.Fatalf("cold region estimates %d rows; must be in (0, %d] to stay tagged", rows, smallDimensionMaxRows)
	}
	if !smallDimensionTag(cat, region) {
		t.Error("cold TPC-H region must be tagged a small dimension")
	}
}

// TestSmallDimensionTagRejectsFactTable is the other half: the tag must not
// spread to a relation with real data behind it, in either statistics state.
func TestSmallDimensionTagRejectsFactTable(t *testing.T) {
	lineitem := tpchLineitem()

	// Cold, with a fact-sized heap (SF=1 lineitem is ~100k blocks; 20k is
	// already far past any plausible threshold).
	if smallDimensionTag(catWithBlocks(20000), lineitem) {
		t.Error("a cold fact table must not be tagged a small dimension")
	}

	// Warm: ANALYZE ran this session and reports 6M rows.
	warm := tpchLineitem()
	warm.Stats = &catalog.TableStats{RowCount: 6001215, Pages: 100000, Analyzed: true}
	if smallDimensionTag(catWithBlocks(100000), warm) {
		t.Error("an ANALYZEd fact table must not be tagged a small dimension")
	}
}

// TestSmallDimensionTagUsesAnalyzedRowCount pins the warm path: when this
// session has ANALYZE stats they decide, and the block-count estimate is not
// consulted. The fixture deliberately gives a tiny relation a LARGE block
// count so the two sources disagree and the winner is visible.
func TestSmallDimensionTagUsesAnalyzedRowCount(t *testing.T) {
	region := tpchRegion()
	region.Stats = &catalog.TableStats{RowCount: 5, Pages: 1, Analyzed: true}
	if !smallDimensionTag(catWithBlocks(50000), region) {
		t.Error("an ANALYZEd 5-row relation must be tagged regardless of its block count")
	}

	big := tpchRegion()
	big.Name = "grown"
	big.Stats = &catalog.TableStats{RowCount: smallDimensionMaxRows + 1, Pages: 500, Analyzed: true}
	if smallDimensionTag(catWithBlocks(1), big) {
		t.Error("an ANALYZEd relation past the threshold must not be tagged, however few blocks the sizer reports")
	}
}

// TestSmallDimensionTagWithoutStorageIsNotSmall covers the failure direction
// that would be silently destructive: a catalog that cannot report a block
// count yields "no estimate", and "no estimate" must never read as "tiny".
// Every planner unit test builds such a catalog, so the opposite reading would
// flip plan shapes across the suite rather than in one place.
func TestSmallDimensionTagWithoutStorageIsNotSmall(t *testing.T) {
	if smallDimensionTag(catalog.NewInMemory(), tpchRegion()) {
		t.Error("no relation sizer must mean not-small")
	}
	if smallDimensionTag(nil, tpchRegion()) {
		t.Error("nil catalog must mean not-small")
	}
	if smallDimensionTag(catWithBlocks(1), nil) {
		t.Error("nil table must mean not-small")
	}
	if smallDimensionTag(catWithBlocks(1), &catalog.Table{Name: "v", Virtual: true}) {
		t.Error("a virtual relation has no heap to measure and must not be tagged")
	}
}

// TestSmallDimensionTagHonoursExplicitHint keeps the fixture path alive:
// `internal/testutil/tpch/tpch.go` sets `SmallDimension` on catalog-only
// tables that have no heap, and those fixtures back the planner's TPC-H plan
// tests. The hint survives as an override with no production writer.
func TestSmallDimensionTagHonoursExplicitHint(t *testing.T) {
	hinted := &catalog.Table{Name: "anything", SmallDimension: true}
	if !smallDimensionTag(catalog.NewInMemory(), hinted) {
		t.Error("an explicit catalog hint must be honoured even with no storage")
	}
}

// TestSmallDimensionTagIgnoresRelSizeFallbackStage pins a deliberate
// asymmetry. GOOPG_RELSIZE_FALLBACK stages which COST consumers trust a
// block-derived cardinality, and turning it off must restore pre-M0125-0003
// plans — plans in which the small-dimension property was populated at every
// stage (by name). Gating this derivation on the same knob would make
// `GOOPG_RELSIZE_FALLBACK=0` also delete M0054-0010's build-side pinning,
// which is not what the knob promises.
func TestSmallDimensionTagIgnoresRelSizeFallbackStage(t *testing.T) {
	defer SetRelSizeFallbackStage(SetRelSizeFallbackStage(0))
	cat := catWithBlocks(1)
	region := tpchRegion()

	for _, stage := range []int{0, 1, 2, 3} {
		SetRelSizeFallbackStage(stage)
		if !smallDimensionTag(cat, region) {
			t.Errorf("stage %d: the small-dimension tag must not depend on the relsize knob", stage)
		}
	}
}

// TestIsSmallDimensionSideReadsTheNodeTag pins the read side after the move off
// `catalog.Table`: the answer travels on the scan node (including through the
// Filter / Project / Sort wrappers the join planner meets), so an IndexScan
// promoted from a tagged SeqScan keeps the tag and an untagged scan on a table
// that merely carries the old catalog field does not resurrect it.
func TestIsSmallDimensionSideReadsTheNodeTag(t *testing.T) {
	tbl := tpchRegion()
	tagged := &SeqScan{Table: tbl, SmallDim: true}

	if !IsSmallDimensionSide(tagged) {
		t.Error("a tagged SeqScan must read as a small-dimension side")
	}
	if !IsSmallDimensionSide(&Filter{Child: &Project{Child: tagged}}) {
		t.Error("the tag must survive the Filter/Project wrappers the join planner meets")
	}
	if !IsSmallDimensionSide(&IndexScan{Table: tbl, SmallDim: true}) {
		t.Error("an IndexScan promoted from a tagged leaf must keep the tag")
	}
	if IsSmallDimensionSide(&SeqScan{Table: tbl}) {
		t.Error("an unstamped scan must not be a small-dimension side")
	}
}
