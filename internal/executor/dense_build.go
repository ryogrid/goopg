package executor

import (
	"unsafe"

	"github.com/goopg/goopg/internal/utils/mmgr"
)

// dense_build.go — EX3-02 Cut 2 (strata B+D).
//
// A per-joinOp build arena pair for retained hash-build rows:
//
//   - stratum B (buildBytes): variable-width payloads. The build-loop
//     retention call routes arena-backed String/Bytes bodies and big-numeric
//     sign+magnitude bodies into buildBytes.AllocBytes instead of per-Datum
//     make/Perm allocations (Cut 1).
//   - stratum D (buildCells): Datum cells. The struct copy lands in
//     buildCells.AllocAligned(w*48, 8) and the returned Row is a view over
//     the chunk (Cut 2) — one chunk allocation covers chunkSize/(w*48) rows
//     instead of one header allocation per row. mmgr's default chunk is
//     64 KiB, exactly the design's denseChunkSize, so the design-point
//     packing math holds with no per-context tuning.
//
// Readers are unchanged (same (offset,length)+ArenaID encoding resolved
// through the mmgr registry).
//
// Lifetime: one mmgr.Context per stratum per joinOp, each parented to the
// statement context (ctx.Mctx), created at build start (buildLazyHashTable,
// next to presizeLazyHash) and released at joinOp Close in the serial case.
// The shared-build (parallel) case transfers ownership to sharedHashBuild,
// released at statement end via the parent chain — "drop at Close" there
// would be a use-after-release against workers probing after the builder is
// gone, or a ContextID-ABA corruption past Release (design §§2.3/3.1, F2/F6;
// full shared-path teardown lands in Cut 3).
//
// Cut-1 boundary (unchanged): MaterializeArena/cloneRowOwned are
// BIT-IDENTICAL (datum.go untouched). materializeBuildDatum is build-only
// and used solely by the build-loop retention call; every other retention
// path (drainRowsCtx, Materialize, transferRowForQueue, the FOR UPDATE ctid
// build) keeps the legacy Perm lane.
//
// Cut-2 boundary (F1, structural, not an optimisation choice): mmgr chunks
// are make([]byte) slabs, i.e. GC-noscan spans — a non-nil Buf stored in a
// chunk keeps its target alive invisibly, so the label/bytes can be
// collected while the dense row still references them. Rows with ANY
// Buf-carrying Datum therefore stay FULLY heap-backed (whole row on the
// per-row path, never struct-copied into chunks). Only ArenaID!=0 payloads
// are re-homed into stratum B, guarded by the F7 pack assertion
// (ArenaID!=0 ==> Buf==nil) against representation drift.
//
// Pool interplay (§2.4): dense rows bypass rowPool entirely (no acquireRow,
// no releaseRow) — Put-ting a chunk view would pin the whole chunk per row.
// INVARIANT: a dense row must never reach releaseRow. No build-row site
// calls releaseRow today (retained rows correctly never return); the
// executable guard is the pool-alias poison test (scribbling an acquired
// pool row must leave filed dense rows intact — a leaked dense view would
// alias), not a runtime check at the release sites, which are untouched.

// ensureBuildBytes creates the stratum-B context parented to the statement
// context. Nil-Mctx (unit tests without a server) leaves buildBytes nil and
// retainBuildRow degrades to the legacy ownedBuildRow path.
func (o *joinOp) ensureBuildBytes(ctx *Context) {
	if o.buildBytes != nil {
		return
	}
	if ctx == nil || ctx.Mctx == nil {
		return
	}
	o.buildBytes = mmgr.Acquire(ctx.Mctx, mmgr.KindStmt)
	o.buildBytesShared = false
}

// releaseBuildBytes drops the stratum-B context. Owned (serial) contexts are
// Released eagerly; shared-adopted ones (applySharedBuild) are only
// dereferenced — statement-end Release of the parent reclaims them (Cut 3
// owns the explicit shared teardown). Safe on a nil receiver state.
func (o *joinOp) releaseBuildBytes() {
	if o.buildBytes == nil {
		return
	}
	if !o.buildBytesShared {
		o.buildBytes.Release()
	}
	o.buildBytes = nil
	o.buildBytesShared = false
}

