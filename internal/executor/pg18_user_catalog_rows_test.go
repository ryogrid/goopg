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
	// public resolves to its fixed OID without a catalog.
	row := buildUserPGClassRow(nil, tbl)
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

// TestBuildUserPGAttributeRowEncodesTypCollation pins that runtime DDL emits
// PG18-canonical attcollation values: DEFAULT_COLLATION_OID (100) for
// text/varchar/bpchar, C_COLLATION_OID (950) for name, 0 for the
// non-collatable scalar types. Regression for M0106-0010 batched-53: PG
// standby raised "42P22 could not determine which collation to use for
// string comparison" on `WHERE textcol = literal` because every user
// pg_attribute row was emitting attcollation=0.
func TestBuildUserPGAttributeRowEncodesTypCollation(t *testing.T) {
	cases := []struct {
		typeName    string
		wantCollOID uint32
	}{
		{"text", defaultCollationOID},
		{"varchar", defaultCollationOID},
		{"bpchar", defaultCollationOID},
		{"int4", 0},
		{"int8", 0},
		{"bool", 0},
		{"bytea", 0},
		{"timestamp", 0},
		{"numeric", 0},
	}
	tbl := &catalog.Table{Schema: "public", Name: "t", OID: 16500}
	const attcollationIdx = 19 // 0-indexed position in pgAttributeColumnsPG18()
	for _, tc := range cases {
		col := catalog.Column{Name: "c", Type: catalog.Type{Name: tc.typeName}, Ordinal: 0}
		row := buildUserPGAttributeRow(nil, tbl, col)
		got := uint32(row[attcollationIdx].Int)
		if got != tc.wantCollOID {
			t.Errorf("%s: attcollation=%d want %d", tc.typeName, got, tc.wantCollOID)
		}
	}
	// `name` is not reachable through TypeNameToOID (it falls back to text);
	// verify the OID 19 mapping directly so the cCollationOID branch is
	// pinned for the day a NAMEOID user column path lands.
	if got := userTypeAttrsForOID(19).TypCollation; got != cCollationOID {
		t.Errorf("name (OID 19) TypCollation=%d want %d", got, cCollationOID)
	}
}

