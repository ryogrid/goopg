package executor

// applyworker_lookup.go — how the apply worker finds the local row an
// UPDATE or DELETE names (M0149-0054a, subscription/t/032_subscribe_use_index.pl).
//
// PG (logical/relation.c FindLogicalRepLocalIndex, execReplication.c
// RelationFindReplTupleByIndex / RelationFindReplTupleSeq):
//
//   - the local index is the replica identity index or the primary key
//     (GetRelationIdentityOrPK); failing that, when the publisher sends the
//     whole old row (REPLICA IDENTITY FULL), the first index in OID order
//     that IsIndexUsableForReplicaIdentityFull accepts — a btree or hash
//     index, not partial, whose leftmost key is a plain column present in
//     the remote relation;
//   - the lookup scans that index with equality keys on its plain columns,
//     rechecks the whole tuple when the index is not the identity, and stops
//     at the FIRST match; with no index it scans the heap for the first
//     match. Each index lookup is one index scan (pgstat_count_index_scan:
//     pg_stat_all_indexes.idx_scan).
//
// Exactly one tuple is updated or deleted: goopg's earlier heap scan
// deleted every matching row, so a REPLICA IDENTITY FULL UPDATE of one of
// several identical rows removed all of them and inserted one back.

import (
	"sort"

	"golang.org/x/text/collate"

	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// applyLocalIndex is FindLogicalRepLocalIndex for local table tbl. claimed
// marks the local columns a remote attribute maps onto (applyRel.claimed);
// nil treats every column as present.
func applyLocalIndex(ctx *Context, tbl *catalog.Table, remote *xlog.DecodedRelation, claimed []bool) *catalog.Index {
	if ctx == nil || ctx.Catalog == nil || tbl == nil {
		return nil
	}
	idxs := append([]*catalog.Index(nil), ctx.Catalog.IndexesOnTable(tbl, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))...)
	// RelationGetIndexList returns the indexes in OID order.
	sort.Slice(idxs, func(i, j int) bool { return idxs[i].OID < idxs[j].OID })
	// GetRelationIdentityOrPK: RelationGetReplicaIndex (the USING INDEX
	// index, or the primary key under DEFAULT), else the primary key.
	if catalog.ReplIdentOrDefault(tbl.ReplicaIdentity)[0] == 'i' {
		for _, idx := range idxs {
			if idx.IsReplicaIdentity && idx.IsImmediate() {
				return idx
			}
		}
	}
	for _, idx := range idxs {
		if idx.Primary && idx.IsImmediate() {
			return idx
		}
	}
	if remote == nil || remote.Replident != 'f' {
		return nil
	}
	for _, idx := range idxs {
		if applyIndexUsableForReplicaIdentityFull(tbl, idx, claimed) {
			return idx
		}
	}
	return nil
}

// applyIndexUsableForReplicaIdentityFull is IsIndexUsableForReplicaIdentityFull:
// an access method with amgettuple and an equality strategy (btree, hash),
// not partial, and a leftmost key that is a plain column the remote
// relation has.
func applyIndexUsableForReplicaIdentityFull(tbl *catalog.Table, idx *catalog.Index, claimed []bool) bool {
	switch idx.Method {
	case "", "btree", "hash":
	default:
		return false
	}
	if idx.HasPredicate || idx.Predicate != nil || len(idx.Columns) == 0 || idx.Columns[0] == "" {
		return false
	}
	if len(idx.ColExprs) > 0 && idx.ColExprs[0] != nil {
		return false
	}
	pos := columnPosition(tbl.Columns, idx.Columns[0])
	if pos < 0 {
		return false
	}
	return claimed == nil || (pos < len(claimed) && claimed[pos])
}

func columnPosition(cols []catalog.Column, name string) int {
	for i, c := range cols {
		if c.Name == name && !c.Dropped {
			return i
		}
	}
	return -1
}

// applyFoundTuple is the located row: its heap TID (the visible chain
// member) and a copy of its decoded columns.
type applyFoundTuple struct {
	ptr storage.ItemPointer
	row Row
}

