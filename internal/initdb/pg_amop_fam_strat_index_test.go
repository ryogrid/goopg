package initdb

import "testing"

// TestPgAmopFamStratIndexSeededFromInitialEntries pins Step 3y's catalog
// seed: pg_amop_fam_strat_index (OID 2653) must appear in
// pgIndexInitialEntries with the PG18-canonical 4-column indkey and
// uniqueness flags.
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_amop.h:90
//	  DECLARE_UNIQUE_INDEX(pg_amop_fam_strat_index, 2653,
//	    AccessMethodStrategyIndexId, pg_amop,
//	    btree(amopfamily oid_ops, amoplefttype oid_ops,
//	          amoprighttype oid_ops, amopstrategy int2_ops));
//	  MAKE_SYSCACHE(AMOPSTRATEGY, pg_amop_fam_strat_index, 64);
//
// pg_amop column attnums (postgres/src/include/catalog/pg_amop_d.h):
//
//	1=oid, 2=amopfamily, 3=amoplefttype, 4=amoprighttype, 5=amopstrategy
//
// Without this entry the populated btree built by
// bootstrapPgIndexIndexrelidIndex cannot include OID 2653's TID and PG's
// RelationIdGetRelation(2653) FATALs with "could not open relation with
// OID 2653" — the Step 3y E2E failover boot blocker.
func TestPgAmopFamStratIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 2653
	var found *pgIndexEntry
	for i, e := range pgIndexInitialEntries() {
		if e.IndexRelid == oid {
			found = &pgIndexInitialEntries()[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_amop_fam_strat_index) missing — Step 3y", oid)
	}
	if found.IndRelid != 2602 {
		t.Errorf("OID %d: IndRelid=%d, want 2602 (pg_amop heap OID)", oid, found.IndRelid)
	}
	if !int16SliceEqual(found.IndKey, []int16{2, 3, 4, 5}) {
		t.Errorf("OID %d: IndKey=%v, want [2 3 4 5] (amopfamily, amoplefttype, amoprighttype, amopstrategy)", oid, found.IndKey)
	}
	if !found.IsUnique {
		t.Errorf("OID %d: IsUnique=false, want true (DECLARE_UNIQUE_INDEX)", oid)
	}
	if found.IsPrimary {
		t.Errorf("OID %d: IsPrimary=true, want false (DECLARE_UNIQUE_INDEX is NOT _PKEY)", oid)
	}
}

// TestNailedLocalRelsContainsPgAmopFamStratIndex pins Step 3y's
// relcache nailed-rel seed: PG's RelationInitIndexAccessInfo
// relnatts/indnatts consistency check (relcache.c:1492) requires this
// entry in nailedLocalRels so the pg_class row's relnatts == 4.
func TestNailedLocalRelsContainsPgAmopFamStratIndex(t *testing.T) {
	const oid uint32 = 2653
	var found *nailedRel
	for i, r := range nailedLocalRels {
		if r.OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_amop_fam_strat_index) missing — Step 3y", oid)
	}
	if found.RelName != "pg_amop_fam_strat_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_amop_fam_strat_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 4 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 4 (4-column composite btree)", oid, found.RelNatts)
	}
}
