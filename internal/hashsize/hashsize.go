// Package hashsize computes the geometry of a hash join's build-side table:
// how many buckets it should have, and how many batches it must be split into
// to stay inside the query's memory budget.
//
// It exists as its own leaf package for one reason — the planner and the
// executor MUST agree. PG's `final_cost_hashjoin` (costsize.c) prices the
// batch I/O of exactly the geometry `ExecChooseHashTableSize` (nodeHash.c:658)
// will pick at run time, because both call that one function. goopg's planner
// imports the executor nowhere (the dependency runs executor → planner), so a
// shared sizing rule has to live below both. Putting it anywhere else would
// recreate the sibling-path divergence the project keeps paying for: a cost
// model that believes a build fits and an executor that spills, or the
// reverse. Design: docs/design/leftdeep-joins/06-hash-spill-and-memory.md
// §2.1 and 04-cost-and-cardinality.md §4 (milestone M0127-P3.1).
//
// The algorithm is `ExecChooseHashTableSize` with goopg's widths substituted
// for PG's. PG measures a hash entry as
//
//	HJTUPLE_OVERHEAD + MAXALIGN(SizeofMinimalTupleHeader) + MAXALIGN(tupwidth)
//
// — a packed MinimalTuple. goopg has no such thing: a build row is a
// `[]Datum` whose elements are 48-byte structs, held in a `map[K][]Row`. The
// footprint of the same logical row is therefore several times PG's, and a
// sizing rule that used PG's constants would predict `nbatch` systematically
// low — the exact warning in 04 §4. See EntryBytes.
//
// Skew buckets (`ExecHashBuildSkewHash`) and the parallel-hash combined-budget
// path are deliberately absent: goopg has neither, and 06 §6 defers both.
package hashsize

import "math"

const (
	// DatumBytes is the fixed footprint of one executor Datum. It is the
	// same 48 that `estimatedRowBytes` (internal/executor/spill.go) counts
	// per column; the two must not drift, which is why this constant is
	// documented as the shared width model rather than duplicated silently.
	DatumBytes = 48

	// RowSliceBytes is the per-row cost of living in the `[]Row` bucket
	// list: one 24-byte slice header in the backing array. (The Row's own
	// Datum array is counted by DatumBytes × columns.)
	RowSliceBytes = 24

	// MapSlotBytes is goopg's analogue of PG's `sizeof(HashJoinTuple)` —
	// the per-bucket cost of the table's index structure. A Go
	// `map[string][]Row` slot holds a 16-byte string header, a 24-byte
	// slice header and roughly a byte of tophash plus the load-factor
	// slack, which rounds to 48. PG's bucket is a bare 8-byte pointer;
	// using PG's 8 here would let the sizing believe six times as many
	// buckets fit in work_mem as actually do.
	MapSlotBytes = 48

	// MinBuckets mirrors nodeHash.c's "don't let nbuckets be really small"
	// floor of 1024.
	MinBuckets = 1024

	// FileBufferBytes is the write buffer assumed per batch temp file in
	// the walk-back below — PG's BLCKSZ. goopg's spillWriter is currently
	// unbuffered (internal/executor/spill.go), so this term is a
	// forward-looking bound that P3.2's framed batch files will make real;
	// see the deferral ledger row for M0127-P3.1.
	FileBufferBytes = 8192

	// MaxTableBytes is the ceiling on a single allocation the geometry may
	// assume, PG's MaxAllocSize (1 GiB - 1). It bounds the bucket count
	// independently of the memory budget.
	MaxTableBytes = 1<<30 - 1

	// DefaultMemLimitBytes is the budget used when a session has no
	// work_mem set (goopg's Context.WorkMem is zero-valued = "unlimited").
	// 512 MiB is the value the executor's one existing work_mem-aware hash
	// path already falls back to, kept identical here so the fallback has
	// a single definition.
	DefaultMemLimitBytes = 512 << 20

	// maxPointerCount is PG's `INT_MAX / 2 + 1` clamp, which keeps both
	// nbuckets and nbatch doublings inside int range.
	maxPointerCount = math.MaxInt32/2 + 1
)

