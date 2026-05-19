package initdb

import "testing"

// TestPgCastSourceTargetIndexSeededFromInitialEntries pins
// M0106-0010 Step 3ac's catalog seed: pg_cast_source_target_index
// (OID 2661) must appear in pgIndexInitialEntries as UNIQUE
// (non-primary) on (castsource attnum=2, casttarget attnum=3).
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_cast.h:60
//	  DECLARE_UNIQUE_INDEX(pg_cast_source_target_index, 2661,
//	    CastSourceTargetIndexId, pg_cast,
//	    btree(castsource oid_ops, casttarget oid_ops));
//
// Without this entry the populated 2-page btree at file 2679
// (pg_index_indexrelid_index) cannot include OID 2661's TID and PG's
// RelationIdGetRelation(2661) FATALs with "could not open relation with
// OID 2661" — the Step 3ac E2E failover boot blocker.
func TestPgCastSourceTargetIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 2661
	var found *pgIndexEntry
	for i, e := range pgIndexInitialEntries() {
		if e.IndexRelid == oid {
			found = &pgIndexInitialEntries()[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_cast_source_target_index) missing — Step 3ac", oid)
	}
	if found.IndRelid != 2605 {
		t.Errorf("OID %d: IndRelid=%d, want 2605 (pg_cast heap OID)", oid, found.IndRelid)
	}
	if !int16SliceEqual(found.IndKey, []int16{2, 3}) {
		t.Errorf("OID %d: IndKey=%v, want [2 3] (castsource=2, casttarget=3)", oid, found.IndKey)
	}
	if !found.IsUnique {
		t.Errorf("OID %d: IsUnique=false, want true (DECLARE_UNIQUE_INDEX)", oid)
	}
	if found.IsPrimary {
		t.Errorf("OID %d: IsPrimary=true, want false (DECLARE_UNIQUE_INDEX, not _PKEY variant)", oid)
	}
}

// TestNailedLocalRelsContainsPgCastSourceTargetIndex pins the relcache
// nailed-rel seed: PG's RelationInitIndexAccessInfo relnatts/indnatts
// consistency check (relcache.c:1492) requires this entry in
// nailedLocalRels so the pg_class row's relnatts == 2 == indnatts.
func TestNailedLocalRelsContainsPgCastSourceTargetIndex(t *testing.T) {
	const oid uint32 = 2661
	var found *nailedRel
	for i, r := range nailedLocalRels {
		if r.OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_cast_source_target_index) missing — Step 3ac", oid)
	}
	if found.RelName != "pg_cast_source_target_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_cast_source_target_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 2 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 2 (two-column composite btree)", oid, found.RelNatts)
	}
}
