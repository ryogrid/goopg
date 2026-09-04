package executor

import (
	"github.com/goopg/goopg/internal/utils/mmgr"
)

// dense_build.go — EX3-02 Cut 1 (stratum B only).
//
// A per-joinOp build arena (stratum B) for variable-width payloads. The
// build-loop retention call (retainBuildRow, the Cut-1 form of ownedBuildRow)
// routes arena-backed String/Bytes bodies and big-numeric sign+magnitude
// bodies into buildBytes.AllocBytes instead of per-Datum make/Perm
// allocations. Row headers stay per-row (make/acquireRow unchanged);
// readers are unchanged (same (offset,length)+ArenaID encoding resolved
// through the mmgr registry).
//
// Lifetime: one mmgr.Context per joinOp, parented to the statement context
// (ctx.Mctx), created at build start (buildLazyHashTable, next to
// presizeLazyHash) and released at joinOp Close in the serial case. The
// shared-build (parallel) case transfers ownership to sharedHashBuild,
// released at statement end via the parent chain — "drop at Close" there
// would be a use-after-release against workers probing after the builder is
// gone, or a ContextID-ABA corruption past Release (design §§2.3/3.1, F2/F6;
// full shared-path teardown lands in Cut 3).
//
// Cut-1 boundary: MaterializeArena/cloneRowOwned are BIT-IDENTICAL (datum.go
// untouched). materializeBuildDatum is build-only and used solely by the
// build-loop retention call; every other retention path (drainRowsCtx,
// Materialize, transferRowForQueue, the FOR UPDATE ctid build) keeps the
// legacy Perm lane.

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

// retainBuildRow copies a build-side row into storage the hash table can hold
// for the life of the join. Header discipline is identical to ownedBuildRow
// (arena lane via acquireRow, else per-row make — Cut 1 does NOT pack Datum
// cells; that is Cut 2's stratum D). Only the payload destination changes:
// arena-backed variable-width/big-numeric bodies land in stratum B instead
// of per-Datum make/Perm allocations.
//
// The M0097-0058 contract is unchanged: the copy still detaches from the
// producer arena (stratum B copies out of it exactly as MaterializeArena's
// make did), and non-arena rows still take the O(width) struct copy (the
// source aliases the producer's reused slot).
func (o *joinOp) retainBuildRow(row Row) Row {
	if o.buildBytes == nil {
		return ownedBuildRow(row)
	}
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
