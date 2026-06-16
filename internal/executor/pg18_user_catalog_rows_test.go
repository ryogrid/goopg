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
		row := buildUserPGAttributeRow(tbl, col)
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
		row := buildUserPGAttributeRow(tbl, col)
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
	row := buildUserPGAttributeRow(tbl, scalar)
	if row[attndimsIdx].Int != 0 || row[atttypidIdx].Int != int64(catalog.OIDText) {
		t.Errorf("scalar text: atttypid=%d attndims=%d want %d/0", row[atttypidIdx].Int, row[attndimsIdx].Int, catalog.OIDText)
	}

	// Slice 87: the scalar "char"/bpchar disambiguation. A quoted `"char"`
	// column (name "char", no args) must resolve to atttypid=18 and render
	// `"char"`; an unquoted `char` (the parser stamps args [1], i.e. bpchar(1))
	// must stay bpchar (1042) and render `character(1)`. Both share the catalog
	// type name "char", so only the args distinguish them.
	realChar := catalog.Column{Name: "c", Type: catalog.Type{Name: "char"}, Ordinal: 0}
	row = buildUserPGAttributeRow(tbl, realChar)
	if got := row[atttypidIdx].Int; got != int64(catalog.OIDChar) {
		t.Errorf("scalar \"char\": atttypid=%d want %d", got, catalog.OIDChar)
	}
	if got := formatTypeOID(row[atttypidIdx].Int, row[atttypmodIdx].Int); got != "\"char\"" {
		t.Errorf("scalar \"char\": format_type=%q want %q", got, "\"char\"")
	}
	bpchar1 := catalog.Column{Name: "c", Type: catalog.Type{Name: "char", Args: []int64{1}}, Ordinal: 0}
	row = buildUserPGAttributeRow(tbl, bpchar1)
	if got := row[atttypidIdx].Int; got != int64(catalog.OIDBpChar) {
		t.Errorf("scalar char(1): atttypid=%d want %d (bpchar)", got, catalog.OIDBpChar)
	}
	if got := formatTypeOID(row[atttypidIdx].Int, row[atttypmodIdx].Int); got != "character(1)" {
		t.Errorf("scalar char(1): format_type=%q want %q", got, "character(1)")
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
		row := buildUserPGAttributeRow(tbl, col)
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
