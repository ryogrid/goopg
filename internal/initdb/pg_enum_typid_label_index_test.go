package initdb

import "testing"

// TestPgEnumTypIdLabelIndexSeededFromInitialEntries pins
// M0106-0010 Step 3ap's catalog seed:
// pg_enum_typid_label_index (OID 3503) must appear in
// pgIndexInitialEntries as UNIQUE (non-PRIMARY) composite on attnums
// {2, 4} = (enumtypid, enumlabel).
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_enum.h:48
//	  DECLARE_UNIQUE_INDEX(pg_enum_typid_label_index, 3503,
//	    EnumTypIdLabelIndexId, pg_enum,
//	    btree(enumtypid oid_ops, enumlabel name_ops));
//
// Companion to OID 3502 (pg_enum_oid_index, UNIQUE PRIMARY, Step 3ao)
// and OID 3534 (pg_enum_typid_sortorder_index, UNIQUE composite
// float4_ops; Step 3aq). Without this entry PG's
// RelationIdGetRelation(3503) FATALs with "could not open relation
// with OID 3503" — the Step 3ap boot blocker that surfaces after
// Step 3ao seeded pg_enum_oid_index.
func TestPgEnumTypIdLabelIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 3503
	var found *pgIndexEntry
	entries := pgIndexInitialEntries()
	for i := range entries {
		if entries[i].IndexRelid == oid {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_enum_typid_label_index) missing — Step 3ap", oid)
	}
	if found.IndRelid != 3501 {
		t.Errorf("OID %d: IndRelid=%d, want 3501 (pg_enum heap OID)", oid, found.IndRelid)
	}
	if !int16SliceEqual(found.IndKey, []int16{2, 4}) {
		t.Errorf("OID %d: IndKey=%v, want [2 4] (enumtypid, enumlabel)", oid, found.IndKey)
	}
	if !found.IsUnique {
		t.Errorf("OID %d: IsUnique=false, want true (DECLARE_UNIQUE_INDEX)", oid)
	}
	if found.IsPrimary {
		t.Errorf("OID %d: IsPrimary=true, want false (DECLARE_UNIQUE_INDEX is non-PKEY)", oid)
	}
	if len(found.IndCollation) != 2 {
		t.Fatalf("OID %d: IndCollation len=%d, want 2", oid, len(found.IndCollation))
	}
	if found.IndCollation[0] != 0 {
		t.Errorf("OID %d: IndCollation[0]=%d, want 0 (oid_ops carries no collation)", oid, found.IndCollation[0])
	}
	// enumlabel is a `name` type; its btree opclass `name_ops` uses C
	// collation (C_COLLATION_OID = 950) — same convention as
	// pg_conversion_name_nsp_index (2669, Step 3aj) and
	// pg_opclass_am_name_nsp_index (2686, Step 3ad).
	const cCollation uint32 = 950
	if found.IndCollation[1] != cCollation {
		t.Errorf("OID %d: IndCollation[1]=%d, want %d (name_ops C collation)", oid, found.IndCollation[1], cCollation)
	}
}

// TestNailedLocalRelsContainsPgEnumTypIdLabelIndex asserts that the
// nailed-rel registry includes OID 3503 with RelKind='i' and RelNatts=2.
// Without this entry no pg_class row gets seeded for 3503 and
// RelationIdGetRelation(3503) FATALs; flattenRels derives RelNatts via
// pgIndexNattsByOID, which must equal pg_index.indnatts to satisfy
// RelationInitIndexAccessInfo's check at relcache.c:1492.
func TestNailedLocalRelsContainsPgEnumTypIdLabelIndex(t *testing.T) {
	const oid uint32 = 3503
	var found *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_enum_typid_label_index) missing — Step 3ap", oid)
	}
	if found.RelName != "pg_enum_typid_label_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_enum_typid_label_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 2 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 2 (enumtypid, enumlabel)", oid, found.RelNatts)
	}
}
