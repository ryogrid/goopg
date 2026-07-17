package executor

// B2.2 slice 5 — the FINAL B2.2 conversion (docs/design/wal-pg-identical-
// stream/02d §1): CREATE/DROP OPERATOR CLASS / OPERATOR FAMILY and ALTER
// OPERATOR FAMILY ADD/DROP journal as real pg_opfamily / pg_opclass /
// pg_amop / pg_amproc heap rows, replacing the bespoke RecordKinds
// CreateOperatorFamily(85)/CreateOperatorClass(86)/DropOperatorClass(87)/
// CreateAmOpMember(88)/DropAmOpMember(89)/CreateAmProcMember(90)/
// DropAmProcMember(91)/DropOperatorFamily(92). All four registries are
// all-scalar and all-OID, so both the rows and the reloads are fully
// physical. Column layouts: postgres/src/include/catalog/pg_opfamily.h,
// pg_opclass.h, pg_amop.h, pg_amproc.h. No ALTER ... RENAME/OWNER surface
// exists for these objects in goopg, so there is no heap-UPDATE path and no
// TID cache — INSERT at create, xmax-stamp at drop.

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// Relation + index OIDs (pg_opfamily.h / pg_opclass.h / pg_amop.h /
// pg_amproc.h). goopg bootstraps no pg_amop_oid_index (2756) or
// pg_amproc_oid_index (2757) — ledger residual, not maintained here.
const (
	pgOpfamilyRelOID            = 2753
	pgOpfamilyAmNameNspIndexOID = 2754
	pgOpfamilyOidIndexOID       = 2755
	pgOpclassRelOID             = 2616
	pgOpclassAmNameNspIndexOID  = 2686
	pgOpclassOidIndexOID        = 2687
	pgAmopRelOID                = 2602
	pgAmopFamStratIndexOID      = 2653
	pgAmopOprFamIndexOID        = 2654
	pgAmprocRelOID              = 2603
	pgAmprocFamProcIndexOID     = 2655
)

// PGOpfamilyColumnsPG18 mirrors FormData_pg_opfamily (5 columns). Exported
// for the initdb reload.
func PGOpfamilyColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "opfmethod", Type: catalog.Type{Name: "oid"}},
		{Name: "opfname", Type: catalog.Type{Name: "name"}},
		{Name: "opfnamespace", Type: catalog.Type{Name: "oid"}},
		{Name: "opfowner", Type: catalog.Type{Name: "oid"}},
	}
}

// PGOpclassColumnsPG18 mirrors FormData_pg_opclass (9 columns).
func PGOpclassColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "opcmethod", Type: catalog.Type{Name: "oid"}},
		{Name: "opcname", Type: catalog.Type{Name: "name"}},
		{Name: "opcnamespace", Type: catalog.Type{Name: "oid"}},
		{Name: "opcowner", Type: catalog.Type{Name: "oid"}},
		{Name: "opcfamily", Type: catalog.Type{Name: "oid"}},
		{Name: "opcintype", Type: catalog.Type{Name: "oid"}},
		{Name: "opcdefault", Type: catalog.Type{Name: "bool"}},
		{Name: "opckeytype", Type: catalog.Type{Name: "oid"}},
	}
}

// PGAmopColumnsPG18 mirrors FormData_pg_amop (9 columns).
func PGAmopColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "amopfamily", Type: catalog.Type{Name: "oid"}},
		{Name: "amoplefttype", Type: catalog.Type{Name: "oid"}},
		{Name: "amoprighttype", Type: catalog.Type{Name: "oid"}},
		{Name: "amopstrategy", Type: catalog.Type{Name: "int2"}},
		{Name: "amoppurpose", Type: catalog.Type{Name: "char"}},
		{Name: "amopopr", Type: catalog.Type{Name: "oid"}},
		{Name: "amopmethod", Type: catalog.Type{Name: "oid"}},
		{Name: "amopsortfamily", Type: catalog.Type{Name: "oid"}},
	}
}

// PGAmprocColumnsPG18 mirrors FormData_pg_amproc (6 columns).
func PGAmprocColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "amprocfamily", Type: catalog.Type{Name: "oid"}},
		{Name: "amproclefttype", Type: catalog.Type{Name: "oid"}},
		{Name: "amprocrighttype", Type: catalog.Type{Name: "oid"}},
		{Name: "amprocnum", Type: catalog.Type{Name: "int2"}},
		{Name: "amproc", Type: catalog.Type{Name: "regproc"}},
	}
}

