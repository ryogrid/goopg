package executor

// B4.6 Stage 1 (docs/design/wal-pg-identical-stream + .ralph/deferral_ledger.md
// "B4.6 pg_database"): CREATE/DROP DATABASE journal a real pg_database heap row
// (SHARED, global/1262) via XLOG_HEAP_INSERT/DELETE + 2671/2672 index
// maintenance, so a real PG18 standby streaming goopg's WAL sees the database
// catalog row. This is ADDITIVE: goopg's own `SELECT * FROM pg_database` is
// served entirely from VirtualRows() (registry-backed — operators_storage.go
// "pg_database is entirely computed by VirtualRows()"), so this heap row is the
// standby's copy + the startup boot-resolution base (detectCatalogDBOID reads
// 1262 raw), exactly the B4.4 pg_subscription split. The goopg-private
// RecordKindCreateDatabase(18)/DropDatabase(19) still carry the registry +
// physical-scaffolding replay; retiring them is B4.6 Stage 3 (RM_DBASE).
//
// Unlike the heap-only shared catalogs (B4.2-B4.4), pg_database's unique
// indexes pg_database_datname_index (2671) and pg_database_oid_index (2672) ARE
// materialized in global/ (initdb bootstraps them as boot-critical), so each
// write maintains both, like B4.1 pg_tablespace (2697/2698) and B4.5 pg_authid
// (2676/2677). Column layout: catalog.PgDatabaseColumnsPG18 (18 cols, PG18
// Form_pg_database). The new database's encoding/locale columns are CLONED from
// the template database's heap row so they match; only the identity columns
// (oid/datname/datdba/datistemplate/datallowconn) are overwritten.

import (
	"encoding/binary"
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

const (
	pgDatabaseDatnameIndexOID = 2671 // pg_database_datname_index
	pgDatabaseOidIndexOID     = 2672 // pg_database_oid_index
)

// pgDatabaseRel is the shared pg_database relation (global/1262).
func pgDatabaseRel() storage.RelFileNode {
	return catalog.SharedCatalogRelFileNode(catalog.PgDatabaseRelationOID)
}

// SyncPgDatabaseCatalogRow INSERTs the pg_database heap row for a freshly
// created database newOid, cloning encoding/locale from the template database's
// existing heap row and overwriting the identity columns. encodingOverride, when
// >= 0, replaces the template's encoding with the user-specified pg_enc ID
// (e.g. from CREATE DATABASE ... ENCODING 'LATIN1'); when -1 the template's
// encoding is inherited verbatim. It maintains the 2671/2672 indexes. Exported
// for the server-layer CREATE DATABASE handler. M0122-0008.
func SyncPgDatabaseCatalogRow(ctx *Context, newOid uint32, name string, owner, templateOid uint32, encodingOverride int32) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	cols := catalog.PgDatabaseColumnsPG18()
	tmpl, ok, err := readPgDatabaseHeapRow(ctx, cols, templateOid)
	if err != nil {
		return err
	}
	if !ok {
		// The template's heap row is missing (e.g. an embedded/test cluster
		// whose pg_database was never bootstrapped) — nothing faithful to
		// clone; skip the standby copy rather than write a malformed row. The
		// registry (VirtualRows) still holds goopg's own truth.
		return nil
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	row := clonePgDatabaseRowForCreate(tmpl, newOid, name, owner, encodingOverride)
	tid, err := writeHeapRowCanonical(ctx, pgDatabaseRel(), cols, row)
	if err != nil {
		return err
	}
	blk, off := uint32(tid.Block), tid.Offset
	if err := insertCanonicalSysBtreeLeafInDB(ctx, 0, pgDatabaseOidIndexOID,
		buildIndexTupleOidKey(blk, off, newOid), cmpKeyUint32); err != nil {
		return err
	}
	if err := insertCanonicalSysBtreeLeafInDB(ctx, 0, pgDatabaseDatnameIndexOID,
		buildIndexTupleNameKey(blk, off, name), cmpKeyName); err != nil {
		return err
	}
	return nil
}

// DeletePgDatabaseCatalogRow stamps the live pg_database heap row for oid
// deleted (DROP DATABASE). The caller has resolved oid before the registry
// removal.
func DeletePgDatabaseCatalogRow(ctx *Context, oid uint32) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	stampCatalogRows(ctx, pgDatabaseRel(), ctx.Tx.XID, func(data []byte) bool {
		return len(data) >= 4 && binary.LittleEndian.Uint32(data[0:4]) == oid
	})
	return nil
}

// clonePgDatabaseRowForCreate copies the template database's decoded row and
// overwrites only the identity columns for the new database: oid, datname,
// datdba (owner), datistemplate=false, datallowconn=true. Every other column
// (encoding, datlocprovider, datfrozenxid, datminmxid, dattablespace, and the
// varlen datcollate/datctype/datlocale/daticurules/datcollversion/datacl) is
// inherited verbatim from the template, matching what CREATE DATABASE ...
// TEMPLATE would produce. Ordinals per catalog.PgDatabaseColumnsPG18.
func clonePgDatabaseRowForCreate(tmpl Row, newOid uint32, name string, owner uint32, encodingOverride int32) Row {
	row := make(Row, len(tmpl))
	copy(row, tmpl)
	row[0] = NewIntDatum(int64(newOid)) // oid
	row[1] = NewStringDatum(name)       // datname
	row[2] = NewIntDatum(int64(owner))  // datdba
	// Ordinal 3 = encoding (int4). Default -1 -> inherit template encoding. M0122-0008.
	if encodingOverride >= 0 {
		row[3] = NewIntDatum(int64(encodingOverride))
	}
	row[5] = NewBoolDatum(false) // datistemplate
	row[6] = NewBoolDatum(true)  // datallowconn
	return row
}

