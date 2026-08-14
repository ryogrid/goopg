package catalog

import (
	"testing"
)

// TestEncodePGClassRowRoundTrip verifies that EncodePGClassRow +
// DecodePGClassRow is a lossless round-trip for every field.
func TestEncodePGClassRowRoundTrip(t *testing.T) {
	want := PGClassRow{
		OID:            1259,
		RelName:        "pg_class",
		RelNamespace:   PGCatalogNamespaceOID,
		RelKind:        "r",
		RelNAtts:       8,
		RelFileNode:    1259,
		RelPersistence: "p",
		RelIsShared:    false,
	}
	data := EncodePGClassRow(want)
	got, err := DecodePGClassRow(data)
	if err != nil {
		t.Fatalf("DecodePGClassRow: %v", err)
	}
	if got != want {
		t.Errorf("round-trip mismatch:\ngot  %+v\nwant %+v", got, want)
	}
}

// TestEncodePGClassRowRelIsSharedTrue checks that RelIsShared=true survives.
func TestEncodePGClassRowRelIsSharedTrue(t *testing.T) {
	want := PGClassRow{OID: 2396, RelName: "pg_auth_members", RelIsShared: true, RelPersistence: "p"}
	data := EncodePGClassRow(want)
	got, err := DecodePGClassRow(data)
	if err != nil {
		t.Fatalf("DecodePGClassRow: %v", err)
	}
	if !got.RelIsShared {
		t.Errorf("RelIsShared: got false, want true")
	}
}

// TestEncodePGAttributeRowRoundTrip verifies that EncodePGAttributeRow +
// DecodePGAttributeRow is a lossless round-trip.
func TestEncodePGAttributeRowRoundTrip(t *testing.T) {
	want := PGAttributeRow{
		AttRelID:     1259,
		AttName:      "relname",
		AttTypID:     OIDText,
		AttNum:       2,
		AttNotNull:   true,
		AttIsDropped: false,
	}
	data := EncodePGAttributeRow(want)
	got, err := DecodePGAttributeRow(data)
	if err != nil {
		t.Fatalf("DecodePGAttributeRow: %v", err)
	}
	if got != want {
		t.Errorf("round-trip mismatch:\ngot  %+v\nwant %+v", got, want)
	}
}

// TestEncodePGAttributeRowDropped checks that AttIsDropped=true survives.
func TestEncodePGAttributeRowDropped(t *testing.T) {
	want := PGAttributeRow{AttRelID: 12345, AttName: "old_col", AttIsDropped: true, AttTypID: OIDInt4, AttNum: 3}
	data := EncodePGAttributeRow(want)
	got, err := DecodePGAttributeRow(data)
	if err != nil {
		t.Fatalf("DecodePGAttributeRow: %v", err)
	}
	if !got.AttIsDropped {
		t.Errorf("AttIsDropped: got false, want true")
	}
}

// TestEncodePGTypeRowRoundTrip verifies that EncodePGTypeRow +
// DecodePGTypeRow is a lossless round-trip.
func TestEncodePGTypeRowRoundTrip(t *testing.T) {
	cases := []PGTypeRow{
		{OID: OIDInt4, TypName: "int4", TypNamespace: PGCatalogNamespaceOID, TypLen: 4, TypByVal: true, TypType: "b", TypCategory: "N"},
		{OID: OIDText, TypName: "text", TypNamespace: PGCatalogNamespaceOID, TypLen: -1, TypByVal: false, TypType: "b", TypCategory: "S"},
		{OID: OIDBool, TypName: "bool", TypNamespace: PGCatalogNamespaceOID, TypLen: 1, TypByVal: true, TypType: "b", TypCategory: "B"},
		{OID: OIDNumeric, TypName: "numeric", TypNamespace: PGCatalogNamespaceOID, TypLen: -1, TypByVal: false, TypType: "b", TypCategory: "N"},
	}
	for _, want := range cases {
		data := EncodePGTypeRow(want)
		got, err := DecodePGTypeRow(data)
		if err != nil {
			t.Fatalf("DecodePGTypeRow(%s): %v", want.TypName, err)
		}
		if got != want {
			t.Errorf("%s round-trip:\ngot  %+v\nwant %+v", want.TypName, got, want)
		}
	}
}

// TestPGClassColumnsCount verifies the column count matches the PGClassRow field count.
func TestPGClassColumnsCount(t *testing.T) {
	if got := len(PGClassColumns()); got != 8 {
		t.Errorf("PGClassColumns: len=%d want 8", got)
	}
}

