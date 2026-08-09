package catalog

import "testing"

// TestEncodingIDNameRoundTrip checks the pg_enc ID↔name mapping that backs
// pg_encoding_to_char (dumpConversion's FOR/TO literals) and CREATE CONVERSION's
// encoding-name resolution. DU-002 slice 399.
func TestEncodingIDNameRoundTrip(t *testing.T) {
	cases := []struct {
		id   int32
		name string
	}{
		{0, "SQL_ASCII"},
		{6, "UTF8"},
		{8, "LATIN1"},
		{41, "SHIFT_JIS_2004"},
	}
	for _, tc := range cases {
		if got := EncodingIDToName(tc.id); got != tc.name {
			t.Errorf("EncodingIDToName(%d) = %q, want %q", tc.id, got, tc.name)
		}
		if got := EncodingNameToID(tc.name); got != tc.id {
			t.Errorf("EncodingNameToID(%q) = %d, want %d", tc.name, got, tc.id)
		}
	}
	// Out-of-range ID → empty string (mirrors pg_encoding_to_char).
	if got := EncodingIDToName(-1); got != "" {
		t.Errorf("EncodingIDToName(-1) = %q, want empty", got)
	}
	if got := EncodingIDToName(99); got != "" {
		t.Errorf("EncodingIDToName(99) = %q, want empty", got)
	}
	// Alias punctuation/case stripping (clean_encoding_name) resolves canonical
	// names regardless of case/separators.
	if got := EncodingNameToID("utf-8"); got != 6 {
		t.Errorf("EncodingNameToID(utf-8) = %d, want 6", got)
	}
	// An unrecognized name → -1.
	if got := EncodingNameToID("NO_SUCH_ENC"); got != -1 {
		t.Errorf("EncodingNameToID(NO_SUCH_ENC) = %d, want -1", got)
	}
	// Non-canonical aliases from pg_encname_tbl resolve just as
	// pg_char_to_encoding does (DU-002 slice 400, closes 399 deferral (a)).
	aliasCases := []struct {
		name string
		id   int32
	}{
		{"unicode", 6},       // → UTF8
		{"UNICODE", 6},       // case-insensitive
		{"windows-1252", 24}, // → WIN1252 (punctuation stripped)
		{"mskanji", 35},      // → SJIS
		{"iso-8859-1", 8},    // → LATIN1
		{"koi8", 22},         // _dirty_ alias → KOI8R
		{"win950", 36},       // → BIG5
	}
	for _, tc := range aliasCases {
		if got := EncodingNameToID(tc.name); got != tc.id {
			t.Errorf("EncodingNameToID(%q) = %d, want %d", tc.name, got, tc.id)
		}
	}
}

// TestPgConversionVirtualRows verifies that a registered conversion surfaces as
// a dumpable pg_conversion row with the encoding IDs, schema-qualified conproc,
// and condefault flag pg_dump's getConversions / dumpConversion expect.
// DU-002 slice 399.
func TestPgConversionVirtualRows(t *testing.T) {
	c := NewInMemory()
	c.RegisterSchema("public")
	uc := &UserConversion{
		Name:        "myconv",
		Owner:       10,
		ForEncoding: EncodingNameToID("UTF8"),
		ToEncoding:  EncodingNameToID("LATIN1"),
		ProcSchema:  "public",
		ProcName:    "myconv_func",
		Default:     true,
	}
	oid, err := c.CreateConversion(uc, "public")
	if err != nil {
		t.Fatalf("CreateConversion: %v", err)
	}
	if oid == 0 {
		t.Fatal("CreateConversion returned OID 0")
	}
	// A duplicate name in the same namespace must be rejected.
	if _, err := c.CreateConversion(&UserConversion{Name: "myconv", ProcName: "x"}, "public"); err == nil {
		t.Error("expected duplicate-conversion error, got nil")
	}

	tbl := c.ns(DefaultDBOid).tables["pg_catalog.pg_conversion"]
	if tbl == nil || tbl.VirtualRows == nil {
		t.Fatal("pg_conversion virtual table missing")
	}
	rows := tbl.VirtualRows()
	if len(rows) != 1 {
		t.Fatalf("pg_conversion rows = %d, want 1", len(rows))
	}
	r := rows[0]
	// cols: oid, conname, connamespace, conowner, conforencoding, contoencoding, conproc, condefault
	if r[1] != "myconv" {
		t.Errorf("conname = %q, want myconv", r[1])
	}
	if r[3] != "10" {
		t.Errorf("conowner = %q, want 10", r[3])
	}
	if r[4] != "6" { // UTF8
		t.Errorf("conforencoding = %q, want 6", r[4])
	}
	if r[5] != "8" { // LATIN1
		t.Errorf("contoencoding = %q, want 8", r[5])
	}
	if r[6] != "public.myconv_func" {
		t.Errorf("conproc = %q, want public.myconv_func", r[6])
	}
	if r[7] != "t" {
		t.Errorf("condefault = %q, want t", r[7])
	}

	// DropConversion removes the dump-visible row.
	if !c.DropConversion("myconv", "public") {
		t.Error("DropConversion returned false")
	}
	if rows := tbl.VirtualRows(); len(rows) != 0 {
		t.Errorf("after drop, rows = %d, want 0", len(rows))
	}
}

