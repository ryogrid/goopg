package initdb

import "testing"

// TestPgEnumTypIdSortOrderIndexSeededFromInitialEntries pins
// M0106-0010 Step 3aq's catalog seed:
// pg_enum_typid_sortorder_index (OID 3534) must appear in
// pgIndexInitialEntries as UNIQUE (non-PRIMARY) composite on attnums
// {2, 3} = (enumtypid, enumsortorder).
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_enum.h:48
//	  DECLARE_UNIQUE_INDEX(pg_enum_typid_sortorder_index, 3534,
//	    EnumTypIdSortOrderIndexId, pg_enum,
//	    btree(enumtypid oid_ops, enumsortorder float4_ops));
//
// Companion to OID 3502 (pg_enum_oid_index, UNIQUE PRIMARY, Step 3ao)
// and OID 3503 (pg_enum_typid_label_index, UNIQUE composite name_ops,
// Step 3ap). Without this entry PG's RelationIdGetRelation(3534)
// FATALs with "could not open relation with OID 3534" — the Step 3aq
// boot blocker that surfaces after Step 3ap seeded
// pg_enum_typid_label_index.
//
// First nailed index keyed on `float4_ops` btree opclass.
// `float4_ops` OID 10012 sourced from
// `postgres/src/backend/catalog/postgres.bki`
// (`insert ( 10012 403 float4_ops 11 10 1970 700 t 0 )`, am=403/btree).
// Neither key carries a collation: oid_ops is a uint OID opclass,
// float4_ops is a scalar numeric opclass.
func TestPgEnumTypIdSortOrderIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 3534
	var found *pgIndexEntry
	entries := pgIndexInitialEntries()
	for i := range entries {
		if entries[i].IndexRelid == oid {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_enum_typid_sortorder_index) missing — Step 3aq", oid)
	}
	if found.IndRelid != 3501 {
		t.Errorf("OID %d: IndRelid=%d, want 3501 (pg_enum heap OID)", oid, found.IndRelid)
	}
	if !int16SliceEqual(found.IndKey, []int16{2, 3}) {
		t.Errorf("OID %d: IndKey=%v, want [2 3] (enumtypid, enumsortorder)", oid, found.IndKey)
	}
	if !found.IsUnique {
		t.Errorf("OID %d: IsUnique=false, want true (DECLARE_UNIQUE_INDEX)", oid)
	}
	if found.IsPrimary {
		t.Errorf("OID %d: IsPrimary=true, want false (DECLARE_UNIQUE_INDEX is non-PKEY)", oid)
	}
	if len(found.IndClass) != 2 {
		t.Fatalf("OID %d: IndClass len=%d, want 2", oid, len(found.IndClass))
	}
	const oidOpsOID uint32 = 1981
	const float4OpsOID uint32 = 10012
	if found.IndClass[0] != oidOpsOID {
		t.Errorf("OID %d: IndClass[0]=%d, want %d (oid_ops)", oid, found.IndClass[0], oidOpsOID)
	}
	if found.IndClass[1] != float4OpsOID {
		t.Errorf("OID %d: IndClass[1]=%d, want %d (float4_ops btree, postgres.bki)", oid, found.IndClass[1], float4OpsOID)
	}
	if len(found.IndCollation) != 2 {
		t.Fatalf("OID %d: IndCollation len=%d, want 2", oid, len(found.IndCollation))
	}
	// Neither key carries a collation. oid_ops is a uint OID opclass;
	// float4_ops is a scalar numeric opclass — no collation slot.
	if found.IndCollation[0] != 0 {
		t.Errorf("OID %d: IndCollation[0]=%d, want 0 (oid_ops carries no collation)", oid, found.IndCollation[0])
	}
	if found.IndCollation[1] != 0 {
		t.Errorf("OID %d: IndCollation[1]=%d, want 0 (float4_ops carries no collation)", oid, found.IndCollation[1])
	}
}

// TestNailedLocalRelsContainsPgEnumTypIdSortOrderIndex asserts that the
// nailed-rel registry includes OID 3534 with RelKind='i' and RelNatts=2.
// Without this entry no pg_class row gets seeded for 3534 and
// RelationIdGetRelation(3534) FATALs; flattenRels derives RelNatts via
// pgIndexNattsByOID, which must equal pg_index.indnatts to satisfy
// RelationInitIndexAccessInfo's check at relcache.c:1492.
func TestNailedLocalRelsContainsPgEnumTypIdSortOrderIndex(t *testing.T) {
	const oid uint32 = 3534
	var found *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_enum_typid_sortorder_index) missing — Step 3aq", oid)
	}
	if found.RelName != "pg_enum_typid_sortorder_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_enum_typid_sortorder_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 2 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 2 (enumtypid, enumsortorder)", oid, found.RelNatts)
	}
}