// TestPGAttributeColumnsCount checks the pg_attribute schema column count.
// The schema is the 25-column layout written by initdb.pgAttrColDefs, in PG18
// FormData_pg_attribute order since M0131-S14.1 — attstattarget at #21
// (ordinal 20), between attcollation and attacl, and attmissingval last.
// Full-order enforcement lives in the initdb/executor canonical-order guards;
// this one spot-checks the ordinals the decoder is most sensitive to.
func TestPGAttributeColumnsCount(t *testing.T) {
	cols := PGAttributeColumns()
	if got := len(cols); got != 25 {
		t.Errorf("PGAttributeColumns: len=%d want 25", got)
	}
	// Verify key columns are present at the right ordinals, including the whole
	// nullable tail — that is the part M0131-S14.1 permuted into PG18 order.
	wantCols := []struct {
		ord  int
		name string
	}{
		{0, "attrelid"}, {1, "attname"}, {2, "atttypid"}, {4, "attnum"},
		{10, "attcompression"}, {11, "attnotnull"}, {16, "attisdropped"},
		{19, "attcollation"}, {20, "attstattarget"}, {21, "attacl"},
		{22, "attoptions"}, {23, "attfdwoptions"}, {24, "attmissingval"},
	}
	for _, wc := range wantCols {
		if wc.ord >= len(cols) {
			t.Errorf("ordinal %d out of range", wc.ord)
			continue
		}
		if got := cols[wc.ord].Name; got != wc.name {
			t.Errorf("cols[%d].Name = %q want %q", wc.ord, got, wc.name)
		}
	}
}

// TestPGTypeColumnsCount checks the pg_type schema column count.
func TestPGTypeColumnsCount(t *testing.T) {
	if got := len(PGTypeColumns()); got != 32 {
		t.Errorf("PGTypeColumns: len=%d want 32", got)
	}
}

// TestBuiltinTypeOIDs verifies the OID constants match upstream.
func TestBuiltinTypeOIDs(t *testing.T) {
	cases := []struct {
		name string
		oid  uint32
		want uint32
	}{
		{"OIDBool", OIDBool, 16},
		{"OIDInt8", OIDInt8, 20},
		{"OIDInt2", OIDInt2, 21},
		{"OIDInt4", OIDInt4, 23},
		{"OIDText", OIDText, 25},
		{"OIDOID", OIDOID, 26},
		{"OIDBpChar", OIDBpChar, 1042},
		{"OIDVarChar", OIDVarChar, 1043},
		{"OIDTimestamp", OIDTimestamp, 1114},
		{"OIDNumeric", OIDNumeric, 1700},
		{"OIDBytea", OIDBytea, 17},
		{"OIDFloat4", OIDFloat4, 700},
		{"OIDFloat8", OIDFloat8, 701},
		{"OIDDate", OIDDate, 1082},
		{"OIDTime", OIDTime, 1083},
		{"OIDTimestampTZ", OIDTimestampTZ, 1184},
	}
	for _, tc := range cases {
		if tc.oid != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.oid, tc.want)
		}
	}
}

