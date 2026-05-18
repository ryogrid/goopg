package initdb

import "testing"

// TestPgForeignDataWrapperOidIndexSeededFromInitialEntries pins
// M0106-0010 Step 3bd's catalog seed:
// pg_foreign_data_wrapper_oid_index (OID 112) must appear in
// pgIndexInitialEntries as UNIQUE PRIMARY KEY single-key on
// attnum {1} = oid.
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_foreign_data_wrapper.h:55
//	  DECLARE_UNIQUE_INDEX_PKEY(pg_foreign_data_wrapper_oid_index, 112,
//	    ForeignDataWrapperOidIndexId, pg_foreign_data_wrapper,
//	    btree(oid oid_ops));
//	  MAKE_SYSCACHE(FOREIGNDATAWRAPPEROID,
//	    pg_foreign_data_wrapper_oid_index, 2);
//
// Heap OID 2328 = pg_foreign_data_wrapper (Step 3bb nailed rel).
// Companion to OID 548 (pg_foreign_data_wrapper_name_index,
// UNIQUE non-PKEY; Step 3bc). Without this entry PG's
// RelationIdGetRelation(112) FATALs with "could not open relation
// with OID 112" — the Step 3bd boot blocker that surfaces after
// Step 3bc seeded pg_foreign_data_wrapper_name_index.
func TestPgForeignDataWrapperOidIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 112
	var found *pgIndexEntry
	entries := pgIndexInitialEntries()
	for i := range entries {
		if entries[i].IndexRelid == oid {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_foreign_data_wrapper_oid_index) missing — Step 3bd", oid)
	}
	if found.IndRelid != 2328 {
		t.Errorf("OID %d: IndRelid=%d, want 2328 (pg_foreign_data_wrapper heap OID)", oid, found.IndRelid)
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

// TestNailedLocalRelsContainsPgForeignDataWrapperOidIndex asserts the
// nailed-rel registry includes OID 112 with RelKind='i' and RelNatts=1.
// Without this entry no pg_class row gets seeded for 112 and
// RelationIdGetRelation(112) FATALs; flattenRels derives RelNatts via
// pgIndexNattsByOID, which must equal pg_index.indnatts to satisfy
// RelationInitIndexAccessInfo's check at relcache.c:1492.
func TestNailedLocalRelsContainsPgForeignDataWrapperOidIndex(t *testing.T) {
	const oid uint32 = 112
	var found *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_foreign_data_wrapper_oid_index) missing — Step 3bd", oid)
	}
	if found.RelName != "pg_foreign_data_wrapper_oid_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_foreign_data_wrapper_oid_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 1 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 1 (single-column oid PKEY)", oid, found.RelNatts)
	}
}
