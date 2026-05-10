package executor

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// TestDDLCreateTimestampBTreeIndexAcceptsType pins the M0044-0003
// acceptance contract: CREATE INDEX on a timestamp column succeeds.
// Pre-M0044-0003 this aborted with `0A000 btree v0 only supports
// int4 / numeric keys`. Seeds rows with dates spanning the TPC-H
// date range and verifies each is searchable.
func TestDDLCreateTimestampBTreeIndexAcceptsType(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE shipments (id int, shipdate timestamp)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "shipments"})
	rel := ctx.Catalog.RelFileNode(tbl)

	dates := []time.Time{
		time.Date(1993, 3, 15, 0, 0, 0, 0, time.UTC),
		time.Date(1995, 6, 20, 0, 0, 0, 0, time.UTC),
		time.Date(1997, 11, 1, 0, 0, 0, 0, time.UTC),
	}
	for i, d := range dates {
		if err := writeHeapRow(ctx, rel, tbl.Columns, Row{
			{Kind: KindInt, Int: int64(i + 1)},
			NewTimeDatum(d),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := runDDL(t, ctx, "CREATE INDEX idx_shipdate ON shipments (shipdate)"); err != nil {
		t.Fatalf("CREATE INDEX on timestamp column: %v", err)
	}
	idx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Name: "idx_shipdate"})
	if !ok {
		t.Fatal("index not in catalog after CREATE INDEX")
	}
	idxRel := ctx.Catalog.IndexRelFileNode(idx)
	tree, err := btree.Open(ctx.Pool, idxRel)
	if err != nil {
		t.Fatal(err)
	}

	for _, d := range dates {
		micros := d.Sub(pgEpoch).Microseconds()
		key := btree.EncodeTimestamp(micros)
		_, found, err := tree.Search(key)
		if err != nil || !found {
			t.Fatalf("Search(%v): found=%v err=%v", d, found, err)
		}
	}
}

// TestDDLTimestampRangeScanParity verifies that a RangeScan over a
// timestamp index returns exactly the same rows as a sequential scan
// for the same date range. This tests the core TPC-H use case:
// queries that filter lineitem by l_shipdate with date ranges.
func TestDDLTimestampRangeScanParity(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE lineitem (id int, shipdate timestamp)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "lineitem"})
	rel := ctx.Catalog.RelFileNode(tbl)

	// Seed: 6 rows, 3 in [1995-01-01, 1995-12-31] and 3 outside.
	allDates := []time.Time{
		time.Date(1994, 12, 31, 0, 0, 0, 0, time.UTC), // outside
		time.Date(1995, 1, 1, 0, 0, 0, 0, time.UTC),   // inside (boundary)
		time.Date(1995, 6, 15, 0, 0, 0, 0, time.UTC),  // inside
		time.Date(1995, 12, 31, 0, 0, 0, 0, time.UTC), // inside (boundary)
		time.Date(1996, 1, 1, 0, 0, 0, 0, time.UTC),   // outside
		time.Date(1997, 3, 1, 0, 0, 0, 0, time.UTC),   // outside
	}
	for i, d := range allDates {
		if err := writeHeapRow(ctx, rel, tbl.Columns, Row{
			{Kind: KindInt, Int: int64(i + 1)},
			NewTimeDatum(d),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := runDDL(t, ctx, "CREATE INDEX idx_shipdate ON lineitem (shipdate)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	idx, _ := ctx.Catalog.LookupIndex(parser.ObjectName{Name: "idx_shipdate"})
	idxRel := ctx.Catalog.IndexRelFileNode(idx)
	tree, err := btree.Open(ctx.Pool, idxRel)
	if err != nil {
		t.Fatal(err)
	}

	// RangeScan [1995-01-01, 1995-12-31] — should return exactly 3 rows.
	lo := btree.EncodeTimestamp(time.Date(1995, 1, 1, 0, 0, 0, 0, time.UTC).Sub(pgEpoch).Microseconds())
	hi := btree.EncodeTimestamp(time.Date(1995, 12, 31, 0, 0, 0, 0, time.UTC).Sub(pgEpoch).Microseconds())
	indexCount := 0
	if err := tree.RangeScan(lo, hi, func(_ []byte, _ storage.ItemPointer) (bool, error) {
		indexCount++
		return true, nil
	}); err != nil {
		t.Fatalf("RangeScan: %v", err)
	}

	// Sequential count for same range (expected: 3).
	seqCount := 0
	rangeStart := time.Date(1995, 1, 1, 0, 0, 0, 0, time.UTC)
	rangeEnd := time.Date(1995, 12, 31, 0, 0, 0, 0, time.UTC)
	for _, d := range allDates {
		if !d.Before(rangeStart) && !d.After(rangeEnd) {
			seqCount++
		}
	}

	if indexCount != seqCount {
		t.Fatalf("parity: indexScan=%d seqScan=%d (want %d)", indexCount, seqCount, seqCount)
	}
	if indexCount != 3 {
		t.Fatalf("expected 3 rows in [1995-01-01, 1995-12-31], got %d", indexCount)
	}
}
