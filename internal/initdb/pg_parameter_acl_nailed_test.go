package initdb

import "testing"

// TestNailedSharedRelsContainsPgParameterAcl guards M0106-0010 step 3bp.
// PG-standby boot opens `pg_parameter_acl` (OID 6243, BKI_SHARED_RELATION)
// during early backend startup and FATALs with `could not open relation
// with OID 6243` if no pg_class row exists. The fix adds an entry to
// `nailedSharedRels`; this test rejects silent removal that would
// re-introduce the FATAL and pins every column against
// `postgres/src/include/catalog/pg_parameter_acl.h` (PG18) and
// `pg_parameter_acl_d.h` authoritative definitions.
func TestNailedSharedRelsContainsPgParameterAcl(t *testing.T) {
	const parameterAclOID = 6243

	var got *nailedRel
	for i := range nailedSharedRels {
		if nailedSharedRels[i].OID == parameterAclOID {
			got = &nailedSharedRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedSharedRels missing OID %d (pg_parameter_acl) — step 3bp regression", parameterAclOID)
	}
	if got.RelName != "pg_parameter_acl" {
		t.Fatalf("OID %d: RelName=%q want %q", parameterAclOID, got.RelName, "pg_parameter_acl")
	}
	if got.RelKind != 'r' {
		t.Fatalf("OID %d: RelKind=%q want 'r'", parameterAclOID, got.RelKind)
	}
	if !got.IsShared {
		t.Fatalf("OID %d: IsShared=false want true (pg_parameter_acl is BKI_SHARED_RELATION)", parameterAclOID)
	}
	// PG18 pg_parameter_acl_d.h: Natts_pg_parameter_acl = 3.
	if got.RelNatts != 3 {
		t.Fatalf("OID %d: RelNatts=%d want 3 (PG18 pg_parameter_acl column count)", parameterAclOID, got.RelNatts)
	}
	if len(got.Attrs) != 3 {
		t.Fatalf("OID %d: len(Attrs)=%d want 3", parameterAclOID, len(got.Attrs))
	}
	// RelType=83 is safe because pg_parameter_acl is not formrdesc'd
	// (no ParameterAclRelation_Rowtype_Id constant in PG18 headers; only
	// pg_database/pg_authid/pg_auth_members/pg_shseclabel/pg_subscription
	// are formrdesc'd shared rels). Confirm we did not accidentally pick
	// up a real rowtype OID — anything other than 83 would suggest a
	// formrdesc tie-in that needs deeper inspection.
	if got.RelType != 83 {
		t.Fatalf("OID %d: RelType=%d want 83 (placeholder, not formrdesc'd)", parameterAclOID, got.RelType)
	}

	// Pin per-attribute (name, TypeOID, Num, Len, NotNull) against
	// pg_parameter_acl.h authoritative definitions. parname is
	// BKI_FORCE_NOT_NULL; paracl is BKI_DEFAULT(_null_) so nullable.
	type want struct {
		Name    string
		TypeOID uint32
		Num     int16
		Len     int16
		NotNull bool
	}
	wantAttrs := []want{
		{"oid", 26, 1, 4, true},       // oid
		{"parname", 25, 2, -1, true},  // text BKI_FORCE_NOT_NULL
		{"paracl", 1034, 3, -1, false}, // aclitem[] BKI_DEFAULT(_null_)
	}
	for i, w := range wantAttrs {
		a := got.Attrs[i]
		if a.Name != w.Name || a.TypeOID != w.TypeOID || a.Num != w.Num || a.Len != w.Len || a.NotNull != w.NotNull {
			t.Errorf("Attrs[%d]=%+v want {%s %d %d %d %v}", i, a, w.Name, w.TypeOID, w.Num, w.Len, w.NotNull)
		}
	}
}