func sysCatalogRel(relOID uint32) storage.RelFileNode {
	return storage.RelFileNode{DBOid: catalog.DefaultDBOid, RelOid: relOID, Fork: storage.MainFork}
}

// buildIndexTupleOidNameOidKey builds the 80-byte (oid, name, oid)
// IndexTuple for pg_opfamily_am_name_nsp_index (2754) and
// pg_opclass_am_name_nsp_index (2686): method OID first, then the name.
func buildIndexTupleOidNameOidKey(heapBlk uint32, heapOff uint16, oid1 uint32, name string, oid2 uint32) []byte {
	const (
		nameDataLen = 64
		hoff        = sysIndexTupleHoff
		size        = 80 // MAXALIGN(8 + 4 + 64 + 4)
	)
	out := make([]byte, size)
	le := binary.LittleEndian
	le.PutUint16(out[0:2], uint16(heapBlk>>16))
	le.PutUint16(out[2:4], uint16(heapBlk&0xFFFF))
	le.PutUint16(out[4:6], heapOff)
	le.PutUint16(out[6:8], uint16(size)&sysIndexSizeMask)
	le.PutUint32(out[hoff:], oid1)
	n := len(name)
	if n > nameDataLen {
		n = nameDataLen
	}
	copy(out[hoff+4:hoff+4+n], name[:n])
	le.PutUint32(out[hoff+4+nameDataLen:], oid2)
	return out
}

// cmpKeyOidNameOid compares (oid, name, oid) keys.
func cmpKeyOidNameOid(a, b []byte) int {
	const nameDataLen = 64
	if c := cmpKeyUint32(a, b); c != 0 {
		return c
	}
	if c := cmpKeyName(a[4:4+nameDataLen], b[4:4+nameDataLen]); c != 0 {
		return c
	}
	return cmpKeyUint32(a[4+nameDataLen:], b[4+nameDataLen:])
}

// buildIndexTupleOidOidOidInt2Key builds the 24-byte (oid, oid, oid, int2)
// IndexTuple for pg_amop_fam_strat_index (2653) and
// pg_amproc_fam_proc_index (2655).
func buildIndexTupleOidOidOidInt2Key(heapBlk uint32, heapOff uint16, oid1, oid2, oid3 uint32, i2 int16) []byte {
	const (
		hoff = sysIndexTupleHoff
		size = 24 // MAXALIGN(8 + 4 + 4 + 4 + 2)
	)
	out := make([]byte, size)
	le := binary.LittleEndian
	le.PutUint16(out[0:2], uint16(heapBlk>>16))
	le.PutUint16(out[2:4], uint16(heapBlk&0xFFFF))
	le.PutUint16(out[4:6], heapOff)
	le.PutUint16(out[6:8], uint16(size)&sysIndexSizeMask)
	le.PutUint32(out[hoff:], oid1)
	le.PutUint32(out[hoff+4:], oid2)
	le.PutUint32(out[hoff+8:], oid3)
	le.PutUint16(out[hoff+12:], uint16(i2))
	return out
}

// cmpKeyOidOidOidInt2 compares (oid, oid, oid, int2 signed) keys.
func cmpKeyOidOidOidInt2(a, b []byte) int {
	if c := cmpKeyUint32(a, b); c != 0 {
		return c
	}
	if c := cmpKeyUint32(a[4:], b[4:]); c != 0 {
		return c
	}
	if c := cmpKeyUint32(a[8:], b[8:]); c != 0 {
		return c
	}
	av := int16(binary.LittleEndian.Uint16(a[12:]))
	bv := int16(binary.LittleEndian.Uint16(b[12:]))
	switch {
	case av < bv:
		return -1
	case av > bv:
		return 1
	default:
		return 0
	}
}

