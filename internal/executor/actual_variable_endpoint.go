package executor

import (
	"strings"

	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// visitedPagesLimit is selfuncs.c's VISITED_PAGES_LIMIT: how many heap pages
// holding only dead tuples get_actual_variable_endpoint will read before it
// gives up on the probe.
const visitedPagesLimit = 100

// ActualVariableEndpointProbe returns the planner's get_actual_variable_endpoint
// (selfuncs.c; M0146-0233), wired into catalog.ActualVariableEndpointFunc at
// startup. For a btree index whose leading column is the probed one, it reads
// the index from its first entry (last=false) or its last (last=true) and
// returns the first entry that has a non-null leading key and a heap tuple
// some snapshot may still see, rendered as ANALYZE renders histogram bounds.
//
// Visibility follows PG's SnapshotNonVacuumable probe: an entry on an
// all-visible heap page is taken without a heap visit; otherwise the HOT chain
// is read and the entry is skipped when every member is dead to all
// (heapChainDeadToAll, the predicate prune and VACUUM use). A heap page found
// to hold only dead tuples counts toward VISITED_PAGES_LIMIT, and passing it
// abandons the probe. Index entries already marked dead are never returned by
// the scan, as index_getnext_tid skips killed tuples.
func ActualVariableEndpointProbe(pool *storage.Pool, txn *transam.Manager, vm *storage.VisibilityMap) func(cat catalog.Catalog, idx *catalog.Index, last bool) (string, bool) {
	return func(cat catalog.Catalog, idx *catalog.Index, last bool) (string, bool) {
		if pool == nil || cat == nil || idx == nil || idx.Table == nil {
			return "", false
		}
		ctx := NewContext()
		ctx.Pool = pool
		ctx.Catalog = cat
		ctx.TxnMgr = txn
		desc := ctx.pgIndexKeyDesc(idx)
		if desc == nil {
			return "", false
		}
		keyCols := pgIndexKeyColumns(idx)
		if len(keyCols) == 0 {
			return "", false
		}
		idxRel := cat.IndexRelFileNode(idx)
		if !pool.Exists(idxRel) {
			return "", false
		}
		bt, err := nbtree.OpenWithOptions(pool, idxRel, nbtree.Options{KeyDesc: desc})
		if err != nil {
			return "", false
		}
		heapRel := cat.RelFileNode(idx.Table)
		var oldestXmin storage.TransactionID
		if txn != nil {
			oldestXmin = txn.OldestXmin()
		}
		var (
			value     string
			found     bool
			visited   int
			lastBlock = storage.InvalidBlockNumber
		)
		visit := func(key []byte, ptr storage.ItemPointer) (bool, error) {
			datums, derr := pgIndexTupleKeyDatums(desc, keyCols, key)
			if derr != nil || len(datums) == 0 || datums[0].IsNull() {
				return true, nil // the scan key is IS NOT NULL
			}
			if vm == nil || !vm.AllVisible(heapRel, ptr.Block) {
				dead := false
				if slot, perr := pool.Pin(storage.BufferTag{Rel: heapRel, Block: ptr.Block}); perr == nil {
					slot.RLock()
					dead = oldestXmin != storage.InvalidTransactionID && heapChainDeadToAll(slot.Page(), ptr.Offset, oldestXmin)
					slot.RUnlock()
					pool.Unpin(slot)
				}
				if dead {
					if ptr.Block != lastBlock {
						lastBlock = ptr.Block
						visited++
						if visited > visitedPagesLimit {
							return false, nil
						}
					}
					return true, nil
				}
			}
			// The key aliases the pinned leaf: render it before returning.
			value, found = formatDatumDateStyle(datums[0], nil), true
			return false, nil
		}
		if last {
			err = bt.ScanFromEnd(visit)
		} else {
			err = bt.RangeScan(nil, nil, visit)
		}
		if err != nil || !found {
			return "", false
		}
		return value, true
	}
}

// ActualVariableRange is get_actual_variable_range's index selection
// (selfuncs.c): the first index on tbl that can answer the column's minimum or
// maximum. That is a btree, with no predicate (a partial index does not cover
// the relation), whose first key column is the plain column under its own
// collation and the type's default operator class. A descending index answers
// from the opposite end. A partitioned parent has no real indexes to read.
func ActualVariableRange(probe func(cat catalog.Catalog, idx *catalog.Index, last bool) (string, bool), cat catalog.Catalog, tbl *catalog.Table, column string, wantMax bool) (string, bool) {
	if probe == nil || cat == nil || tbl == nil || column == "" || len(tbl.PartitionKey) > 0 {
		return "", false
	}
	var colColl string
	found := false
	for _, c := range tbl.Columns {
		if c.Name == column {
			colColl, found = c.Collation, true
			break
		}
	}
	if !found {
		return "", false
	}
	var idxs []*catalog.Index
	if tbl.DBOid != 0 {
		idxs = cat.IndexesOnTable(tbl, tbl.DBOid)
	} else {
		idxs = cat.IndexesOnTable(tbl)
	}
	for _, idx := range idxs {
		if idx == nil || (idx.Method != "" && idx.Method != "btree") || idx.HasPredicate || len(idx.Columns) == 0 {
			continue
		}
		if idx.Columns[0] != column || (len(idx.ColExprs) > 0 && idx.ColExprs[0] != nil) {
			continue
		}
		if len(idx.ColCollations) > 0 && idx.ColCollations[0] != "" && idx.ColCollations[0] != colColl {
			continue
		}
		if len(idx.ColOpClasses) > 0 && idx.ColOpClasses[0] != "" && !isDefaultBtreeOpClassName(idx.ColOpClasses[0]) {
			continue
		}
		last := wantMax
		if len(idx.ColDescending) > 0 && idx.ColDescending[0] {
			last = !last
		}
		if v, ok := probe(cat, idx, last); ok {
			return v, true
		}
	}
	return "", false
}

// isDefaultBtreeOpClassName reports whether a stamped btree operator class
// orders by the type's default `<`, the operator a histogram is built with.
// The pattern classes (text_pattern_ops and kin) order bytewise under their
// own operator family, so get_op_opfamily_strategy finds no strategy for the
// histogram's sort operator and PG skips the index.
func isDefaultBtreeOpClassName(name string) bool {
	return !strings.HasSuffix(name, "_pattern_ops")
}