// TestValidServerEncodingName verifies the server-encoding validity check that
// gatekeeps CREATE DATABASE ... ENCODING — client-only encodings (SJIS,
// BIG5, …) and unknown names are rejected. M0122-0008.
func TestValidServerEncodingName(t *testing.T) {
	// Valid server encodings (PG_VALID_BE_ENCODING: 0-34).
	valid := []string{"UTF8", "utf-8", "LATIN1", "SQL_ASCII", "WIN1252", "KOI8U"}
	for _, name := range valid {
		if got := ValidServerEncodingName(name); got < 0 {
			t.Errorf("ValidServerEncodingName(%q) = %d, want >= 0", name, got)
		}
	}
	// Client-only encodings (ID > 34).
	clientOnly := []string{"SJIS", "BIG5", "GBK", "UHC", "GB18030", "JOHAB", "SHIFT_JIS_2004"}
	for _, name := range clientOnly {
		if got := ValidServerEncodingName(name); got >= 0 {
			t.Errorf("ValidServerEncodingName(%q) = %d, want -1 (client-only)", name, got)
		}
	}
	// Unknown encoding → -1.
	if got := ValidServerEncodingName("NO_SUCH_ENC"); got != -1 {
		t.Errorf("ValidServerEncodingName(NO_SUCH_ENC) = %d, want -1", got)
	}
}

// TestPgConversionVirtualRowsFuncOID verifies that conproc resolves through a
// live FuncOID->pg_proc lookup (mirroring regproc's real OID-reference
// semantics) rather than the as-written ProcSchema/ProcName text — so a
// RENAME on the conversion function after CREATE CONVERSION still dumps the
// current name. DU-002 slice 403 (closes the slice-402 conproc-OID-cross-ref
// deferral).
func TestPgConversionVirtualRowsFuncOID(t *testing.T) {
	c := NewInMemory()
	c.RegisterSchema("public")
	routine, err := c.Routines().Create(&Routine{
		Schema: "public",
		Name:   "myconv_func",
	}, false)
	if err != nil {
		t.Fatalf("Routines().Create: %v", err)
	}
	uc := &UserConversion{
		Name:        "myconv",
		Owner:       10,
		ForEncoding: EncodingNameToID("UTF8"),
		ToEncoding:  EncodingNameToID("LATIN1"),
		// Deliberately stale ProcSchema/ProcName text: FuncOID must win.
		ProcSchema: "public",
		ProcName:   "stale_name_should_not_appear",
		FuncOID:    routine.OID,
	}
	if _, err := c.CreateConversion(uc, "public"); err != nil {
		t.Fatalf("CreateConversion: %v", err)
	}
	tbl := c.ns(DefaultDBOid).tables["pg_catalog.pg_conversion"]
	row := tbl.VirtualRows()[0]
	if row[6] != "public.myconv_func" {
		t.Errorf("conproc = %q, want public.myconv_func (resolved via FuncOID)", row[6])
	}

	// A RENAME on the function propagates without touching the conversion.
	if err := c.Routines().RenameRoutine(routine, "myconv_func_renamed"); err != nil {
		t.Fatalf("RenameRoutine: %v", err)
	}
	row = tbl.VirtualRows()[0]
	if row[6] != "public.myconv_func_renamed" {
		t.Errorf("conproc after rename = %q, want public.myconv_func_renamed", row[6])
	}

	// FuncOID == 0 (e.g. a UserConversion built without a routine registry)
	// falls back to the as-written ProcSchema/ProcName text.
	c.DropConversion("myconv", "public")
	uc2 := &UserConversion{
		Name:        "myconv2",
		Owner:       10,
		ForEncoding: EncodingNameToID("UTF8"),
		ToEncoding:  EncodingNameToID("LATIN1"),
		ProcSchema:  "public",
		ProcName:    "unresolved_func",
	}
	if _, err := c.CreateConversion(uc2, "public"); err != nil {
		t.Fatalf("CreateConversion: %v", err)
	}
	row = tbl.VirtualRows()[0]
	if row[6] != "public.unresolved_func" {
		t.Errorf("conproc fallback = %q, want public.unresolved_func", row[6])
	}
}