// Sizing is the geometry chosen for one hash-join build.
type Sizing struct {
	// NBuckets is the number of hash buckets, always a power of two and
	// at least MinBuckets. It doubles as the capacity hint for the Go map
	// the executor builds.
	NBuckets int

	// NBatch is the number of batches, always a power of two. 1 means the
	// build is projected to fit in memory and no spill files are needed.
	NBatch int

	// SpaceAllowed is the in-memory budget the geometry was solved for, in
	// bytes. It equals the caller's limit except when the walk-back below
	// deliberately raised it to buy back batches; zero means the caller
	// asked for an unlimited budget.
	SpaceAllowed int64

	// EntryBytes is the per-row footprint the geometry assumed. Callers
	// pricing batch I/O multiply it by the row count to get the spilled
	// byte volume.
	EntryBytes float64
}

// EntryBytes returns goopg's projected in-memory footprint of one build row of
// ncols columns whose variable-width payload averages avgVarBytes.
//
// This is the goopg-width part of the design's "goopg-width-aware (48·c + map
// overhead)": the 48-byte Datum array, its slice header in the bucket list,
// and the payload bytes that KindString/KindBytes datums point at. Caller-side
// per-key costs (the key string itself, the map slot) are charged per BUCKET
// via MapSlotBytes, exactly as PG charges `sizeof(HashJoinTuple)` per bucket.
func EntryBytes(ncols int, avgVarBytes float64) float64 {
	if ncols < 0 {
		ncols = 0
	}
	if avgVarBytes < 0 || math.IsNaN(avgVarBytes) {
		avgVarBytes = 0
	}
	return float64(ncols)*DatumBytes + RowSliceBytes + avgVarBytes
}

// EffectiveMemLimit maps an executor Context.WorkMem (where 0 means "no limit
// configured") onto the budget the sizing should actually solve for. Callers
// that want the honest unlimited behaviour pass workMem straight to Choose
// instead; callers sizing a real allocation want a finite number, because an
// unlimited budget lets a bad row estimate presize an arbitrarily large map.
func EffectiveMemLimit(workMem int64) int64 {
	if workMem > 0 {
		return workMem
	}
	return DefaultMemLimitBytes
}