// ensureBuildCells creates the stratum-D context parented to the statement
// context. Nil-Mctx (unit tests without a server) leaves buildCells nil and
// retainBuildRow degrades to the legacy ownedBuildRow path. Same
// statement-parenting/teardown as stratum B, minus the shared flag: workers
// never retain (the leader runs the single serial build loop by
// construction, §3.7-corrected), so there is nothing to adopt — a worker's
// buildCells stays nil and its release is a no-op, while the leader's arena
// (never Closed on the shared path) is reclaimed at statement end via the
// parent chain. Explicit shared teardown is Cut 3's item.
func (o *joinOp) ensureBuildCells(ctx *Context) {
	if o.buildCells != nil {
		return
	}
	if ctx == nil || ctx.Mctx == nil {
		return
	}
	o.buildCells = mmgr.Acquire(ctx.Mctx, mmgr.KindStmt)
}

// releaseBuildCells drops the stratum-D context. Always an owned (serial)
// Release: shared-adopted cells cannot exist (see ensureBuildCells), so
// there is no dereference-only arm. Safe on a nil receiver state.
func (o *joinOp) releaseBuildCells() {
	if o.buildCells == nil {
		return
	}
	o.buildCells.Release()
	o.buildCells = nil
}

// rowHasBuf reports whether any Datum in r carries a Go-heap Buf pointer.
// Such rows are ineligible for stratum D: chunk slabs are GC-noscan, so a
// Buf stored in a chunk would be invisible to the mark phase (F1). This
// covers Buf-backed String/Bytes (ArenaID==0), KindEnum labels, and
// KindToastPointer bodies — all of which must survive chunking bit-for-bit
// on the heap path instead.
func rowHasBuf(r Row) bool {
	for i := range r {
		if r[i].Buf != nil {
			return true
		}
	}
	return false
}

// retainBuildRow copies a build-side row into storage the hash table can hold
// for the life of the join.
//
// Cut-2 dispatch: the dense (chunk-view) path requires BOTH strata. A row
// with any Buf-carrying Datum stays FULLY heap-backed (F1 — whole row on
// the per-row path, never struct-copied into noscan chunks), and the
// rowHasArena gate is preserved (non-arena rows still take the O(width)
// struct copy — the source aliases the producer's reused slot, EX2-01 C8 —
// on per-row make storage, so make-lane int rows stay at 1 alloc/row).
// Either stratum missing (nil-Mctx unit shape) degrades to legacy
// ownedBuildRow, exactly as Cut 1 did.
//
// The M0097-0058 contract is unchanged: the copy still detaches from the
// producer arena (stratum B copies out of it exactly as MaterializeArena's
// make did), and dense rows are immutable after filing (the bumper only
// ever advances; filed ranges are never written again).
func (o *joinOp) retainBuildRow(row Row) Row {
	if o.buildBytes == nil || o.buildCells == nil {
		return ownedBuildRow(row)
	}
	if len(row) == 0 || rowHasBuf(row) || !rowHasArena(row) {
		return o.retainBuildRowHeap(row)
	}
	return o.packDenseBuildRow(row)
}

// retainBuildRowHeap is the Cut-1 per-row retention path, kept for every
// row stratum D cannot take: Buf-carriers (F1 whole-row heap rule),
// non-arena rows (rowHasArena gate), and zero-width rows. Header discipline
// is identical to ownedBuildRow (arena lane via acquireRow, else per-row
// make); only the payload destination changes — arena-backed
// variable-width/big-numeric bodies land in stratum B instead of per-Datum
// make/Perm allocations.
func (o *joinOp) retainBuildRowHeap(row Row) Row {
	if !rowHasArena(row) {
		dup := make(Row, len(row))
		copy(dup, row)
		return dup
	}
	dst := acquireRow(len(row))
	for i, d := range row {
		dst[i] = materializeBuildDatum(o.buildBytes, d)
	}
	return dst
}