// buildIndexTupleOidCharOidKey builds the 24-byte (oid, char, oid)
// IndexTuple for pg_amop_opr_fam_index (2654: amopopr, amoppurpose,
// amopfamily). The char attribute is 1-byte-aligned at offset 4; the
// following oid re-aligns to 8 (index_form_tuple's att_align_nominal).
func buildIndexTupleOidCharOidKey(heapBlk uint32, heapOff uint16, oid1 uint32, ch byte, oid2 uint32) []byte {
	const (
		hoff = sysIndexTupleHoff
		size = 24 // MAXALIGN(8 + 4 + 1 + pad3 + 4)
	)
	out := make([]byte, size)
	le := binary.LittleEndian
	le.PutUint16(out[0:2], uint16(heapBlk>>16))
	le.PutUint16(out[2:4], uint16(heapBlk&0xFFFF))
	le.PutUint16(out[4:6], heapOff)
	le.PutUint16(out[6:8], uint16(size)&sysIndexSizeMask)
	le.PutUint32(out[hoff:], oid1)
	out[hoff+4] = ch
	le.PutUint32(out[hoff+8:], oid2)
	return out
}

// cmpKeyOidCharOid compares (oid, char, oid) keys.
func cmpKeyOidCharOid(a, b []byte) int {
	if c := cmpKeyUint32(a, b); c != 0 {
		return c
	}
	if a[4] != b[4] {
		if a[4] < b[4] {
			return -1
		}
		return 1
	}
	return cmpKeyUint32(a[8:], b[8:])
}

// writeOpFamilyCatalogRow journals CREATE OPERATOR FAMILY (including the
// anonymous family CREATE OPERATOR CLASS auto-creates).
func writeOpFamilyCatalogRow(ctx *Context, fam *catalog.UserOperatorFamily) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	row := Row{
		NewIntDatum(int64(fam.OID)),
		NewIntDatum(int64(fam.Method)),
		NewStringDatum(fam.Name),
		NewIntDatum(int64(fam.NamespaceOIDOrDefault())),
		NewIntDatum(int64(fam.OwnerOrDefault())),
	}
	tid, err := writeHeapRowCanonical(ctx, sysCatalogRel(pgOpfamilyRelOID), PGOpfamilyColumnsPG18(), row)
	if err != nil {
		return err
	}
	blk, off := uint32(tid.Block), tid.Offset
	if err := insertCanonicalSysBtreeLeaf(ctx, pgOpfamilyOidIndexOID,
		buildIndexTupleOidKey(blk, off, fam.OID), cmpKeyUint32); err != nil {
		return err
	}
	if err := insertCanonicalSysBtreeLeaf(ctx, pgOpfamilyAmNameNspIndexOID,
		buildIndexTupleOidNameOidKey(blk, off, fam.Method, fam.Name, fam.NamespaceOIDOrDefault()),
		cmpKeyOidNameOid); err != nil {
		return err
	}
	mirrorOpclassFamilyCatalogFiles(ctx)
	return nil
}

// writeOpClassCatalogRow journals CREATE OPERATOR CLASS.
func writeOpClassCatalogRow(ctx *Context, oc *catalog.UserOperatorClass) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	row := Row{
		NewIntDatum(int64(oc.OID)),
		NewIntDatum(int64(oc.Method)),
		NewStringDatum(oc.Name),
		NewIntDatum(int64(oc.NamespaceOIDOrDefault())),
		NewIntDatum(int64(oc.OwnerOrDefault())),
		NewIntDatum(int64(oc.FamilyOID)),
		NewIntDatum(int64(oc.InTypeOID)),
		NewBoolDatum(oc.IsDefault),
		NewIntDatum(int64(oc.KeyTypeOID)),
	}
	tid, err := writeHeapRowCanonical(ctx, sysCatalogRel(pgOpclassRelOID), PGOpclassColumnsPG18(), row)
	if err != nil {
		return err
	}
	blk, off := uint32(tid.Block), tid.Offset
	if err := insertCanonicalSysBtreeLeaf(ctx, pgOpclassOidIndexOID,
		buildIndexTupleOidKey(blk, off, oc.OID), cmpKeyUint32); err != nil {
		return err
	}
	if err := insertCanonicalSysBtreeLeaf(ctx, pgOpclassAmNameNspIndexOID,
		buildIndexTupleOidNameOidKey(blk, off, oc.Method, oc.Name, oc.NamespaceOIDOrDefault()),
		cmpKeyOidNameOid); err != nil {
		return err
	}
	mirrorOpclassFamilyCatalogFiles(ctx)
	return nil
}

