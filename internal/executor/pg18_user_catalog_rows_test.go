package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// TestUserCreateTableEmitsPG18CanonicalPgClassRow verifies that
// syncTableToCatalogHeap writes a pg_class heap tuple whose byte layout
// decodes via PG18's fixed-offset physical decoder. Pins the M0106-0010
// batched-36 loop 8 fix that switched syncTableToCatalogHeap from the
// goopg-native 8-column row to the PG18-canonical 34-column row,
// unblocking `TestE2E_FailoverGoopgToPG/async` past the
// `relation "public.bench_log" does not exist` parse-analyze error.
func TestUserCreateTableEmitsPG18CanonicalPgClassRow(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	tbl := &catalog.Table{
		Schema: "public",
		Name:   "bench_log",
		OID:    16400,
		Columns: []catalog.Column{
			{Name: "client", Type: catalog.Type{Name: "int4"}, NotNull: true, Ordinal: 0},
			{Name: "src", Type: catalog.Type{Name: "text"}, NotNull: true, Ordinal: 1},
		},
	}
	if err := syncTableToCatalogHeap(ctx, tbl); err != nil {
		t.Fatalf("syncTableToCatalogHeap: %v", err)
	}

	classRel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: catalog.RelationRelationId,
		Fork:   storage.MainFork,
	}
	nBlocks, err := ctx.Pool.NBlocks(classRel)
	if err != nil {
		t.Fatalf("NBlocks(pg_class): %v", err)
	}
	if nBlocks == 0 {
		t.Fatal("pg_class has zero blocks after CREATE TABLE")
	}

	found := false
	page := make(storage.Page, storage.BlockSize)
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: classRel, Block: blk})
		if err != nil {
			t.Fatalf("Pin(blk %d): %v", blk, err)
		}
		copy(page, slot.Page())
		ctx.Pool.Unpin(slot)
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			continue
		}
		for slotIdx := uint16(1); slotIdx <= uint16(count); slotIdx++ {
			ht, err := storage.PageGetHeapTuple(page, slotIdx)
			if err != nil {
				continue
			}
			row, err := catalog.DecodePGClassPhysicalRow(ht.Data)
			if err != nil {
				continue
			}
			if row.OID != tbl.OID {
				continue
			}
			found = true
			if row.RelName != "bench_log" {
				t.Errorf("relname = %q, want %q", row.RelName, "bench_log")
			}
			if row.RelNamespace != catalog.PublicNamespaceOID {
				t.Errorf("relnamespace = %d, want %d (public)", row.RelNamespace, catalog.PublicNamespaceOID)
			}
			if row.RelKind != "r" {
				t.Errorf("relkind = %q, want %q", row.RelKind, "r")
			}
			if row.RelNAtts != 2 {
				t.Errorf("relnatts = %d, want 2", row.RelNAtts)
			}
			if row.RelFileNode != tbl.OID {
				t.Errorf("relfilenode = %d, want %d (tbl.OID)", row.RelFileNode, tbl.OID)
			}
			if row.RelPersistence != "p" {
				t.Errorf("relpersistence = %q, want %q", row.RelPersistence, "p")
			}
			if row.RelIsShared {
				t.Errorf("relisshared = true, want false for user table")
			}
		}
	}
	if !found {
		t.Fatalf("pg_class did not contain a row decodable via DecodePGClassPhysicalRow for bench_log (oid=%d)", tbl.OID)
	}
}

