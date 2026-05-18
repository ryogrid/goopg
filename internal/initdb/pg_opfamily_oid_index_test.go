package initdb

import "testing"

// TestPgOpfamilyOidIndexSeededFromInitialEntries pins M0106-0010 Step 3bo's
// catalog seed: pg_opfamily_oid_index (OID 2755) must appear in
// pgIndexInitialEntries as UNIQUE PRIMARY single-column oid_ops over
// pg_opfamily heap OID 2753.
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_opfamily.h:54
//	  DECLARE_UNIQUE_INDEX_PKEY(pg_opfamily_oid_index, 2755,
//	    OpfamilyOidIndexId, pg_opfamily, btree(oid oid_ops));
//	  MAKE_SYSCACHE(OPFAMILYOID, pg_opfamily_oid_index, 8);
//
// Heap OID 2753 = pg_opfamily (Step 3bm nailed local rel). Without this
// entry PG's RelationIdGetRelation(2755) FATALs with "could not open
// relation with OID 2755" — the Step 3bo boot blocker expected to surface
// via GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async
// after Step 3bn landed.
func TestPgOpfamilyOidIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 2755
	var found *pgIndexEntry
	entries := pgIndexInitialEntries()
	for i := range entries {
		if entries[i].IndexRelid == oid {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_opfamily_oid_index) missing — Step 3bo", oid)
	}
	if found.IndRelid != 2753 {
		t.Errorf("OID %d: IndRelid=%d, want 2753 (pg_opfamily heap OID)", oid, found.IndRelid)
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

// TestNailedLocalRelsContainsPgOpfamilyOidIndex asserts the nailed-rel
// registry includes OID 2755 with RelKind='i' and RelNatts=1. Without
// this entry no pg_class row gets seeded for 2755 and
// RelationIdGetRelation(2755) FATALs; flattenRels derives RelNatts via
// pgIndexNattsByOID, which must equal pg_index.indnatts to satisfy
// RelationInitIndexAccessInfo's check at relcache.c:1492.
func TestNailedLocalRelsContainsPgOpfamilyOidIndex(t *testing.T) {
	const oid uint32 = 2755
	var found *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_opfamily_oid_index) missing — Step 3bo", oid)
	}
	if found.RelName != "pg_opfamily_oid_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_opfamily_oid_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 1 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 1 (single oid column)", oid, found.RelNatts)
	}
}