// writeAmOpMemberRow journals one pg_amop row (an OPERATOR entry from
// CREATE OPERATOR CLASS's AS list or ALTER OPERATOR FAMILY ADD).
func writeAmOpMemberRow(ctx *Context, m *catalog.AmOpMember) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	purpose := "s"
	if m.SortFamilyOID != 0 {
		purpose = "o"
	}
	row := Row{
		NewIntDatum(int64(m.OID)),
		NewIntDatum(int64(m.FamilyOID)),
		NewIntDatum(int64(m.LeftType)),
		NewIntDatum(int64(m.RightType)),
		NewIntDatum(int64(m.Strategy)),
		NewStringDatum(purpose),
		NewIntDatum(int64(m.OperOID)),
		NewIntDatum(int64(m.Method)),
		NewIntDatum(int64(m.SortFamilyOID)),
	}
	tid, err := writeHeapRowCanonical(ctx, sysCatalogRel(pgAmopRelOID), PGAmopColumnsPG18(), row)
	if err != nil {
		return err
	}
	blk, off := uint32(tid.Block), tid.Offset
	if err := insertCanonicalSysBtreeLeaf(ctx, pgAmopFamStratIndexOID,
		buildIndexTupleOidOidOidInt2Key(blk, off, m.FamilyOID, m.LeftType, m.RightType, int16(m.Strategy)),
		cmpKeyOidOidOidInt2); err != nil {
		return err
	}
	if err := insertCanonicalSysBtreeLeaf(ctx, pgAmopOprFamIndexOID,
		buildIndexTupleOidCharOidKey(blk, off, m.OperOID, purpose[0], m.FamilyOID),
		cmpKeyOidCharOid); err != nil {
		return err
	}
	if err := writeAmMemberClassDependRow(ctx, pgAmopRelOID, m.OID, m.ClassOID); err != nil {
		return err
	}
	mirrorOpclassFamilyCatalogFiles(ctx)
	return nil
}

// writeAmMemberClassDependRow journals a member's CLASS attribution the way
// PG does — an INTERNAL pg_depend row on the owning opclass
// (opclasscmds.c storeOperators/storeProcedures: `recordDependencyOn(&myself,
// &referenced, DEPENDENCY_INTERNAL)` where referenced is the opclass). It is
// the physical channel for AmOpMember/AmProcMember.ClassOID, which pg_amop
// and pg_amproc have no column for: the startup reload re-derives the field
// from these rows. classOID == 0 is the ALTER OPERATOR FAMILY ADD case — PG
// records an AUTO dependency on the FAMILY there instead, which the
// pg_depend view already renders from FamilyOID, so no row is written and
// the reload's zero value is correct by construction.
//
// Narrow surface, like the sequence OWNED BY writer beside it: no index
// maintenance (2673/2674 stay bootstrap-empty until B3's full pg_depend
// conversion).
func writeAmMemberClassDependRow(ctx *Context, memberClassID, memberOID, classOID uint32) error {
	if classOID == 0 {
		return nil
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	row := Row{
		NewIntDatum(int64(memberClassID)),   // classid = pg_amop / pg_amproc
		NewIntDatum(int64(memberOID)),       // objid = the member's OID
		NewIntDatum(0),                      // objsubid
		NewIntDatum(int64(pgOpclassRelOID)), // refclassid = pg_opclass
		NewIntDatum(int64(classOID)),        // refobjid = the owning class
		NewIntDatum(0),                      // refobjsubid
		NewStringDatum("i"),                 // deptype = DEPENDENCY_INTERNAL
	}
	if _, err := writeHeapRowCanonical(ctx, pgDependRel(), PGDependColumnsPG18(), row); err != nil {
		return err
	}
	mirrorDependCatalogFiles(ctx)
	return nil
}

// deleteAmMemberClassDependRow stamps xmax on a member's INTERNAL pg_depend
// row (ALTER OPERATOR FAMILY DROP). Column offsets: classid@0, objid@4.
func deleteAmMemberClassDependRow(ctx *Context, memberClassID, memberOID uint32) {
	le := binary.LittleEndian
	stampCatalogRows(ctx, pgDependRel(), ctx.Tx.XID, func(data []byte) bool {
		if len(data) < 8 {
			return false
		}
		return le.Uint32(data[0:4]) == memberClassID && le.Uint32(data[4:8]) == memberOID
	})
	mirrorDependCatalogFiles(ctx)
}

// writeAmProcMemberRow journals one pg_amproc row (a FUNCTION entry).
func writeAmProcMemberRow(ctx *Context, m *catalog.AmProcMember) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	row := Row{
		NewIntDatum(int64(m.OID)),
		NewIntDatum(int64(m.FamilyOID)),
		NewIntDatum(int64(m.LeftType)),
		NewIntDatum(int64(m.RightType)),
		NewIntDatum(int64(m.ProcNum)),
		NewIntDatum(int64(m.ProcOID)),
	}
	tid, err := writeHeapRowCanonical(ctx, sysCatalogRel(pgAmprocRelOID), PGAmprocColumnsPG18(), row)
	if err != nil {
		return err
	}
	if err := insertCanonicalSysBtreeLeaf(ctx, pgAmprocFamProcIndexOID,
		buildIndexTupleOidOidOidInt2Key(uint32(tid.Block), tid.Offset, m.FamilyOID, m.LeftType, m.RightType, int16(m.ProcNum)),
		cmpKeyOidOidOidInt2); err != nil {
		return err
	}
	if err := writeAmMemberClassDependRow(ctx, pgAmprocRelOID, m.OID, m.ClassOID); err != nil {
		return err
	}
	mirrorOpclassFamilyCatalogFiles(ctx)
	return nil
}

