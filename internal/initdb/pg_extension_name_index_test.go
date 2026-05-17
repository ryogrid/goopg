package initdb

import "testing"

// TestPgExtensionNameIndexSeededFromInitialEntries pins
// M0106-0010 Step 3ay's catalog seed:
// pg_extension_name_index (OID 3081) must appear in
// pgIndexInitialEntries as UNIQUE (non-PKEY) single-key on
// attnum {2} = extname with C_COLLATION_OID (950).
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_extension.h:57
//	  DECLARE_UNIQUE_INDEX(pg_extension_name_index, 3081,
//	    ExtensionNameIndexId, pg_extension,
//	    btree(extname name_ops));
//	  MAKE_SYSCACHE(EXTENSIONNAME, pg_extension_name_index, 2);
//
// Heap OID 3079 = pg_extension (Step 3aw nailed rel). Companion
// to OID 3080 (pg_extension_oid_index, UNIQUE PKEY; Step 3ax).
// Without this entry PG's RelationIdGetRelation(3081) FATALs
// with "could not open relation with OID 3081" — the Step 3ay boot
// blocker that surfaces after Step 3ax seeded the companion oid index.
func TestPgExtensionNameIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 3081
	var found *pgIndexEntry
	entries := pgIndexInitialEntries()
	for i := range entries {
		if entries[i].IndexRelid == oid {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_extension_name_index) missing — Step 3ay", oid)
	}
	if found.IndRelid != 3079 {
		t.Errorf("OID %d: IndRelid=%d, want 3079 (pg_extension heap OID)", oid, found.IndRelid)
	}
	if !int16SliceEqual(found.IndKey, []int16{2}) {
		t.Errorf("OID %d: IndKey=%v, want [2] (extname attnum)", oid, found.IndKey)
	}
	if !found.IsUnique {
		t.Errorf("OID %d: IsUnique=false, want true (DECLARE_UNIQUE_INDEX)", oid)
	}
	if found.IsPrimary {
		t.Errorf("OID %d: IsPrimary=true, want false (DECLARE_UNIQUE_INDEX, not DECLARE_UNIQUE_INDEX_PKEY)", oid)
	}
	if len(found.IndCollation) != 1 {
		t.Fatalf("OID %d: IndCollation len=%d, want 1", oid, len(found.IndCollation))
	}
	if found.IndCollation[0] != 950 {
		t.Errorf("OID %d: IndCollation[0]=%d, want 950 (C_COLLATION_OID for name_ops)", oid, found.IndCollation[0])
	}
}

// TestNailedLocalRelsContainsPgExtensionNameIndex asserts that the
// nailed-rel registry includes OID 3081 with RelKind='i' and RelNatts=1.
// Without this entry no pg_class row gets seeded for 3081 and
// RelationIdGetRelation(3081) FATALs; flattenRels derives RelNatts via
// pgIndexNattsByOID, which must equal pg_index.indnatts to satisfy
// RelationInitIndexAccessInfo's check at relcache.c:1492.
func TestNailedLocalRelsContainsPgExtensionNameIndex(t *testing.T) {
	const oid uint32 = 3081
	var found *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_extension_name_index) missing — Step 3ay", oid)
	}
	if found.RelName != "pg_extension_name_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_extension_name_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 1 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 1 (single-column name UNIQUE)", oid, found.RelNatts)
	}
}