// Choose returns the hash-table geometry for a build of ntuples rows, each
// ncols columns wide with avgVarBytes of variable-width payload, under a
// memLimit-byte in-memory budget.
//
// memLimit <= 0 is "unlimited": NBatch is 1 and NBuckets follows the row count
// alone (bounded only by MaxTableBytes).
//
// Faithful to ExecChooseHashTableSize (postgres/src/backend/executor/
// nodeHash.c:658) including its three easily-missed parts: the 1024-bucket
// floor, the re-derivation of nbuckets from the FULL budget once multi-batch
// is forced (buckets are then sized for a memory-full table, not for ntuples),
// and the final walk-back that trades batches for a bigger table when the
// per-batch file buffers would cost more than the table itself.
func Choose(ntuples float64, ncols int, avgVarBytes float64, memLimit int64) Sizing {
	// "Force a plausible relation size if no info" — nodeHash.c:676. In
	// goopg this is the common case, not the rare one: planner.EstimateRows
	// returns 0 for any relation that has never been ANALYZEd.
	if ntuples <= 0 || math.IsNaN(ntuples) {
		ntuples = 1000.0
	}
	entry := EntryBytes(ncols, avgVarBytes)
	innerBytes := ntuples * entry

	if memLimit <= 0 {
		return Sizing{
			NBuckets:     bucketsForRows(ntuples, maxPointers(MaxTableBytes)),
			NBatch:       1,
			SpaceAllowed: 0,
			EntryBytes:   entry,
		}
	}

	tableBytes := memLimit
	ptrs := maxPointers(tableBytes)
	nbuckets := bucketsForRows(ntuples, ptrs)
	bucketBytes := int64(nbuckets) * MapSlotBytes
	nbatch := 1

	if innerBytes+float64(bucketBytes) > float64(tableBytes) {
		// Multi-batch. Buckets are now sized for the table as it will be
		// when memory is FULL (NTUP_PER_BUCKET = 1 rows per bucket plus
		// the bucket itself), not for ntuples — the whole relation no
		// longer lives in the table at once. nodeHash.c:817-830.
		bucketSize := entry + MapSlotBytes
		var sbuckets int64 = 1
		if float64(tableBytes) > bucketSize {
			sbuckets = nextPow2(int64(float64(tableBytes) / bucketSize))
		}
		if sbuckets > ptrs {
			sbuckets = ptrs
		}
		nbuckets = int(nextPow2(sbuckets))
		bucketBytes = int64(nbuckets) * MapSlotBytes

		avail := float64(tableBytes - bucketBytes)
		if avail < 1 {
			// The bucket array alone exhausts the budget. PG asserts
			// this cannot happen (its buckets are 8-byte pointers, so
			// they stay under half of hash_mem); goopg's 48-byte slots
			// make it reachable for a tiny work_mem, so clamp instead
			// of dividing by a non-positive number.
			avail = 1
		}
		dbatch := math.Ceil(innerBytes / avail)
		if dbatch > float64(ptrs) {
			dbatch = float64(ptrs)
		}
		minbatch := int64(dbatch)
		if minbatch < 2 {
			minbatch = 2
		}
		nbatch = int(nextPow2(minbatch))
	}

	spaceAllowed := tableBytes

	// Walk back: total memory is (innerBytes / nbatch) + 2·nbatch·buffer,
	// a U-shaped curve in nbatch that the calculation above ignores.
	// Halving nbatch while doubling the table lowers the total whenever
	// the file buffers dominate. nodeHash.c:898-939.
	for nbatch > 1 {
		if int64(nbuckets) > MaxTableBytes/MapSlotBytes/2 {
			break
		}
		if spaceAllowed > math.MaxInt64/2 {
			break
		}
		// Equivalent to (S + 2·nbatch·B) < (2·S + nbatch·B) without the
		// intermediate overflow: stop once the table already dominates.
		if int64(nbatch) < spaceAllowed/FileBufferBytes {
			break
		}
		nbuckets *= 2
		spaceAllowed *= 2
		nbatch /= 2
	}

	return Sizing{
		NBuckets:     nbuckets,
		NBatch:       nbatch,
		SpaceAllowed: spaceAllowed,
		EntryBytes:   entry,
	}
}

// bucketsForRows is nodeHash.c:778-786: one bucket per row
// (NTUP_PER_BUCKET = 1), capped by how many bucket slots the budget can hold,
// floored at MinBuckets, rounded up to a power of two.
func bucketsForRows(ntuples float64, ptrs int64) int {
	dbuckets := math.Ceil(ntuples)
	if dbuckets > float64(ptrs) {
		dbuckets = float64(ptrs)
	}
	nbuckets := int64(dbuckets)
	if nbuckets < MinBuckets {
		nbuckets = MinBuckets
	}
	return int(nextPow2(nbuckets))
}

// maxPointers is nodeHash.c:770-776: how many bucket slots may be allocated,
// rounded DOWN to a power of two, bounded by both the budget and the
// single-allocation ceiling.
func maxPointers(tableBytes int64) int64 {
	p := tableBytes / MapSlotBytes
	if lim := int64(MaxTableBytes) / MapSlotBytes; p > lim {
		p = lim
	}
	p = prevPow2(p)
	if p > maxPointerCount {
		p = maxPointerCount
	}
	if p < 1 {
		p = 1
	}
	return p
}

// nextPow2 rounds n up to a power of two (pg_nextpower2_*).
func nextPow2(n int64) int64 {
	if n < 1 {
		return 1
	}
	p := int64(1)
	for p < n && p < maxPointerCount {
		p <<= 1
	}
	return p
}

// prevPow2 rounds n down to a power of two (pg_prevpower2_*).
func prevPow2(n int64) int64 {
	if n < 1 {
		return 1
	}
	p := int64(1)
	for p<<1 <= n && p < maxPointerCount {
		p <<= 1
	}
	return p
}
