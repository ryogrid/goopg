package initdb

import "testing"

// TestPgDefaultAclRoleNspObjIndexSeededFromInitialEntries pins
// M0106-0010 Step 3al's catalog seed:
// pg_default_acl_role_nsp_obj_index (OID 827) must appear in
// pgIndexInitialEntries as UNIQUE (non-PRIMARY) on attnums
// {2, 3, 4} = (defaclrole, defaclnamespace, defaclobjtype).
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_default_acl.h:54
//	  DECLARE_UNIQUE_INDEX(pg_default_acl_role_nsp_obj_index, 827,
//	    DefaultAclRoleNspObjIndexId, pg_default_acl,
//	    btree(defaclrole oid_ops, defaclnamespace oid_ops,
//	          defaclobjtype char_ops));
//	  MAKE_SYSCACHE(DEFACLROLENSPOBJ, pg_default_acl_role_nsp_obj_index, 8);
//
// Companion to OID 828 (pg_default_acl_oid_index, UNIQUE PRIMARY KEY
// on `oid`, to be seeded by Step 3am). Without this entry PG's
// RelationIdGetRelation(827) FATALs with "could not open relation
// with OID 827" — the Step 3al boot blocker that surfaces after
// Step 3ak seeded the pg_default_acl heap (OID 826).
func TestPgDefaultAclRoleNspObjIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 827
	var found *pgIndexEntry
	entries := pgIndexInitialEntries()
	for i := range entries {
		if entries[i].IndexRelid == oid {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_default_acl_role_nsp_obj_index) missing — Step 3al", oid)
	}
	if found.IndRelid != 826 {
		t.Errorf("OID %d: IndRelid=%d, want 826 (pg_default_acl heap OID)", oid, found.IndRelid)
	}
	if !int16SliceEqual(found.IndKey, []int16{2, 3, 4}) {
		t.Errorf("OID %d: IndKey=%v, want [2 3 4] (defaclrole, defaclnamespace, defaclobjtype)", oid, found.IndKey)
	}
	if !found.IsUnique {
		t.Errorf("OID %d: IsUnique=false, want true (DECLARE_UNIQUE_INDEX)", oid)
	}
	if found.IsPrimary {
		t.Errorf("OID %d: IsPrimary=true, want false (DECLARE_UNIQUE_INDEX is non-PKEY; PKEY is 828)", oid)
	}
	if len(found.IndCollation) != 3 {
		t.Fatalf("OID %d: IndCollation len=%d, want 3", oid, len(found.IndCollation))
	}
	// All three keys are typeless (oid_ops × 2, char_ops × 1) — no
	// collation slots are populated.
	for i, c := range found.IndCollation {
		if c != 0 {
			t.Errorf("OID %d: IndCollation[%d]=%d, want 0 (oid_ops/char_ops carry no collation)", oid, i, c)
		}
	}
}

// TestNailedLocalRelsContainsPgDefaultAclRoleNspObjIndex asserts that the
// nailed-rel registry includes OID 827 with RelKind='i' and RelNatts=3.
// Without this entry no pg_class row gets seeded for 827 and
// RelationIdGetRelation(827) FATALs; flattenRels derives RelNatts via
// pgIndexNattsByOID, which must equal pg_index.indnatts to satisfy
// RelationInitIndexAccessInfo's check at relcache.c:1492.
func TestNailedLocalRelsContainsPgDefaultAclRoleNspObjIndex(t *testing.T) {
	const oid uint32 = 827
	var found *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_default_acl_role_nsp_obj_index) missing — Step 3al", oid)
	}
	if found.RelName != "pg_default_acl_role_nsp_obj_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_default_acl_role_nsp_obj_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 3 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 3 (defaclrole, defaclnamespace, defaclobjtype)", oid, found.RelNatts)
	}
}
