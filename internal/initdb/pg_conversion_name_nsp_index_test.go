package initdb

import "testing"

// TestPgConversionNameNspIndexSeededFromInitialEntries pins M0106-0010
// Step 3aj's catalog seed: pg_conversion_name_nsp_index (OID 2669) must
// appear in pgIndexInitialEntries as UNIQUE (non-PRIMARY) on attnums
// {2, 3} = (conname, connamespace).
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_conversion.h:64
//	  DECLARE_UNIQUE_INDEX(pg_conversion_name_nsp_index, 2669,
//	    ConversionNameNspIndexId, pg_conversion,
//	    btree(conname name_ops, connamespace oid_ops));
//
// Companion to 2668 (pg_conversion_default_index, composite UNIQUE
// non-PKEY seeded by Step 3ah) and 2670 (pg_conversion_oid_index,
// UNIQUE PRIMARY seeded by Step 3ai) — closing out the last
// pg_conversion companion index. Without this entry PG's
// RelationIdGetRelation(2669) FATALs with "could not open relation
// with OID 2669" — the Step 3aj E2E failover boot blocker that
// surfaced after Step 3ai seeded pg_conversion_oid_index.
func TestPgConversionNameNspIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 2669
	var found *pgIndexEntry
	entries := pgIndexInitialEntries()
	for i := range entries {
		if entries[i].IndexRelid == oid {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_conversion_name_nsp_index) missing — Step 3aj", oid)
	}
	if found.IndRelid != 2607 {
		t.Errorf("OID %d: IndRelid=%d, want 2607 (pg_conversion heap OID)", oid, found.IndRelid)
	}
	if !int16SliceEqual(found.IndKey, []int16{2, 3}) {
		t.Errorf("OID %d: IndKey=%v, want [2 3] (conname, connamespace)", oid, found.IndKey)
	}
	if !found.IsUnique {
		t.Errorf("OID %d: IsUnique=false, want true (DECLARE_UNIQUE_INDEX)", oid)
	}
	if found.IsPrimary {
		t.Errorf("OID %d: IsPrimary=true, want false (DECLARE_UNIQUE_INDEX is non-PKEY)", oid)
	}
	if len(found.IndCollation) != 2 {
		t.Fatalf("OID %d: IndCollation len=%d, want 2", oid, len(found.IndCollation))
	}
	// conname is a `name` type; its btree opclass `name_ops` uses C
	// collation (C_COLLATION_OID = 950).
	const cCollation uint32 = 950
	if found.IndCollation[0] != cCollation {
		t.Errorf("OID %d: IndCollation[0]=%d, want %d (name_ops C collation)", oid, found.IndCollation[0], cCollation)
	}
	if found.IndCollation[1] != 0 {
		t.Errorf("OID %d: IndCollation[1]=%d, want 0 (oid_ops carries no collation)", oid, found.IndCollation[1])
	}
}

// TestNailedLocalRelsContainsPgConversionNameNspIndex asserts that the
// nailed-rel registry includes OID 2669 with RelKind='i' and RelNatts=2.
// Without this entry no pg_class row gets seeded for 2669 and
// RelationIdGetRelation(2669) FATALs; flattenRels derives RelNatts via
// pgIndexNattsByOID, which must equal pg_index.indnatts to satisfy
// RelationInitIndexAccessInfo's check at relcache.c:1492.
func TestNailedLocalRelsContainsPgConversionNameNspIndex(t *testing.T) {
	const oid uint32 = 2669
	var found *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_conversion_name_nsp_index) missing — Step 3aj", oid)
	}
	if found.RelName != "pg_conversion_name_nsp_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_conversion_name_nsp_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 2 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 2 (conname, connamespace)", oid, found.RelNatts)
	}
}
