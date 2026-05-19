package initdb

import "testing"

// TestPgLanguageNameIndexSeededFromInitialEntries pins
// M0106-0010 Step 3bj's catalog seed:
// pg_language_name_index (OID 2681) must appear in
// pgIndexInitialEntries as UNIQUE (NOT primary) single-key on attnum
// {2} = lanname with C collation (950).
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_language.h:69
//	  DECLARE_UNIQUE_INDEX(pg_language_name_index, 2681,
//	    LanguageNameIndexId, pg_language,
//	    btree(lanname name_ops));
//	  MAKE_SYSCACHE(LANGNAME, pg_language_name_index, 4);
//
// Heap OID 2612 = pg_language (already nailed local rel). Without
// this entry PG's RelationIdGetRelation(2681) FATALs with
// "could not open relation with OID 2681" — the Step 3bj boot
// blocker that surfaces after Step 3bi seeded
// pg_foreign_table_relid_index (3119). DECLARE_UNIQUE_INDEX is
// not the _PKEY variant; pg_language's PKEY is OID 2682
// (pg_language_oid_index).
func TestPgLanguageNameIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 2681
	var found *pgIndexEntry
	entries := pgIndexInitialEntries()
	for i := range entries {
		if entries[i].IndexRelid == oid {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_language_name_index) missing — Step 3bj", oid)
	}
	if found.IndRelid != 2612 {
		t.Errorf("OID %d: IndRelid=%d, want 2612 (pg_language heap OID)", oid, found.IndRelid)
	}
	if !int16SliceEqual(found.IndKey, []int16{2}) {
		t.Errorf("OID %d: IndKey=%v, want [2] (lanname attnum)", oid, found.IndKey)
	}
	if !found.IsUnique {
		t.Errorf("OID %d: IsUnique=false, want true (DECLARE_UNIQUE_INDEX)", oid)
	}
	if found.IsPrimary {
		t.Errorf("OID %d: IsPrimary=true, want false (not _PKEY — pg_language PKEY is 2682)", oid)
	}
	if len(found.IndCollation) != 1 {
		t.Fatalf("OID %d: IndCollation len=%d, want 1", oid, len(found.IndCollation))
	}
	if found.IndCollation[0] != 950 {
		t.Errorf("OID %d: IndCollation[0]=%d, want 950 (C_COLLATION_OID for name_ops)", oid, found.IndCollation[0])
	}
}

// TestNailedLocalRelsContainsPgLanguageNameIndex asserts the
// nailed-rel registry includes OID 2681 with RelKind='i' and
// RelNatts=1. Without this entry no pg_class row gets seeded for
// 2681 and RelationIdGetRelation(2681) FATALs; flattenRels derives
// RelNatts via pgIndexNattsByOID, which must equal pg_index.indnatts
// to satisfy RelationInitIndexAccessInfo's check at relcache.c:1492.
func TestNailedLocalRelsContainsPgLanguageNameIndex(t *testing.T) {
	const oid uint32 = 2681
	var found *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_language_name_index) missing — Step 3bj", oid)
	}
	if found.RelName != "pg_language_name_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_language_name_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 1 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 1 (single-column name UNIQUE)", oid, found.RelNatts)
	}
}