// TestUserPGAttributeArrayColumn pins the pg_attribute row for an array-typed
// column (DU-002 slice 62). A `tags text[]` column must store the array
// (_typename) OID in atttypid, attndims=1, and the element typmod in atttypmod
// so that pg_dump's format_type(atttypid, atttypmod) renders the `[]` suffix.
// Before this, the parser's IsArray flag was dropped on the way into the
// catalog, so the column dumped as its bare element type (`text`, not `text[]`).
func TestUserPGAttributeArrayColumn(t *testing.T) {
	const (
		atttypidIdx  = 2
		atttypmodIdx = 5
		attndimsIdx  = 6
	)
	tbl := &catalog.Table{Schema: "public", Name: "t", OID: 16500}
	cases := []struct {
		typeName    string
		args        []int64
		wantTypOID  int64
		wantDisplay string
	}{
		{"text", nil, 1009, "text[]"},
		{"int2", nil, 1005, "smallint[]"},
		{"int4", nil, 1007, "integer[]"},
		{"int8", nil, 1016, "bigint[]"},
		// Slice 63: bool/numeric arrays. The numeric array carries the element
		// typmod, so precision/scale must survive onto the array display.
		{"bool", nil, 1000, "boolean[]"},
		{"numeric", []int64{10, 2}, 1231, "numeric(10,2)[]"},
		// Slice 64: float8/date/timestamp arrays (the date/time families).
		{"float8", nil, 1022, "double precision[]"},
		{"date", nil, 1182, "date[]"},
		{"timestamp", nil, 1115, "timestamp without time zone[]"},
		// Slice 65: float4/time/timestamptz arrays.
		{"float4", nil, 1021, "real[]"},
		{"time", nil, 1183, "time without time zone[]"},
		{"timestamptz", nil, 1185, "timestamp with time zone[]"},
		// Slice 66: uuid array (_uuid 2951). uuid is the first scalar element
		// type wired into TypeNameToOID specifically to back its array.
		{"uuid", nil, 2951, "uuid[]"},
		// Slice 67: bytea array (_bytea 1001). bytea has no typmod, so the
		// array renders as the bare element name with the [] suffix.
		{"bytea", nil, 1001, "bytea[]"},
		// Slice 68: the remaining simple scalar-backed arrays. varchar/bpchar
		// carry the element typmod onto the array (like numeric); oid has none.
		{"varchar", []int64{20}, 1015, "character varying(20)[]"},
		{"varchar", nil, 1015, "character varying[]"},
		{"bpchar", []int64{10}, 1014, "character(10)[]"},
		{"oid", nil, 1028, "oid[]"},
		// Slice 69: the JSON family. json/jsonb are varlena with no typmod, so
		// the arrays render as the bare element name with the [] suffix.
		{"json", nil, 199, "json[]"},
		{"jsonb", nil, 3807, "jsonb[]"},
		// Slice 70: interval (_interval 1187). A bare interval[] column has
		// typmod -1, so the array renders as the bare element name + [].
		{"interval", nil, 1187, "interval[]"},
		// Slice 71: the network-address family. None carry a typmod, so each
		// array renders as the bare element name with the [] suffix.
		{"inet", nil, 1041, "inet[]"},
		{"cidr", nil, 651, "cidr[]"},
		{"macaddr", nil, 1040, "macaddr[]"},
		{"macaddr8", nil, 775, "macaddr8[]"},
		// Slice 72: the geometric family. None carry a typmod, so each array
		// renders as the bare element name with the [] suffix.
		{"point", nil, 1017, "point[]"},
		{"lseg", nil, 1018, "lseg[]"},
		{"path", nil, 1019, "path[]"},
		{"box", nil, 1020, "box[]"},
		{"polygon", nil, 1027, "polygon[]"},
		{"line", nil, 629, "line[]"},
		{"circle", nil, 719, "circle[]"},
		// Slice 73: the full-text-search family. Neither carries a typmod, so each
		// array renders as the bare element name with the [] suffix.
		{"tsvector", nil, 3643, "tsvector[]"},
		{"tsquery", nil, 3645, "tsquery[]"},
		// Slice 74: xml and money. Neither carries a typmod, so each array
		// renders as the bare element name with the [] suffix.
		{"xml", nil, 143, "xml[]"},
		{"money", nil, 791, "money[]"},
		// Slice 75: bit and varbit. Both carry the element typmod (the raw bit
		// length) onto the array; a bare varbit[] column has typmod -1.
		{"bit", []int64{8}, 1561, "bit(8)[]"},
		{"varbit", []int64{8}, 1563, "bit varying(8)[]"},
		{"varbit", nil, 1563, "bit varying[]"},
		// Slice 76: pg_lsn (_pg_lsn 3221). pg_lsn has no typmod, so the array
		// renders as the bare element name with the [] suffix.
		{"pg_lsn", nil, 3221, "pg_lsn[]"},
		// Slice 77: the snapshot types. Neither carries a typmod, so each array
		// renders as the bare element name with the [] suffix.
		{"txid_snapshot", nil, 2949, "txid_snapshot[]"},
		{"pg_snapshot", nil, 5039, "pg_snapshot[]"},
		// Slice 78: xid8 (_xid8 271). xid8 has no typmod, so the array renders as
		// the bare element name with the [] suffix.
		{"xid8", nil, 271, "xid8[]"},
		// Slice 79: tid/xid/cid (_tid 1010, _xid 1011, _cid 1012). None carry a
		// typmod, so each array renders as the bare element name with the [] suffix.
		{"tid", nil, 1010, "tid[]"},
		{"xid", nil, 1011, "xid[]"},
		{"cid", nil, 1012, "cid[]"},
		// Slice 80: the OID-reference ("reg*") family. None carry a typmod, so
		// each array renders as the bare element name with the [] suffix.
		{"regproc", nil, 1008, "regproc[]"},
		{"regprocedure", nil, 2207, "regprocedure[]"},
		{"regoper", nil, 2208, "regoper[]"},
		{"regoperator", nil, 2209, "regoperator[]"},
		{"regclass", nil, 2210, "regclass[]"},
		{"regtype", nil, 2211, "regtype[]"},
		{"regconfig", nil, 3735, "regconfig[]"},
		{"regdictionary", nil, 3770, "regdictionary[]"},
		{"regnamespace", nil, 4090, "regnamespace[]"},
		{"regrole", nil, 4097, "regrole[]"},
		{"regcollation", nil, 4192, "regcollation[]"},
		// Slice 81: int2vector/oidvector (_int2vector 1006, _oidvector 1013). Neither
		// carries a typmod, so each array renders as the bare element name + [].
		// The element name is int2vector/oidvector (NOT smallint/oid) — these are
		// distinct vector types, not the genuine _int2/_oid arrays.
		{"int2vector", nil, 1006, "int2vector[]"},
		{"oidvector", nil, 1013, "oidvector[]"},
		// Slice 82: name (_name 1003). The name type carries no typmod, so the
		// array renders as the bare element name + []. Distinct from text[] —
		// name is the 64-byte fixed-length catalog identifier type.
		{"name", nil, 1003, "name[]"},
		// Slice 83: timetz / time with time zone (_timetz 1270). timetz carries no
		// typmod here (bare timetz[]), so the array renders as the bare element
		// name + []. Distinct from time[] (1183) — timetz tracks a UTC offset.
		{"timetz", nil, 1270, "time with time zone[]"},
		// Slice 84: jsonpath (_jsonpath 4073). jsonpath is varlena with no typmod,
		// so the array renders as the bare element name + []. Distinct from json[]/
		// jsonb[] — jsonpath stores compiled SQL/JSON path expressions.
		{"jsonpath", nil, 4073, "jsonpath[]"},
		// Slice 85: refcursor (_refcursor 2201). refcursor is varlena with no
		// typmod, so the array renders as the bare element name + [].
		{"refcursor", nil, 2201, "refcursor[]"},
		// Slice 86: aclitem (_aclitem 1034). aclitem carries no typmod, so the
		// array renders as the bare element name + []. aclitem is the 16-byte
		// access-control-list item type used internally for catalog *acl columns.
		{"aclitem", nil, 1034, "aclitem[]"},
		// Slice 87: the single-byte "char" type (_char 1002). A `"char"[]` column
		// arrives as name "char" with no args (the quoted form); the args-aware
		// remap resolves the element to OID 18 (not bpchar), so the array is
		// _char (1002), rendered `"char"[]`. Distinct from bpchar's _bpchar (1014).
		{"char", nil, 1002, "\"char\"[]"},
	}
	for _, tc := range cases {
		col := catalog.Column{Name: "c", Type: catalog.Type{Name: tc.typeName, IsArray: true, Args: tc.args}, Ordinal: 0}
		row := buildUserPGAttributeRow(nil, tbl, col)
		if got := row[atttypidIdx].Int; got != tc.wantTypOID {
			t.Errorf("%s[]: atttypid=%d want %d", tc.typeName, got, tc.wantTypOID)
		}
		if got := row[attndimsIdx].Int; got != 1 {
			t.Errorf("%s[]: attndims=%d want 1", tc.typeName, got)
		}
		gotTypmod := row[atttypmodIdx].Int
		if got := formatTypeOID(row[atttypidIdx].Int, gotTypmod); got != tc.wantDisplay {
			t.Errorf("%s[]: format_type(%d,%d)=%q want %q", tc.typeName, row[atttypidIdx].Int, gotTypmod, got, tc.wantDisplay)
		}
	}
	// A non-array column of the same element type must be unaffected:
	// attndims=0 and the scalar OID.
	scalar := catalog.Column{Name: "c", Type: catalog.Type{Name: "text"}, Ordinal: 0}
	row := buildUserPGAttributeRow(nil, tbl, scalar)
	if row[attndimsIdx].Int != 0 || row[atttypidIdx].Int != int64(catalog.OIDText) {
		t.Errorf("scalar text: atttypid=%d attndims=%d want %d/0", row[atttypidIdx].Int, row[attndimsIdx].Int, catalog.OIDText)
	}

	// Slice 87: the scalar "char"/bpchar disambiguation. A quoted `"char"`
	// column (name "char", no args) must resolve to atttypid=18 and render
	// `"char"`; an unquoted `char` (the parser stamps args [1], i.e. bpchar(1))
	// must stay bpchar (1042) and render `character(1)`. Both share the catalog
	// type name "char", so only the args distinguish them.
	realChar := catalog.Column{Name: "c", Type: catalog.Type{Name: "char"}, Ordinal: 0}
	row = buildUserPGAttributeRow(nil, tbl, realChar)
	if got := row[atttypidIdx].Int; got != int64(catalog.OIDChar) {
		t.Errorf("scalar \"char\": atttypid=%d want %d", got, catalog.OIDChar)
	}
	if got := formatTypeOID(row[atttypidIdx].Int, row[atttypmodIdx].Int); got != "\"char\"" {
		t.Errorf("scalar \"char\": format_type=%q want %q", got, "\"char\"")
	}
	bpchar1 := catalog.Column{Name: "c", Type: catalog.Type{Name: "char", Args: []int64{1}}, Ordinal: 0}
	row = buildUserPGAttributeRow(nil, tbl, bpchar1)
	if got := row[atttypidIdx].Int; got != int64(catalog.OIDBpChar) {
		t.Errorf("scalar char(1): atttypid=%d want %d (bpchar)", got, catalog.OIDBpChar)
	}
	if got := formatTypeOID(row[atttypidIdx].Int, row[atttypmodIdx].Int); got != "character(1)" {
		t.Errorf("scalar char(1): format_type=%q want %q", got, "character(1)")
	}
}

