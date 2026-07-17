package executor

// B2.2 slice 3 (docs/design/wal-pg-identical-stream/02d §1 + the staged plan
// in IMPLEMENTATION-TODO): CREATE/DROP OPERATOR journal as real pg_operator
// heap rows with entries in both bootstrap-populated indexes, replacing the
// bespoke RecordKindCreateOperator(83)/DropOperator(84). PG's two-pass
// COMMUTATOR/NEGATOR scheme (OperatorShellMake + OperatorUpd, pg_operator.c)
// maps onto an upsert: a shell or back-patched operator whose row already
// exists gets a canonical non-HOT heap UPDATE at its cached TID; everything
// else is an INSERT. Reload is fully physical except the two argument type
// NAMES (oprleft/oprright reversed via the cast-reload pattern) — every proc
// link (oprcode/oprrest/oprjoin) is stored as an OID in the registry too.

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// pg_operator relation + index OIDs (postgres/src/include/catalog/pg_operator.h).
const (
	pgOperatorRelOID             = 2617
	pgOperatorOidIndexOID        = 2688
	pgOperatorOprnameLRNIndexOID = 2689
)

// PGOperatorColumnsPG18 mirrors FormData_pg_operator (14 columns). Exported
// for the initdb reload.
func PGOperatorColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "oprname", Type: catalog.Type{Name: "name"}},
		{Name: "oprnamespace", Type: catalog.Type{Name: "oid"}},
		{Name: "oprowner", Type: catalog.Type{Name: "oid"}},
		{Name: "oprkind", Type: catalog.Type{Name: "char"}},
		{Name: "oprcanmerge", Type: catalog.Type{Name: "bool"}},
		{Name: "oprcanhash", Type: catalog.Type{Name: "bool"}},
		{Name: "oprleft", Type: catalog.Type{Name: "oid"}},
		{Name: "oprright", Type: catalog.Type{Name: "oid"}},
		{Name: "oprresult", Type: catalog.Type{Name: "oid"}},
		{Name: "oprcom", Type: catalog.Type{Name: "oid"}},
		{Name: "oprnegate", Type: catalog.Type{Name: "oid"}},
		{Name: "oprcode", Type: catalog.Type{Name: "regproc"}},
		{Name: "oprrest", Type: catalog.Type{Name: "regproc"}},
		{Name: "oprjoin", Type: catalog.Type{Name: "regproc"}},
	}
}

// operatorResultTypeOID resolves oprresult from the operator's function:
// the user routine registry by OID, else the hand-curated builtin set.
// 0 for a shell (FuncOID=0) — PG shell rows carry InvalidOid there too.
func operatorResultTypeOID(cat catalog.Catalog, funcOID uint32) uint32 {
	if funcOID == 0 {
		return 0
	}
	if rs := cat.Routines(); rs != nil {
		if r := rs.LookupByOID(funcOID); r != nil {
			return catalog.TypeNameToOID(r.ReturnType.Name)
		}
	}
	if bp, ok := catalog.LookupBuiltinProcByOID(funcOID); ok {
		return catalog.TypeNameToOID(bp.RetType)
	}
	return 0
}

// buildPGOperatorRow builds the pg_operator row for a user (or shell)
// operator. oprkind derives from which arg types are present ('b' binary,
// 'l' prefix; PG18 dropped postfix operators so a missing right arg cannot
// occur via the grammar).
func buildPGOperatorRow(cat catalog.Catalog, op *catalog.UserOperator) Row {
	kind := "b"
	if op.LeftType == "" {
		kind = "l"
	}
	leftOID := uint32(0)
	if op.LeftType != "" {
		leftOID = catalog.TypeNameToOID(op.LeftType)
	}
	rightOID := uint32(0)
	if op.RightType != "" {
		rightOID = catalog.TypeNameToOID(op.RightType)
	}
	return Row{
		NewIntDatum(int64(op.OID)),
		NewStringDatum(op.Name),
		NewIntDatum(int64(op.NamespaceOIDOrDefault())),
		NewIntDatum(int64(op.OwnerOrDefault())),
		NewStringDatum(kind),
		NewBoolDatum(op.CanMerge),
		NewBoolDatum(op.CanHash),
		NewIntDatum(int64(leftOID)),
		NewIntDatum(int64(rightOID)),
		NewIntDatum(int64(operatorResultTypeOID(cat, op.FuncOID))),
		NewIntDatum(int64(op.CommutatorOID)),
		NewIntDatum(int64(op.NegatorOID)),
		NewIntDatum(int64(op.FuncOID)),
		NewIntDatum(int64(op.RestrictOID)),
		NewIntDatum(int64(op.JoinOID)),
	}
}

func pgOperatorRel() storage.RelFileNode {
	return storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: pgOperatorRelOID,
		Fork:   storage.MainFork,
	}
}

