package executor

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// dateBTreeKey mirrors the encodeBTreeKeyForColumn date path: a date is stored
// as int32 days since the PG epoch (2000-01-01) and encoded via the
// order-preserving int4 key encoder.
func dateBTreeKey(d time.Time) []byte {
	micros := d.UTC().UnixMicro() - pgEpochUnixMicros
	days := int32(micros / (24 * 3600 * 1000000))
	return btree.EncodeInt4(days)
}

// TestDDLCreateDateBTreeIndexAcceptsType pins the M0118-0001 acceptance
// contract: CREATE INDEX on a date column succeeds. Before M0118-0001 this
// aborted with `0A000 btree v0 only supports int4 / numeric keys, got "date"`,
// which blocked the temporal-range-integrity isolation spec (composite PK
// (text, date)). Seeds rows spanning a date range and verifies each is
// searchable through the int4-days key encoding.
func TestDDLCreateDateBTreeIndexAcceptsType(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE events (id int, eff_date date)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "events"})
	rel := ctx.Catalog.RelFileNode(tbl)

	dates := []time.Time{
		time.Date(1999, 12, 31, 0, 0, 0, 0, time.UTC), // before epoch boundary
		time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),   // epoch (days == 0)
		time.Date(2008, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2009, 5, 15, 0, 0, 0, 0, time.UTC),
	}
	for i, d := range dates {
		if err := writeHeapRow(ctx, rel, tbl.Columns, Row{
			{Kind: KindInt, Int: int64(i + 1)},
			NewTimeDatum(d),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := runDDL(t, ctx, "CREATE INDEX idx_eff_date ON events (eff_date)"); err != nil {
		t.Fatalf("CREATE INDEX on date column: %v", err)
	}
	idx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Name: "idx_eff_date"})
	if !ok {
		t.Fatal("index not in catalog after CREATE INDEX")
	}
	idxRel := ctx.Catalog.IndexRelFileNode(idx)
	tree, err := btree.Open(ctx.Pool, idxRel)
	if err != nil {
		t.Fatal(err)
	}

	for _, d := range dates {
		key := dateBTreeKey(d)
		_, found, err := tree.Search(key)
		if err != nil || !found {
			t.Fatalf("Search(%v): found=%v err=%v", d, found, err)
		}
	}
}

// TestDDLDateRangeScanParity verifies a RangeScan over a date index returns
// exactly the rows a sequential scan would for the same range — the access
// pattern temporal-range-integrity's predicate reads depend on. Boundaries are
// inclusive on both ends (matching btree RangeScan semantics).
func TestDDLDateRangeScanParity(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE statute (id int, eff_date date)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "statute"})
	rel := ctx.Catalog.RelFileNode(tbl)

	allDates := []time.Time{
		time.Date(2007, 12, 31, 0, 0, 0, 0, time.UTC), // outside
		time.Date(2008, 1, 1, 0, 0, 0, 0, time.UTC),   // inside (boundary)
		time.Date(2008, 6, 15, 0, 0, 0, 0, time.UTC),  // inside
		time.Date(2009, 5, 15, 0, 0, 0, 0, time.UTC),  // inside (boundary)
		time.Date(2009, 5, 16, 0, 0, 0, 0, time.UTC),  // outside
	}
	for i, d := range allDates {
		if err := writeHeapRow(ctx, rel, tbl.Columns, Row{
			{Kind: KindInt, Int: int64(i + 1)},
			NewTimeDatum(d),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := runDDL(t, ctx, "CREATE INDEX idx_eff_date ON statute (eff_date)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	idx, _ := ctx.Catalog.LookupIndex(parser.ObjectName{Name: "idx_eff_date"})
	idxRel := ctx.Catalog.IndexRelFileNode(idx)
	tree, err := btree.Open(ctx.Pool, idxRel)
	if err != nil {
		t.Fatal(err)
	}

	rangeStart := time.Date(2008, 1, 1, 0, 0, 0, 0, time.UTC)
	rangeEnd := time.Date(2009, 5, 15, 0, 0, 0, 0, time.UTC)
	lo := dateBTreeKey(rangeStart)
	hi := dateBTreeKey(rangeEnd)
	indexCount := 0
	if err := tree.RangeScan(lo, hi, func(_ []byte, _ storage.ItemPointer) (bool, error) {
		indexCount++
		return true, nil
	}); err != nil {
		t.Fatalf("RangeScan: %v", err)
	}

	seqCount := 0
	for _, d := range allDates {
		if !d.Before(rangeStart) && !d.After(rangeEnd) {
			seqCount++
		}
	}

	if indexCount != seqCount {
		t.Fatalf("parity: indexScan=%d seqScan=%d", indexCount, seqCount)
	}
	if indexCount != 3 {
		t.Fatalf("expected 3 rows in [2008-01-01, 2009-05-15], got %d", indexCount)
	}
}