// TestUserPGAttributeStorageOverride pins the per-column storage override
// (DU-002 slice 182). A column carrying an ALTER COLUMN ... SET STORAGE result
// (catalog.Column.Storage) must report that strategy in pg_attribute.attstorage
// rather than the type's default. pg_dump compares attstorage against the type's
// typstorage and emits `ALTER TABLE ... SET STORAGE <mode>` only when they
// differ; without the override attstorage always echoed the type default, so a
// SET STORAGE was silently dropped from the dump.
func TestUserPGAttributeStorageOverride(t *testing.T) {
	const attstorageIdx = 9 // attstorage position in the user pg_attribute row
	tbl := &catalog.Table{Name: "t", OID: 99001}

	// No override: a text column reports its type default ('x' = extended).
	base := buildUserPGAttributeRow(nil, tbl, catalog.Column{Name: "c", Type: catalog.Type{Name: "text"}, Ordinal: 0})
	if got := base[attstorageIdx].StringValue(); got != "x" {
		t.Fatalf("text column without override: attstorage=%q want \"x\" (extended)", got)
	}

	// Each SET STORAGE strategy maps to its single-char attstorage code.
	for _, tc := range []struct {
		storage string
		want    string
	}{
		{"plain", "p"},
		{"main", "m"},
		{"external", "e"},
		{"extended", "x"},
		{"EXTERNAL", "e"}, // case-insensitive (parser lowercases, but be robust)
	} {
		col := catalog.Column{Name: "c", Type: catalog.Type{Name: "text"}, Ordinal: 0, Storage: tc.storage}
		row := buildUserPGAttributeRow(nil, tbl, col)
		if got := row[attstorageIdx].StringValue(); got != tc.want {
			t.Errorf("SET STORAGE %s: attstorage=%q want %q", tc.storage, got, tc.want)
		}
	}

	// An unrecognized strategy leaves the type default intact (no corruption).
	bogus := catalog.Column{Name: "c", Type: catalog.Type{Name: "text"}, Ordinal: 0, Storage: "bogus"}
	if got := buildUserPGAttributeRow(nil, tbl, bogus)[attstorageIdx].StringValue(); got != "x" {
		t.Errorf("unrecognized storage: attstorage=%q want \"x\" (type default unchanged)", got)
	}
}

// TestUserPGAttributeCompressionOverride pins the per-column compression override
// (DU-002 slice 183). A column carrying a `COMPRESSION <method>` /
// `ALTER COLUMN ... SET COMPRESSION` result (catalog.Column.Compression) must
// report that method in pg_attribute.attcompression ('p' for pglz, 'l' for lz4).
// pg_dump re-emits `ALTER TABLE ... SET COMPRESSION <method>` whenever
// attcompression is 'p' or 'l'; the PG18 default is '\0' (encoded as ""), for
// which pg_dump emits nothing. attcompression was hardcoded to "" so a declared
// method was silently dropped from the dump.
func TestUserPGAttributeCompressionOverride(t *testing.T) {
	const attcompressionIdx = 10 // attcompression position in the user pg_attribute row
	tbl := &catalog.Table{Name: "t", OID: 99002}

	// No method: a text column reports the default '\0' (encoded as "").
	base := buildUserPGAttributeRow(nil, tbl, catalog.Column{Name: "c", Type: catalog.Type{Name: "text"}, Ordinal: 0})
	if got := base[attcompressionIdx].StringValue(); got != "" {
		t.Fatalf("text column without method: attcompression=%q want \"\" (default)", got)
	}

	// Each compression method maps to its single-char attcompression code.
	for _, tc := range []struct {
		method string
		want   string
	}{
		{"pglz", "p"},
		{"lz4", "l"},
		{"LZ4", "l"}, // case-insensitive (parser normalizes, but be robust)
	} {
		col := catalog.Column{Name: "c", Type: catalog.Type{Name: "text"}, Ordinal: 0, Compression: tc.method}
		row := buildUserPGAttributeRow(nil, tbl, col)
		if got := row[attcompressionIdx].StringValue(); got != tc.want {
			t.Errorf("COMPRESSION %s: attcompression=%q want %q", tc.method, got, tc.want)
		}
	}

	// An unrecognized method leaves the default intact (no corruption).
	bogus := catalog.Column{Name: "c", Type: catalog.Type{Name: "text"}, Ordinal: 0, Compression: "bogus"}
	if got := buildUserPGAttributeRow(nil, tbl, bogus)[attcompressionIdx].StringValue(); got != "" {
		t.Errorf("unrecognized method: attcompression=%q want \"\" (default unchanged)", got)
	}
}

