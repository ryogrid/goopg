package initdb

import "testing"

// TestPgOpclassAmNameNspIndexSeededFromInitialEntries pins
// M0106-0010 Step 3ad's catalog seed: pg_opclass_am_name_nsp_index
// (OID 2686) must appear in pgIndexInitialEntries as UNIQUE
// (non-primary) on (opcmethod attnum=2, opcname attnum=3,
// opcnamespace attnum=4) of pg_opclass (OID 2616).
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_opclass.h:85
//	  DECLARE_UNIQUE_INDEX(pg_opclass_am_name_nsp_index, 2686,
//	    OpclassAmNameNspIndexId, pg_opclass,
//	    btree(opcmethod oid_ops, opcname name_ops, opcnamespace oid_ops));
//	  MAKE_SYSCACHE(CLAAMNAMENSP, pg_opclass_am_name_nsp_index, 8);
//
// Without this entry the populated 2-page btree at file 2679
// (pg_index_indexrelid_index) cannot include OID 2686's TID and PG's
// RelationIdGetRelation(2686) FATALs with "could not open relation
// with OID 2686" — the Step 3ad E2E failover boot blocker observed
// after Step 3ac (pg_cast_source_target_index) seeding.
func TestPgOpclassAmNameNspIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 2686
	var found *pgIndexEntry
	entries := pgIndexInitialEntries()
	for i, e := range entries {
		if e.IndexRelid == oid {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_opclass_am_name_nsp_index) missing — Step 3ad", oid)
	}
	if found.IndRelid != 2616 {
		t.Errorf("OID %d: IndRelid=%d, want 2616 (pg_opclass heap OID)", oid, found.IndRelid)
	}
	if !int16SliceEqual(found.IndKey, []int16{2, 3, 4}) {
		t.Errorf("OID %d: IndKey=%v, want [2 3 4] (opcmethod=2, opcname=3, opcnamespace=4)", oid, found.IndKey)
	}
	if !found.IsUnique {
		t.Errorf("OID %d: IsUnique=false, want true (DECLARE_UNIQUE_INDEX)", oid)
	}
	if found.IsPrimary {
		t.Errorf("OID %d: IsPrimary=true, want false (DECLARE_UNIQUE_INDEX, not _PKEY variant — PKEY is 2687)", oid)
	}
	// opcname (col 3) is a `name` column whose btree opclass uses C
	// collation (C_COLLATION_OID=950) — same convention as
	// pg_database_datname_index (2671) and pg_namespace_nspname_index (2684).
	if len(found.IndCollation) != 3 {
		t.Fatalf("OID %d: len(IndCollation)=%d, want 3", oid, len(found.IndCollation))
	}
	if found.IndCollation[0] != 0 || found.IndCollation[1] != 950 || found.IndCollation[2] != 0 {
		t.Errorf("OID %d: IndCollation=%v, want [0 950 0] (C collation only on opcname)", oid, found.IndCollation)
	}
}

// TestNailedLocalRelsContainsPgOpclassAmNameNspIndex pins the relcache
// nailed-rel seed: PG's RelationInitIndexAccessInfo relnatts/indnatts
// consistency check (relcache.c:1492) requires this entry in
// nailedLocalRels so the pg_class row's relnatts == 3 == indnatts.
func TestNailedLocalRelsContainsPgOpclassAmNameNspIndex(t *testing.T) {
	const oid uint32 = 2686
	var found *nailedRel
	for i, r := range nailedLocalRels {
		if r.OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_opclass_am_name_nsp_index) missing — Step 3ad", oid)
	}
	if found.RelName != "pg_opclass_am_name_nsp_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_opclass_am_name_nsp_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 3 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 3 (three-column composite btree)", oid, found.RelNatts)
	}
}
