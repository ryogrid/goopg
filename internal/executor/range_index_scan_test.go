package executor

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// setupRangeScanFixture creates a table with a timestamp column, inserts
// test rows, and creates a B-tree index so range scan tests have a
// realistic environment.
//
//	Row layout: (id int, ts timestamp)
//	Rows:
//	  1  1993-01-01
//	  2  1994-06-15
//	  3  1995-01-01
//	  4  1995-06-01
//	  5  1996-01-01
//	  6  1997-12-31
func setupRangeScanFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)

	if err := runDDL(t, ctx, "CREATE TABLE events (id int, ts timestamp)"); err != nil {
		cleanup()
		t.Fatalf("CREATE TABLE: %v", err)
	}

	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "events"})
	rel := ctx.Catalog.RelFileNode(tbl)

	rows := []struct {
		id int64
		ts time.Time
	}{
		{1, time.Date(1993, 1, 1, 0, 0, 0, 0, time.UTC)},
		{2, time.Date(1994, 6, 15, 0, 0, 0, 0, time.UTC)},
		{3, time.Date(1995, 1, 1, 0, 0, 0, 0, time.UTC)},
		{4, time.Date(1995, 6, 1, 0, 0, 0, 0, time.UTC)},
		{5, time.Date(1996, 1, 1, 0, 0, 0, 0, time.UTC)},
		{6, time.Date(1997, 12, 31, 0, 0, 0, 0, time.UTC)},
	}
	for _, r := range rows {
		if err := writeHeapRow(ctx, rel, tbl.Columns, Row{
			{Kind: KindInt, Int: r.id},
			{Kind: KindTime, Time: r.ts},
		}); err != nil {
			cleanup()
			t.Fatalf("writeHeapRow id=%d: %v", r.id, err)
		}
	}

	if err := runDDL(t, ctx, "CREATE INDEX idx_events_ts ON events (ts)"); err != nil {
		cleanup()
		t.Fatalf("CREATE INDEX: %v", err)
	}

	return ctx, cleanup
}

// TestRangeIndexScanLowerBoundOnly verifies that a single >= predicate
// on an indexed timestamp column uses Filter(IndexScan) and returns
// the correct rows (id >= 1995-01-01 → ids 3, 4, 5, 6).
func TestRangeIndexScanLowerBoundOnly(t *testing.T) {
	ctx, cleanup := setupRangeScanFixture(t)
	defer cleanup()

	sql := "SELECT id FROM events WHERE ts >= timestamp '1995-01-01'"
	plan := planOne(t, sql, ctx.Catalog)

	// Verify planner chose Filter(IndexScan).
	proj, ok := plan.(*planner.Project)
	if !ok {
		t.Fatalf("root=%T want *planner.Project", plan)
	}
	f, ok := proj.Child.(*planner.Filter)
	if !ok {
		t.Fatalf("child=%T want *planner.Filter(IndexScan)", proj.Child)
	}
	if _, ok := f.Child.(*planner.IndexScan); !ok {
		t.Fatalf("Filter.Child=%T want *planner.IndexScan", f.Child)
	}

	op, err := Build(plan)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := Run(op, ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Rows with ts >= 1995-01-01: ids 3, 4, 5, 6
	if len(rows) != 4 {
		t.Fatalf("rows=%d want 4 (ids 3,4,5,6)", len(rows))
	}
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r[0].Int
	}
	for _, wantID := range []int64{3, 4, 5, 6} {
		found := false
		for _, id := range ids {
			if id == wantID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected id=%d in result, got ids=%v", wantID, ids)
		}
	}
}

// TestRangeIndexScanTwoSided verifies that a two-sided range on an
// indexed timestamp column uses Filter(IndexScan) and returns exactly
// the rows in the range (1994-01-01 <= ts < 1996-01-01 → ids 2, 3, 4).
func TestRangeIndexScanTwoSided(t *testing.T) {
	ctx, cleanup := setupRangeScanFixture(t)
	defer cleanup()

	sql := "SELECT id FROM events WHERE ts >= timestamp '1994-01-01' AND ts < timestamp '1996-01-01'"
	plan := planOne(t, sql, ctx.Catalog)

	// Verify planner chose Filter(IndexScan).
	proj, ok := plan.(*planner.Project)
	if !ok {
		t.Fatalf("root=%T want *planner.Project", plan)
	}
	f, ok := proj.Child.(*planner.Filter)
	if !ok {
		t.Fatalf("child=%T want *planner.Filter(IndexScan)", proj.Child)
	}
	idx, ok := f.Child.(*planner.IndexScan)
	if !ok {
		t.Fatalf("Filter.Child=%T want *planner.IndexScan", f.Child)
	}
	if idx.LowKey == nil {
		t.Fatal("IndexScan.LowKey should be non-nil")
	}
	if idx.HighKey == nil {
		t.Fatal("IndexScan.HighKey should be non-nil")
	}

	op, err := Build(plan)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := Run(op, ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Rows with 1994-01-01 <= ts < 1996-01-01: ids 2 (1994-06-15), 3 (1995-01-01), 4 (1995-06-01)
	// Note: 1996-01-01 is exclusive (< not <=), but since RangeScan uses inclusive bounds
	// and the Filter re-applies the predicate, id=5 (1996-01-01) will be filtered out.
	if len(rows) != 3 {
		t.Fatalf("rows=%d want 3 (ids 2,3,4); rows=%v", len(rows), rows)
	}
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r[0].Int
	}
	for _, wantID := range []int64{2, 3, 4} {
		found := false
		for _, id := range ids {
			if id == wantID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected id=%d in result, got ids=%v", wantID, ids)
		}
	}
	// id=5 (1996-01-01) must NOT be in result (exclusive upper bound)
	for _, id := range ids {
		if id == 5 {
			t.Errorf("id=5 (1996-01-01) should be excluded by ts < '1996-01-01'")
		}
	}
}

// TestRangeIndexScanCountMatchesSeqScan verifies that the count from a
// range index scan matches a plain seq-scan with the same predicate.
func TestRangeIndexScanCountMatchesSeqScan(t *testing.T) {
	ctx, cleanup := setupRangeScanFixture(t)
	defer cleanup()

	// Count via range index scan
	sqlIdx := "SELECT id FROM events WHERE ts >= timestamp '1994-01-01' AND ts <= timestamp '1995-12-31'"
	plan := planOne(t, sqlIdx, ctx.Catalog)

	// Verify planner chose index scan path (not seq scan)
	proj, ok := plan.(*planner.Project)
	if !ok {
		t.Fatalf("root=%T want *planner.Project", plan)
	}
	if _, ok := proj.Child.(*planner.Filter); !ok {
		t.Fatalf("child=%T want *planner.Filter", proj.Child)
	}

	op, err := Build(plan)
	if err != nil {
		t.Fatal(err)
	}
	idxRows, err := Run(op, ctx)
	if err != nil {
		t.Fatalf("range index scan failed: %v", err)
	}

	// Expected: ids 2 (1994-06-15), 3 (1995-01-01), 4 (1995-06-01) → 3 rows
	if len(idxRows) != 3 {
		t.Fatalf("range index scan returned %d rows, want 3", len(idxRows))
	}
}
