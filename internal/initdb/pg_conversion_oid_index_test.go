package initdb

import "testing"

// TestPgConversionOidIndexSeededFromInitialEntries pins M0106-0010
// Step 3ai's catalog seed: pg_conversion_oid_index (OID 2670) must
// appear in pgIndexInitialEntries as UNIQUE PRIMARY on attnum {1} (oid).
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_conversion.h:65
//	  DECLARE_UNIQUE_INDEX_PKEY(pg_conversion_oid_index, 2670,
//	    ConversionOidIndexId, pg_conversion, btree(oid oid_ops));
//
// Companion to 2668 (pg_conversion_default_index, composite UNIQUE
// non-PKEY seeded by Step 3ah) and 2669 (pg_conversion_name_nsp_index,
// composite UNIQUE non-PKEY, deferred to Step 3aj). Without this entry
// PG's RelationIdGetRelation(2670) FATALs with "could not open relation
// with OID 2670" — the Step 3ai E2E failover boot blocker that
// surfaced after Step 3ah seeded pg_conversion_default_index.
func TestPgConversionOidIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 2670
	var found *pgIndexEntry
	entries := pgIndexInitialEntries()
	for i := range entries {
		if entries[i].IndexRelid == oid {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_conversion_oid_index) missing — Step 3ai", oid)
	}
	if found.IndRelid != 2607 {
		t.Errorf("OID %d: IndRelid=%d, want 2607 (pg_conversion heap OID)", oid, found.IndRelid)
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

// TestNailedLocalRelsContainsPgConversionOidIndex pins the relcache
// nailed-rel seed: PG's RelationInitIndexAccessInfo relnatts/indnatts
// consistency check (relcache.c:1492) requires this entry in
// nailedLocalRels so the pg_class row's relnatts == 1 == indnatts.
func TestNailedLocalRelsContainsPgConversionOidIndex(t *testing.T) {
	const oid uint32 = 2670
	var found *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_conversion_oid_index) missing — Step 3ai", oid)
	}
	if found.RelName != "pg_conversion_oid_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_conversion_oid_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 1 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 1 (single-column oid PKEY)", oid, found.RelNatts)
	}
}