// deleteOpclassFamilyRowByOID stamps xmax on the row of rel whose oid
// column (col 0) matches (DROP OPERATOR CLASS / FAMILY). Members are NOT
// cascaded — matching the registry drops, which also leave members behind
// (pre-existing semantics; full dependency-driven cascade is B3 pg_depend
// scope).
func deleteOpclassFamilyRowByOID(ctx *Context, relOID, rowOID uint32) {
	if !catalogHeapSyncAvailable(ctx) {
		return
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return
	}
	stampCatalogRows(ctx, sysCatalogRel(relOID), ctx.Tx.XID, func(data []byte) bool {
		if len(data) < 4 {
			return false
		}
		return binary.LittleEndian.Uint32(data[0:4]) == rowOID
	})
	mirrorOpclassFamilyCatalogFiles(ctx)
}

// deleteAmMemberRow stamps xmax on the pg_amop/pg_amproc row matching
// (family, lefttype, righttype, number) — ALTER OPERATOR FAMILY DROP.
// Column layout (both catalogs): oid@0, family@4, left@8, right@12,
// int2 number@16.
func deleteAmMemberRow(ctx *Context, relOID, familyOID, leftType, rightType uint32, number int16) {
	if !catalogHeapSyncAvailable(ctx) {
		return
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return
	}
	le := binary.LittleEndian
	var memberOIDs []uint32
	stampCatalogRows(ctx, sysCatalogRel(relOID), ctx.Tx.XID, func(data []byte) bool {
		if len(data) < 18 {
			return false
		}
		if le.Uint32(data[4:8]) == familyOID &&
			le.Uint32(data[8:12]) == leftType &&
			le.Uint32(data[12:16]) == rightType &&
			int16(le.Uint16(data[16:18])) == number {
			memberOIDs = append(memberOIDs, le.Uint32(data[0:4]))
			return true
		}
		return false
	})
	// The member's class-attribution row goes with it (PG's own
	// deleteDependencyRecordsFor inside RemoveAmOpEntryById).
	for _, oid := range memberOIDs {
		deleteAmMemberClassDependRow(ctx, relOID, oid)
	}
	mirrorOpclassFamilyCatalogFiles(ctx)
}

// mirrorOpclassFamilyCatalogFiles propagates all four heaps + their five
// maintained indexes to the postgres DB's copies (reload reads base/5).
func mirrorOpclassFamilyCatalogFiles(ctx *Context) {
	for _, oid := range []uint32{
		pgOpfamilyRelOID, pgOpfamilyAmNameNspIndexOID, pgOpfamilyOidIndexOID,
		pgOpclassRelOID, pgOpclassAmNameNspIndexOID, pgOpclassOidIndexOID,
		pgAmopRelOID, pgAmopFamStratIndexOID, pgAmopOprFamIndexOID,
		pgAmprocRelOID, pgAmprocFamProcIndexOID,
	} {
		_ = mirrorCatalogRelToPostgresDB(ctx, oid)
	}
}
