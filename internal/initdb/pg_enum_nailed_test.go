package initdb

import "testing"

// TestNailedLocalRelsContainsPgEnum guards M0106-0010 step 3an.
// PG-standby boot opens `pg_enum` (OID 3501) during early backend
// startup once the pg_default_acl family (Steps 3ak/3al/3am) cleared
// the prior FATAL. Without a `nailedLocalRels` entry,
// `RelationBuildDesc(3501) → ScanPgRelation(3501)` returns NULL and
// the backend FATALs with `could not open relation with OID 3501`.
// This test rejects silent removal that would re-introduce the FATAL
// and pins every column against `pg_enum.h` authoritative definitions.
func TestNailedLocalRelsContainsPgEnum(t *testing.T) {
	const enumOID = 3501

	var got *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == enumOID {
			got = &nailedLocalRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedLocalRels missing OID %d (pg_enum) — step 3an regression", enumOID)
	}
	if got.RelName != "pg_enum" {
		t.Fatalf("OID %d: RelName=%q want %q", enumOID, got.RelName, "pg_enum")
	}
	if got.RelKind != 'r' {
		t.Fatalf("OID %d: RelKind=%q want 'r'", enumOID, got.RelKind)
	}
	// PG18 pg_enum_d.h: Natts_pg_enum = 4 (oid, enumtypid, enumsortorder,
	// enumlabel).
	if got.RelNatts != 4 {
		t.Fatalf("OID %d: RelNatts=%d want 4 (PG18 pg_enum column count)", enumOID, got.RelNatts)
	}
	if len(got.Attrs) != 4 {
		t.Fatalf("OID %d: len(Attrs)=%d want 4", enumOID, len(got.Attrs))
	}

	// Pin per-attribute (name, TypeOID, Num, Len, NotNull) against
	// pg_enum_d.h / pg_enum.h authoritative definitions. enumsortorder is
	// float4 (TypeOID 700), enumlabel is name (TypeOID 19, Len 64).
	type want struct {
		Name    string
		TypeOID uint32
		Num     int16
		Len     int16
		NotNull bool
	}
	wantAttrs := []want{
		{"oid", 26, 1, 4, true},
		{"enumtypid", 26, 2, 4, true},
		{"enumsortorder", 700, 3, 4, true},
		{"enumlabel", 19, 4, 64, true},
	}
	for i, w := range wantAttrs {
		a := got.Attrs[i]
		if a.Name != w.Name || a.TypeOID != w.TypeOID || a.Num != w.Num || a.Len != w.Len || a.NotNull != w.NotNull {
			t.Errorf("Attrs[%d]=%+v want {%s %d %d %d %v}", i, a, w.Name, w.TypeOID, w.Num, w.Len, w.NotNull)
		}
	}
}
