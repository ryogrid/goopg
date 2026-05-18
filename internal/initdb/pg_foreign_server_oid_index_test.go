package initdb

import "testing"

// TestPgForeignServerOidIndexSeededFromInitialEntries pins
// M0106-0010 Step 3bg's catalog seed:
// pg_foreign_server_oid_index (OID 113) must appear in
// pgIndexInitialEntries as UNIQUE PRIMARY KEY single-key on attnum
// {1} = oid with collation 0.
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_foreign_server.h:58
//	  DECLARE_UNIQUE_INDEX_PKEY(pg_foreign_server_oid_index, 113,
//	    ForeignServerOidIndexId, pg_foreign_server,
//	    btree(oid oid_ops));
//	  MAKE_SYSCACHE(FOREIGNSERVEROID,
//	    pg_foreign_server_oid_index, 2);
//
// Heap OID 1417 = pg_foreign_server (Step 3be nailed rel).
// Companion to OID 549 (pg_foreign_server_name_index, Step 3bf).
// Without this entry PG's RelationIdGetRelation(113) FATALs with
// "could not open relation with OID 113" — the Step 3bg boot
// blocker that surfaces after Step 3bf seeded the name companion.
func TestPgForeignServerOidIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 113
	var found *pgIndexEntry
	entries := pgIndexInitialEntries()
	for i := range entries {
		if entries[i].IndexRelid == oid {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_foreign_server_oid_index) missing — Step 3bg", oid)
	}
	if found.IndRelid != 1417 {
		t.Errorf("OID %d: IndRelid=%d, want 1417 (pg_foreign_server heap OID)", oid, found.IndRelid)
	}
	if !int16SliceEqual(found.IndKey, []int16{1}) {
		t.Errorf("OID %d: IndKey=%v, want [1] (oid attnum)", oid, found.IndKey)
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
		t.Errorf("OID %d: IndCollation[0]=%d, want 0 (oid_ops has no collation)", oid, found.IndCollation[0])
	}
}

// TestNailedLocalRelsContainsPgForeignServerOidIndex asserts the
// nailed-rel registry includes OID 113 with RelKind='i' and RelNatts=1.
// Without this entry no pg_class row gets seeded for 113 and
// RelationIdGetRelation(113) FATALs; flattenRels derives RelNatts via
// pgIndexNattsByOID, which must equal pg_index.indnatts to satisfy
// RelationInitIndexAccessInfo's check at relcache.c:1492.
func TestNailedLocalRelsContainsPgForeignServerOidIndex(t *testing.T) {
	const oid uint32 = 113
	var found *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_foreign_server_oid_index) missing — Step 3bg", oid)
	}
	if found.RelName != "pg_foreign_server_oid_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_foreign_server_oid_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 1 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 1 (single-column oid UNIQUE PKEY)", oid, found.RelNatts)
	}
}
