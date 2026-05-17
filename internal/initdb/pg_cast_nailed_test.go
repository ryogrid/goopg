package initdb

import "testing"

// TestNailedLocalRelsContainsPgCast guards M0106-0010 step 3aa: PG
// standby's early backend startup opens `pg_cast` (OID 2605) and FATALs
// with `could not open relation with OID 2605` if no pg_class row exists.
// The fix adds an entry to `nailedLocalRels`; this test rejects silent
// removal that would re-introduce the FATAL.
func TestNailedLocalRelsContainsPgCast(t *testing.T) {
	const castOID = 2605

	var got *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == castOID {
			got = &nailedLocalRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedLocalRels missing OID %d (pg_cast) — step 3aa regression", castOID)
	}
	if got.RelName != "pg_cast" {
		t.Fatalf("OID %d: RelName=%q want %q", castOID, got.RelName, "pg_cast")
	}
	if got.RelKind != 'r' {
		t.Fatalf("OID %d: RelKind=%q want 'r'", castOID, got.RelKind)
	}
	// PG18 pg_cast_d.h: Natts_pg_cast = 6 (oid, castsource, casttarget,
	// castfunc, castcontext, castmethod).
	if got.RelNatts != 6 {
		t.Fatalf("OID %d: RelNatts=%d want 6 (PG18 pg_cast column count)", castOID, got.RelNatts)
	}
	if len(got.Attrs) != 6 {
		t.Fatalf("OID %d: len(Attrs)=%d want 6", castOID, len(got.Attrs))
	}

	// Pin per-attribute (name, TypeOID, Num, Len, NotNull) against
	// pg_cast_d.h authoritative definitions.
	type want struct {
		Name    string
		TypeOID uint32
		Num     int16
		Len     int16
		NotNull bool
	}
	wantAttrs := []want{
		{"oid", 26, 1, 4, true},
		{"castsource", 26, 2, 4, true},
		{"casttarget", 26, 3, 4, true},
		{"castfunc", 26, 4, 4, true},
		{"castcontext", 18, 5, 1, true},
		{"castmethod", 18, 6, 1, true},
	}
	for i, w := range wantAttrs {
		a := got.Attrs[i]
		if a.Name != w.Name || a.TypeOID != w.TypeOID || a.Num != w.Num || a.Len != w.Len || a.NotNull != w.NotNull {
			t.Errorf("Attrs[%d]=%+v want {%s %d %d %d %v}", i, a, w.Name, w.TypeOID, w.Num, w.Len, w.NotNull)
		}
	}
}
