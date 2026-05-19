package initdb

import "testing"

// TestPgAggregateFnoidIndexSeededFromInitialEntries pins Step 3x's catalog
// seed: pg_aggregate_fnoid_index (OID 2650) must appear in
// pgIndexInitialEntries with the PG18-canonical indkey/uniqueness flags.
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_aggregate.h:113
//	  DECLARE_UNIQUE_INDEX_PKEY(pg_aggregate_fnoid_index, 2650,
//	    AggregateFnoidIndexId, pg_aggregate,
//	    btree(aggfnoid oid_ops));
//	  MAKE_SYSCACHE(AGGFNOID, pg_aggregate_fnoid_index, 16);
//
// Without this entry the populated btree built by
// bootstrapPgIndexIndexrelidIndex cannot include OID 2650's TID and PG's
// `SearchSysCache1(INDEXRELID, 2650)` (or any RelationIdGetRelation(2650)
// probe via systable_beginscan on pg_class_oid_index) FATALs with
// "could not open relation with OID 2650".
func TestPgAggregateFnoidIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 2650
	var found *pgIndexEntry
	for i, e := range pgIndexInitialEntries() {
		if e.IndexRelid == oid {
			found = &pgIndexInitialEntries()[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_aggregate_fnoid_index) missing — Step 3x", oid)
	}
	if found.IndRelid != 2600 {
		t.Errorf("OID %d: IndRelid=%d, want 2600 (pg_aggregate heap OID)", oid, found.IndRelid)
	}
	if !int16SliceEqual(found.IndKey, []int16{1}) {
		t.Errorf("OID %d: IndKey=%v, want [1] (aggfnoid attnum)", oid, found.IndKey)
	}
	if !found.IsUnique {
		t.Errorf("OID %d: IsUnique=false, want true (DECLARE_UNIQUE_INDEX_PKEY)", oid)
	}
	if !found.IsPrimary {
		t.Errorf("OID %d: IsPrimary=false, want true (DECLARE_UNIQUE_INDEX_PKEY)", oid)
	}
}

// TestNailedLocalRelsContainsPgAggregateFnoidIndex pins Step 3x's
// relcache nailed-rel seed: PG's RelationInitIndexAccessInfo
// relnatts/indnatts consistency check requires this entry in
// nailedLocalRels so its pg_class row's relnatts == 1.
func TestNailedLocalRelsContainsPgAggregateFnoidIndex(t *testing.T) {
	const oid uint32 = 2650
	var found *nailedRel
	for i, r := range nailedLocalRels {
		if r.OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_aggregate_fnoid_index) missing — Step 3x", oid)
	}
	if found.RelName != "pg_aggregate_fnoid_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_aggregate_fnoid_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 1 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 1 (single-column oid_ops key)", oid, found.RelNatts)
	}
}
