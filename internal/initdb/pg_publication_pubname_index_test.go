package initdb

import "testing"

// TestNailedLocalRelsContainsPgPublicationPubnameIndex pins M0106-0010
// Step 3bv: nailedLocalRels must include OID 6111
// (pg_publication_pubname_index). Without this entry PG's
// `RelationCacheInitializePhase3 → load_critical_index(6111)` FATALs with
// `could not open relation with OID 6111` from
// `RelationIdGetRelation(6111)`. The fix adds a Form_pg_class row for the
// index so the relcache init data carries `pg_publication_pubname_index`
// with the expected RelKind='i' and RelNatts=1 (single pubname name_ops
// key derived from pgIndexNattsByOID).
//
//	postgres/src/include/catalog/pg_publication.h:73
//	  DECLARE_UNIQUE_INDEX(pg_publication_pubname_index, 6111,
//	    PublicationNameIndexId, pg_publication, btree(pubname name_ops));
//	postgres/src/include/catalog/pg_publication.h:76
//	  MAKE_SYSCACHE(PUBLICATIONNAME, pg_publication_pubname_index, 8);
func TestNailedLocalRelsContainsPgPublicationPubnameIndex(t *testing.T) {
	const idxOID = 6111
	var got *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == idxOID {
			got = &nailedLocalRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedLocalRels missing OID %d (pg_publication_pubname_index) — Step 3bv regression", idxOID)
	}
	if got.RelName != "pg_publication_pubname_index" {
		t.Errorf("OID %d: RelName=%q want %q", idxOID, got.RelName, "pg_publication_pubname_index")
	}
	if got.RelKind != 'i' {
		t.Errorf("OID %d: RelKind=%q want 'i' (index)", idxOID, got.RelKind)
	}
	if got.RelNatts != 1 {
		t.Errorf("OID %d: RelNatts=%d want 1 (single pubname name_ops key)", idxOID, got.RelNatts)
	}
}

// TestPgPublicationPubnameIndexInitialEntry pins the Form_pg_index row
// emitted by pgIndexInitialEntries. PG's RelationInitIndexAccessInfo
// asserts relnatts == indnatts (relcache.c:1492); a mismatch FATALs with
// `relnatts disagrees with indnatts for index 6111`. The RelNatts above
// is derived from pgIndexNattsByOID, which must equal pg_index.indnatts
// to satisfy that invariant.
func TestPgPublicationPubnameIndexInitialEntry(t *testing.T) {
	const idxOID = 6111
	var found *pgIndexEntry
	for _, e := range pgIndexInitialEntries() {
		if e.IndexRelid == idxOID {
			tmp := e
			found = &tmp
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries missing OID %d — Step 3bv regression", idxOID)
	}
	if found.IndRelid != 6104 {
		t.Errorf("OID %d: IndRelid=%d want 6104 (pg_publication heap)", idxOID, found.IndRelid)
	}
	if want := []int16{2}; !int16SliceEqual(found.IndKey, want) {
		t.Errorf("OID %d: IndKey=%v want %v (pubname attnum=2)", idxOID, found.IndKey, want)
	}
	if !found.IsUnique {
		t.Errorf("OID %d: IsUnique=false want true (DECLARE_UNIQUE_INDEX)", idxOID)
	}
	if found.IsPrimary {
		t.Errorf("OID %d: IsPrimary=true want false (DECLARE_UNIQUE_INDEX, not _PKEY)", idxOID)
	}
}
