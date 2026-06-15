package amcheck

import (
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// This file ports the relation-walking driver of upstream verify_heapam() — the
// outer loop of the SRF body (postgres/contrib/amcheck/verify_heapam.c:367-405
// and 480-501) that resolves the block range, iterates the relation's blocks,
// and runs the per-page checks (already ported as VerifyHeapPage*) on each.
//
// It is the heap-side counterpart to the B-tree engine's relation-walking
// entry points (VerifyBtreeLevelSiblingLinks / VerifyBtreeParentDownlinks),
// which already take a PageSource seam. Keeping the block loop here — behind the
// same PageSource seam — rather than in the eventual verify_heapam SRF executor
// op means the SQL surface (slice S3 in docs/design/0110-0008) reduces to a thin
// adapter: fill a PageSource from goopg's smgr/buffer manager, supply nblocks
// and (optionally) the relation's natts + a clog-backed XidStatusFunc, and emit
// one output row per returned HeapRelReport. The block-iteration logic — which is
// faithfully page-structural and needs no goopg execution context — is unit
// tested here with a fake PageSource instead of through the executor.
//
// What this driver deliberately does NOT do, mirroring where upstream draws the
// line: the relkind / relam guard (verify_heapam.c:333-349, "cannot check
// relation" / "only heap AM is supported") and relation open/lock are catalog-
// and goopg-storage-coupled, so they stay in the SRF executor (slice S3); the
// caller is responsible for only invoking this on a heap relation. The toast
// relation walk and the read-stream skip-pages optimisation are likewise out of
// scope (check_toast is goopg-divergent — see the package doc — and the skip
// option is a buffer-manager concern). What remains here is exactly the part
// that is a deterministic function of the page bytes plus the block range.

// HeapRelReport is one corruption finding from a relation walk: a per-page
// Report (Offset + Msg) tagged with the block it was found on. It maps 1:1 to a
// verify_heapam() SRF output row — Blkno→blkno, Offset→offnum, Msg→msg — with
// the SRF supplying attnum (always -1 for these page-structural checks, which
// are not attribute-specific). It is a flattened HeapRelReport-per-finding
// rather than a per-block grouping so the SRF can stream rows without
// regrouping.
type HeapRelReport struct {
	Blkno  storage.BlockNumber
	Offset uint16
	Msg    string
}

// HeapRelOptions carries the verify_heapam() SRF arguments that affect the block
// walk. StartBlock / EndBlock mirror the SRF's startblock / endblock int8 args: a
// nil pointer is SQL NULL, which upstream defaults to 0 and nblocks-1
// respectively (verify_heapam.c:379-405). XidStatus is threaded straight through
// to VerifyHeapPageWithXminStatus to enable the clog-dependent HOT-chain checks;
// a nil XidStatus disables exactly those, matching the page-bytes-only entry
// points. Rel.Natts, when non-zero, enables the tuple-natts-vs-table check.
type HeapRelOptions struct {
	StartBlock *int64
	EndBlock   *int64
	Rel        RelDesc
	XidStatus  XidStatusFunc
}

// VerifyHeapRelation walks blocks [start, end] of a heap relation through the
// per-page checker and returns every structural-corruption finding, in
// (block, offset) order. nblocks is the relation's current block count
// (RelationGetNumberOfBlocks); src yields each block's raw 8 KiB bytes by number
// — the SRF fills it from the buffer manager, tests from a map.
//
// It mirrors the verify_heapam() SRF body: an empty relation (nblocks == 0)
// yields no rows (verify_heapam.c:367-373, the SRF's PG_RETURN_NULL); otherwise
// the start/end block numbers are resolved from opts (NULL → 0 / nblocks-1) and
// range-checked, returning an error whose text matches upstream's
// ERRCODE_INVALID_PARAMETER_VALUE ereports (lines 386-404) when out of range.
// Each block is read via src and run through VerifyHeapPageWithXminStatus; a
// brand-new (all-zero) page contributes nothing, exactly as the per-page checker
// short-circuits it. A read error or an unparseable page header is returned as
// an error (the SRF surfaces it), not swallowed.
func VerifyHeapRelation(src PageSource, nblocks storage.BlockNumber, opts HeapRelOptions) ([]HeapRelReport, error) {
	if src == nil {
		return nil, fmt.Errorf("amcheck: nil page source")
	}

	// Early exit for an empty relation: upstream returns no rows
	// (verify_heapam.c:367-373).
	if nblocks == 0 {
		return nil, nil
	}

	// Resolve and validate the block range. A nil pointer is SQL NULL, which
	// defaults to the whole relation (verify_heapam.c:379-405).
	firstBlock := storage.BlockNumber(0)
	if opts.StartBlock != nil {
		fb := *opts.StartBlock
		if fb < 0 || fb >= int64(nblocks) {
			return nil, fmt.Errorf("starting block number must be between 0 and %d", nblocks-1)
		}
		firstBlock = storage.BlockNumber(fb)
	}
	lastBlock := nblocks - 1
	if opts.EndBlock != nil {
		lb := *opts.EndBlock
		if lb < 0 || lb >= int64(nblocks) {
			return nil, fmt.Errorf("ending block number must be between 0 and %d", nblocks-1)
		}
		lastBlock = storage.BlockNumber(lb)
	}

	var reports []HeapRelReport
	for blkno := firstBlock; blkno <= lastBlock; blkno++ {
		p, err := src(blkno)
		if err != nil {
			return nil, fmt.Errorf("amcheck: reading block %d: %w", blkno, err)
		}
		pageReports, err := verifyHeapPage(p, blkno, opts.Rel, opts.XidStatus)
		if err != nil {
			return nil, fmt.Errorf("amcheck: checking block %d: %w", blkno, err)
		}
		for _, r := range pageReports {
			reports = append(reports, HeapRelReport{Blkno: blkno, Offset: r.Offset, Msg: r.Msg})
		}
		// lastBlock is the final iteration; guard against BlockNumber (uint32)
		// wraparound when lastBlock == math.MaxUint32 (not reachable for a real
		// relation, but keeps the loop total).
		if blkno == lastBlock {
			break
		}
	}
	return reports, nil
}
