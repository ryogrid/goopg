// Package hashsize computes the geometry of a hash join's build-side table:
// how many buckets it should have, and how many batches it must be split into
// to stay inside the query's memory budget.
//
// It exists as its own leaf package for one reason — the planner and the
// executor MUST agree. PG's `final_cost_hashjoin` (costsize.c) prices the
// batch I/O of exactly the geometry `ExecChooseHashTableSize` (nodeHash.c:658)
// will pick at run time, because both call that one function. Putting the rule
// anywhere the two sides could drift apart would recreate the sibling-path
// divergence the project keeps paying for: a cost model that believes a build
// fits and an executor that spills, or the reverse.
// Design: docs/design/leftdeep-joins/06-hash-spill-and-memory.md §2.1 and
// 04-cost-and-cardinality.md §4 (milestone M0127-P3.1).
//
// INVARIANT — do not "fix" this package's imports. It lives under
// internal/executor/ to mirror `ExecChooseHashTableSize`'s home in
// src/backend/executor/nodeHash.c, but it imports NEITHER internal/executor
// NOR internal/optimizer, and it must stay that way. In Go a directory is not
// a dependency: internal/optimizer importing internal/executor/hashsize
// creates no edge to internal/executor, so the planner still imports the
// executor nowhere (the dependency runs executor → optimizer). An import of
// either parent from here would make that false and introduce a cycle.
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
	// the per-bucket cost of the table's index structure. PG's bucket is a
	// bare 8-byte pointer; using PG's 8 here would let the sizing believe
	// six times as many buckets fit in work_mem as actually do.
	//
	// KNOWN 2x LOW — 48 is a hand-derived guess, not a measurement, and it
	// is retained deliberately. The derivation below (a 16-byte string
	// header, a 24-byte slice header, a tophash byte and load-factor slack,
	// "which rounds to 48") was checked against go1.25's swisstable runtime
	// during D-05 and came out at 96.1 B for `map[string][]Row` and 80.1 B
	// for `map[int64][]Row`.
	//
	// Correcting it to 96 was implemented and measured (2026-09-06): it
	// halved the bucket heap, 586.7 -> 286.0 MB live, per-worker peak
	// -34.5%, batches unchanged, values 24/24 MATCH — and cost +10.4% on
	// TPC-H because Q14 flipped Hash Join -> Nested Loop (+3364%). Q14 ran
	// `Batches: 1` in BOTH arms, so nothing spilled: the PLANNER prices a
	// 9-column build the executor never builds, and an honest bucket price
	// pushed that phantom 1.5% past the budget.
	//
	// So this constant cannot be fixed on its own. It is blocked behind the
	// cost-side narrowing fix (deferral ledger `take3-D-05-costside-unnarrowed`);
	// with the narrowed input the same build sits at 53.4 of 134.2 MB, a
	// 2.5x margin instead of a 1.5% one. Patch preserved at
	// `tmp/d05p2-bucket-charge.patch`; artifact
	// `analysis/minimize-datum/d05-bucket-charge-20260906/README.md`.
	// Do not raise it without that fix, and do not read 48 as validated.
	MapSlotBytes = 48

	// MinBuckets mirrors nodeHash.c's "don't let nbuckets be really small"
	// floor of 1024.
	MinBuckets = 1024

	// FileBufferBytes is the write buffer per batch temp file assumed by
	// the walk-back below — PG's BLCKSZ. No longer forward-looking: P3.2
	// landed the framed batch files and sized spillWriter's bufio writer at
	// exactly this constant (`bufio.NewWriterSize(f, hashsize.FileBufferBytes)`,
	// internal/executor/spill.go), so the assumption is true rather than
	// aspirational. The M0127-P3.1 ledger row that recorded the gap is
	// closed by that change; this comment previously still described the
	// unbuffered pre-P3.2 writer.
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

