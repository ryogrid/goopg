package initdb

import "testing"

// TestPgCastOidIndexSeededFromInitialEntries pins M0106-0010 Step 3ab's
// catalog seed: pg_cast_oid_index (OID 2660) must appear in
// pgIndexInitialEntries as UNIQUE PRIMARY KEY on attnum 1 (oid).
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_cast.h:59
//	  DECLARE_UNIQUE_INDEX_PKEY(pg_cast_oid_index, 2660,
//	    CastOidIndexId, pg_cast, btree(oid oid_ops));
//
// Without this entry the populated 2-page btree at file 2679
// (pg_index_indexrelid_index) cannot include OID 2660's TID and PG's
// RelationIdGetRelation(2660) FATALs with "could not open relation with
// OID 2660" — the Step 3ab E2E failover boot blocker.
func TestPgCastOidIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 2660
	var found *pgIndexEntry
	for i, e := range pgIndexInitialEntries() {
		if e.IndexRelid == oid {
			found = &pgIndexInitialEntries()[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_cast_oid_index) missing — Step 3ab", oid)
	}
	if found.IndRelid != 2605 {
		t.Errorf("OID %d: IndRelid=%d, want 2605 (pg_cast heap OID)", oid, found.IndRelid)
	}
	if !int16SliceEqual(found.IndKey, []int16{1}) {
		t.Errorf("OID %d: IndKey=%v, want [1] (oid is pg_cast attnum 1)", oid, found.IndKey)
	}
	if !found.IsUnique {
		t.Errorf("OID %d: IsUnique=false, want true (DECLARE_UNIQUE_INDEX_PKEY)", oid)
	}
	if !found.IsPrimary {
		t.Errorf("OID %d: IsPrimary=false, want true (DECLARE_UNIQUE_INDEX_PKEY)", oid)
	}
}

// TestNailedLocalRelsContainsPgCastOidIndex pins the relcache nailed-rel
// seed: PG's RelationInitIndexAccessInfo relnatts/indnatts consistency
// check (relcache.c:1492) requires this entry in nailedLocalRels so the
// pg_class row's relnatts == 1 == indnatts.
func TestNailedLocalRelsContainsPgCastOidIndex(t *testing.T) {
	const oid uint32 = 2660
	var found *nailedRel
	for i, r := range nailedLocalRels {
		if r.OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_cast_oid_index) missing — Step 3ab", oid)
	}
	if found.RelName != "pg_cast_oid_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_cast_oid_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 1 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 1 (single-column oid btree)", oid, found.RelNatts)
	}
}
