package initdb

import "testing"

// TestPgIndex2678And2679AreDistinctWithCorrectFlags pins the M0106-0010
// Step 3q split of pg_index OIDs 2678 / 2679. Before Step 3q,
// pgIndexInitialEntries had a single entry mis-labelled "2679 =
// pg_index_indexrelid_index" with indkey={1} and IsPrimary=true; the
// authoritative PG18 layout in postgres/src/include/catalog/indexing.h
// is:
//
//	IndexRelidIndexId    = 2678 = pg_index_indexrelid_index
//	    btree(indexrelid oid_ops)  UNIQUE  PRIMARY KEY
//	IndexIndrelidIndexId = 2679 = pg_index_indrelid_index
//	    btree(indrelid    oid_ops)  UNIQUE  (not primary)
//
// PG's RelationCacheInitializePhase3 SHARED critical-index pass
// (relcache.c:4214) loads pg_database_datname_index (OID 2671), which
// cascades into a `SearchSysCache1(INDEXRELID, 2671)` lookup; once
// criticalRelcachesBuilt has flipped (after the LOCAL pass), the
// catcache miss falls back to a sysscan against 2678. With 2678 missing
// from pgIndexInitialEntries and nailedLocalRels, no heap row for the
// 2678 index relation exists, no btree leaf carries it, and the FATAL
// "cache lookup failed for index 2671" follows. This pin guards both
// halves of the split so any future refactor of the entry table cannot
// silently collapse them back into a single entry.
func TestPgIndex2678And2679AreDistinctWithCorrectFlags(t *testing.T) {
	var got2678, got2679 *pgIndexEntry
	for i := range pgIndexInitialEntries() {
		e := pgIndexInitialEntries()[i]
		switch e.IndexRelid {
		case 2678:
			cp := e
			got2678 = &cp
		case 2679:
			cp := e
			got2679 = &cp
		}
	}
	if got2678 == nil {
		t.Fatalf("pgIndexInitialEntries: missing OID 2678 (pg_index_indexrelid_index)")
	}
	if got2679 == nil {
		t.Fatalf("pgIndexInitialEntries: missing OID 2679 (pg_index_indrelid_index)")
	}
	// 2678: PRIMARY on indexrelid.
	if got2678.IndRelid != 2610 {
		t.Errorf("OID 2678 IndRelid=%d, want 2610 (pg_index)", got2678.IndRelid)
	}
	if !int16SliceEqual(got2678.IndKey, []int16{1}) {
		t.Errorf("OID 2678 IndKey=%v, want [1] (indexrelid)", got2678.IndKey)
	}
	if !got2678.IsUnique || !got2678.IsPrimary {
		t.Errorf("OID 2678 IsUnique=%t IsPrimary=%t, want both true", got2678.IsUnique, got2678.IsPrimary)
	}
	// 2679: UNIQUE on indrelid (NOT primary).
	if got2679.IndRelid != 2610 {
		t.Errorf("OID 2679 IndRelid=%d, want 2610 (pg_index)", got2679.IndRelid)
	}
	if !int16SliceEqual(got2679.IndKey, []int16{2}) {
		t.Errorf("OID 2679 IndKey=%v, want [2] (indrelid)", got2679.IndKey)
	}
	if !got2679.IsUnique || got2679.IsPrimary {
		t.Errorf("OID 2679 IsUnique=%t IsPrimary=%t, want unique=true primary=false", got2679.IsUnique, got2679.IsPrimary)
	}
}

// TestNailedLocalRelsContainsPgIndexIndexrelidIndex pins the second
// half of Step 3q: a pgIndexInitialEntries row for OID 2678 alone is
// not enough — PG's load_critical_index walks the relcache, which
// goopg derives from nailedLocalRels. If 2678 is missing from
// nailedLocalRels, RelationBuildDesc has no pg_class row and the
// SHARED critical-index pass FATALs on the cascading sysscan.
func TestNailedLocalRelsContainsPgIndexIndexrelidIndex(t *testing.T) {
	var found bool
	for _, r := range nailedLocalRels {
		if r.OID == 2678 {
			if r.RelKind != 'i' {
				t.Errorf("nailedLocalRels[2678] RelKind=%q, want 'i'", r.RelKind)
			}
			if r.RelNatts != 1 {
				t.Errorf("nailedLocalRels[2678] RelNatts=%d, want 1 (matches indnatts of single-column indexrelid key)", r.RelNatts)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("nailedLocalRels: OID 2678 (pg_index_indexrelid_index) missing — Step 3p's btree is unreachable without it")
	}
}