// TestUserPGAttributeStatTargetOverride pins the per-column statistics-target
// override (DU-002 slice 184). A column carrying an `ALTER COLUMN ... SET
// STATISTICS <n>` result (catalog.Column.StatTarget) must report that value in
// pg_attribute.attstattarget; pg_dump re-emits `ALTER TABLE ... SET STATISTICS
// <n>` whenever attstattarget >= 0. The PG18 default is NULL, for which pg_dump
// emits nothing; attstattarget was hardcoded NULL so a declared target was
// silently dropped from the dump.
func TestUserPGAttributeStatTargetOverride(t *testing.T) {
	const attstattargetIdx = 24 // attstattarget position (last) in the user pg_attribute row
	tbl := &catalog.Table{Name: "t", OID: 99003}

	// No override: attstattarget is NULL (the PG18 default → no SET STATISTICS).
	base := buildUserPGAttributeRow(nil, tbl, catalog.Column{Name: "c", Type: catalog.Type{Name: "int4"}, Ordinal: 0})
	if !base[attstattargetIdx].IsNull() {
		t.Fatalf("column without override: attstattarget=%v want NULL", base[attstattargetIdx])
	}

	// Each explicit target (including 0, which disables stats) reports its value.
	for _, want := range []int{0, 100, 10000} {
		v := want
		col := catalog.Column{Name: "c", Type: catalog.Type{Name: "int4"}, Ordinal: 0, StatTarget: &v}
		row := buildUserPGAttributeRow(nil, tbl, col)
		if row[attstattargetIdx].IsNull() {
			t.Errorf("SET STATISTICS %d: attstattarget=NULL want %d", want, want)
			continue
		}
		if got := int(row[attstattargetIdx].Int); got != want {
			t.Errorf("SET STATISTICS %d: attstattarget=%d want %d", want, got, want)
		}
	}

	// A negative target (the reset sentinel) reports NULL — pg_dump emits nothing.
	neg := -1
	col := catalog.Column{Name: "c", Type: catalog.Type{Name: "int4"}, Ordinal: 0, StatTarget: &neg}
	if got := buildUserPGAttributeRow(nil, tbl, col)[attstattargetIdx]; !got.IsNull() {
		t.Errorf("negative target: attstattarget=%v want NULL", got)
	}
}

// TestUserPGAttributeOptionsOverride pins the per-column attribute-options
// override (ALTER COLUMN ... SET (opt=value, …), DU-002 slice 185). A column
// with no options reports pg_attribute.attoptions=NULL (the PG18 default → no
// SET (...) clause); a column with options reports a PG text-array literal that
// goopg's array_to_string(attoptions, ', ') renders so pg_dump re-emits the
// clause.
func TestUserPGAttributeOptionsOverride(t *testing.T) {
	const attoptionsIdx = 21 // attoptions position in the user pg_attribute row
	tbl := &catalog.Table{Name: "t", OID: 99004}

	// No options: attoptions is NULL.
	base := buildUserPGAttributeRow(nil, tbl, catalog.Column{Name: "c", Type: catalog.Type{Name: "int4"}, Ordinal: 0})
	if !base[attoptionsIdx].IsNull() {
		t.Fatalf("column without options: attoptions=%v want NULL", base[attoptionsIdx])
	}

	// A single option renders as a one-element text-array literal.
	one := catalog.Column{Name: "c", Type: catalog.Type{Name: "int4"}, Ordinal: 0, Options: []string{"n_distinct=0.5"}}
	if got := buildUserPGAttributeRow(nil, tbl, one)[attoptionsIdx]; got.StringValue() != "{n_distinct=0.5}" {
		t.Errorf("one option: attoptions=%q want %q", got.StringValue(), "{n_distinct=0.5}")
	}

	// Multiple options render as a comma-joined text-array literal.
	multi := catalog.Column{Name: "c", Type: catalog.Type{Name: "int4"}, Ordinal: 0,
		Options: []string{"n_distinct=0.5", "n_distinct_inherited=-0.1"}}
	if got := buildUserPGAttributeRow(nil, tbl, multi)[attoptionsIdx]; got.StringValue() != "{n_distinct=0.5,n_distinct_inherited=-0.1}" {
		t.Errorf("multi option: attoptions=%q want %q", got.StringValue(), "{n_distinct=0.5,n_distinct_inherited=-0.1}")
	}
}

