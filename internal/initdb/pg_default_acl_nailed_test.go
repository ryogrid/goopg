package initdb

import "testing"

// TestNailedLocalRelsContainsPgDefaultAcl guards M0106-0010 step 3ak.
// PG-standby boot opens `pg_default_acl` (OID 826) during early backend
// startup and FATALs with `could not open relation with OID 826` if no
// pg_class row exists. The fix adds an entry to `nailedLocalRels`; this
// test rejects silent removal that would re-introduce the FATAL and pins
// every column against `pg_default_acl.h` authoritative definitions.
func TestNailedLocalRelsContainsPgDefaultAcl(t *testing.T) {
	const defaultAclOID = 826

	var got *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == defaultAclOID {
			got = &nailedLocalRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedLocalRels missing OID %d (pg_default_acl) — step 3ak regression", defaultAclOID)
	}
	if got.RelName != "pg_default_acl" {
		t.Fatalf("OID %d: RelName=%q want %q", defaultAclOID, got.RelName, "pg_default_acl")
	}
	if got.RelKind != 'r' {
		t.Fatalf("OID %d: RelKind=%q want 'r'", defaultAclOID, got.RelKind)
	}
	// PG18 pg_default_acl_d.h: Natts_pg_default_acl = 5 (oid, defaclrole,
	// defaclnamespace, defaclobjtype, defaclacl).
	if got.RelNatts != 5 {
		t.Fatalf("OID %d: RelNatts=%d want 5 (PG18 pg_default_acl column count)", defaultAclOID, got.RelNatts)
	}
	if len(got.Attrs) != 5 {
		t.Fatalf("OID %d: len(Attrs)=%d want 5", defaultAclOID, len(got.Attrs))
	}

	// Pin per-attribute (name, TypeOID, Num, Len, NotNull) against
	// pg_default_acl_d.h / pg_default_acl.h authoritative definitions.
	// defaclacl is aclitem[] (OID 1034, varlena Len=-1) and carries
	// BKI_FORCE_NOT_NULL in the upstream header.
	type want struct {
		Name    string
		TypeOID uint32
		Num     int16
		Len     int16
		NotNull bool
	}
	wantAttrs := []want{
		{"oid", 26, 1, 4, true},
		{"defaclrole", 26, 2, 4, true},
		{"defaclnamespace", 26, 3, 4, true},
		{"defaclobjtype", 18, 4, 1, true},
		{"defaclacl", 1034, 5, -1, true},
	}
	for i, w := range wantAttrs {
		a := got.Attrs[i]
		if a.Name != w.Name || a.TypeOID != w.TypeOID || a.Num != w.Num || a.Len != w.Len || a.NotNull != w.NotNull {
			t.Errorf("Attrs[%d]=%+v want {%s %d %d %d %v}", i, a, w.Name, w.TypeOID, w.Num, w.Len, w.NotNull)
		}
	}
}