// applyFindTuple finds the first visible row of rel matching keyRow
// (rowMatchesKeyCollated), through idx when one is given and a probe can be
// built, by heap scan otherwise. A nil result means no row matched.
func applyFindTuple(ctx *Context, rel storage.RelFileNode, tbl *catalog.Table, cols []catalog.Column, keyRow Row, idx *catalog.Index) (*applyFoundTuple, error) {
	colls := nondeterministicCollators(ctx, cols)
	if idx != nil && tbl != nil {
		if found, used, err := applyFindTupleByIndex(ctx, rel, tbl, cols, keyRow, idx, colls); used || err != nil {
			return found, err
		}
	}
	return applyFindTupleSeq(ctx, rel, cols, keyRow, colls)
}

// applyFindTupleByIndex is RelationFindReplTupleByIndex. used is false when
// the index cannot serve this key (no plain leading column with a value, or
// an index whose entries may omit NULL-keyed rows), and the caller scans the
// heap instead.
func applyFindTupleByIndex(ctx *Context, rel storage.RelFileNode, tbl *catalog.Table, cols []catalog.Column, keyRow Row, idx *catalog.Index, colls []*collate.Collator) (*applyFoundTuple, bool, error) {
	// An index that does not store NULL-keyed entries (the blob format, or a
	// cluster without the capability — goopg's interim guard) omits a row
	// with a NULL key, so it can serve the lookup only when the row being
	// looked for has an entry: a value in every plain key column, and a key
	// the maintenance path would file for it (indexEntryKey, or
	// encodeExprIndexKey for an expression index — NULL expressions file
	// nothing). A matching row has the same key-column values, hence the
	// same entry.
	if !catalog.IndexHasNullKeyedEntries(idx) && !applyIndexHasEntryFor(ctx, idx, tbl, cols, keyRow) {
		return nil, false, nil
	}
	// goopg's index keys compare bytewise: a key column with a
	// nondeterministic collation (equal values with different bytes, 'FOO'
	// = 'foo') cannot be probed, so the collation-aware heap scan decides
	// (012_collation; PG's index compares under the collation).
	for _, name := range idx.Columns {
		if p := columnPosition(tbl.Columns, name); p >= 0 && p < len(colls) && colls[p] != nil {
			return nil, false, nil
		}
	}
	// Equality on the leading plain key columns the key row has a value
	// for (build_replindex_scan_key keys every plain column; the rest are
	// rechecked below, as tuples_equal does for a non-identity index).
	var parts []indexProbeKeyPart
	for i, name := range idx.Columns {
		if name == "" || (i < len(idx.ColExprs) && idx.ColExprs[i] != nil) {
			break
		}
		p := columnPosition(tbl.Columns, name)
		if p < 0 || p >= len(keyRow) || keyRow[p].IsNull() {
			break
		}
		parts = append(parts, indexProbeKeyPart{col: &tbl.Columns[p], val: keyRow[p]})
	}
	if len(parts) == 0 {
		return nil, false, nil
	}
	lo, encErr := ctx.indexProbeKey(idx, parts)
	if encErr != nil {
		return nil, false, nil
	}
	hi := ctx.compositeUpperBound(idx, lo)
	tree, err := openIndexBTree(ctx, idx, ctx.Catalog.IndexRelFileNode(idx))
	if err != nil {
		return nil, false, nil
	}
	recordApplyIndexScan(ctx, idx.OID)
	var found *applyFoundTuple
	scanRow := make(Row, len(cols))
	err = tree.RangeScan(lo, hi, func(_ []byte, ptr storage.ItemPointer) (bool, error) {
		s, perr := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: ptr.Block})
		if perr != nil {
			return true, nil
		}
		s.RLock()
		eachHeapChainMember(s.Page(), ptr.Offset, func(t storage.HeapTuple, mslot uint16) bool {
			if !transam.TupleVisibleSubxact(t.Header, ctx.Snap, ctx.Tx.XID, ctx.TxnMgr, ctx.CmdID, ctx.comboStore(), ctx.MultiXact) {
				return true
			}
			if err := DecodeHeapTupleRowInto(scanRow, cols, t, nil); err == nil && rowMatchesKeyCollated(scanRow, keyRow, colls) {
				found = &applyFoundTuple{ptr: storage.ItemPointer{Block: ptr.Block, Offset: mslot}, row: append(Row(nil), scanRow...)}
			}
			return false
		})
		s.RUnlock()
		ctx.Pool.Unpin(s)
		return found == nil, nil
	})
	if err != nil {
		return nil, true, err
	}
	return found, true, nil
}

