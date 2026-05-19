package initdb

import "testing"

// TestPgCollationNameEncNspIndexSeededFromInitialEntries pins
// M0106-0010 Step 3ae's catalog seed: pg_collation_name_enc_nsp_index
// (OID 3164) must appear in pgIndexInitialEntries as UNIQUE
// (non-primary) on (collname attnum=2, collencoding attnum=7,
// collnamespace attnum=3) of pg_collation (OID 3456).
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_collation.h:62
//	  DECLARE_UNIQUE_INDEX(pg_collation_name_enc_nsp_index, 3164,
//	    CollationNameEncNspIndexId, pg_collation,
//	    btree(collname name_ops, collencoding int4_ops, collnamespace oid_ops));
//	  MAKE_SYSCACHE(COLLNAMEENCNSP, pg_collation_name_enc_nsp_index, 8);
//
// Without this entry the populated 2-page btree at file 2679
// (pg_index_indexrelid_index) cannot include OID 3164's TID and PG's
// RelationIdGetRelation(3164) FATALs with "could not open relation
// with OID 3164" — the Step 3ae E2E failover boot blocker observed
// after Step 3ad (pg_opclass_am_name_nsp_index) seeding.
func TestPgCollationNameEncNspIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 3164
	var found *pgIndexEntry
	entries := pgIndexInitialEntries()
	for i, e := range entries {
		if e.IndexRelid == oid {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_collation_name_enc_nsp_index) missing — Step 3ae", oid)
	}
	if found.IndRelid != 3456 {
		t.Errorf("OID %d: IndRelid=%d, want 3456 (pg_collation heap OID)", oid, found.IndRelid)
	}
	if !int16SliceEqual(found.IndKey, []int16{2, 7, 3}) {
		t.Errorf("OID %d: IndKey=%v, want [2 7 3] (collname=2, collencoding=7, collnamespace=3)", oid, found.IndKey)
	}
	if !found.IsUnique {
		t.Errorf("OID %d: IsUnique=false, want true (DECLARE_UNIQUE_INDEX)", oid)
	}
	if found.IsPrimary {
		t.Errorf("OID %d: IsPrimary=true, want false (DECLARE_UNIQUE_INDEX, not _PKEY variant — PKEY is 3085)", oid)
	}
	// collname (col 2) is a `name` column whose btree opclass uses C
	// collation (C_COLLATION_OID=950) — same convention as
	// pg_database_datname_index (2671), pg_namespace_nspname_index (2684),
	// and pg_opclass_am_name_nsp_index (2686).
	if len(found.IndCollation) != 3 {
		t.Fatalf("OID %d: len(IndCollation)=%d, want 3", oid, len(found.IndCollation))
	}
	if found.IndCollation[0] != 950 || found.IndCollation[1] != 0 || found.IndCollation[2] != 0 {
		t.Errorf("OID %d: IndCollation=%v, want [950 0 0] (C collation only on collname)", oid, found.IndCollation)
	}
}

// TestNailedLocalRelsContainsPgCollationNameEncNspIndex pins the relcache
// nailed-rel seed: PG's RelationInitIndexAccessInfo relnatts/indnatts
// consistency check (relcache.c:1492) requires this entry in
// nailedLocalRels so the pg_class row's relnatts == 3 == indnatts.
func TestNailedLocalRelsContainsPgCollationNameEncNspIndex(t *testing.T) {
	const oid uint32 = 3164
	var found *nailedRel
	for i, r := range nailedLocalRels {
		if r.OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_collation_name_enc_nsp_index) missing — Step 3ae", oid)
	}
	if found.RelName != "pg_collation_name_enc_nsp_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_collation_name_enc_nsp_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 3 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 3 (three-column composite btree)", oid, found.RelNatts)
	}
}