// TestUserCreateTableEmitsPG18CanonicalPgAttributeRows pins the pg_attribute
// side of the same fix: each user column lands as a 25-column PG18-canonical
// row that decodes via DecodePGAttributePhysicalRow.
func TestUserCreateTableEmitsPG18CanonicalPgAttributeRows(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	tbl := &catalog.Table{
		Schema: "public",
		Name:   "bench_log",
		OID:    16400,
		Columns: []catalog.Column{
			{Name: "client", Type: catalog.Type{Name: "int4"}, NotNull: true, Ordinal: 0},
			{Name: "src", Type: catalog.Type{Name: "text"}, NotNull: true, Ordinal: 1},
		},
	}
	if err := syncTableToCatalogHeap(ctx, tbl); err != nil {
		t.Fatalf("syncTableToCatalogHeap: %v", err)
	}

	attrRel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: catalog.AttributeRelationId,
		Fork:   storage.MainFork,
	}
	nBlocks, err := ctx.Pool.NBlocks(attrRel)
	if err != nil {
		t.Fatalf("NBlocks(pg_attribute): %v", err)
	}
	if nBlocks == 0 {
		t.Fatal("pg_attribute has zero blocks after CREATE TABLE")
	}

	byAttNum := map[int32]catalog.PGAttributeRow{}
	page := make(storage.Page, storage.BlockSize)
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: attrRel, Block: blk})
		if err != nil {
			t.Fatalf("Pin(blk %d): %v", blk, err)
		}
		copy(page, slot.Page())
		ctx.Pool.Unpin(slot)
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			continue
		}
		for slotIdx := uint16(1); slotIdx <= uint16(count); slotIdx++ {
			ht, err := storage.PageGetHeapTuple(page, slotIdx)
			if err != nil {
				continue
			}
			row, err := catalog.DecodePGAttributePhysicalRow(ht.Data)
			if err != nil {
				continue
			}
			if row.AttRelID != tbl.OID {
				continue
			}
			byAttNum[row.AttNum] = row
		}
	}
	if len(byAttNum) != 2 {
		t.Fatalf("decoded %d pg_attribute rows for bench_log, want 2 (%+v)", len(byAttNum), byAttNum)
	}
	a1, ok := byAttNum[1]
	if !ok {
		t.Fatalf("missing attnum=1, byAttNum=%+v", byAttNum)
	}
	if a1.AttName != "client" {
		t.Errorf("attnum=1 attname=%q want %q", a1.AttName, "client")
	}
	if a1.AttTypID != catalog.OIDInt4 {
		t.Errorf("attnum=1 atttypid=%d want %d (int4)", a1.AttTypID, catalog.OIDInt4)
	}
	if !a1.AttNotNull {
		t.Errorf("attnum=1 attnotnull=false want true")
	}
	a2, ok := byAttNum[2]
	if !ok {
		t.Fatalf("missing attnum=2, byAttNum=%+v", byAttNum)
	}
	if a2.AttName != "src" {
		t.Errorf("attnum=2 attname=%q want %q", a2.AttName, "src")
	}
	if a2.AttTypID != catalog.OIDText {
		t.Errorf("attnum=2 atttypid=%d want %d (text)", a2.AttTypID, catalog.OIDText)
	}
	if !a2.AttNotNull {
		t.Errorf("attnum=2 attnotnull=false want true")
	}
}

// TestUserPGClassRowFixedFieldsOID verifies the OID byte at offset 0 in the
// emitted pg_class row equals the relation's OID. Anchors the most common
// PG-standby lookup invariant: pg_class_oid_index reads the leading 4 bytes.
func TestUserPGClassRowFixedFieldsOID(t *testing.T) {
	tbl := &catalog.Table{
		Schema: "public",
		Name:   "bench_log",
		OID:    16400,
		Columns: []catalog.Column{
			{Name: "client", Type: catalog.Type{Name: "int4"}, NotNull: true, Ordinal: 0},
			{Name: "src", Type: catalog.Type{Name: "text"}, NotNull: true, Ordinal: 1},
		},
	}
	row := buildUserPGClassRow(tbl)
	cols := pgClassColumnsPG18()
	if len(row) != len(cols) {
		t.Fatalf("row len=%d, schema len=%d", len(row), len(cols))
	}
	body, err := EncodeRowPG(cols, row)
	if err != nil {
		t.Fatalf("EncodeRowPG: %v", err)
	}
	// PG18 fixed-offset constants from internal/catalog/codec.go.
	if got, want := uint32(body[0])|uint32(body[1])<<8|uint32(body[2])<<16|uint32(body[3])<<24, uint32(16400); got != want {
		t.Errorf("oid bytes = %d, want %d", got, want)
	}
	// relname (offset 4, NameData 64 bytes, NUL-padded)
	name := body[4 : 4+64]
	if string(name[:9]) != "bench_log" {
		t.Errorf("relname[0:9] = %q, want %q", string(name[:9]), "bench_log")
	}
	if name[9] != 0 {
		t.Errorf("relname[9] = %d, want 0 (NUL padding)", name[9])
	}
}


