package initdb

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPgAggregateInitialEntriesCount guards against accidental truncation of
// the 161-row pg_aggregate seed table (OID 2600). PG18's pg_aggregate.dat
// defines exactly 161 aggregate function rows.
func TestPgAggregateInitialEntriesCount(t *testing.T) {
	entries := pgAggregateInitialEntries()
	if len(entries) != 161 {
		t.Errorf("pgAggregateInitialEntries: got %d entries, want 161", len(entries))
	}
}

// TestPgAggregateAllAggfnoidNonZero verifies every row has a non-zero aggfnoid
// (i.e., the function OID resolution succeeded for all 161 aggregates).
func TestPgAggregateAllAggfnoidNonZero(t *testing.T) {
	for _, e := range pgAggregateInitialEntries() {
		if e.AggFnOID == 0 {
			t.Errorf("pgAggregateInitialEntries: entry with AggFnOID=0 found (AggKind=%c)", e.AggKind)
		}
	}
}

// TestPgAggregateAllTransTypeNonZero verifies every row has a non-zero
// aggtranstype (the aggregate state type must resolve).
func TestPgAggregateAllTransTypeNonZero(t *testing.T) {
	for _, e := range pgAggregateInitialEntries() {
		if e.AggTransType == 0 {
			t.Errorf("pgAggregateInitialEntries: aggfnoid=%d has zero aggtranstype", e.AggFnOID)
		}
	}
}

// TestPgAggregateAggfnoidUnique guards against duplicate aggfnoid values in
// the seed table (aggfnoid is the primary key of pg_aggregate).
func TestPgAggregateAggfnoidUnique(t *testing.T) {
	seen := make(map[uint32]bool)
	for _, e := range pgAggregateInitialEntries() {
		if seen[e.AggFnOID] {
			t.Errorf("duplicate AggFnOID %d in pgAggregateInitialEntries", e.AggFnOID)
		}
		seen[e.AggFnOID] = true
	}
}

// TestPgAggregateSpotCheck verifies representative rows against pg_aggregate.dat.
func TestPgAggregateSpotCheck(t *testing.T) {
	entries := pgAggregateInitialEntries()
	byOID := make(map[uint32]pgAggregateEntry, len(entries))
	for _, e := range entries {
		byOID[e.AggFnOID] = e
	}

	cases := []struct {
		aggfnoid  uint32
		desc      string
		transfn   uint32
		transtype uint32
		sortop    uint32
		initval   string
		aggkind   byte
	}{
		// avg(int8): transfn=int8_avg_accum(2746), transtype=internal(2281)
		{2100, "avg(int8)", 2746, 2281, 0, "", 'n'},
		// avg(int4): transfn=int4_avg_accum(1963), transtype=_int8(1016), initval="{0,0}"
		{2101, "avg(int4)", 1963, 1016, 0, "{0,0}", 'n'},
		// max(int8): sortop=>(int8,int8)=413, transtype=int8(20)
		{2115, "max(int8)", 1236, 20, 413, "", 'n'},
		// min(int8): sortop=<(int8,int8)=412, transtype=int8(20)
		{2131, "min(int8)", 1237, 20, 412, "", 'n'},
		// count(any): transfn=int8inc_any(2804), transtype=int8(20), initval="0"
		{2147, "count(any)", 2804, 20, 0, "0", 'n'},
		// count(*): transfn=int8inc(1219), transtype=int8(20), initval="0"
		{2803, "count(*)", 1219, 20, 0, "0", 'n'},
	}

	for _, tc := range cases {
		e, ok := byOID[tc.aggfnoid]
		if !ok {
			t.Errorf("aggfnoid %d (%s) not found in entries", tc.aggfnoid, tc.desc)
			continue
		}
		if e.AggTransFn != tc.transfn {
			t.Errorf("%s: AggTransFn=%d, want %d", tc.desc, e.AggTransFn, tc.transfn)
		}
		if e.AggTransType != tc.transtype {
			t.Errorf("%s: AggTransType=%d, want %d", tc.desc, e.AggTransType, tc.transtype)
		}
		if e.AggSortOp != tc.sortop {
			t.Errorf("%s: AggSortOp=%d, want %d", tc.desc, e.AggSortOp, tc.sortop)
		}
		if e.AggInitVal != tc.initval {
			t.Errorf("%s: AggInitVal=%q, want %q", tc.desc, e.AggInitVal, tc.initval)
		}
		if e.AggKind != tc.aggkind {
			t.Errorf("%s: AggKind=%c, want %c", tc.desc, e.AggKind, tc.aggkind)
		}
	}
}

// TestBootstrapPgAggregateTuplesWritesHeap verifies that
// bootstrapPgAggregateTuples writes 161 TIDs to the heap file
// base/{1,5}/2600 under a temporary data directory.
func TestBootstrapPgAggregateTuplesWritesHeap(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "base", "1"), 0o700); err != nil {
		t.Fatalf("mkdir base/1: %v", err)
	}
	tids, err := bootstrapPgAggregateTuples(dir)
	if err != nil {
		t.Fatalf("bootstrapPgAggregateTuples: %v", err)
	}
	if len(tids) != 161 {
		t.Errorf("TID map length: got %d, want 161", len(tids))
	}
	for _, e := range pgAggregateInitialEntries() {
		if _, ok := tids[e.AggFnOID]; !ok {
			t.Errorf("no TID for aggfnoid %d", e.AggFnOID)
		}
	}
	// Verify heap file exists in both base/1 and base/5.
	for _, sub := range []string{"base/1/2600", "base/5/2600"} {
		info, err := os.Stat(filepath.Join(dir, sub))
		if err != nil {
			t.Errorf("heap file %s missing: %v", sub, err)
			continue
		}
		if info.Size()%8192 != 0 {
			t.Errorf("heap file %s size %d is not a multiple of 8192", sub, info.Size())
		}
	}
}

// TestBootstrapPgAggregateFnoidIndexWritesPopulatedBtree verifies that
// bootstrapPgAggregateFnoidIndex writes a populated btree to base/{1,5}/2650.
func TestBootstrapPgAggregateFnoidIndexWritesPopulatedBtree(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "base", "1"), 0o700); err != nil {
		t.Fatalf("mkdir base/1: %v", err)
	}
	tids, err := bootstrapPgAggregateTuples(dir)
	if err != nil {
		t.Fatalf("bootstrapPgAggregateTuples: %v", err)
	}
	if err := bootstrapPgAggregateFnoidIndex(dir, tids); err != nil {
		t.Fatalf("bootstrapPgAggregateFnoidIndex: %v", err)
	}
	for _, sub := range []string{"base/1/2650", "base/5/2650"} {
		info, err := os.Stat(filepath.Join(dir, sub))
		if err != nil {
			t.Errorf("index file %s missing: %v", sub, err)
			continue
		}
		// Populated index must be >1 page (metapage + at least one leaf).
		if info.Size() < 2*8192 {
			t.Errorf("index file %s size %d too small (want ≥2 pages)", sub, info.Size())
		}
	}
}
