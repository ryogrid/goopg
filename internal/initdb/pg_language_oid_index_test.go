package initdb

import "testing"

// TestPgLanguageOidIndexSeededFromInitialEntries pins
// M0106-0010 Step 3bk's catalog seed:
// pg_language_oid_index (OID 2682) must appear in
// pgIndexInitialEntries as UNIQUE PRIMARY single-key on attnum
// {1} = oid with no collation.
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_language.h:70
//	  DECLARE_UNIQUE_INDEX_PKEY(pg_language_oid_index, 2682,
//	    LanguageOidIndexId, pg_language, btree(oid oid_ops));
//	  MAKE_SYSCACHE(LANGOID, pg_language_oid_index, 4);
//
// Heap OID 2612 = pg_language (already nailed local rel). Without
// this entry PG's RelationIdGetRelation(2682) FATALs with
// "could not open relation with OID 2682" — the Step 3bk boot
// blocker that surfaces after Step 3bj seeded
// pg_language_name_index (2681).
func TestPgLanguageOidIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 2682
	var found *pgIndexEntry
	entries := pgIndexInitialEntries()
	for i := range entries {
		if entries[i].IndexRelid == oid {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_language_oid_index) missing — Step 3bk", oid)
	}
	if found.IndRelid != 2612 {
		t.Errorf("OID %d: IndRelid=%d, want 2612 (pg_language heap OID)", oid, found.IndRelid)
	}
	if !int16SliceEqual(found.IndKey, []int16{1}) {
		t.Errorf("OID %d: IndKey=%v, want [1] (oid attnum)", oid, found.IndKey)
	}
	if !found.IsUnique {
		t.Errorf("OID %d: IsUnique=false, want true (DECLARE_UNIQUE_INDEX_PKEY)", oid)
	}
	if !found.IsPrimary {
		t.Errorf("OID %d: IsPrimary=false, want true (_PKEY variant)", oid)
	}
	if len(found.IndCollation) != 1 {
		t.Fatalf("OID %d: IndCollation len=%d, want 1", oid, len(found.IndCollation))
	}
	if found.IndCollation[0] != 0 {
		t.Errorf("OID %d: IndCollation[0]=%d, want 0 (oid_ops carries no collation)", oid, found.IndCollation[0])
	}
}

// TestNailedLocalRelsContainsPgLanguageOidIndex asserts the
// nailed-rel registry includes OID 2682 with RelKind='i' and
// RelNatts=1. Without this entry no pg_class row gets seeded for
// 2682 and RelationIdGetRelation(2682) FATALs; flattenRels derives
// RelNatts via pgIndexNattsByOID, which must equal pg_index.indnatts
// to satisfy RelationInitIndexAccessInfo's check at relcache.c:1492.
func TestNailedLocalRelsContainsPgLanguageOidIndex(t *testing.T) {
	const oid uint32 = 2682
	var found *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_language_oid_index) missing — Step 3bk", oid)
	}
	if found.RelName != "pg_language_oid_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_language_oid_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 1 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 1 (single-column oid UNIQUE PRIMARY)", oid, found.RelNatts)
	}
}
