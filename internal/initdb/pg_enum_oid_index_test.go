package initdb

import "testing"

// TestPgEnumOidIndexSeededFromInitialEntries pins
// M0106-0010 Step 3ao's catalog seed:
// pg_enum_oid_index (OID 3502) must appear in pgIndexInitialEntries as
// UNIQUE PRIMARY KEY on attnum {1} = oid.
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_enum.h:47
//	  DECLARE_UNIQUE_INDEX_PKEY(pg_enum_oid_index, 3502,
//	    EnumOidIndexId, pg_enum, btree(oid oid_ops));
//
// Companion to OID 3503 (pg_enum_typid_label_index, UNIQUE composite
// name_ops; Step 3ap) and OID 3534 (pg_enum_typid_sortorder_index, UNIQUE
// composite float4_ops; Step 3aq). Without this entry PG's
// RelationIdGetRelation(3502) FATALs with "could not open relation with
// OID 3502" — the Step 3ao boot blocker that surfaces after Step 3an
// seeded the pg_enum heap.
func TestPgEnumOidIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 3502
	var found *pgIndexEntry
	entries := pgIndexInitialEntries()
	for i := range entries {
		if entries[i].IndexRelid == oid {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_enum_oid_index) missing — Step 3ao", oid)
	}
	if found.IndRelid != 3501 {
		t.Errorf("OID %d: IndRelid=%d, want 3501 (pg_enum heap OID)", oid, found.IndRelid)
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

// TestNailedLocalRelsContainsPgEnumOidIndex asserts that the nailed-rel
// registry includes OID 3502 with RelKind='i' and RelNatts=1. Without
// this entry no pg_class row gets seeded for 3502 and
// RelationIdGetRelation(3502) FATALs; flattenRels derives RelNatts via
// pgIndexNattsByOID, which must equal pg_index.indnatts to satisfy
// RelationInitIndexAccessInfo's check at relcache.c:1492.
func TestNailedLocalRelsContainsPgEnumOidIndex(t *testing.T) {
	const oid uint32 = 3502
	var found *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_enum_oid_index) missing — Step 3ao", oid)
	}
	if found.RelName != "pg_enum_oid_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_enum_oid_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 1 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 1 (single-column oid PKEY)", oid, found.RelNatts)
	}
}
