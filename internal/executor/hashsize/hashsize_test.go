package hashsize

import (
	"math"
	"testing"
)

func isPow2(n int) bool { return n > 0 && n&(n-1) == 0 }

// TestEntryBytesUsesGoopgWidths pins the width model itself. The whole point
// of the shared package is that goopg's per-row footprint is NOT PG's packed
// MinimalTuple: a 4-column row costs 4×48 for the Datum array plus the 24-byte
// slice header that puts it in a bucket list. If this number ever silently
// tracks PG's constants again, every nbatch prediction goes low and the
// executor spills where the planner priced a single batch.
func TestEntryBytesUsesGoopgWidths(t *testing.T) {
	if got, want := EntryBytes(4, 0), float64(4*DatumBytes+RowSliceBytes); got != want {
		t.Fatalf("EntryBytes(4,0) = %v, want %v", got, want)
	}
	// Variable-width payload is additive on top of the fixed Datum cost —
	// mirrors estimatedRowBytes (executor/spill.go), which adds len() of
	// KindString/KindBytes payloads to the 48-per-column base.
	if got, want := EntryBytes(2, 100), float64(2*DatumBytes+RowSliceBytes+100); got != want {
		t.Fatalf("EntryBytes(2,100) = %v, want %v", got, want)
	}
	// Defensive normalisation: nonsense inputs must not produce NaN
	// geometry downstream.
	if got := EntryBytes(-3, math.NaN()); got != RowSliceBytes {
		t.Fatalf("EntryBytes(-3,NaN) = %v, want %v", got, float64(RowSliceBytes))
	}
}

// TestChooseSingleBatchWhenItFits: a build that fits work_mem must not be
// batched, and its bucket count follows the row count (NTUP_PER_BUCKET = 1),
// rounded up to a power of two.
func TestChooseSingleBatchWhenItFits(t *testing.T) {
	s := Choose(5000, 4, 0, 64<<20)
	if s.NBatch != 1 {
		t.Fatalf("NBatch = %d, want 1 (5000 rows × %v B is far under 64 MiB)", s.NBatch, EntryBytes(4, 0))
	}
	if s.NBuckets != 8192 {
		t.Fatalf("NBuckets = %d, want 8192 (nextpow2(5000))", s.NBuckets)
	}
	if s.SpaceAllowed != 64<<20 {
		t.Fatalf("SpaceAllowed = %d, want the caller's budget untouched", s.SpaceAllowed)
	}
}

// TestChooseBucketFloor: nodeHash.c refuses to build a table with fewer than
// 1024 buckets however tiny the build is.
func TestChooseBucketFloor(t *testing.T) {
	s := Choose(3, 2, 0, 64<<20)
	if s.NBuckets != MinBuckets {
		t.Fatalf("NBuckets = %d, want the %d floor", s.NBuckets, MinBuckets)
	}
	if s.NBatch != 1 {
		t.Fatalf("NBatch = %d, want 1", s.NBatch)
	}
}

// TestChooseNoEstimateForcesPlausibleSize: goopg's planner returns 0 rows for
// any relation without stats, which is the COMMON case here (ANALYZE is
// per-session). PG's "force a plausible relation size" fallback must therefore
// be live, not theoretical: 0 rows behaves exactly like 1000 rows.
func TestChooseNoEstimateForcesPlausibleSize(t *testing.T) {
	zero := Choose(0, 3, 0, 32<<20)
	thousand := Choose(1000, 3, 0, 32<<20)
	if zero != thousand {
		t.Fatalf("Choose(0,…) = %+v, want identical to Choose(1000,…) = %+v", zero, thousand)
	}
	if zero.NBuckets != 1024 {
		t.Fatalf("NBuckets = %d, want 1024", zero.NBuckets)
	}
}

// TestChooseUnlimitedBudget: WorkMem is zero-valued (= unlimited) on a session
// that never set work_mem, and the honest answer there is "one batch" — there
// is no budget to overflow. SpaceAllowed reports 0 so a caller cannot mistake
// the reply for a real bound.
func TestChooseUnlimitedBudget(t *testing.T) {
	s := Choose(1e9, 8, 0, 0)
	if s.NBatch != 1 {
		t.Fatalf("NBatch = %d, want 1 under an unlimited budget", s.NBatch)
	}
	if s.SpaceAllowed != 0 {
		t.Fatalf("SpaceAllowed = %d, want 0 to signal 'no bound'", s.SpaceAllowed)
	}
	if !isPow2(s.NBuckets) {
		t.Fatalf("NBuckets = %d, want a power of two", s.NBuckets)
	}
}

// TestChooseBatchesWhenOversized is the core obligation: an oversized build
// must be split into enough power-of-two batches that ONE batch plus the
// bucket array fits the budget it was solved for. That inequality is what the
// planner will price and the executor will honour, so it is asserted rather
// than a specific nbatch value.
func TestChooseBatchesWhenOversized(t *testing.T) {
	const rows = 2_000_000
	const cols = 6
	s := Choose(rows, cols, 0, 16<<20)
	if s.NBatch <= 1 {
		t.Fatalf("NBatch = %d, want > 1 (%v B of rows vs a 16 MiB budget)", s.NBatch, rows*EntryBytes(cols, 0))
	}
	if !isPow2(s.NBatch) || !isPow2(s.NBuckets) {
		t.Fatalf("NBatch = %d / NBuckets = %d, both must be powers of two", s.NBatch, s.NBuckets)
	}
	perBatch := rows * s.EntryBytes / float64(s.NBatch)
	bucketBytes := float64(s.NBuckets) * MapSlotBytes
	if perBatch+bucketBytes > float64(s.SpaceAllowed) {
		t.Fatalf("one batch (%.0f B) + buckets (%.0f B) exceeds SpaceAllowed %d",
			perBatch, bucketBytes, s.SpaceAllowed)
	}
}

