package initdb

import "testing"

// TestPgExtensionOidIndexSeededFromInitialEntries pins
// M0106-0010 Step 3ax's catalog seed:
// pg_extension_oid_index (OID 3080) must appear in
// pgIndexInitialEntries as UNIQUE PRIMARY KEY single-key on
// attnum {1} = oid.
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_extension.h:56
//	  DECLARE_UNIQUE_INDEX_PKEY(pg_extension_oid_index, 3080,
//	    ExtensionOidIndexId, pg_extension,
//	    btree(oid oid_ops));
//	  MAKE_SYSCACHE(EXTENSIONOID, pg_extension_oid_index, 2);
//
// Heap OID 3079 = pg_extension (Step 3aw nailed rel). Companion
// to OID 3081 (pg_extension_name_index, UNIQUE non-PKEY; Step
// 3ay). Without this entry PG's RelationIdGetRelation(3080) FATALs
// with "could not open relation with OID 3080" — the Step 3ax boot
// blocker that surfaces after Step 3aw seeded the pg_extension heap.
func TestPgExtensionOidIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 3080
	var found *pgIndexEntry
	entries := pgIndexInitialEntries()
	for i := range entries {
		if entries[i].IndexRelid == oid {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_extension_oid_index) missing — Step 3ax", oid)
	}
	if found.IndRelid != 3079 {
		t.Errorf("OID %d: IndRelid=%d, want 3079 (pg_extension heap OID)", oid, found.IndRelid)
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

// TestNailedLocalRelsContainsPgExtensionOidIndex asserts that the
// nailed-rel registry includes OID 3080 with RelKind='i' and RelNatts=1.
// Without this entry no pg_class row gets seeded for 3080 and
// RelationIdGetRelation(3080) FATALs; flattenRels derives RelNatts via
// pgIndexNattsByOID, which must equal pg_index.indnatts to satisfy
// RelationInitIndexAccessInfo's check at relcache.c:1492.
func TestNailedLocalRelsContainsPgExtensionOidIndex(t *testing.T) {
	const oid uint32 = 3080
	var found *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_extension_oid_index) missing — Step 3ax", oid)
	}
	if found.RelName != "pg_extension_oid_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_extension_oid_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 1 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 1 (single-column oid PKEY)", oid, found.RelNatts)
	}
}
