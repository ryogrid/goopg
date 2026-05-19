package initdb

import "testing"

// TestNailedSharedRelsContainsPgSubscriptionIndexes pins the
// M0106-0010 Step 3cf seed: both declared indexes of the
// pg_subscription shared catalog must appear in the flattened
// nailed-shared-rel list so PG's load_critical_index pass (which opens
// every declared index of a nailed rel) finds a pg_class row for each.
//
//	6114 pg_subscription_oid_index     : UNIQUE PRIMARY btree(oid oid_ops)
//	6115 pg_subscription_subname_index : UNIQUE composite
//	                                     btree(subdbid oid_ops, subname name_ops)
//
// Authoritative: postgres/src/include/catalog/pg_subscription.h:103-104.
func TestNailedSharedRelsContainsPgSubscriptionIndexes(t *testing.T) {
	cases := []struct {
		oid       uint32
		name      string
		relnatts  int16
	}{
		{6114, "pg_subscription_oid_index", 1},
		{6115, "pg_subscription_subname_index", 2},
	}
	for _, c := range cases {
		var rel *nailedRel
		for i := range nailedSharedRels {
			if nailedSharedRels[i].OID == c.oid {
				rel = &nailedSharedRels[i]
				break
			}
		}
		if rel == nil {
			t.Errorf("nailedSharedRels missing OID %d (%s)", c.oid, c.name)
			continue
		}
		if rel.RelName != c.name {
			t.Errorf("OID %d: Name=%q, want %s", c.oid, rel.RelName, c.name)
		}
		if rel.RelKind != 'i' {
			t.Errorf("OID %d: RelKind=%q, want i", c.oid, rel.RelKind)
		}
		if rel.RelNatts != c.relnatts {
			t.Errorf("OID %d: RelNatts=%d, want %d", c.oid, rel.RelNatts, c.relnatts)
		}
		// IsShared is intentionally not asserted on index nailedRel
		// entries — flattenRels' indexNailed() does not propagate the
		// flag from heap to index; PG derives shared-ness via the
		// index's referent heap. Matches the existing 6246/6247/6001/
		// 6002 shared-index seeds.
	}
}

// TestPgSubscriptionIndexInitialEntries pins the pgIndexInitialEntries
// rows for OID 6114 and 6115. IndKey / IndClass / IndCollation /
// IsUnique / IsPrimary verbatim from
// postgres/src/include/catalog/pg_subscription.h:103-104:
//
//	DECLARE_UNIQUE_INDEX_PKEY(pg_subscription_oid_index, 6114,
//	    SubscriptionObjectIndexId, pg_subscription,
//	    btree(oid oid_ops));
//	DECLARE_UNIQUE_INDEX(pg_subscription_subname_index, 6115,
//	    SubscriptionNameIndexId, pg_subscription,
//	    btree(subdbid oid_ops, subname name_ops));
func TestPgSubscriptionIndexInitialEntries(t *testing.T) {
	const (
		oidOpsID    uint32 = 1981
		nameOpsID   uint32 = 1986
		cCollation  uint32 = 950
	)
	type want struct {
		indrelid   uint32
		indkey     []int16
		indclass   []uint32
		indcoll    []uint32
		isUnique   bool
		isPrimary  bool
	}
	wantMap := map[uint32]want{
		6114: {6100, []int16{1}, []uint32{oidOpsID}, []uint32{0}, true, true},
		6115: {6100, []int16{2, 4}, []uint32{oidOpsID, nameOpsID}, []uint32{0, cCollation}, true, false},
	}
	for _, e := range pgIndexInitialEntries() {
		w, ok := wantMap[e.IndexRelid]
		if !ok {
			continue
		}
		delete(wantMap, e.IndexRelid)
		if e.IndRelid != w.indrelid {
			t.Errorf("OID %d: IndRelid=%d, want %d", e.IndexRelid, e.IndRelid, w.indrelid)
		}
		if !int16SliceEqual(e.IndKey, w.indkey) {
			t.Errorf("OID %d: IndKey=%v, want %v", e.IndexRelid, e.IndKey, w.indkey)
		}
		if !uint32SliceEqual(e.IndClass, w.indclass) {
			t.Errorf("OID %d: IndClass=%v, want %v", e.IndexRelid, e.IndClass, w.indclass)
		}
		if !uint32SliceEqual(e.IndCollation, w.indcoll) {
			t.Errorf("OID %d: IndCollation=%v, want %v", e.IndexRelid, e.IndCollation, w.indcoll)
		}
		if e.IsUnique != w.isUnique {
			t.Errorf("OID %d: IsUnique=%v, want %v", e.IndexRelid, e.IsUnique, w.isUnique)
		}
		if e.IsPrimary != w.isPrimary {
			t.Errorf("OID %d: IsPrimary=%v, want %v", e.IndexRelid, e.IsPrimary, w.isPrimary)
		}
	}
	for oid := range wantMap {
		t.Errorf("pgIndexInitialEntries missing OID %d", oid)
	}
}
