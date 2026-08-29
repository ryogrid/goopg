package executor

import (
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// heapFillfactor resolves the `fillfactor` reloption of the relation behind
// rel, returning storage.HeapDefaultFillfactor (100) when the relation has no
// explicit setting or is not a user table at all (catalog heaps, TOAST
// relations and the throwaway relations used by tests all take this path).
//
// PG reads the value straight off the open Relation's rd_options, which the
// relcache has already materialised for the caller
// (RelationGetFillFactor, postgres/src/include/utils/rel.h:374). goopg's
// writeHeapRowReturning holds only a RelFileNode, and catalog.InMemory has no
// OID index — LookupTableByOIDAllDBs walks every namespace's table map — so
// resolving on every inserted row would put an O(tables) scan under a shared
// RLock on the hottest write path in the engine. The per-Context memo below is
// this layer's stand-in for the relcache entry: one resolution per relation
// per session, an ordinary map hit thereafter.
//
// Staleness is bounded and harmless in kind: fillfactor changes only how
// densely future inserts pack pages, never which rows exist or what they
// contain. ALTER TABLE ... SET/RESET (fillfactor) drops the entry for its own
// session (see invalidateHeapFillfactor); a concurrent session keeps its
// memoised value until it reconnects. See the M0134-0175a deferral row.
func (ctx *Context) heapFillfactor(rel storage.RelFileNode) int {
	if ctx == nil || rel.RelOid == 0 {
		return storage.HeapDefaultFillfactor
	}
	if ff, ok := ctx.heapFillfactorCache[rel.RelOid]; ok {
		return ff
	}
	ff := storage.HeapDefaultFillfactor
	if im, ok := ctx.Catalog.(*catalog.InMemory); ok && im != nil {
		if tbl, _, found := im.LookupTableByOIDAllDBs(rel.RelOid); found && tbl != nil {
			// catalog.Table.Fillfactor keeps PG's "unset" convention as 0
			// (operators_ddl.go's CREATE TABLE path only assigns a non-zero
			// value when WITH (fillfactor=N) was given), and the DDL path has
			// already bounds-checked N into [10,100].
			if tbl.Fillfactor > 0 {
				ff = tbl.Fillfactor
			}
		}
	}
	if ctx.heapFillfactorCache == nil {
		ctx.heapFillfactorCache = make(map[uint32]int)
	}
	ctx.heapFillfactorCache[rel.RelOid] = ff
	return ff
}

// invalidateHeapFillfactor drops the memoised fillfactor for relOID so the
// next insert re-reads it from the catalog. Called by the ALTER TABLE
// SET/RESET reloptions path, which is the only way an existing relation's
// fillfactor can change.
func (ctx *Context) invalidateHeapFillfactor(relOID uint32) {
	if ctx == nil || ctx.heapFillfactorCache == nil {
		return
	}
	delete(ctx.heapFillfactorCache, relOID)
}