// denseDatumCellSize is the Datum struct size: the stratum-D extent per row
// is w*denseDatumCellSize at 8-byte alignment. Datum is asserted 48 B in
// datum.go (const _ uintptr = 48 - unsafe.Sizeof(Datum{})), so this is the
// design's w*48 without hardcoding the layout.
const denseDatumCellSize = int(unsafe.Sizeof(Datum{}))

// packDenseBuildRow files one arena-lane, Buf-free build row into stratum D:
// the struct copy lands in buildCells.AllocAligned(w*48, 8) and the returned
// Row is a view over the chunk. Payloads are re-homed into stratum B by the
// same materializeBuildDatum the heap path uses, so the encoding readers
// resolve is identical on both lanes.
//
// Preconditions (established by retainBuildRow): both strata non-nil,
// len(row) > 0, no Buf-carrying Datum, at least one ArenaID!=0 Datum.
//
// The F7 pack assertion guards representation drift: any Datum with both
// ArenaID!=0 and non-nil Buf would silently keep the Buf alias while
// readers prefer the arena — and, worse, would plant a GC-invisible pointer
// in noscan chunk memory. That shape must never enter a chunk; panic
// loudly rather than corrupt silently.
func (o *joinOp) packDenseBuildRow(row Row) Row {
	mem := o.buildCells.AllocAligned(len(row)*denseDatumCellSize, 8)
	cells := unsafe.Slice((*Datum)(unsafe.Pointer(&mem[0])), len(row))
	for i, d := range row {
		if d.ArenaID != 0 && d.Buf != nil {
			panic("EX3-02 Cut 2 (F7): ArenaID!=0 Datum with non-nil Buf must never enter stratum D")
		}
		cells[i] = materializeBuildDatum(o.buildBytes, d)
	}
	return Row(cells)
}

// materializeBuildDatum re-homes one arena-backed Datum's payload into the
// build (stratum-B) context, preserving the (offset,length)+ArenaID encoding
// readers resolve. Value-semantics are identical to MaterializeArena's for
// every input that can occur; only the destination (and therefore the
// lifetime) differs: join-bounded instead of per-Datum heap / process Perm.
//
// Re-homed (raw byte copy, no decode round-trip):
//   - KindString/KindBytes with ArenaID!=0 (replaces make+copy);
//   - KindNumeric flagBigNumeric (replaces the Perm lane — the per-row
//     process-lifetime leak on the Q8 shape; the copy moves sign+magnitude
//     bytes directly, skipping NumericBigValue's big.Int decode and
//     newBigNumericInCtx's re-encode temporaries).
//
// Passed through or legacy-fallback (never re-homed):
//   - ArenaID==0 (fixed-width, owned Buf, KindEnum labels, KindToastPointer
//     bodies — re-homing those would shorten/mis-scope their lifetime, and
//     chunk slabs are GC-noscan so Buf pointers would be invisible to the
//     mark phase: §3.4/F1, structural);
//   - ArenaID!=0 with Buf!=nil or on any other Kind (representation drift —
//     Cut 2's F7 assertion owns that classification; here the legacy path).
func materializeBuildDatum(bctx *mmgr.Context, d Datum) Datum {
	if d.ArenaID == 0 || bctx == nil {
		return d
	}
	if d.Buf != nil {
		return d.MaterializeArena()
	}
	switch {
	case d.Kind == KindString || d.Kind == KindBytes:
	case d.Kind == KindNumeric && d.Flags&flagBigNumeric != 0:
	default:
		return d.MaterializeArena()
	}
	length := uint32(d.Int & 0xFFFFFFFF)
	if length == 0 {
		return Datum{Kind: d.Kind}
	}
	srcCtx := mmgr.Lookup(d.ArenaID)
	if srcCtx == nil {
		return Datum{Kind: d.Kind}
	}
	src := srcCtx.Bytes(uint32(d.Int>>32), length)
	if len(src) != int(length) {
		return Datum{Kind: d.Kind}
	}
	nd := d
	nd.ArenaID = bctx.ID()
	off, ln := bctx.AllocBytes(src)
	nd.Int = int64(off)<<32 | int64(ln)&0xFFFFFFFF
	return nd
}
