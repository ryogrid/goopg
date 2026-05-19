package initdb

import "testing"

// TestPgConversionDefaultIndexSeededFromInitialEntries pins M0106-0010
// Step 3ah's catalog seed: pg_conversion_default_index (OID 2668) must
// appear in pgIndexInitialEntries as UNIQUE (not PRIMARY) on attnums
// {3, 5, 6, 1} (connamespace, conforencoding, contoencoding, oid).
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_conversion.h:63
//	  DECLARE_UNIQUE_INDEX(pg_conversion_default_index, 2668,
//	    ConversionDefaultIndexId, pg_conversion,
//	    btree(connamespace oid_ops, conforencoding int4_ops,
//	          contoencoding int4_ops, oid oid_ops));
//	  MAKE_SYSCACHE(CONDEFAULT, pg_conversion_default_index, 8);
//
// Without this entry PG's RelationIdGetRelation(2668) FATALs with
// "could not open relation with OID 2668" — the Step 3ah E2E failover
// boot blocker that surfaced after Step 3ag seeded the pg_conversion
// heap (OID 2607).
func TestPgConversionDefaultIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 2668
	var found *pgIndexEntry
	entries := pgIndexInitialEntries()
	for i := range entries {
		if entries[i].IndexRelid == oid {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_conversion_default_index) missing — Step 3ah", oid)
	}
	if found.IndRelid != 2607 {
		t.Errorf("OID %d: IndRelid=%d, want 2607 (pg_conversion heap OID)", oid, found.IndRelid)
	}
	if !int16SliceEqual(found.IndKey, []int16{3, 5, 6, 1}) {
		t.Errorf("OID %d: IndKey=%v, want [3 5 6 1] (connamespace, conforencoding, contoencoding, oid)", oid, found.IndKey)
	}
	if !found.IsUnique {
		t.Errorf("OID %d: IsUnique=false, want true (DECLARE_UNIQUE_INDEX)", oid)
	}
	if found.IsPrimary {
		t.Errorf("OID %d: IsPrimary=true, want false (DECLARE_UNIQUE_INDEX, not _PKEY variant — PKEY is 2670)", oid)
	}
	if len(found.IndCollation) != 4 {
		t.Fatalf("OID %d: IndCollation len=%d, want 4", oid, len(found.IndCollation))
	}
	for i, c := range found.IndCollation {
		if c != 0 {
			t.Errorf("OID %d: IndCollation[%d]=%d, want 0 (oid_ops/int4_ops carry no collation)", oid, i, c)
		}
	}
}

// TestNailedLocalRelsContainsPgConversionDefaultIndex pins the relcache
// nailed-rel seed: PG's RelationInitIndexAccessInfo relnatts/indnatts
// consistency check (relcache.c:1492) requires this entry in
// nailedLocalRels so the pg_class row's relnatts == 4 == indnatts.
func TestNailedLocalRelsContainsPgConversionDefaultIndex(t *testing.T) {
	const oid uint32 = 2668
	var found *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_conversion_default_index) missing — Step 3ah", oid)
	}
	if found.RelName != "pg_conversion_default_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_conversion_default_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 4 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 4 (composite UNIQUE on 4 columns)", oid, found.RelNatts)
	}
}
