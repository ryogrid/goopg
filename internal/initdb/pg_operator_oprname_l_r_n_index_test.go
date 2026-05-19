package initdb

import "testing"

// TestPgOperatorOprnameLRNIndexSeededFromInitialEntries pins
// M0106-0010 Step 3bl's catalog seed:
// pg_operator_oprname_l_r_n_index (OID 2689) must appear in
// pgIndexInitialEntries as a 4-key UNIQUE (NOT primary) btree on
// (oprname name_ops, oprleft oid_ops, oprright oid_ops,
// oprnamespace oid_ops). The PG18 heap attnums for those columns
// are {2, 8, 9, 3} per postgres/src/include/catalog/pg_operator.h
// (struct order: 1=oid, 2=oprname, 3=oprnamespace, 4=oprowner,
// 5=oprkind, 6=oprcanmerge, 7=oprcanhash, 8=oprleft, 9=oprright, …).
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_operator.h:86
//	  DECLARE_UNIQUE_INDEX(pg_operator_oprname_l_r_n_index, 2689,
//	    OperatorNameNspIndexId, pg_operator,
//	    btree(oprname name_ops, oprleft oid_ops, oprright oid_ops,
//	          oprnamespace oid_ops));
//	  MAKE_SYSCACHE(OPERNAMENSP, pg_operator_oprname_l_r_n_index, 256);
//
// Heap OID 2617 = pg_operator (already nailed local rel,
// relcache_init.go:122). Without this entry PG's
// RelationIdGetRelation(2689) FATALs with "could not open relation
// with OID 2689" — the Step 3bl boot blocker that surfaces after
// Step 3bk seeded pg_language_oid_index (2682).
func TestPgOperatorOprnameLRNIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 2689
	var found *pgIndexEntry
	entries := pgIndexInitialEntries()
	for i := range entries {
		if entries[i].IndexRelid == oid {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_operator_oprname_l_r_n_index) missing — Step 3bl", oid)
	}
	if found.IndRelid != 2617 {
		t.Errorf("OID %d: IndRelid=%d, want 2617 (pg_operator heap OID)", oid, found.IndRelid)
	}
	wantIndKey := []int16{2, 8, 9, 3}
	if !int16SliceEqual(found.IndKey, wantIndKey) {
		t.Errorf("OID %d: IndKey=%v, want %v (oprname, oprleft, oprright, oprnamespace)", oid, found.IndKey, wantIndKey)
	}
	if !found.IsUnique {
		t.Errorf("OID %d: IsUnique=false, want true (DECLARE_UNIQUE_INDEX)", oid)
	}
	if found.IsPrimary {
		t.Errorf("OID %d: IsPrimary=true, want false (not the _PKEY variant — PKEY is 2688)", oid)
	}
	if len(found.IndCollation) != 4 {
		t.Fatalf("OID %d: IndCollation len=%d, want 4", oid, len(found.IndCollation))
	}
	// Slot 0 (oprname/name_ops) carries C collation (950); the three
	// oid_ops slots carry no collation.
	if found.IndCollation[0] == 0 {
		t.Errorf("OID %d: IndCollation[0]=%d, want non-zero (name_ops uses C collation)", oid, found.IndCollation[0])
	}
	for i := 1; i < 4; i++ {
		if found.IndCollation[i] != 0 {
			t.Errorf("OID %d: IndCollation[%d]=%d, want 0 (oid_ops carries no collation)", oid, i, found.IndCollation[i])
		}
	}
}

// TestNailedLocalRelsContainsPgOperatorOprnameLRNIndex asserts the
// nailed-rel registry includes OID 2689 with RelKind='i' and
// RelNatts=4. Without this entry no pg_class row gets seeded for
// 2689 and RelationIdGetRelation(2689) FATALs; flattenRels derives
// RelNatts via pgIndexNattsByOID, which must equal pg_index.indnatts
// to satisfy RelationInitIndexAccessInfo's check at relcache.c:1492.
func TestNailedLocalRelsContainsPgOperatorOprnameLRNIndex(t *testing.T) {
	const oid uint32 = 2689
	var found *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_operator_oprname_l_r_n_index) missing — Step 3bl", oid)
	}
	if found.RelName != "pg_operator_oprname_l_r_n_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_operator_oprname_l_r_n_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 4 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 4 (oprname, oprleft, oprright, oprnamespace)", oid, found.RelNatts)
	}
}