const (
	// SpillFrameBytes is the per-row overhead a spilled row pays outside its
	// datums: writeFrame's 4-byte little-endian length prefix plus
	// WriteRowHashed's 4-byte join hash value (internal/executor/spill.go).
	// The unhashed WriteRow pays only the first 4, so charging both errs
	// high by 4 bytes on the merge-spill path, which is the safe direction.
	SpillFrameBytes = 8

	// SpillColumnBytes is the encoded size of one fixed-width column:
	// encodeDatum's 1-byte Kind tag plus an 8-byte payload.
	//
	// The planner knows ncols and avgVarBytes but not the KIND MIX, so this
	// is one number standing for a switch. Its error against every arm of
	// encodeDatum is bounded and small:
	//
	//	KindInt        1+8       = 9   exact
	//	KindString     1+4+len   = 5   +4 (model over-charges the header)
	//	KindBytes      1+4+len   = 5   +4
	//	KindToastPtr   1+4+len   = 5   +4
	//	KindNull       1         = 1   +8
	//	KindBool       1+1       = 2   +7
	//	KindTime       1+8+1+2   = 12  -3
	//	KindEnum       1+8+4+len = 13  -4
	//	KindInterval   1+4+4+8   = 17  -8
	//	KindNumeric    1+2+4+1+len(mag) = 8 + mag  ≈ +1
	//
	// i.e. within ±8 B/column either way, against the 39 B/column
	// (DatumBytes - SpillColumnBytes) of over-statement it removes.
	SpillColumnBytes = 9

	// SpillCountBytes is appendRowPayload's uvarint column count. One byte
	// for any row of fewer than 128 columns, which is every row goopg
	// spills in practice; wider rows are under-charged by a byte or two.
	SpillCountBytes = 1
)

// SpillBytes returns the ON-DISK size of one spilled row of ncols columns
// whose variable-width payload averages avgVarBytes, as
// internal/executor/spill.go actually encodes it.
//
// It exists because EntryBytes is the wrong model for batch I/O and was being
// used for it. EntryBytes measures the IN-MEMORY entry — a 48-byte Datum
// struct per column plus a 24-byte slice header — while the file holds a kind
// tag and a payload. The gap is not a constant: it runs from about 5x (a
// narrow fixed-width row, where the 48 bytes are almost pure overhead) down to
// about 1.2x (a wide text row, where variable-width payload is carried at par
// on both sides), so no single multiplier corrects it. Design:
// docs/design/planner-spill-cost-calibration/DESIGN.md §6.2; the ledgered
// approximation it retires is M0127-P5.7-a.
//
// This is deliberately NOT the model Choose uses. Choose must keep predicting
// the executor's in-memory geometry through EntryBytes, because that identity
// with joinOp.buildGeometry is what makes `NBatch > 1` mean "the executor will
// really write files". Only the I/O CHARGE moves to this model.
//
// spill.go's encoder and this function are a sibling pair and must change
// together; TestSpillBytesAgreesWithEncoder (internal/executor) pins them by
// encoding real rows and comparing byte lengths.
func SpillBytes(ncols int, avgVarBytes float64) float64 {
	if ncols < 0 {
		ncols = 0
	}
	if avgVarBytes < 0 || math.IsNaN(avgVarBytes) {
		avgVarBytes = 0
	}
	return SpillFrameBytes + SpillCountBytes +
		float64(ncols)*SpillColumnBytes + avgVarBytes
}

// EffectiveMemLimit maps an executor Context.WorkMem (where 0 means "no limit
// configured") onto the budget the sizing should actually solve for. Callers
// that want the honest unlimited behaviour pass workMem straight to Choose
// instead; callers sizing a real allocation want a finite number, because an
// unlimited budget lets a bad row estimate presize an arbitrarily large map.
// HashMemLimit is upstream's `get_hash_memory_limit`
// (postgres/src/backend/executor/nodeHash.c:3622):
//
//	mem_limit = work_mem * hash_mem_multiplier * 1024
//
// take2 P2-03. goopg budgeted a hash build at `work_mem` alone, so with PG's
// default `hash_mem_multiplier = 2.0` every hash table in goopg had HALF the
// memory PostgreSQL would give it. At the aligned `work_mem = 64MB` that is a
// 64MB budget against PG's 128MB — one of the reasons a build that PG keeps in
// a single batch spills in goopg.
//
// Both the planner's cost model and the executor's sizing must call this, for
// the same reason they both call Choose: a budget they disagree on prices a
// geometry that will not be built.
func HashMemLimit(workMem int64, hashMemMultiplier float64) int64 {
	if hashMemMultiplier <= 0 {
		hashMemMultiplier = DefaultHashMemMultiplier
	}
	base := EffectiveMemLimit(workMem)
	lim := float64(base) * hashMemMultiplier
	// "Clamp in case it doesn't fit in size_t" — nodeHash.c:3630.
	if lim > float64(math.MaxInt64) {
		return math.MaxInt64
	}
	return int64(lim)
}

// DefaultHashMemMultiplier is PG 18's `hash_mem_multiplier` boot value
// (guc_tables.c); goopg registers the same default in
// internal/utils/misc/defaults.go.
const DefaultHashMemMultiplier = 2.0

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
