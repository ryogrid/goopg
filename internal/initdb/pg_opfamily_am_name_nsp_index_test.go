package initdb

import "testing"

// TestPgOpfamilyAmNameNspIndexSeededFromInitialEntries pins
// M0106-0010 Step 3bn's catalog seed:
// pg_opfamily_am_name_nsp_index (OID 2754) must appear in
// pgIndexInitialEntries as UNIQUE non-PRIMARY composite key on
// attnums {2, 3, 4} = (opfmethod, opfname, opfnamespace) with
// opclasses (oid_ops, name_ops, oid_ops) and collations
// (0, C_COLLATION_OID=950, 0).
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_opfamily.h:47
//	  DECLARE_UNIQUE_INDEX(pg_opfamily_am_name_nsp_index, 2754,
//	    OpfamilyAmNameNspIndexId, pg_opfamily,
//	    btree(opfmethod oid_ops, opfname name_ops,
//	          opfnamespace oid_ops));
//	  MAKE_SYSCACHE(OPFAMILYAMNAMENSP,
//	    pg_opfamily_am_name_nsp_index, 8);
//
// Heap OID 2753 = pg_opfamily (Step 3bm nailed local rel).
// Without this entry PG's RelationIdGetRelation(2754) FATALs with
// "could not open relation with OID 2754" — the Step 3bn boot
// blocker confirmed by GOOPG_RUN_BLOCKED_M0102_E2E=1
// TestE2E_FailoverGoopgToPG/async after Step 3bm landed.
func TestPgOpfamilyAmNameNspIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 2754
	var found *pgIndexEntry
	entries := pgIndexInitialEntries()
	for i := range entries {
		if entries[i].IndexRelid == oid {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_opfamily_am_name_nsp_index) missing — Step 3bn", oid)
	}
	if found.IndRelid != 2753 {
		t.Errorf("OID %d: IndRelid=%d, want 2753 (pg_opfamily heap OID)", oid, found.IndRelid)
	}
	if !int16SliceEqual(found.IndKey, []int16{2, 3, 4}) {
		t.Errorf("OID %d: IndKey=%v, want [2 3 4] (opfmethod, opfname, opfnamespace)", oid, found.IndKey)
	}
	if !found.IsUnique {
		t.Errorf("OID %d: IsUnique=false, want true (DECLARE_UNIQUE_INDEX)", oid)
	}
	if found.IsPrimary {
		t.Errorf("OID %d: IsPrimary=true, want false (DECLARE_UNIQUE_INDEX is not the _PKEY variant; PKEY is 2755)", oid)
	}
	if len(found.IndCollation) != 3 {
		t.Fatalf("OID %d: IndCollation len=%d, want 3", oid, len(found.IndCollation))
	}
	wantColl := []uint32{0, 950, 0} // oid_ops, name_ops C-collation, oid_ops
	for i, w := range wantColl {
		if found.IndCollation[i] != w {
			t.Errorf("OID %d: IndCollation[%d]=%d, want %d", oid, i, found.IndCollation[i], w)
		}
	}
}

// TestNailedLocalRelsContainsPgOpfamilyAmNameNspIndex asserts the
// nailed-rel registry includes OID 2754 with RelKind='i' and
// RelNatts=3. Without this entry no pg_class row gets seeded for
// 2754 and RelationIdGetRelation(2754) FATALs; flattenRels derives
// RelNatts via pgIndexNattsByOID, which must equal pg_index.indnatts
// to satisfy RelationInitIndexAccessInfo's check at relcache.c:1492.
func TestNailedLocalRelsContainsPgOpfamilyAmNameNspIndex(t *testing.T) {
	const oid uint32 = 2754
	var found *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_opfamily_am_name_nsp_index) missing — Step 3bn", oid)
	}
	if found.RelName != "pg_opfamily_am_name_nsp_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_opfamily_am_name_nsp_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 3 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 3 (3-column composite)", oid, found.RelNatts)
	}
}