// TestUserPGAttributeEnumColumn pins the enum-column resolution (DU-002 slice
// 88). A column whose declared type is a user-defined enum must report
// pg_attribute.atttypid = the enum's dynamic pg_type OID (not the text
// fallback), and carry the enum's pg_type shape (4-byte, int-aligned, plain
// storage). format_type(atttypid, -1) must then render the schema-qualified
// enum name (pg_dump runs with search_path=”) so the column dumps as
// `feeling public.mood`, not `feeling text`.
func TestUserPGAttributeEnumColumn(t *testing.T) {
	const (
		atttypidIdx   = 2
		attlenIdx     = 3
		attndimsIdx   = 6
		attbyvalIdx   = 7
		attalignIdx   = 8
		attstorageIdx = 9
	)
	cat := catalog.NewInMemory()
	et, err := cat.RegisterEnum("mood", []string{"sad", "ok", "happy"})
	if err != nil {
		t.Fatalf("RegisterEnum: %v", err)
	}
	tbl := &catalog.Table{Schema: "public", Name: "moody", OID: 16500}
	col := catalog.Column{Name: "feeling", Type: catalog.Type{Name: "mood"}, Ordinal: 1}
	row := buildUserPGAttributeRow(cat, tbl, col)

	if got := uint32(row[atttypidIdx].Int); got != et.OID {
		t.Errorf("enum column: atttypid=%d want %d (enum OID)", got, et.OID)
	}
	if got := row[attlenIdx].Int; got != 4 {
		t.Errorf("enum column: attlen=%d want 4", got)
	}
	if got := row[attndimsIdx].Int; got != 0 {
		t.Errorf("enum column: attndims=%d want 0", got)
	}
	if got := row[attbyvalIdx].BoolValue(); got != false {
		t.Errorf("enum column: attbyval=%v want false", got)
	}
	if got := row[attalignIdx].StringValue(); got != "i" {
		t.Errorf("enum column: attalign=%q want \"i\"", got)
	}
	if got := row[attstorageIdx].StringValue(); got != "p" {
		t.Errorf("enum column: attstorage=%q want \"p\"", got)
	}

	// LookupEnumByOID is the inverse used by format_type to render the column
	// type. With the enum registered, the OID round-trips to its name; an
	// unrelated built-in OID must NOT resolve to an enum (text stays text).
	if got, ok := cat.LookupEnumByOID(et.OID); !ok || got.Name != "mood" {
		t.Errorf("LookupEnumByOID(%d)=%v,%v want mood,true", et.OID, got, ok)
	}
	if _, ok := cat.LookupEnumByOID(uint32(catalog.OIDText)); ok {
		t.Errorf("LookupEnumByOID(text OID) unexpectedly resolved to an enum")
	}
}

// TestUserPGTypeRowForComposite verifies that CREATE TYPE x AS (...) allocates a
// stable pair of OIDs (the composite type + its `_name` array) and that the
// synthesized pg_type rows carry the PG18-canonical composite shape
// (typtype='c'/typcategory='C', varlena, double-aligned) and the array
// companion (typtype='b'/typcategory='A', typelem=composite OID). DU-002 slice 242.
func TestUserPGTypeRowForComposite(t *testing.T) {
	const (
		oidIdx         = 0
		typnameIdx     = 1
		typlenIdx      = 4
		typbyvalIdx    = 5
		typtypeIdx     = 6
		typcategoryIdx = 7
		typrelidIdx    = 11
		typelemIdx     = 13
		typarrayIdx    = 14
		typalignIdx    = 22
		typstorageIdx  = 23
	)
	cat := catalog.NewInMemory()
	ct := cat.RegisterCompositeTypeWithFields("addr", []catalog.CompositeField{
		{Name: "street", ColType: "text"},
		{Name: "zip", ColType: "int"},
	})
	if ct == nil || ct.OID == 0 || ct.ArrayOID != ct.OID+1 || ct.RelOID != ct.OID+2 {
		t.Fatalf("RegisterCompositeTypeWithFields OID alloc: %+v", ct)
	}
	// Re-registration (e.g. CREATE OR REPLACE-style re-run) keeps OIDs stable.
	ct2 := cat.RegisterCompositeTypeWithFields("addr", ct.Fields)
	if ct2.OID != ct.OID || ct2.ArrayOID != ct.ArrayOID || ct2.RelOID != ct.RelOID {
		t.Fatalf("re-register changed OIDs: %+v vs %+v", ct2, ct)
	}
	// LookupCompositeType is case-insensitive.
	if got := cat.LookupCompositeType("ADDR"); got == nil || got.OID != ct.OID {
		t.Fatalf("LookupCompositeType(ADDR)=%+v want OID %d", got, ct.OID)
	}

	row := buildUserPGTypeRowForComposite(ct)
	if got := uint32(row[oidIdx].Int); got != ct.OID {
		t.Errorf("oid=%d want %d", got, ct.OID)
	}
	if got := row[typnameIdx].StringValue(); got != "addr" {
		t.Errorf("typname=%q want addr", got)
	}
	if got := row[typtypeIdx].StringValue(); got != "c" {
		t.Errorf("typtype=%q want c (composite)", got)
	}
	if got := row[typcategoryIdx].StringValue(); got != "C" {
		t.Errorf("typcategory=%q want C", got)
	}
	if got := row[typlenIdx].Int; got != -1 {
		t.Errorf("typlen=%d want -1 (varlena)", got)
	}
	if got := row[typbyvalIdx].BoolValue(); got != false {
		t.Errorf("typbyval=%v want false", got)
	}
	if got := uint32(row[typarrayIdx].Int); got != ct.ArrayOID {
		t.Errorf("typarray=%d want %d", got, ct.ArrayOID)
	}
	if got := uint32(row[typrelidIdx].Int); got != ct.RelOID {
		t.Errorf("typrelid=%d want %d (implicit pg_class relation)", got, ct.RelOID)
	}
	if got := row[typalignIdx].StringValue(); got != "d" {
		t.Errorf("typalign=%q want d", got)
	}
	if got := row[typstorageIdx].StringValue(); got != "x" {
		t.Errorf("typstorage=%q want x", got)
	}

	// Array companion row (`_addr`).
	arr := buildUserPGTypeRowForCompositeArray(ct)
	if got := uint32(arr[oidIdx].Int); got != ct.ArrayOID {
		t.Errorf("array oid=%d want %d", got, ct.ArrayOID)
	}
	if got := arr[typnameIdx].StringValue(); got != "_addr" {
		t.Errorf("array typname=%q want _addr", got)
	}
	if got := arr[typtypeIdx].StringValue(); got != "b" {
		t.Errorf("array typtype=%q want b (base)", got)
	}
	if got := arr[typcategoryIdx].StringValue(); got != "A" {
		t.Errorf("array typcategory=%q want A", got)
	}
	if got := uint32(arr[typelemIdx].Int); got != ct.OID {
		t.Errorf("array typelem=%d want %d (composite element)", got, ct.OID)
	}
}

