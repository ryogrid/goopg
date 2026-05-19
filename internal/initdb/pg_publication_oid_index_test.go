package initdb

import "testing"

// TestNailedLocalRelsContainsPgPublicationOidIndex pins M0106-0010
// Step 3bw: nailedLocalRels must include OID 6110
// (pg_publication_oid_index). Without this entry PG's
// `RelationCacheInitializePhase3 → load_critical_index(6110)` FATALs with
// `could not open relation with OID 6110` from
// `RelationIdGetRelation(6110)`. The fix adds a Form_pg_class row for the
// index so the relcache init data carries `pg_publication_oid_index` with
// the expected RelKind='i' and RelNatts=1 (single oid_ops key derived
// from pgIndexNattsByOID).
//
//	postgres/src/include/catalog/pg_publication.h:72
//	  DECLARE_UNIQUE_INDEX_PKEY(pg_publication_oid_index, 6110,
//	    PublicationObjectIndexId, pg_publication, btree(oid oid_ops));
//	postgres/src/include/catalog/pg_publication.h:75
//	  MAKE_SYSCACHE(PUBLICATIONOID, pg_publication_oid_index, 8);
func TestNailedLocalRelsContainsPgPublicationOidIndex(t *testing.T) {
	const idxOID = 6110
	var got *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == idxOID {
			got = &nailedLocalRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedLocalRels missing OID %d (pg_publication_oid_index) — Step 3bw regression", idxOID)
	}
	if got.RelName != "pg_publication_oid_index" {
		t.Errorf("OID %d: RelName=%q want %q", idxOID, got.RelName, "pg_publication_oid_index")
	}
	if got.RelKind != 'i' {
		t.Errorf("OID %d: RelKind=%q want 'i' (index)", idxOID, got.RelKind)
	}
	if got.RelNatts != 1 {
		t.Errorf("OID %d: RelNatts=%d want 1 (single oid_ops key)", idxOID, got.RelNatts)
	}
}

// TestPgPublicationOidIndexInitialEntry pins the Form_pg_index row
// emitted by pgIndexInitialEntries. PG's RelationInitIndexAccessInfo
// asserts relnatts == indnatts (relcache.c:1492); a mismatch FATALs with
// `relnatts disagrees with indnatts for index 6110`. The RelNatts above
// is derived from pgIndexNattsByOID, which must equal pg_index.indnatts
// to satisfy that invariant.
func TestPgPublicationOidIndexInitialEntry(t *testing.T) {
	const idxOID = 6110
	var found *pgIndexEntry
	for _, e := range pgIndexInitialEntries() {
		if e.IndexRelid == idxOID {
			tmp := e
			found = &tmp
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries missing OID %d — Step 3bw regression", idxOID)
	}
	if found.IndRelid != 6104 {
		t.Errorf("OID %d: IndRelid=%d want 6104 (pg_publication heap)", idxOID, found.IndRelid)
	}
	if want := []int16{1}; !int16SliceEqual(found.IndKey, want) {
		t.Errorf("OID %d: IndKey=%v want %v (oid attnum=1)", idxOID, found.IndKey, want)
	}
	if !found.IsUnique {
		t.Errorf("OID %d: IsUnique=false want true (DECLARE_UNIQUE_INDEX_PKEY)", idxOID)
	}
	if !found.IsPrimary {
		t.Errorf("OID %d: IsPrimary=false want true (_PKEY variant)", idxOID)
	}
	if len(found.IndClass) != 1 || found.IndClass[0] != 1981 {
		t.Errorf("OID %d: IndClass=%v want [1981] (oid_ops)", idxOID, found.IndClass)
	}
	if len(found.IndCollation) != 1 || found.IndCollation[0] != 0 {
		t.Errorf("OID %d: IndCollation=%v want [0] (oid_ops carries no collation)", idxOID, found.IndCollation)
	}
}