// readPgDatabaseHeapRow scans the pg_database heap for the live tuple whose oid
// matches, decoding it into a Row. Mirrors persistDatFrozenXID's page scan +
// liveness filter (a row is live iff xmin valid and xmax invalid).
func readPgDatabaseHeapRow(ctx *Context, cols []catalog.Column, oid uint32) (Row, bool, error) {
	if ctx == nil || ctx.Pool == nil {
		return nil, false, nil
	}
	rel := pgDatabaseRel()
	nblocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return nil, false, err
	}
	for blk := storage.BlockNumber(0); blk < nblocks; blk++ {
		slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return nil, false, err
		}
		row, found, derr := decodePgDatabaseRowOnPage(slot, cols, oid)
		ctx.Pool.Unpin(slot)
		if derr != nil {
			return nil, false, derr
		}
		if found {
			return row, true, nil
		}
	}
	return nil, false, nil
}

func decodePgDatabaseRowOnPage(slot *storage.Slot, cols []catalog.Column, oid uint32) (Row, bool, error) {
	slot.Lock()
	defer slot.Unlock()
	page := slot.Page()
	count, err := storage.PageLinePointerCount(page)
	if err != nil {
		return nil, false, err
	}
	for s := uint16(1); s <= uint16(count); s++ {
		tup, terr := storage.PageGetHeapTuple(page, s)
		if terr != nil {
			continue
		}
		if tup.Header.Xmin == storage.InvalidTransactionID || tup.Header.Xmax != storage.InvalidTransactionID {
			continue // dead or deleted
		}
		if len(tup.Data) < 4 || binary.LittleEndian.Uint32(tup.Data[0:4]) != oid {
			continue
		}
		natts := int(tup.Header.Infomask2 & 0x07FF)
		row := make(Row, len(cols))
		if derr := DecodeRowIntoMctxPGTuple(row, cols, tup.Data, tup.Bitmap, natts, nil); derr != nil {
			return nil, false, fmt.Errorf("decode pg_database template row: %w", derr)
		}
		return row, true, nil
	}
	return nil, false, nil
}

// PersistDatConnLimit does an in-place update of the on-disk pg_database
// heap tuple (global/1262) for the database identified by dbOid, setting
// datconnlimit to newLimit. It mirrors persistDatFrozenXID's heap_inplace_update
// pattern: scan every page, locate the live tuple whose oid matches dbOid,
// decode it, overwrite the datconnlimit column, re-encode, and
// PageReplaceItemRaw. The caller (nextVirtualPgDatabase) treats any error as
// non-fatal — the in-memory registry (SetDatabaseConnLimit) is goopg's truth;
// this heap write exists purely for restart durability and standby parity
// (M0122-0006).
//
// Unlike persistDatFrozenXID, which hard-codes the session database's OID, this
// takes dbOid explicitly because the UPDATE's WHERE clause can name any
// database.
func PersistDatConnLimit(ctx *Context, dbOid uint32, newLimit int32) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	rel := pgDatabaseRel()
	cols := catalog.PgDatabaseColumnsPG18()

	nblocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return err
	}
	for blk := storage.BlockNumber(0); blk < nblocks; blk++ {
		slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return err
		}
		updated, done, err := updatePgDatabaseConnLimitOnPage(ctx, slot, rel, cols, dbOid, newLimit)
		ctx.Pool.Unpin(slot)
		if err != nil {
			return err
		}
		if done {
			_ = updated
			return nil
		}
	}
	return nil
}

// updatePgDatabaseConnLimitOnPage scans one pg_database page for the live tuple
// whose oid matches dbOid, performs an in-place overwrite of datconnlimit, and
// marks the page dirty. done is true once the row has been located on this
// page. Mirrors updatePgDatabaseTupleOnPage (operators_vacuum_datfrozenxid.go).
func updatePgDatabaseConnLimitOnPage(ctx *Context, slot *storage.Slot, rel storage.RelFileNode, cols []catalog.Column, dbOid uint32, newLimit int32) (updated, done bool, err error) {
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
		if tup.Header.Xmin == storage.InvalidTransactionID || tup.Header.Xmax != storage.InvalidTransactionID {
			continue
		}
		if len(tup.Data) < 4 || binary.LittleEndian.Uint32(tup.Data[0:4]) != dbOid {
			continue
		}

		natts := int(tup.Header.Infomask2 & 0x07FF)
		row := make(Row, len(cols))
		if derr := DecodeRowIntoMctxPGTuple(row, cols, tup.Data, tup.Bitmap, natts, nil); derr != nil {
			return false, true, derr
		}

		current := int32(row[catalog.PgDatabaseDatConnLimitOrdinal].Int)
		if current == newLimit {
			return false, true, nil // already at target — no write
		}
		row[catalog.PgDatabaseDatConnLimitOrdinal] = NewIntDatum(int64(newLimit))

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
		ctx.Pool.MarkDirtyUnlogged(slot, "pg_database in-place update: no WAL record exists for it (M0131-S26 class B)")
		return true, true, nil
	}
	return false, false, nil
}
