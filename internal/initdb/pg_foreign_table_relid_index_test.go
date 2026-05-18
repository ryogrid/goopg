package initdb

import "testing"

// TestPgForeignTableRelidIndexSeededFromInitialEntries pins
// M0106-0010 Step 3bi's catalog seed:
// pg_foreign_table_relid_index (OID 3119) must appear in
// pgIndexInitialEntries as UNIQUE PRIMARY KEY single-key on attnum
// {1} = ftrelid with collation 0.
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_foreign_table.h:47
//	  DECLARE_UNIQUE_INDEX_PKEY(pg_foreign_table_relid_index, 3119,
//	    ForeignTableRelidIndexId, pg_foreign_table,
//	    btree(ftrelid oid_ops));
//	  MAKE_SYSCACHE(FOREIGNTABLEREL,
//	    pg_foreign_table_relid_index, 4);
//
// Heap OID 3118 = pg_foreign_table (Step 3bh nailed rel). Without
// this entry PG's RelationIdGetRelation(3119) FATALs with
// "could not open relation with OID 3119" — the Step 3bi boot
// blocker that surfaces after Step 3bh seeded the heap rel.
//
// Note: pg_foreign_table has no system `oid` column, but the
// primary key index points at attnum 1 = ftrelid (oid type), so
// IndKey is still {1}.
func TestPgForeignTableRelidIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 3119
	var found *pgIndexEntry
	entries := pgIndexInitialEntries()
	for i := range entries {
		if entries[i].IndexRelid == oid {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_foreign_table_relid_index) missing — Step 3bi", oid)
	}
	if found.IndRelid != 3118 {
		t.Errorf("OID %d: IndRelid=%d, want 3118 (pg_foreign_table heap OID)", oid, found.IndRelid)
	}
	if !int16SliceEqual(found.IndKey, []int16{1}) {
		t.Errorf("OID %d: IndKey=%v, want [1] (ftrelid attnum)", oid, found.IndKey)
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
		t.Errorf("OID %d: IndCollation[0]=%d, want 0 (oid_ops has no collation)", oid, found.IndCollation[0])
	}
}

// TestNailedLocalRelsContainsPgForeignTableRelidIndex asserts the
// nailed-rel registry includes OID 3119 with RelKind='i' and
// RelNatts=1. Without this entry no pg_class row gets seeded for
// 3119 and RelationIdGetRelation(3119) FATALs; flattenRels derives
// RelNatts via pgIndexNattsByOID, which must equal pg_index.indnatts
// to satisfy RelationInitIndexAccessInfo's check at relcache.c:1492.
func TestNailedLocalRelsContainsPgForeignTableRelidIndex(t *testing.T) {
	const oid uint32 = 3119
	var found *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_foreign_table_relid_index) missing — Step 3bi", oid)
	}
	if found.RelName != "pg_foreign_table_relid_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_foreign_table_relid_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 1 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 1 (single-column oid UNIQUE PKEY)", oid, found.RelNatts)
	}
}
