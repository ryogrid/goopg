package initdb

import "testing"

// TestPgForeignDataWrapperNameIndexSeededFromInitialEntries pins
// M0106-0010 Step 3bc's catalog seed:
// pg_foreign_data_wrapper_name_index (OID 548) must appear in
// pgIndexInitialEntries as UNIQUE (non-PKEY) single-key on attnum
// {2} = fdwname with C_COLLATION_OID (950).
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_foreign_data_wrapper.h:56
//	  DECLARE_UNIQUE_INDEX(pg_foreign_data_wrapper_name_index, 548,
//	    ForeignDataWrapperNameIndexId, pg_foreign_data_wrapper,
//	    btree(fdwname name_ops));
//	  MAKE_SYSCACHE(FOREIGNDATAWRAPPERNAME,
//	    pg_foreign_data_wrapper_name_index, 2);
//
// Heap OID 2328 = pg_foreign_data_wrapper (Step 3bb nailed rel).
// Companion to OID 112 (pg_foreign_data_wrapper_oid_index,
// UNIQUE PKEY) — deferred until a concrete E2E blocker surfaces
// (only 548 has FATAL'd in TestE2E_FailoverGoopgToPG/async so far).
// Without this entry PG's RelationIdGetRelation(548) FATALs with
// "could not open relation with OID 548" — the Step 3bc boot
// blocker that surfaces after Step 3bb seeded pg_foreign_data_wrapper.
func TestPgForeignDataWrapperNameIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 548
	var found *pgIndexEntry
	entries := pgIndexInitialEntries()
	for i := range entries {
		if entries[i].IndexRelid == oid {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_foreign_data_wrapper_name_index) missing — Step 3bc", oid)
	}
	if found.IndRelid != 2328 {
		t.Errorf("OID %d: IndRelid=%d, want 2328 (pg_foreign_data_wrapper heap OID)", oid, found.IndRelid)
	}
	if !int16SliceEqual(found.IndKey, []int16{2}) {
		t.Errorf("OID %d: IndKey=%v, want [2] (fdwname attnum)", oid, found.IndKey)
	}
	if !found.IsUnique {
		t.Errorf("OID %d: IsUnique=false, want true (DECLARE_UNIQUE_INDEX)", oid)
	}
	if found.IsPrimary {
		t.Errorf("OID %d: IsPrimary=true, want false (DECLARE_UNIQUE_INDEX, not DECLARE_UNIQUE_INDEX_PKEY)", oid)
	}
	if len(found.IndCollation) != 1 {
		t.Fatalf("OID %d: IndCollation len=%d, want 1", oid, len(found.IndCollation))
	}
	if found.IndCollation[0] != 950 {
		t.Errorf("OID %d: IndCollation[0]=%d, want 950 (C_COLLATION_OID for name_ops)", oid, found.IndCollation[0])
	}
}

// TestNailedLocalRelsContainsPgForeignDataWrapperNameIndex asserts the
// nailed-rel registry includes OID 548 with RelKind='i' and RelNatts=1.
// Without this entry no pg_class row gets seeded for 548 and
// RelationIdGetRelation(548) FATALs; flattenRels derives RelNatts via
// pgIndexNattsByOID, which must equal pg_index.indnatts to satisfy
// RelationInitIndexAccessInfo's check at relcache.c:1492.
func TestNailedLocalRelsContainsPgForeignDataWrapperNameIndex(t *testing.T) {
	const oid uint32 = 548
	var found *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_foreign_data_wrapper_name_index) missing — Step 3bc", oid)
	}
	if found.RelName != "pg_foreign_data_wrapper_name_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_foreign_data_wrapper_name_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 1 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 1 (single-column name UNIQUE)", oid, found.RelNatts)
	}
}
