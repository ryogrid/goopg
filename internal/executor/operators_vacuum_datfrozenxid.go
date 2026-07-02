package executor

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// persistDatFrozenXID mirrors PostgreSQL's vac_update_datfrozenxid
// (postgres/src/backend/commands/vacuum.c): at VACUUM end it advances the
// on-disk pg_database.datfrozenxid tuple — an in-place overwrite
// (heap_inplace_update), not a new MVCC row version — to the freshly
// recomputed cluster-wide freeze horizon. Without this the persisted tuple
// is permanently stale at its initdb bootstrap value, so an attached PG
// standby or any external tool reading the shared catalog heap directly
// under-reports the freeze horizon (M0117-0008 Part B).
//
// goopg's own CLOG truncation reads catalog.InMemory.DatFrozenXID() directly
// (internal/initdb/open.go's TruncateCLOGFn) and is therefore unaffected by
// whether this persistence step runs or fails — this call exists purely for
// external (standby / tooling) parity. The caller (vacuumOp.Next) must treat
// any returned error as non-fatal to the client's VACUUM, mirroring
// PostgreSQL's own vac_update_datfrozenxid call site (it never rolls back
// VACUUM on account of the pg_database update).
//
// v0 scope (single logical database, docs/design/0117-0008-datfrozenxid-persistence.md):
// only the live database's row — OID = catalog.InMemory.DBOID(), resolved by
// detectCatalogDBOID at startup (e.g. "postgres") — is updated.
func persistDatFrozenXID(ctx *Context) error {
	if ctx == nil || ctx.Pool == nil {
		return nil
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	horizon := im.DatFrozenXID()
	if horizon == storage.InvalidTransactionID {
		// No user table has a valid relfrozenxid yet (e.g. an empty
		// database) — PG's own newFrozenXid seed
		// (GetOldestNonRemovableTransactionId) never leaves this
		// invalid, but goopg's simplified min-over-user-tables can.
		// Nothing to advance to.
		return nil
	}

	rel := catalog.SharedCatalogRelFileNode(catalog.PgDatabaseRelationOID)
	cols := catalog.PgDatabaseColumnsPG18()
	dbOid := im.DBOID()

	nblocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return err
	}
	for blk := storage.BlockNumber(0); blk < nblocks; blk++ {
		slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return err
		}
		updated, done, err := updatePgDatabaseTupleOnPage(ctx, slot, blk, rel, cols, dbOid, horizon)
		ctx.Pool.Unpin(slot)
		if err != nil {
			return err
		}
		if done {
			_ = updated
			return nil
		}
	}
	// The live database's row was not found on any page — nothing to
	// persist (defensive; v0's single-page bootstrap always has it).
	return nil
}

// updatePgDatabaseTupleOnPage scans one pg_database page under the page's
// exclusive content lock for the live tuple whose oid matches dbOid. done is
// true once that row has been located on this page (whether or not the
// horizon actually advanced it) — the caller stops scanning further blocks.
func updatePgDatabaseTupleOnPage(ctx *Context, slot *storage.Slot, blk storage.BlockNumber, rel storage.RelFileNode, cols []catalog.Column, dbOid uint32, horizon storage.TransactionID) (updated, done bool, err error) {
	slot.Lock()
	defer slot.Unlock()
	page := slot.Page()
	count, err := storage.PageLinePointerCount(page)
	if err != nil {
		return false, false, err
	}
	for s := uint16(1); s <= uint16(count); s++ {
		tup, terr := storage.PageGetHeapTuple(page, s)
		if terr != nil {
			continue
		}
		// Live tuple only: inserted (valid xmin) and never deleted (no
		// xmax), same liveness test as
		// internal/initdb/open.go:decodePGDatabasePhysicalRow.
		if tup.Header.Xmin == storage.InvalidTransactionID || tup.Header.Xmax != storage.InvalidTransactionID {
			continue
		}
		if len(tup.Data) < 4 {
			continue
		}
		if pgDatabaseTupleOID(tup.Data) != dbOid {
			continue
		}

		natts := int(tup.Header.Infomask2 & 0x07FF)
		row := make(Row, len(cols))
		if derr := DecodeRowIntoMctxPGTuple(row, cols, tup.Data, tup.Bitmap, natts, nil); derr != nil {
			return false, true, derr
		}
		current := storage.TransactionID(uint32(row[catalog.PgDatabaseDatFrozenXIDOrdinal].Int))
		if current != storage.InvalidTransactionID && !storage.XIDPrecedes(current, horizon) {
			// Already at or ahead of the new horizon — matches PG's
			// dirty-guard (TransactionIdPrecedes(old, new)); no write.
			return false, true, nil
		}
		row[catalog.PgDatabaseDatFrozenXIDOrdinal] = NewIntDatum(int64(horizon))

		newData, eerr := EncodeRowPG(cols, row)
		if eerr != nil {
			return false, true, eerr
		}
		newTup := storage.HeapTuple{Header: tup.Header, Bitmap: tup.Bitmap, Data: newData}
		raw, merr := newTup.MarshalBinary()
		if merr != nil {
			return false, true, merr
		}
		if rerr := storage.PageReplaceItemRaw(page, s, raw); rerr != nil {
			return false, true, rerr
		}
		if ctx.LogCanonical != nil {
			endLSN, lerr := catalog.PgCanonicalHeapInplace(rel, blk, page, s, uint32(ctx.Tx.XID), ctx.LogCanonical)
			if lerr != nil {
				return false, true, lerr
			}
			if endLSN != 0 {
				storage.MustHeader(page).SetLSN(storage.LSN(endLSN))
			}
		}
		ctx.Pool.MarkDirty(slot)
		return true, true, nil
	}
	return false, false, nil
}

// pgDatabaseTupleOID reads the oid column (ordinal 0, offset 0 — always the
// tuple's first fixed-width, non-nullable field) directly from raw physical
// tuple data, mirroring internal/initdb/open.go:decodePGDatabasePhysicalRow.
func pgDatabaseTupleOID(data []byte) uint32 {
	return binary.LittleEndian.Uint32(data[0:4])
}
