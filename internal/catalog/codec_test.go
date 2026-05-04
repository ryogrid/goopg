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
func TestPGAttributeColumnsCount(t *testing.T) {
	if got := len(PGAttributeColumns()); got != 6 {
		t.Errorf("PGAttributeColumns: len=%d want 6", got)
	}
}

// TestPGTypeColumnsCount checks the pg_type schema column count.
func TestPGTypeColumnsCount(t *testing.T) {
	if got := len(PGTypeColumns()); got != 7 {
		t.Errorf("PGTypeColumns: len=%d want 7", got)
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
	}
	for _, tc := range cases {
		if tc.oid != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.oid, tc.want)
		}
	}
}
