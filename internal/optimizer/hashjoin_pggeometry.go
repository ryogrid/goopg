package optimizer

import "math"

// pgHashGeometryResult is the planner-only part of PostgreSQL's
// ExecChooseHashTableSize that final_cost_hashjoin calls virtual buckets. It is
// intentionally separate from executor/hashsize: Goopg's executor stores
// []Datum rows in map[K][]Row, while PostgreSQL's planner prices a packed
// HashJoinTuple table.
type pgHashGeometryResult struct {
	numBuckets     int64
	numBatches     int64
	virtualBuckets int64
}

const (
	pgHashAlign              int64 = 8
	pgHashTupleOverhead      int64 = 16 // HJTUPLE_OVERHEAD
	pgMinimalTupleHeader     int64 = 16 // MAXALIGN(SizeofMinimalTupleHeader)
	pgHashBucketPointerBytes int64 = 8  // sizeof(HashJoinTuple)
	pgSkewPointerBytes       int64 = 8 * 8
	pgIntBytes               int64 = 4
	pgSkewBucketOverhead     int64 = 16
	pgSkewHashMemPercent     int64 = 2
	pgHashMinBuckets         int64 = 1024
	pgHashBlockSize          int64 = 8192
	pgHashMaxAllocSize       int64 = 1<<30 - 1
	pgHashMaxPointerCount    int64 = 1 << 30 // INT_MAX / 2 + 1
	pgHashMaxInt32           int64 = 1<<31 - 1
	pgHashMaxInt64           int64 = int64(^uint64(0) >> 1)
)