// TestChooseGoopgWidthForcesBatchesPGWouldNotSee is the regression this
// package exists to prevent. 300k rows of 10 columns is ~2.9 MB by PG's
// MinimalTuple math (well under 4 MiB) but ~150 MB by goopg's — 48 bytes per
// Datum, not one packed byte per column. Sizing with PG's constants would
// promise a single batch and then blow the budget by 35×.
func TestChooseGoopgWidthForcesBatchesPGWouldNotSee(t *testing.T) {
	s := Choose(300_000, 10, 0, 4<<20)
	if s.NBatch <= 1 {
		t.Fatalf("NBatch = %d, want > 1: 300k × %v B does not fit 4 MiB", s.NBatch, EntryBytes(10, 0))
	}
}

// TestChooseWalkBackTradesBatchesForTable: with a small budget and a huge
// build, the first-pass nbatch is large enough that 2·nbatch file buffers
// would cost more memory than the hash table. PG walks nbatch back down,
// doubling the allowed table each step; the result must both shrink nbatch and
// admit the larger SpaceAllowed it bought.
func TestChooseWalkBackTradesBatchesForTable(t *testing.T) {
	const memLimit = 1 << 20
	s := Choose(1e7, 1, 0, memLimit)
	if s.NBatch <= 1 {
		t.Fatalf("NBatch = %d, want > 1", s.NBatch)
	}
	if s.SpaceAllowed <= memLimit {
		t.Fatalf("SpaceAllowed = %d, want > %d — the walk-back must report the budget it actually solved for",
			s.SpaceAllowed, memLimit)
	}
	// Termination condition of the loop (nodeHash.c:925): once the table
	// dominates the file buffers, stop.
	if int64(s.NBatch) >= s.SpaceAllowed/FileBufferBytes {
		t.Fatalf("walk-back did not converge: NBatch %d vs SpaceAllowed/%d = %d",
			s.NBatch, FileBufferBytes, s.SpaceAllowed/FileBufferBytes)
	}
}

// TestChooseTinyBudgetDoesNotDivideByZero: goopg's 48-byte map slots (vs PG's
// 8-byte pointers) make "the bucket array alone exhausts work_mem" reachable,
// which PG only asserts against. The clamp must hold rather than produce Inf
// or a negative batch count.
func TestChooseTinyBudgetDoesNotDivideByZero(t *testing.T) {
	for _, mem := range []int64{1, 64, 4096, 64 << 10} {
		s := Choose(1e6, 12, 200, mem)
		if s.NBatch < 1 || !isPow2(s.NBatch) {
			t.Fatalf("memLimit=%d: NBatch = %d, want a positive power of two", mem, s.NBatch)
		}
		if s.NBuckets < 1 || !isPow2(s.NBuckets) {
			t.Fatalf("memLimit=%d: NBuckets = %d, want a positive power of two", mem, s.NBuckets)
		}
	}
}

// TestChooseMonotoneInRows: growing the build may never REDUCE the number of
// batches at a fixed budget. A non-monotone sizing rule would let the planner
// prefer the bigger build side, which is the failure the shared function is
// meant to make impossible.
func TestChooseMonotoneInRows(t *testing.T) {
	prev := 0
	for rows := 1e3; rows <= 1e8; rows *= 10 {
		s := Choose(rows, 5, 0, 8<<20)
		if s.NBatch < prev {
			t.Fatalf("rows=%v: NBatch %d < previous %d", rows, s.NBatch, prev)
		}
		prev = s.NBatch
	}
}

// TestEffectiveMemLimit: Context.WorkMem == 0 means "unlimited" in the
// executor, but a caller sizing a REAL allocation needs a finite number, or a
// bad row estimate presizes an arbitrarily large map.
func TestEffectiveMemLimit(t *testing.T) {
	if got := EffectiveMemLimit(0); got != DefaultMemLimitBytes {
		t.Fatalf("EffectiveMemLimit(0) = %d, want %d", got, int64(DefaultMemLimitBytes))
	}
	if got := EffectiveMemLimit(-1); got != DefaultMemLimitBytes {
		t.Fatalf("EffectiveMemLimit(-1) = %d, want %d", got, int64(DefaultMemLimitBytes))
	}
	if got := EffectiveMemLimit(1 << 20); got != 1<<20 {
		t.Fatalf("EffectiveMemLimit(1MiB) = %d, want it unchanged", got)
	}
}

func TestPow2Helpers(t *testing.T) {
	cases := []struct{ in, next, prev int64 }{
		{0, 1, 1}, {1, 1, 1}, {2, 2, 2}, {3, 4, 2}, {5, 8, 4}, {1024, 1024, 1024}, {1025, 2048, 1024},
	}
	for _, c := range cases {
		if got := nextPow2(c.in); got != c.next {
			t.Errorf("nextPow2(%d) = %d, want %d", c.in, got, c.next)
		}
		if got := prevPow2(c.in); got != c.prev {
			t.Errorf("prevPow2(%d) = %d, want %d", c.in, got, c.prev)
		}
	}
}
