package executor

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// timestamptzBTreeKey mirrors the encodeBTreeKeyForColumn timestamptz path:
// timestamptz shares the int64-micros-since-epoch on-disk form with timestamp
// without time zone, so it encodes via EncodeTimestamp(micros).
func timestamptzBTreeKey(ts time.Time) []byte {
	return btree.EncodeTimestamp(ts.Sub(pgEpoch).Microseconds())
}

// TestDDLCreateTimestamptzBTreeIndexAcceptsType pins the M0118-0001 acceptance
// contract: CREATE INDEX on a timestamptz column succeeds. Before M0118-0001
// this aborted with `0A000 btree v0 only supports ...`, which blocked the
// classroom-scheduling isolation spec (composite PK (room_id text, start_time
// timestamptz)). Seeds rows at sub-hour granularity — the spec books rooms on
// the half-hour — and verifies each is searchable through the shared timestamp
// key encoding.
func TestDDLCreateTimestamptzBTreeIndexAcceptsType(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE bookings (id int, start_time timestamptz)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "bookings"})
	rel := ctx.Catalog.RelFileNode(tbl)

	times := []time.Time{
		time.Date(2010, 4, 1, 10, 0, 0, 0, time.UTC),
		time.Date(2010, 4, 1, 13, 0, 0, 0, time.UTC),
		time.Date(2010, 4, 1, 13, 30, 0, 0, time.UTC),
		time.Date(2010, 4, 1, 14, 30, 0, 0, time.UTC),
	}
	for i, ts := range times {
		if err := writeHeapRow(ctx, rel, tbl.Columns, Row{
			{Kind: KindInt, Int: int64(i + 1)},
			NewTimeDatum(ts),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := runDDL(t, ctx, "CREATE INDEX idx_start_time ON bookings (start_time)"); err != nil {
		t.Fatalf("CREATE INDEX on timestamptz column: %v", err)
	}
	idx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Name: "idx_start_time"})
	if !ok {
		t.Fatal("index not in catalog after CREATE INDEX")
	}
	idxRel := ctx.Catalog.IndexRelFileNode(idx)
	tree, err := btree.Open(ctx.Pool, idxRel)
	if err != nil {
		t.Fatal(err)
	}

	for _, ts := range times {
		key := timestamptzBTreeKey(ts)
		_, found, err := tree.Search(key)
		if err != nil || !found {
			t.Fatalf("Search(%v): found=%v err=%v", ts, found, err)
		}
	}
}

// TestDDLTimestamptzRangeScanParity verifies a RangeScan over a timestamptz
// index returns exactly the rows a sequential scan would for the same range —
// the access pattern classroom-scheduling's overlap predicates depend on.
// Boundaries are inclusive on both ends (matching btree RangeScan semantics).
func TestDDLTimestamptzRangeScanParity(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE reservations (id int, start_time timestamptz)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "reservations"})
	rel := ctx.Catalog.RelFileNode(tbl)

	allTimes := []time.Time{
		time.Date(2010, 4, 1, 9, 0, 0, 0, time.UTC),   // outside
		time.Date(2010, 4, 1, 13, 0, 0, 0, time.UTC),  // inside (boundary)
		time.Date(2010, 4, 1, 13, 30, 0, 0, time.UTC), // inside
		time.Date(2010, 4, 1, 14, 0, 0, 0, time.UTC),  // inside (boundary)
		time.Date(2010, 4, 1, 15, 0, 0, 0, time.UTC),  // outside
	}
	for i, ts := range allTimes {
		if err := writeHeapRow(ctx, rel, tbl.Columns, Row{
			{Kind: KindInt, Int: int64(i + 1)},
			NewTimeDatum(ts),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := runDDL(t, ctx, "CREATE INDEX idx_start_time ON reservations (start_time)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	idx, _ := ctx.Catalog.LookupIndex(parser.ObjectName{Name: "idx_start_time"})
	idxRel := ctx.Catalog.IndexRelFileNode(idx)
	tree, err := btree.Open(ctx.Pool, idxRel)
	if err != nil {
		t.Fatal(err)
	}

	rangeStart := time.Date(2010, 4, 1, 13, 0, 0, 0, time.UTC)
	rangeEnd := time.Date(2010, 4, 1, 14, 0, 0, 0, time.UTC)
	lo := timestamptzBTreeKey(rangeStart)
	hi := timestamptzBTreeKey(rangeEnd)
	indexCount := 0
	if err := tree.RangeScan(lo, hi, func(_ []byte, _ storage.ItemPointer) (bool, error) {
		indexCount++
		return true, nil
	}); err != nil {
		t.Fatalf("RangeScan: %v", err)
	}

	seqCount := 0
	for _, ts := range allTimes {
		if !ts.Before(rangeStart) && !ts.After(rangeEnd) {
			seqCount++
		}
	}

	if indexCount != seqCount {
		t.Fatalf("parity: indexScan=%d seqScan=%d", indexCount, seqCount)
	}
	if indexCount != 3 {
		t.Fatalf("expected 3 rows in [13:00, 14:00], got %d", indexCount)
	}
}
