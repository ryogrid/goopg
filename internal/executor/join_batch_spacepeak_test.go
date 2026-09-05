package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/executor/hashsize"
)

// TestSpacePeakIncludesTheBucketArray pins the reporting fix.
//
// `Memory Usage:` used to print only the retained rows, which is the
// SMALLER of a hash join's two memory terms — on the TPC-H Q9 `orders`
// build it reported 44,026 kB of rows while omitting 98,304 kB of buckets.
// That under-reporting is why four successive measurements of this join's
// memory behaviour were read against a number that hid the larger half.
//
// PG folds the bucket array into the reported spaceUsed for exactly this
// reason (nodeHash.c: "Account for the buckets in spaceUsed (reported in
// EXPLAIN ANALYZE)").
func TestSpacePeakIncludesTheBucketArray(t *testing.T) {
	st := &HashJoinStats{}
	bs := &hashBatchState{
		stats:     st,
		nbuckets:  1024,
		peakSpace: 5000,
	}
	bs.publish()

	want := int64(5000) + 1024*hashsize.MapSlotBytes
	if st.SpacePeak != want {
		t.Fatalf("SpacePeak = %d, want %d (rows %d + %d buckets x %d B). "+
			"Reporting rows alone hides the larger of the join's two memory terms",
			st.SpacePeak, want, 5000, 1024, hashsize.MapSlotBytes)
	}
	if st.SpacePeak <= 5000 {
		t.Fatal("SpacePeak did not grow past the row-only figure at all")
	}
}

// TestSpacePeakDoesNotChangeTheGrowthTrigger is the counter-pin. The
// budget is ALREADY bucket-aware: spaceAllowed is pre-deducted by
// nbuckets*MapSlotBytes where it is computed, which makes the trigger
// algebraically identical to PG's. Charging the buckets into the trigger
// as well would double-count them and batch early, so the reporting change
// must not touch spaceAllowed or the compared value.
func TestSpacePeakDoesNotChangeTheGrowthTrigger(t *testing.T) {
	st := &HashJoinStats{}
	bs := &hashBatchState{
		stats:        st,
		nbuckets:     1024,
		peakSpace:    5000,
		spaceUsed:    5000,
		spaceAllowed: 6000,
	}
	before := bs.spaceAllowed
	bs.publish()
	if bs.spaceAllowed != before {
		t.Fatalf("publish changed spaceAllowed %d -> %d; reporting must "+
			"not move the growth trigger", before, bs.spaceAllowed)
	}
	if bs.spaceUsed != 5000 {
		t.Fatalf("publish changed spaceUsed to %d; the trigger's own "+
			"counter must stay rows-only (the buckets are pre-deducted from "+
			"spaceAllowed instead)", bs.spaceUsed)
	}
}
