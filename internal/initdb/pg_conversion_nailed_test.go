package initdb

import "testing"

// TestNailedLocalRelsContainsPgConversion guards M0106-0010 step 3ag.
// PG-standby boot opens `pg_conversion` (OID 2607) during early backend
// startup and FATALs with `could not open relation with OID 2607` if no
// pg_class row exists. The fix adds an entry to `nailedLocalRels`; this
// test rejects silent removal that would re-introduce the FATAL and pins
// every column against `pg_conversion_d.h` authoritative definitions.
func TestNailedLocalRelsContainsPgConversion(t *testing.T) {
	const conversionOID = 2607

	var got *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == conversionOID {
			got = &nailedLocalRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedLocalRels missing OID %d (pg_conversion) — step 3ag regression", conversionOID)
	}
	if got.RelName != "pg_conversion" {
		t.Fatalf("OID %d: RelName=%q want %q", conversionOID, got.RelName, "pg_conversion")
	}
	if got.RelKind != 'r' {
		t.Fatalf("OID %d: RelKind=%q want 'r'", conversionOID, got.RelKind)
	}
	// PG18 pg_conversion_d.h: Natts_pg_conversion = 8.
	if got.RelNatts != 8 {
		t.Fatalf("OID %d: RelNatts=%d want 8 (PG18 pg_conversion column count)", conversionOID, got.RelNatts)
	}
	if len(got.Attrs) != 8 {
		t.Fatalf("OID %d: len(Attrs)=%d want 8", conversionOID, len(got.Attrs))
	}

	// Pin per-attribute (name, TypeOID, Num, Len, NotNull) against
	// pg_conversion_d.h / pg_conversion.h authoritative definitions.
	type want struct {
		Name    string
		TypeOID uint32
		Num     int16
		Len     int16
		NotNull bool
	}
	wantAttrs := []want{
		{"oid", 26, 1, 4, true},
		{"conname", 19, 2, 64, true},
		{"connamespace", 26, 3, 4, true},
		{"conowner", 26, 4, 4, true},
		{"conforencoding", 23, 5, 4, true},
		{"contoencoding", 23, 6, 4, true},
		{"conproc", 24, 7, 4, true},
		{"condefault", 16, 8, 1, true},
	}
	for i, w := range wantAttrs {
		a := got.Attrs[i]
		if a.Name != w.Name || a.TypeOID != w.TypeOID || a.Num != w.Num || a.Len != w.Len || a.NotNull != w.NotNull {
			t.Errorf("Attrs[%d]=%+v want {%s %d %d %d %v}", i, a, w.Name, w.TypeOID, w.Num, w.Len, w.NotNull)
		}
	}
}
