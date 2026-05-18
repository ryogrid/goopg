package initdb

import "testing"

// TestPgParameterAclOidIndexSeededFromInitialEntries guards
// M0106-0010 step 3br. After Step 3bq seeded pg_parameter_acl_parname_index
// (OID 6246), PG-standby boot's next FATAL is `could not open relation with
// OID 6247` from `RelationIdGetRelation(6247)`. The fix adds a
// `Form_pg_index` row to pgIndexInitialEntries pinning
// `(IndRelid=6243, IndKey=[1], IsUnique=true, IsPrimary=true)` per
// `postgres/src/include/catalog/pg_parameter_acl.h:54`:
//
//	DECLARE_UNIQUE_INDEX_PKEY(pg_parameter_acl_oid_index, 6247,
//	  ParameterAclOidIndexId, pg_parameter_acl, btree(oid oid_ops));
//
// This test rejects silent removal that would re-introduce the FATAL.
func TestPgParameterAclOidIndexSeededFromInitialEntries(t *testing.T) {
	const idxOID = 6247
	var got *pgIndexEntry
	for i, e := range pgIndexInitialEntries() {
		if e.IndexRelid == idxOID {
			got = &pgIndexInitialEntries()[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("pgIndexInitialEntries missing OID %d (pg_parameter_acl_oid_index) — Step 3br regression", idxOID)
	}
	if got.IndRelid != 6243 {
		t.Errorf("OID %d: IndRelid=%d want 6243 (pg_parameter_acl heap OID)", idxOID, got.IndRelid)
	}
	if len(got.IndKey) != 1 || got.IndKey[0] != 1 {
		t.Errorf("OID %d: IndKey=%v want [1] (oid attnum)", idxOID, got.IndKey)
	}
	if !got.IsUnique {
		t.Errorf("OID %d: IsUnique=false want true (DECLARE_UNIQUE_INDEX_PKEY)", idxOID)
	}
	if !got.IsPrimary {
		t.Errorf("OID %d: IsPrimary=false want true (_PKEY variant)", idxOID)
	}
	if len(got.IndClass) != 1 || got.IndClass[0] != 1981 {
		t.Errorf("OID %d: IndClass=%v want [1981] (oid_ops)", idxOID, got.IndClass)
	}
	if len(got.IndCollation) != 1 || got.IndCollation[0] != 0 {
		t.Errorf("OID %d: IndCollation=%v want [0] (oid_ops carries no collation)", idxOID, got.IndCollation)
	}
}

// TestNailedSharedRelsContainsPgParameterAclOidIndex guards
// M0106-0010 step 3br's complementary edit to relcache_init.go: without
// the nailedSharedRels entry, `bootstrapPgClassTuples` never writes a
// pg_class row for OID 6247 and PG's `RelationIdGetRelation(6247)` still
// FATALs even though the Form_pg_index row exists.
func TestNailedSharedRelsContainsPgParameterAclOidIndex(t *testing.T) {
	const idxOID = 6247
	var got *nailedRel
	for i := range nailedSharedRels {
		if nailedSharedRels[i].OID == idxOID {
			got = &nailedSharedRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedSharedRels missing OID %d (pg_parameter_acl_oid_index) — Step 3br regression", idxOID)
	}
	if got.RelName != "pg_parameter_acl_oid_index" {
		t.Errorf("OID %d: RelName=%q want %q", idxOID, got.RelName, "pg_parameter_acl_oid_index")
	}
	if got.RelKind != 'i' {
		t.Errorf("OID %d: RelKind=%q want 'i' (index)", idxOID, got.RelKind)
	}
	// RelNatts derived from pgIndexNattsByOID(6247); the index has 1 key
	// column (oid), so flattenRels sets RelNatts=1.
	if got.RelNatts != 1 {
		t.Errorf("OID %d: RelNatts=%d want 1 (single oid_ops key)", idxOID, got.RelNatts)
	}
}