// TestUserPGClassAndAttributeForComposite pins the implicit pg_class relation
// (relkind='c', reltype=composite OID, oid=ct.RelOID, relnatts=#fields) and the
// per-field pg_attribute rows that pg_dump's dumpCompositeType walks via
// pg_type.typrelid → pg_class → pg_attribute. DU-002 slice 243.
func TestUserPGClassAndAttributeForComposite(t *testing.T) {
	const (
		// pg_class column indices (pgClassColumnsPG18 layout).
		relOidIdx       = 0
		relnameIdx      = 1
		reltypeIdx      = 3
		relamIdx        = 6
		relfilenodeIdx  = 7
		relkindIdx      = 17
		relnattsIdx     = 18
		relfrozenxidIdx = 29
		// pg_attribute column indices (pgAttributeColumnsPG18 layout).
		attrelidIdx  = 0
		attnameIdx   = 1
		atttypidIdx  = 2
		attnumIdx    = 4
		atttypmodIdx = 5
	)
	cat := catalog.NewInMemory()
	ct := cat.RegisterCompositeTypeWithFields("addr", []catalog.CompositeField{
		{Name: "street", ColType: "text"},
		{Name: "zip", ColType: "int"},
	})

	cls := buildUserPGClassRowForComposite(cat, ct)
	if got := uint32(cls[relOidIdx].Int); got != ct.RelOID {
		t.Errorf("relation oid=%d want %d", got, ct.RelOID)
	}
	if got := cls[relnameIdx].StringValue(); got != "addr" {
		t.Errorf("relname=%q want addr", got)
	}
	if got := uint32(cls[reltypeIdx].Int); got != ct.OID {
		t.Errorf("reltype=%d want %d (composite type OID)", got, ct.OID)
	}
	if got := cls[relkindIdx].StringValue(); got != "c" {
		t.Errorf("relkind=%q want c (composite)", got)
	}
	if got := cls[relnattsIdx].Int; got != 2 {
		t.Errorf("relnatts=%d want 2", got)
	}
	if got := cls[relamIdx].Int; got != 0 {
		t.Errorf("relam=%d want 0 (no access method)", got)
	}
	if got := cls[relfilenodeIdx].Int; got != 0 {
		t.Errorf("relfilenode=%d want 0 (no storage)", got)
	}
	if got := cls[relfrozenxidIdx].Int; got != 0 {
		t.Errorf("relfrozenxid=%d want 0 (no storage)", got)
	}

	// Field 1: street text → atttypid=text, atttypmod=-1, attnum=1.
	a0 := buildUserPGAttributeRowForCompositeField(ct, ct.Fields[0], 1)
	if got := uint32(a0[attrelidIdx].Int); got != ct.RelOID {
		t.Errorf("field0 attrelid=%d want %d", got, ct.RelOID)
	}
	if got := a0[attnameIdx].StringValue(); got != "street" {
		t.Errorf("field0 attname=%q want street", got)
	}
	if got := uint32(a0[atttypidIdx].Int); got != catalog.OIDText {
		t.Errorf("field0 atttypid=%d want %d (text)", got, catalog.OIDText)
	}
	if got := a0[attnumIdx].Int; got != 1 {
		t.Errorf("field0 attnum=%d want 1", got)
	}
	// Field 2: zip int → atttypid=int4, attnum=2.
	a1 := buildUserPGAttributeRowForCompositeField(ct, ct.Fields[1], 2)
	if got := uint32(a1[atttypidIdx].Int); got != catalog.OIDInt4 {
		t.Errorf("field1 atttypid=%d want %d (int4)", got, catalog.OIDInt4)
	}
	if got := a1[attnumIdx].Int; got != 2 {
		t.Errorf("field1 attnum=%d want 2", got)
	}

	// A typmod-bearing field round-trips its modifier: numeric(10,2) →
	// atttypmod = ((10<<16)|2)+VARHDRSZ.
	ctn := cat.RegisterCompositeTypeWithFields("money_row", []catalog.CompositeField{
		{Name: "amt", ColType: "numeric ( 10 , 2 )"},
	})
	an := buildUserPGAttributeRowForCompositeField(ctn, ctn.Fields[0], 1)
	if got := uint32(an[atttypidIdx].Int); got != catalog.OIDNumeric {
		t.Errorf("numeric field atttypid=%d want %d", got, catalog.OIDNumeric)
	}
	wantMod := int64((10<<16|2)&0xffffffff) + 4
	if got := an[atttypmodIdx].Int; got != wantMod {
		t.Errorf("numeric(10,2) atttypmod=%d want %d", got, wantMod)
	}
}

