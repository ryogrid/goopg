package initdb

import "testing"

// TestPgCollationOidIndexSeededFromInitialEntries pins M0106-0010 Step 3af's
// catalog seed: pg_collation_oid_index (OID 3085) must appear in
// pgIndexInitialEntries as UNIQUE PRIMARY KEY on attnum 1 (oid).
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_collation.h:63
//	  DECLARE_UNIQUE_INDEX_PKEY(pg_collation_oid_index, 3085,
//	    CollationOidIndexId, pg_collation, btree(oid oid_ops));
//
// Without this entry PG's RelationIdGetRelation(3085) FATALs with
// "could not open relation with OID 3085" — the Step 3af E2E failover
// boot blocker that surfaced after Step 3ae closed 3164.
func TestPgCollationOidIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 3085
	var found *pgIndexEntry
	entries := pgIndexInitialEntries()
	for i := range entries {
		if entries[i].IndexRelid == oid {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_collation_oid_index) missing — Step 3af", oid)
	}
	if found.IndRelid != 3456 {
		t.Errorf("OID %d: IndRelid=%d, want 3456 (pg_collation heap OID)", oid, found.IndRelid)
	}
	if !int16SliceEqual(found.IndKey, []int16{1}) {
		t.Errorf("OID %d: IndKey=%v, want [1] (oid is pg_collation attnum 1)", oid, found.IndKey)
	}
	if !found.IsUnique {
		t.Errorf("OID %d: IsUnique=false, want true (DECLARE_UNIQUE_INDEX_PKEY)", oid)
	}
	if !found.IsPrimary {
		t.Errorf("OID %d: IsPrimary=false, want true (DECLARE_UNIQUE_INDEX_PKEY)", oid)
	}
	if len(found.IndCollation) != 1 || found.IndCollation[0] != 0 {
		t.Errorf("OID %d: IndCollation=%v, want [0] (oid_ops has no collation)", oid, found.IndCollation)
	}
}

// TestNailedLocalRelsContainsPgCollationOidIndex pins the relcache
// nailed-rel seed: PG's RelationInitIndexAccessInfo relnatts/indnatts
// consistency check (relcache.c:1492) requires this entry in
// nailedLocalRels so the pg_class row's relnatts == 1 == indnatts.
func TestNailedLocalRelsContainsPgCollationOidIndex(t *testing.T) {
	const oid uint32 = 3085
	var found *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_collation_oid_index) missing — Step 3af", oid)
	}
	if found.RelName != "pg_collation_oid_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_collation_oid_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 1 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 1 (single-column oid btree)", oid, found.RelNatts)
	}
}