// TestTypeNameToOIDRoundTrip verifies that TypeNameToOID and OIDToTypeName
// are inverses for the canonical type names (M0030-0005).
func TestTypeNameToOIDRoundTrip(t *testing.T) {
	pairs := []struct {
		name string
		oid  uint32
	}{
		{"bool", OIDBool},
		{"bytea", OIDBytea},
		{"int8", OIDInt8},
		{"int2", OIDInt2},
		{"int4", OIDInt4},
		{"text", OIDText},
		{"oid", OIDOID},
		{"float4", OIDFloat4},
		{"float8", OIDFloat8},
		{"date", OIDDate},
		{"time", OIDTime},
		{"timestamp", OIDTimestamp},
		{"timestamptz", OIDTimestampTZ},
		{"bpchar", OIDBpChar},
		{"varchar", OIDVarChar},
		{"numeric", OIDNumeric},
		{"uuid", OIDUUID},
		// DU-002 slice 80: the OID-reference ("reg*") family round-trips its
		// canonical name. Also guards against name collisions with the existing
		// scalar names above (e.g. "regproc" must not resolve to text).
		{"regproc", OIDRegproc},
		{"regprocedure", OIDRegprocedure},
		{"regoper", OIDRegoper},
		{"regoperator", OIDRegoperator},
		{"regclass", OIDRegclass},
		{"regtype", OIDRegtype},
		{"regconfig", OIDRegconfig},
		{"regdictionary", OIDRegdictionary},
		{"regnamespace", OIDRegnamespace},
		{"regrole", OIDRegrole},
		{"regcollation", OIDRegcollation},
		// DU-002 slice 81: int2vector/oidvector round-trip their canonical names
		// (NOT smallint[]/oid[], which are the genuine _int2/_oid array types).
		{"int2vector", OIDInt2vector},
		{"oidvector", OIDOidvector},
		// DU-002 slice 82: name (the 64-byte identifier type) round-trips its
		// canonical name instead of falling back to text.
		{"name", OIDName},
		// DU-002 slice 83: timetz (time with time zone) round-trips its canonical
		// name instead of falling back to text (the codec had no timetz→OID entry).
		{"timetz", OIDTimeTZ},
		// DU-002 slice 84: jsonpath (SQL/JSON path) round-trips its canonical
		// name instead of falling back to text (the codec had no jsonpath→OID entry).
		{"jsonpath", OIDJsonpath},
		// DU-002 slice 85: refcursor (cursor-name reference) round-trips its
		// canonical name instead of falling back to text.
		{"refcursor", OIDRefcursor},
		// DU-002 slice 86: aclitem (access-control-list item) round-trips its
		// canonical name instead of falling back to text.
		{"aclitem", OIDAclitem},
	}
	for _, p := range pairs {
		gotOID := TypeNameToOID(p.name)
		if gotOID != p.oid {
			t.Errorf("TypeNameToOID(%q) = %d, want %d", p.name, gotOID, p.oid)
		}
		gotName := OIDToTypeName(p.oid)
		if gotName != p.name {
			t.Errorf("OIDToTypeName(%d) = %q, want %q", p.oid, gotName, p.name)
		}
	}

	// DU-002 slice 82: name ↔ _name array OID mapping round-trips, so a
	// `name[]` column resolves to _name (1003) and reconstructs from it.
	if got := ArrayOIDForBase(OIDName); got != OIDArrayName {
		t.Errorf("ArrayOIDForBase(OIDName) = %d, want %d (_name)", got, OIDArrayName)
	}
	if got, ok := BaseOIDForArray(OIDArrayName); !ok || got != OIDName {
		t.Errorf("BaseOIDForArray(OIDArrayName) = (%d,%v), want (%d,true)", got, ok, OIDName)
	}
	if OIDName != 19 || OIDArrayName != 1003 {
		t.Errorf("name OIDs drifted: OIDName=%d (want 19), OIDArrayName=%d (want 1003)", OIDName, OIDArrayName)
	}

	// DU-002 slice 83: timetz ↔ _timetz array OID mapping round-trips, so a
	// `timetz[]` column resolves to _timetz (1270) and reconstructs from it.
	if got := ArrayOIDForBase(OIDTimeTZ); got != OIDArrayTimeTZ {
		t.Errorf("ArrayOIDForBase(OIDTimeTZ) = %d, want %d (_timetz)", got, OIDArrayTimeTZ)
	}
	if got, ok := BaseOIDForArray(OIDArrayTimeTZ); !ok || got != OIDTimeTZ {
		t.Errorf("BaseOIDForArray(OIDArrayTimeTZ) = (%d,%v), want (%d,true)", got, ok, OIDTimeTZ)
	}
	if OIDTimeTZ != 1266 || OIDArrayTimeTZ != 1270 {
		t.Errorf("timetz OIDs drifted: OIDTimeTZ=%d (want 1266), OIDArrayTimeTZ=%d (want 1270)", OIDTimeTZ, OIDArrayTimeTZ)
	}

	// DU-002 slice 84: jsonpath ↔ _jsonpath array OID mapping round-trips, so a
	// `jsonpath[]` column resolves to _jsonpath (4073) and reconstructs from it.
	if got := ArrayOIDForBase(OIDJsonpath); got != OIDArrayJsonpath {
		t.Errorf("ArrayOIDForBase(OIDJsonpath) = %d, want %d (_jsonpath)", got, OIDArrayJsonpath)
	}
	if got, ok := BaseOIDForArray(OIDArrayJsonpath); !ok || got != OIDJsonpath {
		t.Errorf("BaseOIDForArray(OIDArrayJsonpath) = (%d,%v), want (%d,true)", got, ok, OIDJsonpath)
	}
	if OIDJsonpath != 4072 || OIDArrayJsonpath != 4073 {
		t.Errorf("jsonpath OIDs drifted: OIDJsonpath=%d (want 4072), OIDArrayJsonpath=%d (want 4073)", OIDJsonpath, OIDArrayJsonpath)
	}

	// DU-002 slice 85: refcursor ↔ _refcursor array OID mapping round-trips, so a
	// `refcursor[]` column resolves to _refcursor (2201) and reconstructs from it.
	if got := ArrayOIDForBase(OIDRefcursor); got != OIDArrayRefcursor {
		t.Errorf("ArrayOIDForBase(OIDRefcursor) = %d, want %d (_refcursor)", got, OIDArrayRefcursor)
	}
	if got, ok := BaseOIDForArray(OIDArrayRefcursor); !ok || got != OIDRefcursor {
		t.Errorf("BaseOIDForArray(OIDArrayRefcursor) = (%d,%v), want (%d,true)", got, ok, OIDRefcursor)
	}
	if OIDRefcursor != 1790 || OIDArrayRefcursor != 2201 {
		t.Errorf("refcursor OIDs drifted: OIDRefcursor=%d (want 1790), OIDArrayRefcursor=%d (want 2201)", OIDRefcursor, OIDArrayRefcursor)
	}

	// DU-002 slice 86: aclitem ↔ _aclitem array OID mapping round-trips, so an
	// `aclitem[]` column resolves to _aclitem (1034) and reconstructs from it.
	if got := ArrayOIDForBase(OIDAclitem); got != OIDArrayAclitem {
		t.Errorf("ArrayOIDForBase(OIDAclitem) = %d, want %d (_aclitem)", got, OIDArrayAclitem)
	}
	if got, ok := BaseOIDForArray(OIDArrayAclitem); !ok || got != OIDAclitem {
		t.Errorf("BaseOIDForArray(OIDArrayAclitem) = (%d,%v), want (%d,true)", got, ok, OIDAclitem)
	}
	if OIDAclitem != 1033 || OIDArrayAclitem != 1034 {
		t.Errorf("aclitem OIDs drifted: OIDAclitem=%d (want 1033), OIDArrayAclitem=%d (want 1034)", OIDAclitem, OIDArrayAclitem)
	}

	// DU-002 slice 87: the single-byte "char" type (18). Unlike the other
	// slices its name→OID lookup is intentionally NOT inverse: TypeNameToOID
	// folds the spelling "char" to bpchar because the catalog "char" vs
	// bpchar(1) distinction is args-based and cannot be made from a name alone
	// (buildUserPGAttributeRow does the args-aware remap). But OIDToTypeName(18)
	// must reconstruct "char", and the char ↔ _char array mapping must hold so
	// a `"char"[]` column resolves to _char (1002) and reconstructs from it.
	if got := OIDToTypeName(OIDChar); got != "char" {
		t.Errorf("OIDToTypeName(OIDChar) = %q, want %q", got, "char")
	}
	if got := TypeNameToOID("char"); got != OIDBpChar {
		t.Errorf("TypeNameToOID(%q) = %d, want %d (bpchar — char/bpchar disambiguation is args-based)", "char", got, OIDBpChar)
	}
	if got := ArrayOIDForBase(OIDChar); got != OIDArrayChar {
		t.Errorf("ArrayOIDForBase(OIDChar) = %d, want %d (_char)", got, OIDArrayChar)
	}
	if got, ok := BaseOIDForArray(OIDArrayChar); !ok || got != OIDChar {
		t.Errorf("BaseOIDForArray(OIDArrayChar) = (%d,%v), want (%d,true)", got, ok, OIDChar)
	}
	if OIDChar != 18 || OIDArrayChar != 1002 {
		t.Errorf("char OIDs drifted: OIDChar=%d (want 18), OIDArrayChar=%d (want 1002)", OIDChar, OIDArrayChar)
	}
}

