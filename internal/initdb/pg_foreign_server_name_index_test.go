package initdb

import "testing"

// TestPgForeignServerNameIndexSeededFromInitialEntries pins
// M0106-0010 Step 3bf's catalog seed:
// pg_foreign_server_name_index (OID 549) must appear in
// pgIndexInitialEntries as UNIQUE (non-PKEY) single-key on attnum
// {2} = srvname with C_COLLATION_OID (950).
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_foreign_server.h:55
//	  DECLARE_UNIQUE_INDEX(pg_foreign_server_name_index, 549,
//	    ForeignServerNameIndexId, pg_foreign_server,
//	    btree(srvname name_ops));
//	  MAKE_SYSCACHE(FOREIGNSERVERNAME,
//	    pg_foreign_server_name_index, 2);
//
// Heap OID 1417 = pg_foreign_server (Step 3be nailed rel).
// Companion to OID 113 (pg_foreign_server_oid_index,
// UNIQUE PKEY) — deferred until a concrete E2E blocker surfaces
// (only 549 has FATAL'd in TestE2E_FailoverGoopgToPG/async so far).
// Without this entry PG's RelationIdGetRelation(549) FATALs with
// "could not open relation with OID 549" — the Step 3bf boot
// blocker that surfaces after Step 3be seeded pg_foreign_server.
func TestPgForeignServerNameIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 549
	var found *pgIndexEntry
	entries := pgIndexInitialEntries()
	for i := range entries {
		if entries[i].IndexRelid == oid {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_foreign_server_name_index) missing — Step 3bf", oid)
	}
	if found.IndRelid != 1417 {
		t.Errorf("OID %d: IndRelid=%d, want 1417 (pg_foreign_server heap OID)", oid, found.IndRelid)
	}
	if !int16SliceEqual(found.IndKey, []int16{2}) {
		t.Errorf("OID %d: IndKey=%v, want [2] (srvname attnum)", oid, found.IndKey)
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

// TestNailedLocalRelsContainsPgForeignServerNameIndex asserts the
// nailed-rel registry includes OID 549 with RelKind='i' and RelNatts=1.
// Without this entry no pg_class row gets seeded for 549 and
// RelationIdGetRelation(549) FATALs; flattenRels derives RelNatts via
// pgIndexNattsByOID, which must equal pg_index.indnatts to satisfy
// RelationInitIndexAccessInfo's check at relcache.c:1492.
func TestNailedLocalRelsContainsPgForeignServerNameIndex(t *testing.T) {
	const oid uint32 = 549
	var found *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_foreign_server_name_index) missing — Step 3bf", oid)
	}
	if found.RelName != "pg_foreign_server_name_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_foreign_server_name_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 1 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 1 (single-column name UNIQUE)", oid, found.RelNatts)
	}
}