// pgHashGeometry reproduces the non-parallel, useskew=true geometry from PG
// 18's ExecChooseHashTableSize (nodeHash.c). The result is consumed only for
// final_cost_hashjoin's unmatched inner-unique denominator; it neither sizes
// Goopg's map nor decides its batches or spill I/O. Unknown width or an
// arithmetic state PostgreSQL represents outside this model declines safely.
func pgHashGeometry(ntuples float64, width int, hashMem int64) (pgHashGeometryResult, bool) {
	if width <= 0 || hashMem <= 0 || math.IsNaN(ntuples) || math.IsInf(ntuples, 0) {
		return pgHashGeometryResult{}, false
	}
	if ntuples <= 0 {
		ntuples = 1000 // "Force a plausible relation size if no info."
	}

	alignedWidth, ok := pgHashMaxAlign(int64(width))
	if !ok {
		return pgHashGeometryResult{}, false
	}
	tupsize, ok := pgHashAdd(pgHashTupleOverhead, pgMinimalTupleHeader)
	if !ok {
		return pgHashGeometryResult{}, false
	}
	tupsize, ok = pgHashAdd(tupsize, alignedWidth)
	if !ok {
		return pgHashGeometryResult{}, false
	}
	innerBytes := ntuples * float64(tupsize)
	if math.IsNaN(innerBytes) || math.IsInf(innerBytes, 0) || innerBytes > float64(pgHashMaxInt64) {
		return pgHashGeometryResult{}, false
	}

	// `space_allowed` remains the full get_hash_memory_limit() value; PG
	// removes the possible skew table only from the main-table budget.
	spaceAllowed := hashMem
	hashTableBytes := hashMem
	bytesPerMCV, ok := pgHashAdd(tupsize, pgSkewPointerBytes)
	if !ok {
		return pgHashGeometryResult{}, false
	}
	bytesPerMCV, ok = pgHashAdd(bytesPerMCV, pgIntBytes)
	if !ok {
		return pgHashGeometryResult{}, false
	}
	bytesPerMCV, ok = pgHashAdd(bytesPerMCV, pgSkewBucketOverhead)
	if !ok || bytesPerMCV <= 0 {
		return pgHashGeometryResult{}, false
	}
	skewMCVs := (hashTableBytes / bytesPerMCV * pgSkewHashMemPercent) / 100
	if skewMCVs > pgHashMaxInt32 {
		skewMCVs = pgHashMaxInt32
	}
	if skewMCVs > 0 {
		reserve, ok := pgHashMul(skewMCVs, bytesPerMCV)
		if !ok || reserve >= hashTableBytes {
			return pgHashGeometryResult{}, false
		}
		hashTableBytes -= reserve
	}

	maxPointers := hashTableBytes / pgHashBucketPointerBytes
	if maxAllocPointers := pgHashMaxAllocSize / pgHashBucketPointerBytes; maxPointers > maxAllocPointers {
		maxPointers = maxAllocPointers
	}
	maxPointers = pgHashPrevPow2(maxPointers)
	if maxPointers > pgHashMaxPointerCount {
		maxPointers = pgHashMaxPointerCount
	}
	if maxPointers < 1 {
		return pgHashGeometryResult{}, false
	}

	dbuckets, ok := pgHashCeil(ntuples)
	if !ok {
		return pgHashGeometryResult{}, false
	}
	if dbuckets > maxPointers {
		dbuckets = maxPointers
	}
	if dbuckets < pgHashMinBuckets {
		dbuckets = pgHashMinBuckets
	}
	nbuckets, ok := pgHashNextPow2(dbuckets)
	if !ok {
		return pgHashGeometryResult{}, false
	}
	bucketBytes, ok := pgHashMul(nbuckets, pgHashBucketPointerBytes)
	if !ok || bucketBytes >= hashTableBytes {
		return pgHashGeometryResult{}, false
	}
	nbatches := int64(1)

	if innerBytes+float64(bucketBytes) > float64(hashTableBytes) {
		bucketSize, ok := pgHashAdd(tupsize, pgHashBucketPointerBytes)
		if !ok || bucketSize <= 0 {
			return pgHashGeometryResult{}, false
		}
		sbuckets := int64(1)
		if hashTableBytes > bucketSize {
			sbuckets, ok = pgHashNextPow2(hashTableBytes / bucketSize)
			if !ok {
				return pgHashGeometryResult{}, false
			}
		}
		if sbuckets > maxPointers {
			sbuckets = maxPointers
		}
		nbuckets, ok = pgHashNextPow2(sbuckets)
		if !ok {
			return pgHashGeometryResult{}, false
		}
		bucketBytes, ok = pgHashMul(nbuckets, pgHashBucketPointerBytes)
		if !ok || bucketBytes >= hashTableBytes {
			return pgHashGeometryResult{}, false
		}
		dbatch, ok := pgHashCeil(innerBytes / float64(hashTableBytes-bucketBytes))
		if !ok {
			return pgHashGeometryResult{}, false
		}
		if dbatch > maxPointers {
			dbatch = maxPointers
		}
		if dbatch < 2 {
			dbatch = 2
		}
		nbatches, ok = pgHashNextPow2(dbatch)
		if !ok {
			return pgHashGeometryResult{}, false
		}
	}

	// PG18's walk-back trades two batch-file buffers for a larger table while
	// that lowers total memory. This exact loop deliberately uses the full
	// spaceAllowed, not the skew-reduced main-table budget.
	for nbatches > 1 {
		if nbuckets > pgHashMaxAllocSize/pgHashBucketPointerBytes/2 ||
			spaceAllowed > pgHashMaxInt64/2 ||
			nbatches < spaceAllowed/pgHashBlockSize {
			break
		}
		nbuckets *= 2
		spaceAllowed *= 2
		nbatches /= 2
	}
	virtual, ok := pgHashMul(nbuckets, nbatches)
	if !ok || virtual <= 0 {
		return pgHashGeometryResult{}, false
	}
	return pgHashGeometryResult{numBuckets: nbuckets, numBatches: nbatches, virtualBuckets: virtual}, true
}

func pgHashMaxAlign(n int64) (int64, bool) {
	if n < 0 || n > pgHashMaxInt64-(pgHashAlign-1) {
		return 0, false
	}
	return (n + pgHashAlign - 1) &^ (pgHashAlign - 1), true
}

func pgHashAdd(a, b int64) (int64, bool) {
	if a < 0 || b < 0 || a > pgHashMaxInt64-b {
		return 0, false
	}
	return a + b, true
}

func pgHashMul(a, b int64) (int64, bool) {
	if a < 0 || b < 0 || (a != 0 && b > pgHashMaxInt64/a) {
		return 0, false
	}
	return a * b, true
}

func pgHashCeil(v float64) (int64, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 || v > float64(pgHashMaxInt64) {
		return 0, false
	}
	return int64(math.Ceil(v)), true
}

func pgHashPrevPow2(n int64) int64 {
	if n < 1 {
		return 0
	}
	p := int64(1)
	for p <= n/2 {
		p *= 2
	}
	return p
}

func pgHashNextPow2(n int64) (int64, bool) {
	if n < 1 || n > pgHashMaxPointerCount {
		return 0, false
	}
	p := int64(1)
	for p < n {
		p *= 2
	}
	return p, true
}
