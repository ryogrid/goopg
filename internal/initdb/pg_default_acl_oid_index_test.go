package initdb

import "testing"

// TestPgDefaultAclOidIndexSeededFromInitialEntries pins
// M0106-0010 Step 3am's catalog seed:
// pg_default_acl_oid_index (OID 828) must appear in
// pgIndexInitialEntries as UNIQUE PRIMARY KEY on attnum {1} = oid.
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_default_acl.h:55
//	  DECLARE_UNIQUE_INDEX_PKEY(pg_default_acl_oid_index, 828,
//	    DefaultAclOidIndexId, pg_default_acl, btree(oid oid_ops));
//
// Companion to OID 827 (pg_default_acl_role_nsp_obj_index, UNIQUE
// non-PKEY composite, Step 3al). Without this entry PG's
// RelationIdGetRelation(828) FATALs with "could not open relation
// with OID 828" — the Step 3am boot blocker that surfaces after
// Step 3al seeded the composite UNIQUE companion index.
func TestPgDefaultAclOidIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 828
	var found *pgIndexEntry
	entries := pgIndexInitialEntries()
	for i := range entries {
		if entries[i].IndexRelid == oid {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_default_acl_oid_index) missing — Step 3am", oid)
	}
	if found.IndRelid != 826 {
		t.Errorf("OID %d: IndRelid=%d, want 826 (pg_default_acl heap OID)", oid, found.IndRelid)
	}
	if !int16SliceEqual(found.IndKey, []int16{1}) {
		t.Errorf("OID %d: IndKey=%v, want [1] (oid)", oid, found.IndKey)
	}
	if !found.IsUnique {
		t.Errorf("OID %d: IsUnique=false, want true (DECLARE_UNIQUE_INDEX_PKEY)", oid)
	}
	if !found.IsPrimary {
		t.Errorf("OID %d: IsPrimary=false, want true (DECLARE_UNIQUE_INDEX_PKEY)", oid)
	}
	if len(found.IndCollation) != 1 {
		t.Fatalf("OID %d: IndCollation len=%d, want 1", oid, len(found.IndCollation))
	}
	if found.IndCollation[0] != 0 {
		t.Errorf("OID %d: IndCollation[0]=%d, want 0 (oid_ops carries no collation)", oid, found.IndCollation[0])
	}
}

// TestNailedLocalRelsContainsPgDefaultAclOidIndex asserts that the
// nailed-rel registry includes OID 828 with RelKind='i' and RelNatts=1.
// Without this entry no pg_class row gets seeded for 828 and
// RelationIdGetRelation(828) FATALs; flattenRels derives RelNatts via
// pgIndexNattsByOID, which must equal pg_index.indnatts to satisfy
// RelationInitIndexAccessInfo's check at relcache.c:1492.
func TestNailedLocalRelsContainsPgDefaultAclOidIndex(t *testing.T) {
	const oid uint32 = 828
	var found *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_default_acl_oid_index) missing — Step 3am", oid)
	}
	if found.RelName != "pg_default_acl_oid_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_default_acl_oid_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 1 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 1 (single-column oid PKEY)", oid, found.RelNatts)
	}
}
