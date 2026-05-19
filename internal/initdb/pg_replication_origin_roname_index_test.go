package initdb

import "testing"

// TestPgReplicationOriginRonameIndexSeededFromInitialEntries guards
// M0106-0010 step 3ca. Companion to the PKEY 6001, this pins the
// `Form_pg_index` row for the UNIQUE (non-PKEY) name index 6002.
//
//	postgres/src/include/catalog/pg_replication_origin.h:58
//	DECLARE_UNIQUE_INDEX(pg_replication_origin_roname_index, 6002,
//	  ReplicationOriginNameIndex, pg_replication_origin,
//	  btree(roname text_ops));
//
// Without this entry RelationIdGetRelation(6002) FATALs.
func TestPgReplicationOriginRonameIndexSeededFromInitialEntries(t *testing.T) {
	const idxOID = 6002
	var got *pgIndexEntry
	for i, e := range pgIndexInitialEntries() {
		if e.IndexRelid == idxOID {
			got = &pgIndexInitialEntries()[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("pgIndexInitialEntries missing OID %d (pg_replication_origin_roname_index) — Step 3ca regression", idxOID)
	}
	if got.IndRelid != 6000 {
		t.Errorf("OID %d: IndRelid=%d want 6000 (pg_replication_origin heap OID)", idxOID, got.IndRelid)
	}
	if len(got.IndKey) != 1 || got.IndKey[0] != 2 {
		t.Errorf("OID %d: IndKey=%v want [2] (roname attnum)", idxOID, got.IndKey)
	}
	if !got.IsUnique {
		t.Errorf("OID %d: IsUnique=false want true (DECLARE_UNIQUE_INDEX)", idxOID)
	}
	if got.IsPrimary {
		t.Errorf("OID %d: IsPrimary=true want false (not _PKEY — PKEY is 6001)", idxOID)
	}
	if len(got.IndClass) != 1 || got.IndClass[0] != 3126 {
		t.Errorf("OID %d: IndClass=%v want [3126] (text_ops)", idxOID, got.IndClass)
	}
	if len(got.IndCollation) != 1 || got.IndCollation[0] != 950 {
		t.Errorf("OID %d: IndCollation=%v want [950] (C_COLLATION_OID — required for text_ops)", idxOID, got.IndCollation)
	}
}

// TestNailedSharedRelsContainsPgReplicationOriginRonameIndex guards
// M0106-0010 step 3ca's complementary edit to relcache_init.go.
func TestNailedSharedRelsContainsPgReplicationOriginRonameIndex(t *testing.T) {
	const idxOID = 6002
	var got *nailedRel
	for i := range nailedSharedRels {
		if nailedSharedRels[i].OID == idxOID {
			got = &nailedSharedRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedSharedRels missing OID %d (pg_replication_origin_roname_index) — Step 3ca regression", idxOID)
	}
	if got.RelName != "pg_replication_origin_roname_index" {
		t.Errorf("OID %d: RelName=%q want %q", idxOID, got.RelName, "pg_replication_origin_roname_index")
	}
	if got.RelKind != 'i' {
		t.Errorf("OID %d: RelKind=%q want 'i' (index)", idxOID, got.RelKind)
	}
	// RelNatts derived from pgIndexNattsByOID(6002); the index has 1 key
	// column (roname), so flattenRels sets RelNatts=1.
	if got.RelNatts != 1 {
		t.Errorf("OID %d: RelNatts=%d want 1 (single text_ops key)", idxOID, got.RelNatts)
	}
}