// TestSyncTableStampsHeapHasVarWidthOnPGClassRow pins M0106-0010 batched-49:
// the pg_class row written by `syncTableToCatalogHeap` for a user CREATE
// TABLE must carry HEAP_HASVARWIDTH in t_infomask, because PG18's
// nocachegetattr fast path (heaptuple.c:642) trips
// `Assert(j > attnum)` when HEAP_HASVARWIDTH is unset and the TupleDesc
// places a varlena attribute (relacl/reloptions/relpartbound) before the
// target attnum (e.g. reloptions=33). Without this bit, an attaching
// PG18 standby crashes when parsing `SELECT … FROM public.bench_log`
// inside `extractRelOptions → fastgetattr(pg_class_tuple, 33, …)`.
func TestSyncTableStampsHeapHasVarWidthOnPGClassRow(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	tbl := &catalog.Table{
		Schema: "public",
		Name:   "bench_log",
		OID:    16400,
		Columns: []catalog.Column{
			{Name: "client", Type: catalog.Type{Name: "int4"}, NotNull: true, Ordinal: 0},
			{Name: "src", Type: catalog.Type{Name: "text"}, NotNull: true, Ordinal: 1},
		},
	}
	if err := syncTableToCatalogHeap(ctx, tbl); err != nil {
		t.Fatalf("syncTableToCatalogHeap: %v", err)
	}

	classRel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: catalog.RelationRelationId,
		Fork:   storage.MainFork,
	}
	nBlocks, err := ctx.Pool.NBlocks(classRel)
	if err != nil {
		t.Fatalf("NBlocks(pg_class): %v", err)
	}
	page := make(storage.Page, storage.BlockSize)
	found := false
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: classRel, Block: blk})
		if err != nil {
			t.Fatalf("Pin(blk %d): %v", blk, err)
		}
		copy(page, slot.Page())
		ctx.Pool.Unpin(slot)
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			continue
		}
		for slotIdx := uint16(1); slotIdx <= uint16(count); slotIdx++ {
			ht, err := storage.PageGetHeapTuple(page, slotIdx)
			if err != nil {
				continue
			}
			row, err := catalog.DecodePGClassPhysicalRow(ht.Data)
			if err != nil {
				continue
			}
			if row.OID != tbl.OID {
				continue
			}
			found = true
			if ht.Header.Infomask&storage.HeapHasVarWidth == 0 {
				t.Errorf("pg_class tuple for bench_log: HEAP_HASVARWIDTH unset (infomask=0x%04x); PG18 nocachegetattr will Assert(j > attnum) on reloptions", ht.Header.Infomask)
			}
			if ht.Header.Infomask&storage.HeapXmaxInvalid == 0 {
				t.Errorf("pg_class tuple for bench_log: HEAP_XMAX_INVALID unset (infomask=0x%04x)", ht.Header.Infomask)
			}
		}
	}
	if !found {
		t.Fatalf("pg_class did not contain a decodable bench_log row (oid=%d)", tbl.OID)
	}
}

// TestPgRowHasVarWidthDetectsVarlenaCols pins the helper-level contract
// underpinning TestSyncTableStampsHeapHasVarWidthOnPGClassRow.
func TestPgRowHasVarWidthDetectsVarlenaCols(t *testing.T) {
	// Fixed-only row: no varlena.
	fixedCols := []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "relname", Type: catalog.Type{Name: "name"}},
		{Name: "relpages", Type: catalog.Type{Name: "int4"}},
	}
	fixedRow := Row{NewIntDatum(1), NewStringDatum("x"), NewIntDatum(0)}
	if pgRowHasVarWidth(fixedCols, fixedRow) {
		t.Errorf("pgRowHasVarWidth(fixedCols, fixedRow) = true, want false")
	}
	// Varlena column with a non-null value: must report true.
	varCols := []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "reloptions", Type: catalog.Type{Name: "text[]"}},
	}
	varRow := Row{NewIntDatum(1), NewStringDatum("{}")}
	if !pgRowHasVarWidth(varCols, varRow) {
		t.Errorf("pgRowHasVarWidth(varCols, varRow) = false, want true")
	}
	// Varlena column NULL: must report false (matches PG heap_fill_tuple).
	nullVarRow := Row{NewIntDatum(1), NullDatum}
	if pgRowHasVarWidth(varCols, nullVarRow) {
		t.Errorf("pgRowHasVarWidth(varCols, nullVarRow) = true, want false (null varlena does not stamp HEAP_HASVARWIDTH)")
	}
}