// applyIndexHasEntryFor reports whether idx files an entry for keyRow, by
// the same key builders maintainUniqueIndexesForInsert uses.
func applyIndexHasEntryFor(ctx *Context, idx *catalog.Index, tbl *catalog.Table, cols []catalog.Column, keyRow Row) bool {
	for _, name := range idx.Columns {
		if name == "" {
			continue
		}
		p := columnPosition(tbl.Columns, name)
		if p < 0 || p >= len(keyRow) || keyRow[p].IsNull() {
			return false
		}
	}
	key, err := ctx.indexEntryKey(idx, cols, keyRow, storage.ItemPointer{})
	if err != nil {
		return false
	}
	if key == nil && ctx.pgIndexKeyDesc(idx) == nil {
		key = encodeExprIndexKey(ctx, idx, tbl, keyRow)
	}
	return key != nil
}

// applyFindTupleSeq is RelationFindReplTupleSeq: the first visible heap
// row matching keyRow.
func applyFindTupleSeq(ctx *Context, rel storage.RelFileNode, cols []catalog.Column, keyRow Row, colls []*collate.Collator) (*applyFoundTuple, error) {
	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return nil, err
	}
	scanRow := make(Row, len(cols))
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return nil, err
		}
		s.RLock()
		page := s.Page()
		if storage.IsNew(page) {
			s.RUnlock()
			ctx.Pool.Unpin(s)
			continue
		}
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			s.RUnlock()
			ctx.Pool.Unpin(s)
			return nil, err
		}
		for slot := uint16(1); slot <= uint16(count); slot++ {
			tup, err := storage.PageGetHeapTuple(page, slot)
			if err != nil {
				continue
			}
			if !transam.TupleVisibleSubxact(tup.Header, ctx.Snap, ctx.Tx.XID, ctx.TxnMgr, ctx.CmdID, ctx.comboStore(), ctx.MultiXact) {
				continue
			}
			if err := DecodeHeapTupleRowInto(scanRow, cols, tup, nil); err != nil {
				continue
			}
			if rowMatchesKeyCollated(scanRow, keyRow, colls) {
				out := &applyFoundTuple{ptr: storage.ItemPointer{Block: blk, Offset: slot}, row: append(Row(nil), scanRow...)}
				s.RUnlock()
				ctx.Pool.Unpin(s)
				return out, nil
			}
		}
		s.RUnlock()
		ctx.Pool.Unpin(s)
	}
	return nil, nil
}

// applyDeleteTuple stamps xmax on the located tuple (simple_table_tuple_delete).
func applyDeleteTuple(ctx *Context, rel storage.RelFileNode, ptr storage.ItemPointer) error {
	sw, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: ptr.Block})
	if err != nil {
		return err
	}
	sw.Lock()
	oldTup, _ := storage.PageGetHeapTuple(sw.Page(), ptr.Offset)
	oldBytes, _ := oldTup.MarshalBinary()
	if err := storage.PageSetHeapTupleXmax(sw.Page(), ptr.Offset, ctx.Tx.XID); err == nil {
		_ = markHeapDeleteDirtyAndClearVM(ctx, sw, rel, ptr.Block, ptr.Offset, ctx.Tx.XID, oldBytes)
	}
	sw.Unlock()
	ctx.Pool.Unpin(sw)
	return nil
}
