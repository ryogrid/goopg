package initdb

import "testing"

// TestPgParameterAclParnameIndexSeededFromInitialEntries guards
// M0106-0010 step 3bq. After Step 3bp seeded pg_parameter_acl (OID 6243)
// as a nailed shared rel, PG-standby boot's next FATAL is `could not
// open relation with OID 6246` from `RelationIdGetRelation(6246)`. The
// fix adds a `Form_pg_index` row to pgIndexInitialEntries pinning
// `(IndRelid=6243, IndKey=[2], IsUnique=true, IsPrimary=false)` per
// `postgres/src/include/catalog/pg_parameter_acl.h:53`:
//
//	DECLARE_UNIQUE_INDEX(pg_parameter_acl_parname_index, 6246,
//	  ParameterAclParnameIndexId, pg_parameter_acl,
//	  btree(parname text_ops));
//
// This test rejects silent removal that would re-introduce the FATAL.
func TestPgParameterAclParnameIndexSeededFromInitialEntries(t *testing.T) {
	const idxOID = 6246
	var got *pgIndexEntry
	for i, e := range pgIndexInitialEntries() {
		if e.IndexRelid == idxOID {
			got = &pgIndexInitialEntries()[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("pgIndexInitialEntries missing OID %d (pg_parameter_acl_parname_index) — Step 3bq regression", idxOID)
	}
	if got.IndRelid != 6243 {
		t.Errorf("OID %d: IndRelid=%d want 6243 (pg_parameter_acl heap OID)", idxOID, got.IndRelid)
	}
	if len(got.IndKey) != 1 || got.IndKey[0] != 2 {
		t.Errorf("OID %d: IndKey=%v want [2] (parname attnum)", idxOID, got.IndKey)
	}
	if !got.IsUnique {
		t.Errorf("OID %d: IsUnique=false want true (DECLARE_UNIQUE_INDEX)", idxOID)
	}
	if got.IsPrimary {
		t.Errorf("OID %d: IsPrimary=true want false (not _PKEY — PKEY is 6247)", idxOID)
	}
	if len(got.IndClass) != 1 || got.IndClass[0] != 3126 {
		t.Errorf("OID %d: IndClass=%v want [3126] (text_ops)", idxOID, got.IndClass)
	}
	if len(got.IndCollation) != 1 || got.IndCollation[0] != 950 {
		t.Errorf("OID %d: IndCollation=%v want [950] (C_COLLATION_OID — required for text_ops)", idxOID, got.IndCollation)
	}
}

// TestNailedSharedRelsContainsPgParameterAclParnameIndex guards
// M0106-0010 step 3bq's complementary edit to relcache_init.go: without
// the nailedSharedRels entry, `bootstrapPgClassTuples` never writes a
// pg_class row for OID 6246 and PG's `RelationIdGetRelation(6246)` still
// FATALs even though the Form_pg_index row exists.
func TestNailedSharedRelsContainsPgParameterAclParnameIndex(t *testing.T) {
	const idxOID = 6246
	var got *nailedRel
	for i := range nailedSharedRels {
		if nailedSharedRels[i].OID == idxOID {
			got = &nailedSharedRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedSharedRels missing OID %d (pg_parameter_acl_parname_index) — Step 3bq regression", idxOID)
	}
	if got.RelName != "pg_parameter_acl_parname_index" {
		t.Errorf("OID %d: RelName=%q want %q", idxOID, got.RelName, "pg_parameter_acl_parname_index")
	}
	if got.RelKind != 'i' {
		t.Errorf("OID %d: RelKind=%q want 'i' (index)", idxOID, got.RelKind)
	}
	// RelNatts derived from pgIndexNattsByOID(6246); the index has 1 key
	// column (parname), so flattenRels sets RelNatts=1.
	if got.RelNatts != 1 {
		t.Errorf("OID %d: RelNatts=%d want 1 (single text_ops key)", idxOID, got.RelNatts)
	}
}
