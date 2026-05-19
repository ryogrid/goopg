package initdb

import "testing"

// TestPgPartitionedTablePartrelidIndexSeededFromInitialEntries guards
// M0106-0010 Step 3bt. After Step 3bs seeded pg_partitioned_table
// (OID 3350), PG-standby boot's next FATAL is `could not open relation
// with OID 3351` from `RelationIdGetRelation(3351)`. The fix adds a
// `Form_pg_index` row to pgIndexInitialEntries pinning
// `(IndRelid=3350, IndKey=[1], IsUnique=true, IsPrimary=true)` per
// `postgres/src/include/catalog/pg_partitioned_table.h:69`:
//
//	DECLARE_UNIQUE_INDEX_PKEY(pg_partitioned_table_partrelid_index, 3351,
//	  PartitionedRelidIndexId, pg_partitioned_table,
//	  btree(partrelid oid_ops));
//
// pg_partitioned_table has no `oid` system column — partrelid (attnum 1)
// IS the primary key, mirroring pg_foreign_table's ftrelid (Step 3bi).
// This test rejects silent removal that would re-introduce the FATAL.
func TestPgPartitionedTablePartrelidIndexSeededFromInitialEntries(t *testing.T) {
	const idxOID = 3351
	var got *pgIndexEntry
	for i, e := range pgIndexInitialEntries() {
		if e.IndexRelid == idxOID {
			got = &pgIndexInitialEntries()[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("pgIndexInitialEntries missing OID %d (pg_partitioned_table_partrelid_index) — Step 3bt regression", idxOID)
	}
	if got.IndRelid != 3350 {
		t.Errorf("OID %d: IndRelid=%d want 3350 (pg_partitioned_table heap OID)", idxOID, got.IndRelid)
	}
	if len(got.IndKey) != 1 || got.IndKey[0] != 1 {
		t.Errorf("OID %d: IndKey=%v want [1] (partrelid attnum)", idxOID, got.IndKey)
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

// TestNailedLocalRelsContainsPgPartitionedTablePartrelidIndex guards
// M0106-0010 Step 3bt's complementary edit to relcache_init.go: without
// the nailedLocalRels entry, `bootstrapPgClassTuples` never writes a
// pg_class row for OID 3351 and PG's `RelationIdGetRelation(3351)` still
// FATALs even though the Form_pg_index row exists.
func TestNailedLocalRelsContainsPgPartitionedTablePartrelidIndex(t *testing.T) {
	const idxOID = 3351
	var got *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == idxOID {
			got = &nailedLocalRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedLocalRels missing OID %d (pg_partitioned_table_partrelid_index) — Step 3bt regression", idxOID)
	}
	if got.RelName != "pg_partitioned_table_partrelid_index" {
		t.Errorf("OID %d: RelName=%q want %q", idxOID, got.RelName, "pg_partitioned_table_partrelid_index")
	}
	if got.RelKind != 'i' {
		t.Errorf("OID %d: RelKind=%q want 'i' (index)", idxOID, got.RelKind)
	}
	if got.RelNatts != 1 {
		t.Errorf("OID %d: RelNatts=%d want 1 (single partrelid oid_ops key)", idxOID, got.RelNatts)
	}
}
