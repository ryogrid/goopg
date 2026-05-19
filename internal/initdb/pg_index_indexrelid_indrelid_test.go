package initdb

import "testing"

// TestPgIndex2678And2679AreDistinctWithCorrectFlags pins the M0106-0010
// Step 3q/3r split of pg_index OIDs 2678 / 2679.
//
// Step 3q first split the entry but assigned the (indkey, flags) pairs
// to the wrong OIDs; Step 3r restores the authoritative PG18 layout in
// postgres/src/include/catalog/pg_index_d.h + indexing.h:
//
//	IndexIndrelidIndexId = 2678 = pg_index_indrelid_index
//	    btree(indrelid    oid_ops)  NON-UNIQUE (DECLARE_INDEX)
//	IndexRelidIndexId    = 2679 = pg_index_indexrelid_index
//	    btree(indexrelid  oid_ops)  UNIQUE PRIMARY KEY
//
// PG's RelationCacheInitializePhase3 SHARED critical-index pass
// (relcache.c:4214) loads pg_database_datname_index (OID 2671), which
// cascades into a `SearchSysCache1(INDEXRELID, 2671)` lookup; once
// criticalRelcachesBuilt has flipped (after the LOCAL pass), the
// catcache miss falls back to a sysscan against
// IndexRelidIndexId = 2679 (pg_index_indexrelid_index). With 2679's
// indkey wrong (e.g. {2} rather than {1}), genam.c:446 FATALs
// "column is not in index"; with 2679's pg_class row pointing at an
// empty btree, the cache lookup fails entirely. This pin guards both
// halves of the split so any future refactor cannot silently invert or
// collapse the entries.
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
		t.Fatalf("pgIndexInitialEntries: missing OID 2678 (pg_index_indrelid_index)")
	}
	if got2679 == nil {
		t.Fatalf("pgIndexInitialEntries: missing OID 2679 (pg_index_indexrelid_index)")
	}
	// 2678: NON-UNIQUE on indrelid (DECLARE_INDEX, not DECLARE_UNIQUE_INDEX).
	if got2678.IndRelid != 2610 {
		t.Errorf("OID 2678 IndRelid=%d, want 2610 (pg_index)", got2678.IndRelid)
	}
	if !int16SliceEqual(got2678.IndKey, []int16{2}) {
		t.Errorf("OID 2678 IndKey=%v, want [2] (indrelid)", got2678.IndKey)
	}
	if got2678.IsUnique || got2678.IsPrimary {
		t.Errorf("OID 2678 IsUnique=%t IsPrimary=%t, want both false (DECLARE_INDEX)", got2678.IsUnique, got2678.IsPrimary)
	}
	// 2679: UNIQUE PRIMARY on indexrelid.
	if got2679.IndRelid != 2610 {
		t.Errorf("OID 2679 IndRelid=%d, want 2610 (pg_index)", got2679.IndRelid)
	}
	if !int16SliceEqual(got2679.IndKey, []int16{1}) {
		t.Errorf("OID 2679 IndKey=%v, want [1] (indexrelid)", got2679.IndKey)
	}
	if !got2679.IsUnique || !got2679.IsPrimary {
		t.Errorf("OID 2679 IsUnique=%t IsPrimary=%t, want both true (DECLARE_UNIQUE_INDEX_PKEY)", got2679.IsUnique, got2679.IsPrimary)
	}
}

// TestNailedLocalRelsContainsPgIndexIndexrelidIndex pins the second
// half of Step 3q/3r: a pgIndexInitialEntries row for OID 2679 alone is
// not enough — PG's load_critical_index walks the relcache, which goopg
// derives from nailedLocalRels. If 2679 is missing from nailedLocalRels,
// RelationBuildDesc has no pg_class row and the SHARED critical-index
// pass FATALs on the cascading sysscan. Step 3r ensures both 2678
// (pg_index_indrelid_index) and 2679 (pg_index_indexrelid_index) are
// present with the correct PG18 labels.
func TestNailedLocalRelsContainsPgIndexIndexrelidIndex(t *testing.T) {
	want := map[uint32]string{
		2678: "pg_index_indrelid_index",
		2679: "pg_index_indexrelid_index",
	}
	got := map[uint32]string{}
	for _, r := range nailedLocalRels {
		if _, ok := want[r.OID]; !ok {
			continue
		}
		got[r.OID] = r.RelName
		if r.RelKind != 'i' {
			t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", r.OID, r.RelKind)
		}
		if r.RelNatts != 1 {
			t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 1 (single-column key)", r.OID, r.RelNatts)
		}
	}
	for oid, name := range want {
		if g, ok := got[oid]; !ok {
			t.Fatalf("nailedLocalRels: OID %d (%s) missing — Step 3p/3r's btree is unreachable without it", oid, name)
		} else if g != name {
			t.Errorf("nailedLocalRels[%d] RelName=%q, want %q (PG18 label per indexing.h)", oid, g, name)
		}
	}
}
