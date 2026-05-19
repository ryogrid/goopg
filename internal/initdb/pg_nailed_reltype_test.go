package initdb

import "testing"

// TestNailedRelTypesMatchPG18FormrdescConstants pins every nailed
// catalog's RelType against the canonical PG18 *Relation_Rowtype_Id
// constants (postgres/src/include/catalog/pg_*_d.h). PG's Phase2/Phase3
// (relcache.c::RelationCacheInitializePhase2 / Phase3) invokes
// `formrdesc("<name>", <Rowtype_Id>, ...)` whenever loading the
// pg_internal.init file fails — and Phase3's
// `Assert(relation->rd_att->tdtypeid == relp->reltype)` then PANICs
// every connecting backend if the heap row's reltype disagrees.
//
// This pin caught Step 3v: pg_shseclabel RelType was 4065 but
// SharedSecLabelRelation_Rowtype_Id is 4066, so every PG standby boot
// loop-PANICed in `RelationCacheInitializePhase3` once
// `criticalSharedRelcachesBuilt` advanced past Phase2's formrdesc path.
func TestNailedRelTypesMatchPG18FormrdescConstants(t *testing.T) {
	cases := []struct {
		name     string
		oid      uint32
		wantType uint32 // PG18 *Relation_Rowtype_Id
	}{
		// Shared formrdesc list (relcache.c Phase2).
		{"pg_database", 1262, 1248},        // DatabaseRelation_Rowtype_Id
		{"pg_authid", 1260, 2842},          // AuthIdRelation_Rowtype_Id
		{"pg_auth_members", 1261, 2843},    // AuthMemRelation_Rowtype_Id
		{"pg_shseclabel", 3592, 4066},      // SharedSecLabelRelation_Rowtype_Id
		{"pg_subscription", 6100, 6101},    // SubscriptionRelation_Rowtype_Id
		// Local formrdesc list (relcache.c Phase3).
		{"pg_type", 1247, 71},              // TypeRelation_Rowtype_Id
		{"pg_attribute", 1249, 75},         // AttributeRelation_Rowtype_Id
		{"pg_class", 1259, 83},             // RelationRelation_Rowtype_Id
		{"pg_proc", 1255, 81},              // ProcedureRelation_Rowtype_Id
	}

	byOID := make(map[uint32]nailedRel)
	for _, r := range nailedSharedRels {
		byOID[r.OID] = r
	}
	for _, r := range nailedLocalRels {
		byOID[r.OID] = r
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, ok := byOID[c.oid]
			if !ok {
				t.Fatalf("nailed rel OID %d (%s) not registered in nailedShared/LocalRels", c.oid, c.name)
			}
			if r.RelName != c.name {
				t.Fatalf("OID %d: RelName = %q, want %q", c.oid, r.RelName, c.name)
			}
			if r.RelType != c.wantType {
				t.Fatalf("%s (OID %d): RelType = %d, want %d "+
					"(PG18 formrdesc constant; mismatch PANICs every client "+
					"backend at relcache.c:4293)", c.name, c.oid, r.RelType, c.wantType)
			}
		})
	}
}
