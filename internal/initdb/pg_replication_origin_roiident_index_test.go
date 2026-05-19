package initdb

import "testing"

// TestPgReplicationOriginRoiidentIndexSeededFromInitialEntries guards
// M0106-0010 step 3ca. After Step 3ca seeded pg_replication_origin (OID 6000)
// as a nailed shared rel, PG-standby boot's index lookups require a
// `Form_pg_index` row for the PKEY 6001. The fix adds an entry pinning
// `(IndRelid=6000, IndKey=[1], IsUnique=true, IsPrimary=true)` per
// `postgres/src/include/catalog/pg_replication_origin.h:57`:
//
//	DECLARE_UNIQUE_INDEX_PKEY(pg_replication_origin_roiident_index, 6001,
//	  ReplicationOriginIdentIndex, pg_replication_origin,
//	  btree(roident oid_ops));
//
// This test rejects silent removal that would re-introduce the FATAL.
func TestPgReplicationOriginRoiidentIndexSeededFromInitialEntries(t *testing.T) {
	const idxOID = 6001
	var got *pgIndexEntry
	for i, e := range pgIndexInitialEntries() {
		if e.IndexRelid == idxOID {
			got = &pgIndexInitialEntries()[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("pgIndexInitialEntries missing OID %d (pg_replication_origin_roiident_index) — Step 3ca regression", idxOID)
	}
	if got.IndRelid != 6000 {
		t.Errorf("OID %d: IndRelid=%d want 6000 (pg_replication_origin heap OID)", idxOID, got.IndRelid)
	}
	if len(got.IndKey) != 1 || got.IndKey[0] != 1 {
		t.Errorf("OID %d: IndKey=%v want [1] (roident attnum)", idxOID, got.IndKey)
	}
	if !got.IsUnique {
		t.Errorf("OID %d: IsUnique=false want true (DECLARE_UNIQUE_INDEX_PKEY)", idxOID)
	}
	if !got.IsPrimary {
		t.Errorf("OID %d: IsPrimary=false want true (_PKEY variant)", idxOID)
	}
	if len(got.IndClass) != 1 || got.IndClass[0] != 1981 {
		t.Errorf("OID %d: IndClass=%v want [1981] (oid_ops)", idxOID, got.IndClass)
	}
	if len(got.IndCollation) != 1 || got.IndCollation[0] != 0 {
		t.Errorf("OID %d: IndCollation=%v want [0] (oid_ops carries no collation)", idxOID, got.IndCollation)
	}
}

// TestNailedSharedRelsContainsPgReplicationOriginRoiidentIndex guards
// M0106-0010 step 3ca's complementary edit to relcache_init.go: without
// the nailedSharedRels entry, `bootstrapPgClassTuples` never writes a
// pg_class row for OID 6001 and PG's `RelationIdGetRelation(6001)` still
// FATALs even though the Form_pg_index row exists.
func TestNailedSharedRelsContainsPgReplicationOriginRoiidentIndex(t *testing.T) {
	const idxOID = 6001
	var got *nailedRel
	for i := range nailedSharedRels {
		if nailedSharedRels[i].OID == idxOID {
			got = &nailedSharedRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedSharedRels missing OID %d (pg_replication_origin_roiident_index) — Step 3ca regression", idxOID)
	}
	if got.RelName != "pg_replication_origin_roiident_index" {
		t.Errorf("OID %d: RelName=%q want %q", idxOID, got.RelName, "pg_replication_origin_roiident_index")
	}
	if got.RelKind != 'i' {
		t.Errorf("OID %d: RelKind=%q want 'i' (index)", idxOID, got.RelKind)
	}
	// RelNatts derived from pgIndexNattsByOID(6001); the index has 1 key
	// column (roident), so flattenRels sets RelNatts=1.
	if got.RelNatts != 1 {
		t.Errorf("OID %d: RelNatts=%d want 1 (single oid_ops key)", idxOID, got.RelNatts)
	}
}