// TestUserPGAttributeEnumArrayColumn pins the enum-ARRAY resolution (DU-002
// slice 89). A `mood[]` column must report pg_attribute.atttypid = the enum's
// auto-generated array OID (et.ArrayOID = et.OID+1), attndims=1, and carry a
// varlena-array pg_type shape (-1 length, int-aligned, extended storage).
// format_type then renders it as `public.mood[]` via LookupEnumByArrayOID, so
// the column dumps as `feelings public.mood[]`, not `feelings text[]`.
func TestUserPGAttributeEnumArrayColumn(t *testing.T) {
	const (
		atttypidIdx   = 2
		attlenIdx     = 3
		attndimsIdx   = 6
		attbyvalIdx   = 7
		attalignIdx   = 8
		attstorageIdx = 9
	)
	cat := catalog.NewInMemory()
	et, err := cat.RegisterEnum("mood", []string{"sad", "ok", "happy"})
	if err != nil {
		t.Fatalf("RegisterEnum: %v", err)
	}
	if et.ArrayOID != et.OID+1 {
		t.Fatalf("enum ArrayOID=%d want OID+1=%d", et.ArrayOID, et.OID+1)
	}
	tbl := &catalog.Table{Schema: "public", Name: "moody", OID: 16500}
	col := catalog.Column{Name: "feelings", Type: catalog.Type{Name: "mood", IsArray: true}, Ordinal: 2}
	row := buildUserPGAttributeRow(cat, tbl, col)

	if got := uint32(row[atttypidIdx].Int); got != et.ArrayOID {
		t.Errorf("enum array column: atttypid=%d want %d (enum array OID)", got, et.ArrayOID)
	}
	if got := row[attndimsIdx].Int; got != 1 {
		t.Errorf("enum array column: attndims=%d want 1", got)
	}
	if got := row[attlenIdx].Int; got != -1 {
		t.Errorf("enum array column: attlen=%d want -1 (varlena)", got)
	}
	if got := row[attbyvalIdx].BoolValue(); got != false {
		t.Errorf("enum array column: attbyval=%v want false", got)
	}
	if got := row[attalignIdx].StringValue(); got != "i" {
		t.Errorf("enum array column: attalign=%q want \"i\"", got)
	}
	if got := row[attstorageIdx].StringValue(); got != "x" {
		t.Errorf("enum array column: attstorage=%q want \"x\" (extended)", got)
	}

	// LookupEnumByArrayOID is the inverse used by format_type. The array OID
	// resolves to the enum; the SCALAR OID must NOT resolve via the array path
	// (it is reached through LookupEnumByOID instead).
	if got, ok := cat.LookupEnumByArrayOID(et.ArrayOID); !ok || got.Name != "mood" {
		t.Errorf("LookupEnumByArrayOID(%d)=%v,%v want mood,true", et.ArrayOID, got, ok)
	}
	if _, ok := cat.LookupEnumByArrayOID(et.OID); ok {
		t.Errorf("LookupEnumByArrayOID(scalar enum OID) unexpectedly resolved")
	}
}

// TestUserPGAttributeDomainColumn pins the DOMAIN column resolution (DU-002
// slice 90). A column declared with a domain type is stored with its type name
// already resolved to the base (CREATE TABLE → catalog.ResolveColumnType), with
// the original domain name in DeclaredTypeName. buildUserPGAttributeRow must
// re-resolve it to the domain's pg_type OID (so format_type renders the domain
// name, not the base) while reporting the BASE type's physical layout.
func TestUserPGAttributeDomainColumn(t *testing.T) {
	const (
		atttypidIdx   = 2
		attlenIdx     = 3
		attndimsIdx   = 6
		attbyvalIdx   = 7
		attalignIdx   = 8
		attstorageIdx = 9
	)
	cat := catalog.NewInMemory()
	d, err := cat.RegisterDomain("zipcode", catalog.Type{Name: "text"}, false)
	if err != nil {
		t.Fatalf("RegisterDomain: %v", err)
	}
	tbl := &catalog.Table{Schema: "public", Name: "dom", OID: 16500}
	// CREATE TABLE stores the base type name with the domain in DeclaredTypeName.
	col := catalog.Column{Name: "zip", Type: catalog.Type{Name: "text"}, DeclaredTypeName: "zipcode", Ordinal: 1}
	row := buildUserPGAttributeRow(cat, tbl, col)

	if got := uint32(row[atttypidIdx].Int); got != d.OID {
		t.Errorf("domain column: atttypid=%d want %d (domain OID)", got, d.OID)
	}
	if got := row[attndimsIdx].Int; got != 0 {
		t.Errorf("domain column: attndims=%d want 0", got)
	}
	// Physical layout follows the base type (text: varlena, int-aligned, extended).
	if got := row[attlenIdx].Int; got != -1 {
		t.Errorf("domain column: attlen=%d want -1 (base text varlena)", got)
	}
	if got := row[attbyvalIdx].BoolValue(); got != false {
		t.Errorf("domain column: attbyval=%v want false", got)
	}
	if got := row[attalignIdx].StringValue(); got != "i" {
		t.Errorf("domain column: attalign=%q want \"i\"", got)
	}
	if got := row[attstorageIdx].StringValue(); got != "x" {
		t.Errorf("domain column: attstorage=%q want \"x\" (base text extended)", got)
	}

	// The pg_type row carries typtype='d' and typbasetype=text so dumpDomain can
	// re-render `CREATE DOMAIN ... AS text`.
	const (
		typtypeIdx     = 6
		typnotnullIdx  = 24
		typbasetypeIdx = 25
	)
	trow := buildUserPGTypeRowForDomain(d)
	if got := trow[typtypeIdx].StringValue(); got != "d" {
		t.Errorf("domain pg_type: typtype=%q want \"d\"", got)
	}
	if got := uint32(trow[typbasetypeIdx].Int); got != catalog.OIDText {
		t.Errorf("domain pg_type: typbasetype=%d want %d (text)", got, catalog.OIDText)
	}
	if got := trow[typnotnullIdx].BoolValue(); got != false {
		t.Errorf("domain pg_type: typnotnull=%v want false", got)
	}

	// LookupDomainByOID is the inverse used by format_type; the domain OID
	// resolves to its name, and an unrelated built-in OID must NOT resolve.
	if got, ok := cat.LookupDomainByOID(d.OID); !ok || got.Name != "zipcode" {
		t.Errorf("LookupDomainByOID(%d)=%v,%v want zipcode,true", d.OID, got, ok)
	}
	if _, ok := cat.LookupDomainByOID(uint32(catalog.OIDText)); ok {
		t.Errorf("LookupDomainByOID(text OID) unexpectedly resolved to a domain")
	}
}

