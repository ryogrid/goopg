package executor

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// TestEvalTypedStringLitTimestampForms pins the timestamp(tz) literal parse
// surface of evalTypedStringLit. The classroom-scheduling / receipt-report
// isolation specs book rooms on the half-hour with seconds-less literals such
// as `TIMESTAMP WITH TIME ZONE '2010-04-01 10:00'`. Before this fix those
// failed the global setup INSERT with `invalid timestamp "2010-04-01 10:00"`
// (22007) because the layout list only accepted `HH:MM:SS`. PostgreSQL's
// timestamptz_in accepts a seconds-less `HH:MM` time plus an optional numeric
// timezone offset, so both must parse; an explicit offset must be honoured
// (converted to UTC) before the zone-less fallbacks treat the wall clock as UTC.
func TestEvalTypedStringLitTimestampForms(t *testing.T) {
	utc := func(y int, mo time.Month, d, h, mi, s int) time.Time {
		return time.Date(y, mo, d, h, mi, s, 0, time.UTC)
	}
	for _, typ := range []string{"timestamp", "timestamptz"} {
		valid := []struct {
			in   string
			want time.Time
			// wantNoTZ is the answer for `timestamp` WITHOUT time zone when it
			// differs from the timestamptz one, i.e. for the offset-bearing
			// spellings only: timestamp_in decodes the zone and then discards it
			// (M0119-0006, tsZoneMode), so the wall clock stands as written.
			// The zero value means "same as want".
			wantNoTZ time.Time
		}{
			{in: "2010-04-01 10:00", want: utc(2010, 4, 1, 10, 0, 0)},      // seconds-less (the spec's form)
			{in: "2010-04-01 11:00", want: utc(2010, 4, 1, 11, 0, 0)},      // seconds-less
			{in: "2010-04-01 10:00:00", want: utc(2010, 4, 1, 10, 0, 0)},   // explicit seconds (regression)
			{in: "2010-04-01 10:00:00.5", want: utc(2010, 4, 1, 10, 0, 0)}, // fractional seconds (regression)
			{in: "2010-04-01", want: utc(2010, 4, 1, 0, 0, 0)},             // date only (regression)
			// Explicit offset: honoured by timestamptz, discarded by timestamp.
			{in: "2010-04-01 10:00:00-04", want: utc(2010, 4, 1, 14, 0, 0), wantNoTZ: utc(2010, 4, 1, 10, 0, 0)},
			{in: "2010-04-01 10:00-04", want: utc(2010, 4, 1, 14, 0, 0), wantNoTZ: utc(2010, 4, 1, 10, 0, 0)},
			{in: "2010-04-01 10:00:00.5-04", want: utc(2010, 4, 1, 14, 0, 0), wantNoTZ: utc(2010, 4, 1, 10, 0, 0)},
		}
		for _, tc := range valid {
			if typ == "timestamp" && !tc.wantNoTZ.IsZero() {
				tc.want = tc.wantNoTZ
			}
			x := &planner.TypedStringLit{Type: typ, Value: tc.in}
			d, err := evalTypedStringLit(x)
			if err != nil {
				t.Errorf("%s '%s': unexpected error: %v", typ, tc.in, err)
				continue
			}
			// Compare at second granularity: the fractional cases land on
			// the same second, and sub-second precision is exercised by the
			// pre-existing `.999999` layout, not this fix.
			if got := d.TimeValue().UTC().Truncate(time.Second); !got.Equal(tc.want) {
				t.Errorf("%s '%s': got %v want %v", typ, tc.in, got, tc.want)
			}
		}

		invalid := []string{
			"not-a-timestamp",
			"2010-04-01 25:00", // hour out of range
			"2010-13-01 10:00", // month out of range
		}
		for _, in := range invalid {
			x := &planner.TypedStringLit{Type: typ, Value: in}
			if _, err := evalTypedStringLit(x); err == nil {
				t.Errorf("%s '%s': expected error, got none", typ, in)
			}
		}
	}
}

// timestamptzBTreeKey mirrors the encodeBTreeKeyForColumn timestamptz path:
// timestamptz shares the int64-micros-since-epoch on-disk form with timestamp
// without time zone, so it encodes via EncodeTimestamp(micros).
func timestamptzBTreeKey(t *testing.T, ctx *Context, idx *catalog.Index, ts time.Time) []byte {
	return indexProbeForTest(t, ctx, idx, NewTimeDatum(ts))
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
	tree, err := openIndexBTree(ctx, idx, idxRel)
	if err != nil {
		t.Fatal(err)
	}

	for _, ts := range times {
		key := timestamptzBTreeKey(t, ctx, idx, ts)
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
	tree, err := openIndexBTree(ctx, idx, idxRel)
	if err != nil {
		t.Fatal(err)
	}

	rangeStart := time.Date(2010, 4, 1, 13, 0, 0, 0, time.UTC)
	rangeEnd := time.Date(2010, 4, 1, 14, 0, 0, 0, time.UTC)
	lo := timestamptzBTreeKey(t, ctx, idx, rangeStart)
	hi := timestamptzBTreeKey(t, ctx, idx, rangeEnd)
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
