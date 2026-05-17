package initdb

import "testing"

// TestPgEventTriggerEvtnameIndexSeededFromInitialEntries pins
// M0106-0010 Step 3as's catalog seed:
// pg_event_trigger_evtname_index (OID 3467) must appear in
// pgIndexInitialEntries as UNIQUE (non-PRIMARY) single-key on
// attnum {2} = evtname (name_ops, C_COLLATION_OID=950).
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_event_trigger.h:54
//	  DECLARE_UNIQUE_INDEX(pg_event_trigger_evtname_index, 3467,
//	    EventTriggerNameIndexId, pg_event_trigger,
//	    btree(evtname name_ops));
//	  MAKE_SYSCACHE(EVENTTRIGGERNAME, pg_event_trigger_evtname_index, 8);
//
// Heap OID 3466 = pg_event_trigger (Step 3ar nailed rel). Without this
// entry PG's RelationIdGetRelation(3467) FATALs with "could not open
// relation with OID 3467" — the Step 3as boot blocker that surfaces
// after Step 3ar seeded pg_event_trigger.
func TestPgEventTriggerEvtnameIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 3467
	var found *pgIndexEntry
	entries := pgIndexInitialEntries()
	for i := range entries {
		if entries[i].IndexRelid == oid {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_event_trigger_evtname_index) missing — Step 3as", oid)
	}
	if found.IndRelid != 3466 {
		t.Errorf("OID %d: IndRelid=%d, want 3466 (pg_event_trigger heap OID)", oid, found.IndRelid)
	}
	if !int16SliceEqual(found.IndKey, []int16{2}) {
		t.Errorf("OID %d: IndKey=%v, want [2] (evtname)", oid, found.IndKey)
	}
	if !found.IsUnique {
		t.Errorf("OID %d: IsUnique=false, want true (DECLARE_UNIQUE_INDEX)", oid)
	}
	if found.IsPrimary {
		t.Errorf("OID %d: IsPrimary=true, want false (DECLARE_UNIQUE_INDEX is non-PKEY; companion 3468 is the PKEY)", oid)
	}
	if len(found.IndCollation) != 1 {
		t.Fatalf("OID %d: IndCollation len=%d, want 1", oid, len(found.IndCollation))
	}
	// name_ops uses C collation (C_COLLATION_OID = 950) — same
	// convention as pg_namespace_nspname_index (2684, Step 3t) and
	// pg_conversion_name_nsp_index (2669, Step 3aj).
	const cCollation uint32 = 950
	if found.IndCollation[0] != cCollation {
		t.Errorf("OID %d: IndCollation[0]=%d, want %d (name_ops C collation)", oid, found.IndCollation[0], cCollation)
	}
}

// TestNailedLocalRelsContainsPgEventTriggerEvtnameIndex asserts that the
// nailed-rel registry includes OID 3467 with RelKind='i' and RelNatts=1.
// Without this entry no pg_class row gets seeded for 3467 and
// RelationIdGetRelation(3467) FATALs; flattenRels derives RelNatts via
// pgIndexNattsByOID, which must equal pg_index.indnatts to satisfy
// RelationInitIndexAccessInfo's check at relcache.c:1492.
func TestNailedLocalRelsContainsPgEventTriggerEvtnameIndex(t *testing.T) {
	const oid uint32 = 3467
	var found *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == oid {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedLocalRels: OID %d (pg_event_trigger_evtname_index) missing — Step 3as", oid)
	}
	if found.RelName != "pg_event_trigger_evtname_index" {
		t.Errorf("nailedLocalRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_event_trigger_evtname_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 1 {
		t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 1 (evtname)", oid, found.RelNatts)
	}
}