// TestUserPGAttributeTypmod pins the atttypmod computation for typmod-bearing
// columns and the matching format_type round-trip (DU-002 slice 48). Before
// this, buildUserPGAttributeRow hardcoded atttypmod=-1, so pg_dump rendered
// every numeric(p,s)/varchar(n)/char(n) column as its bare base type — a
// schema-fidelity loss. The stored value is PG-canonical (VARHDRSZ added for
// numeric/char/varchar), so formatTypeOID decodes it identically to the
// upstream typmodout functions.
func TestUserPGAttributeTypmod(t *testing.T) {
	const atttypmodIdx = 5 // 0-indexed: attrelid,attname,atttypid,attlen,attnum,atttypmod
	tbl := &catalog.Table{Schema: "public", Name: "t", OID: 16500}
	cases := []struct {
		typeName    string
		args        []int64
		wantTypmod  int64
		wantDisplay string
	}{
		{"numeric", []int64{10, 2}, 655366, "numeric(10,2)"}, // ((10<<16)|2)+4
		{"numeric", []int64{8}, 524292, "numeric(8,0)"},      // ((8<<16)|0)+4
		{"numeric", nil, -1, "numeric"},                      // no modifier
		{"varchar", []int64{64}, 68, "character varying(64)"},
		{"varchar", nil, -1, "character varying"},
		{"bpchar", []int64{10}, 14, "character(10)"},
		// Slice 75: bit(n)/varbit(n) store the raw bit length as typmod (no
		// VARHDRSZ); a bare varbit column has typmod -1 (unlimited).
		{"bit", []int64{8}, 8, "bit(8)"},
		{"varbit", []int64{16}, 16, "bit varying(16)"},
		{"varbit", nil, -1, "bit varying"},
		// Slice 76: pg_lsn has no typmod, so atttypmod stays -1 and the scalar
		// renders as its bare name.
		{"pg_lsn", nil, -1, "pg_lsn"},
		// Slice 77: the snapshot types carry no typmod either.
		{"txid_snapshot", nil, -1, "txid_snapshot"},
		{"pg_snapshot", nil, -1, "pg_snapshot"},
		// Slice 78: xid8 carries no typmod either.
		{"xid8", nil, -1, "xid8"},
		// Slice 79: tid/xid/cid carry no typmod either.
		{"tid", nil, -1, "tid"},
		{"xid", nil, -1, "xid"},
		{"cid", nil, -1, "cid"},
		// Slice 80: the OID-reference ("reg*") family carries no typmod either, so
		// atttypmod stays -1 and each scalar renders as its bare name.
		{"regproc", nil, -1, "regproc"},
		{"regprocedure", nil, -1, "regprocedure"},
		{"regoper", nil, -1, "regoper"},
		{"regoperator", nil, -1, "regoperator"},
		{"regclass", nil, -1, "regclass"},
		{"regtype", nil, -1, "regtype"},
		{"regconfig", nil, -1, "regconfig"},
		{"regdictionary", nil, -1, "regdictionary"},
		{"regnamespace", nil, -1, "regnamespace"},
		{"regrole", nil, -1, "regrole"},
		{"regcollation", nil, -1, "regcollation"},
		// Slice 81: int2vector/oidvector carry no typmod, so atttypmod stays -1 and
		// each scalar renders as its bare vector name (NOT smallint[]/oid[]).
		{"int2vector", nil, -1, "int2vector"},
		{"oidvector", nil, -1, "oidvector"},
		{"int4", []int64{}, -1, "integer"}, // typmod ignored for plain types
	}
	for _, tc := range cases {
		col := catalog.Column{Name: "c", Type: catalog.Type{Name: tc.typeName, Args: tc.args}, Ordinal: 0}
		row := buildUserPGAttributeRow(nil, tbl, col)
		gotTypmod := row[atttypmodIdx].Int
		if gotTypmod != tc.wantTypmod {
			t.Errorf("%s%v: atttypmod=%d want %d", tc.typeName, tc.args, gotTypmod, tc.wantTypmod)
		}
		typOID := int64(catalog.TypeNameToOID(tc.typeName))
		if got := formatTypeOID(typOID, gotTypmod); got != tc.wantDisplay {
			t.Errorf("%s%v: format_type(%d,%d)=%q want %q", tc.typeName, tc.args, typOID, gotTypmod, got, tc.wantDisplay)
		}
	}
}

// TestAttGeneratedForStorageStrategy covers DU-002 slice 194: attgenerated must
// report 'v' for a VIRTUAL generated column, 's' for an explicit STORED one, and
// "" (the empty/zero discriminator) for an ordinary column. pg_dump reads this
// to choose between `GENERATED ALWAYS AS (expr)` (virtual) and `… STORED`.
func TestAttGeneratedForStorageStrategy(t *testing.T) {
	cases := []struct {
		name string
		col  catalog.Column
		want string
	}{
		{"ordinary", catalog.Column{Name: "a"}, ""},
		{"stored", catalog.Column{Name: "g", GeneratedExpr: "a + 1", GeneratedAlways: true, GeneratedVirtual: false}, "s"},
		{"virtual", catalog.Column{Name: "g", GeneratedExpr: "a + 1", GeneratedAlways: true, GeneratedVirtual: true}, "v"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := attGeneratedFor(tc.col); got != tc.want {
				t.Errorf("attGeneratedFor(%s) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}