// TestTypeNameToOIDAlternativeNames verifies type name aliases resolve correctly.
func TestTypeNameToOIDAlternativeNames(t *testing.T) {
	cases := []struct {
		alias   string
		wantOID uint32
	}{
		{"integer", OIDInt4},
		{"int", OIDInt4},
		{"bigint", OIDInt8},
		{"smallint", OIDInt2},
		{"boolean", OIDBool},
		{"decimal", OIDNumeric},
		{"real", OIDFloat4},
		{"double precision", OIDFloat8},
		{"character varying", OIDVarChar},
		{"character", OIDBpChar},
		{"timestamp without time zone", OIDTimestamp},
		{"timestamp with time zone", OIDTimestampTZ},
		{"time without time zone", OIDTime},
	}
	for _, tc := range cases {
		if got := TypeNameToOID(tc.alias); got != tc.wantOID {
			t.Errorf("TypeNameToOID(%q) = %d, want %d", tc.alias, got, tc.wantOID)
		}
	}
}

// TestTypeNameToOIDUnknownFallsBackToText verifies the safe default.
func TestTypeNameToOIDUnknownFallsBackToText(t *testing.T) {
	if got := TypeNameToOID("totally_unknown_type"); got != OIDText {
		t.Errorf("unknown type OID = %d, want %d (OIDText)", got, OIDText)
	}
}