// buildIndexTupleNameOidOidOidKey builds the 88-byte (name, oid, oid, oid)
// IndexTuple for pg_operator_oprname_l_r_n_index (2689) — executor twin of
// initdb's pgBuildIndexTupleNameOidOidOidKey.
func buildIndexTupleNameOidOidOidKey(heapBlk uint32, heapOff uint16, name string, oid1, oid2, oid3 uint32) []byte {
	const (
		nameDataLen = 64
		hoff        = sysIndexTupleHoff
		size        = 88 // MAXALIGN(8 + 64 + 4 + 4 + 4)
	)
	out := make([]byte, size)
	le := binary.LittleEndian
	le.PutUint16(out[0:2], uint16(heapBlk>>16))
	le.PutUint16(out[2:4], uint16(heapBlk&0xFFFF))
	le.PutUint16(out[4:6], heapOff)
	le.PutUint16(out[6:8], uint16(size)&sysIndexSizeMask)
	nb := []byte(name)
	if len(nb) > nameDataLen-1 {
		nb = nb[:nameDataLen-1]
	}
	copy(out[hoff:], nb)
	le.PutUint32(out[hoff+nameDataLen:], oid1)
	le.PutUint32(out[hoff+nameDataLen+4:], oid2)
	le.PutUint32(out[hoff+nameDataLen+8:], oid3)
	return out
}

// cmpKeyNameOidOidOid compares (name, oid, oid, oid) keys.
func cmpKeyNameOidOidOid(a, b []byte) int {
	const nameDataLen = 64
	if c := cmpKeyName(a[:nameDataLen], b[:nameDataLen]); c != 0 {
		return c
	}
	if c := cmpKeyUint32(a[nameDataLen:], b[nameDataLen:]); c != 0 {
		return c
	}
	if c := cmpKeyUint32(a[nameDataLen+4:], b[nameDataLen+4:]); c != 0 {
		return c
	}
	return cmpKeyUint32(a[nameDataLen+8:], b[nameDataLen+8:])
}

// upsertOperatorCatalogRow journals one operator's CURRENT registry state:
// a canonical non-HOT heap UPDATE when the row exists (shell fill-in,
// COMMUTATOR/NEGATOR back-patch — PG's OperatorUpd), else a heap INSERT
// (CREATE OPERATOR, OperatorShellMake). Both paths insert fresh index
// entries at the new TID; the superseded version's entries die with it
// (liveness-filtered, the sys-btree convention).
func upsertOperatorCatalogRow(ctx *Context, op *catalog.UserOperator) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	row := buildPGOperatorRow(ctx.Catalog, op)
	var tid storage.ItemPointer
	var err error
	if old, ok := im.OperatorHeapTID(op.OID); ok {
		oldTID := storage.ItemPointer{Block: storage.BlockNumber(old.Block), Offset: old.Offset}
		tid, err = updateHeapRowCanonicalPG(ctx, pgOperatorRel(), PGOperatorColumnsPG18(), oldTID, row)
	} else {
		tid, err = writeHeapRowCanonical(ctx, pgOperatorRel(), PGOperatorColumnsPG18(), row)
	}
	if err != nil {
		return err
	}
	im.SetOperatorHeapTID(op.OID, catalog.SchemaHeapTID{Block: uint32(tid.Block), Offset: tid.Offset})
	blk, off := uint32(tid.Block), tid.Offset
	if err := insertCanonicalSysBtreeLeaf(ctx, pgOperatorOidIndexOID,
		buildIndexTupleOidKey(blk, off, op.OID), cmpKeyUint32); err != nil {
		return err
	}
	leftOID := uint32(0)
	if op.LeftType != "" {
		leftOID = catalog.TypeNameToOID(op.LeftType)
	}
	rightOID := uint32(0)
	if op.RightType != "" {
		rightOID = catalog.TypeNameToOID(op.RightType)
	}
	if err := insertCanonicalSysBtreeLeaf(ctx, pgOperatorOprnameLRNIndexOID,
		buildIndexTupleNameOidOidOidKey(blk, off, op.Name, leftOID, rightOID, op.NamespaceOIDOrDefault()),
		cmpKeyNameOidOidOid); err != nil {
		return err
	}
	mirrorOperatorCatalogFiles(ctx)
	return nil
}

// deleteOperatorCatalogRow stamps xmax on the operator's row (DROP OPERATOR).
func deleteOperatorCatalogRow(ctx *Context, opOID uint32) {
	if !catalogHeapSyncAvailable(ctx) {
		return
	}
	// The statement may not have written anything yet — materialize the
	// writer XID or the stamp below writes xmax=0 (a silent no-op).
	if err := ctx.MaterializeWriterXID(); err != nil {
		return
	}
	stampCatalogRows(ctx, pgOperatorRel(), ctx.Tx.XID, func(data []byte) bool {
		if len(data) < 4 {
			return false
		}
		return binary.LittleEndian.Uint32(data[0:4]) == opOID
	})
	if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
		im.DropOperatorHeapTID(opOID)
	}
	mirrorOperatorCatalogFiles(ctx)
}

// mirrorOperatorCatalogFiles propagates the pg_operator heap + both indexes
// to the postgres DB's copies (reload reads base/5).
func mirrorOperatorCatalogFiles(ctx *Context) {
	_ = mirrorCatalogRelToPostgresDB(ctx, pgOperatorRelOID)
	_ = mirrorCatalogRelToPostgresDB(ctx, pgOperatorOidIndexOID)
	_ = mirrorCatalogRelToPostgresDB(ctx, pgOperatorOprnameLRNIndexOID)
}
