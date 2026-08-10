package executor

import (
	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// ---------------------------------------------------------------------------
// M0130-S11.4 slice 3b-2c-ii-A — the descriptor PLUMBING, behaviour-preserving.
//
// 3b-2c-i put one comparer (`keyComparer`) behind every ordering decision in
// `internal/access/btree`, and 3b-2b built `buildPGIndexKeyDesc`, the
// catalog → `btree.PGIndexKeyDesc` mapper. What was missing between them is a
// route: the engine opened index btrees from nineteen scattered
// `btree.Open(ctx.Pool, idxRel)` / `btree.CreateWithXID` / `BulkCreateWithXID`
// calls, none of which had ever seen an index descriptor, so
// `Options.KeyDesc` was nil everywhere by construction.
//
// This file is that route, and nothing more. Every index btree the executor
// opens or builds now goes through one of the three helpers below, each of
// which resolves the descriptor once and passes it in `Options`. The FLIP —
// making the writers emit tuple-shaped, per-column-datum keys so a non-nil
// descriptor is correct — is 3b-2c-ii-B, and is REINDEX-required; until it
// lands `pgIndexTupleKeys` is false and every helper hands the btree a nil
// descriptor, i.e. exactly the bytewise `CompareKeys` ordering the current
// on-disk keys are built for.
//
// Splitting the plumbing out of the flip is not cosmetic. The flip has to be
// atomic across the writers and the comparison layer (3b-2c-i's header
// explains why a half-flipped tree mis-ORDERS rather than merely mis-reads),
// and "did every btree-opening site learn about descriptors?" is a question
// about nineteen call sites that is much better answered against a tree whose
// behaviour has not moved.
//
// See docs/design/0130-0011-nbtree-pg-on-disk-format.md.
// ---------------------------------------------------------------------------

// pgIndexTupleKeys gates the per-column-datum key format.
//
// It is a var rather than a const so the plumbing is testable ahead of the
// flip: `pgindex_btree_test.go` turns it on to assert that a descriptor
// actually reaches the tree, and that an index this layer cannot describe
// still opens (with nil) instead of failing. Production code must never write
// it — 3b-2c-ii-B deletes the gate along with the last blob writer.
//
// false: `Options.KeyDesc` is always nil → `CompareKeys` (bytes.Compare) over
// goopg's opaque order-preserving key encoding, which is what is on disk.
var pgIndexTupleKeys = false

// pgIndexKeyDesc returns the key descriptor for idx, or nil when this layer
// cannot order the index exactly the way PostgreSQL does.
//
// A nil result is NOT an error: `buildPGIndexKeyDesc` deliberately refuses
// expression keys, explicit operator classes, non-bytewise collations and
// every type without a 3b-2a comparator, and those indexes must keep working.
// They keep the blob key path — which is why the flip cannot simply delete it
// (a dual-format decision 3b-2c-ii-B has to make explicit; see the ledger).
//
// The result is memoised per statement because the hot callers are per-ROW:
// maintainUniqueIndexesForInsert and friends open every index on the table for
// every tuple written, and buildPGIndexKeyDesc walks the key columns doing
// string type-name resolution. The cache is keyed by index OID and lives on
// the per-statement Context, so the only staleness window is DDL that alters a
// key column's type inside the very statement that is also writing through the
// index — which cannot happen, since ALTER TABLE takes AccessExclusiveLock.
func (ctx *Context) pgIndexKeyDesc(idx *catalog.Index) *btree.PGIndexKeyDesc {
	if !pgIndexTupleKeys || idx == nil {
		return nil
	}
	if desc, ok := ctx.pgKeyDescCache[idx.OID]; ok {
		// Present-but-nil is the memoised "cannot describe" answer; the
		// comma-ok is what keeps that from re-deriving the failure per row.
		return desc
	}
	desc, err := buildPGIndexKeyDesc(idx)
	if err != nil {
		desc = nil
	}
	if ctx.pgKeyDescCache == nil {
		ctx.pgKeyDescCache = make(map[uint32]*btree.PGIndexKeyDesc)
	}
	ctx.pgKeyDescCache[idx.OID] = desc
	return desc
}

// compositeUpperBound returns the inclusive upper bound to hand
// `RangeScan`/`RangeScanPos` for a probe on a COMPOSITE index whose search key
// may name only the leading key attributes.
//
// M0130-S11.4 slice 3b-2c-ii-B2-c-ii — the upper-bound funnel. The two key
// formats express "everything under this prefix" in opposite ways, and this is
// the one place that knows which:
//
//   - blob (desc == nil): a page key is the concatenation of every column's
//     encoding, and `CompareKeys` is `bytes.Compare`, so a leading-column key
//     compares BELOW every member of its own group. The bound has to be faked
//     upwards with bytes — `appendCompositeUpperPadding`'s 64 trailing 0xFF
//     (M0053-0001). Nothing about that is a valid key; it is a byte string
//     chosen to sort above any plausible suffix encoding.
//   - tuple (desc != nil): the key IS the prefix, unchanged. 0xFF padding is
//     not available (an 0xFF run is a malformed attribute image, not a large
//     one, and upstream never invents a maximal key either — `_bt_check_compare`
//     stops when the compared ATTRIBUTES exceed the bound). Instead slice
//     3b-2c-ii-B2-c-i taught the scan's high-end test to read a truncated bound
//     as PLUS infinity beyond the attributes it names (`indexFormat.compareHigh`),
//     which is exactly what a prefix pivot means. A bound that happens to name
//     every key attribute is unaffected: `compareHigh` then agrees with
//     `compare` attribute for attribute, heap-TID tiebreak included.
//
// So under the tuple format this is a no-op, and the callers' `len(Columns) > 1`
// guard becomes merely a cheap skip rather than a correctness condition —
// widening is a blob-format repair, not a scan requirement.
//
// The returned slice is caller-owned in the blob case and ALIASES key in the
// tuple case; callers only read it (they hand it straight to a scan).
func (ctx *Context) compositeUpperBound(idx *catalog.Index, key []byte) []byte {
	if ctx.pgIndexKeyDesc(idx) != nil {
		return key
	}
	return appendCompositeUpperPadding(key)
}

// indexBTreeOptions is the Options every index btree in the engine is
// opened/created with: the pool's split-WAL hook (what plain btree.Open
// supplies) plus this index's key descriptor.
func indexBTreeOptions(ctx *Context, idx *catalog.Index) btree.Options {
	opts := btree.Options{KeyDesc: ctx.pgIndexKeyDesc(idx)}
	if ctx.Pool != nil {
		// btree.Open takes the same hook from the pool; asking a nil pool for
		// it dereferences (storage.(*Pool).LogBtreeSplit). The open itself
		// fails right after on a nil pool, so guarding here only keeps the
		// options constructor callable on its own — which is what lets the
		// descriptor wiring be tested without a buffer pool.
		opts.LogSplit = btree.PoolLogSplit(ctx.Pool)
	}
	return opts
}

// openIndexBTree opens the btree backing a catalog index. It replaces the
// engine's direct `btree.Open(ctx.Pool, idxRel)` calls: idxRel alone cannot
// name a descriptor, idx can.
//
// idxRel is passed separately rather than re-derived from idx because several
// callers open a relfilenode that is NOT ctx.Catalog.IndexRelFileNode(idx) —
// REINDEX CONCURRENTLY builds into a shadow relfile that carries the same key
// shape as the index it will replace.
func openIndexBTree(ctx *Context, idx *catalog.Index, idxRel storage.RelFileNode) (*btree.BTree, error) {
	return btree.OpenWithOptions(ctx.Pool, idxRel, indexBTreeOptions(ctx, idx))
}

// createIndexBTree initialises an empty btree for a catalog index, stamping
// the creating xid onto the block-0 smgr-create record (what
// btree.CreateWithXID does) and installing the key descriptor.
func createIndexBTree(ctx *Context, idx *catalog.Index, idxRel storage.RelFileNode) (*btree.BTree, error) {
	opts := indexBTreeOptions(ctx, idx)
	opts.CreateXID = ctx.Tx.XID
	return btree.CreateWithOptions(ctx.Pool, idxRel, opts)
}

// bulkCreateIndexBTree sort-builds a btree for a catalog index (CREATE INDEX /
// REINDEX), installing the key descriptor so the build's own sort and the
// later readers agree on the ordering — the one place where disagreeing would
// produce a tree that is wrong from birth rather than wrong on the next write.
func bulkCreateIndexBTree(ctx *Context, idx *catalog.Index, idxRel storage.RelFileNode, entries []btree.BulkEntry) (*btree.BTree, error) {
	opts := indexBTreeOptions(ctx, idx)
	opts.CreateXID = ctx.Tx.XID
	return btree.BulkCreateWithOptions(ctx.Pool, idxRel, entries, opts)
}