// TestTypeNameToOIDArrayNames pins the `[]` array arms added for deferral row
// 1364 — every array spelling maps to its canonical _typename ARRAY OID,
// mirroring initdb/pg_proc_view.go:typeNameToOIDStr (the reference table this
// block was ported from). The char spellings are the tricky pair: BOTH `char[]`
// and `"char"[]` → _char (1002), so the scalar `"char"`/`character`→1042 arm
// must never leak a 1014 (_bpchar) into the array path. PG oracle (18.3):
// 'int4[]'::regtype::oid=1007, '"char"[]'=1002, 'char[]'=1014 (bare), 'date[]'
// =1182. Unknown elements (a user type `foo[]`) keep the OIDText(25) fallback.
func TestTypeNameToOIDArrayNames(t *testing.T) {
	cases := []struct {
		name    string
		wantOID uint32
	}{
		{"bool[]", OIDArrayBool},
		{"boolean[]", OIDArrayBool},
		{"bytea[]", OIDArrayBytea},
		{"char[]", OIDArrayChar},
		{`"char"[]`, OIDArrayChar},
		{"name[]", OIDArrayName},
		{"int8[]", OIDArrayInt8},
		{"bigint[]", OIDArrayInt8},
		{"int2[]", OIDArrayInt2},
		{"smallint[]", OIDArrayInt2},
		{"int4[]", OIDArrayInt4},
		{"int[]", OIDArrayInt4},
		{"integer[]", OIDArrayInt4},
		{"text[]", OIDArrayText},
		{"oid[]", OIDArrayOID},
		{"float4[]", OIDArrayFloat4},
		{"real[]", OIDArrayFloat4},
		{"float8[]", OIDArrayFloat8},
		{"float[]", OIDArrayFloat8},
		{"double precision[]", OIDArrayFloat8},
		{"varchar[]", OIDArrayVarChar},
		{"character varying[]", OIDArrayVarChar},
		{"date[]", OIDArrayDate},
		{"timestamp[]", OIDArrayTimestamp},
		{"timestamp without time zone[]", OIDArrayTimestamp},
		{"timestamptz[]", OIDArrayTimestampTZ},
		{"timestamp with time zone[]", OIDArrayTimestampTZ},
		{"interval[]", OIDArrayInterval},
		{"numeric[]", OIDArrayNumeric},
		{"decimal[]", OIDArrayNumeric},
		{"uuid[]", OIDArrayUUID},
		{"record[]", 2287}, // _record; no catalog.OIDArrayRecord constant exists
	}
	for _, tc := range cases {
		if got := TypeNameToOID(tc.name); got != tc.wantOID {
			t.Errorf("TypeNameToOID(%q) = %d, want %d", tc.name, got, tc.wantOID)
		}
	}
	// Unknown element: no fabricated OID — keeps the OIDText(25) safe default.
	for _, name := range []string{"foo[]", `"mytype"[]`, "int4[][]"} {
		if got := TypeNameToOID(name); got != OIDText {
			t.Errorf("TypeNameToOID(%q) = %d, want %d (OIDText)", name, got, OIDText)
		}
	}
	// Scalar spellings are unchanged: `char` (bare, bpchar) stays 1042 and the
	// quoted `"char"` scalar (not an array) has NO arm — it falls to OIDText(25)
	// exactly as before the array block.
	if got := TypeNameToOID("char"); got != OIDBpChar {
		t.Errorf("TypeNameToOID(\"char\") = %d, want %d (OIDBpChar)", got, OIDBpChar)
	}
	if got := TypeNameToOID(`"char"`); got != OIDText {
		t.Errorf("TypeNameToOID(%q) = %d, want %d (OIDText)", `"char"`, got, OIDText)
	}
}
