package initdb

import (
	"testing"
)

// TestNailedLocalRelsContainsPgSubscriptionRel pins the heap entry for
// pg_subscription_rel (OID 6102) plus every column descriptor. Verbatim
// against PG18 `postgres/src/include/catalog/pg_subscription_rel.h:31`
// (`CATALOG(pg_subscription_rel,6102,SubscriptionRelRelationId)`) and
// `pg_subscription_rel_d.h` (Anum_pg_subscription_rel_* 1..4,
// Natts_pg_subscription_rel == 4). M0106-0010 Step 3cg.
func TestNailedLocalRelsContainsPgSubscriptionRel(t *testing.T) {
	var rel *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == 6102 {
			rel = &nailedLocalRels[i]
			break
		}
	}
	if rel == nil {
		t.Fatal("nailedLocalRels missing OID 6102 (pg_subscription_rel)")
	}
	if rel.RelName != "pg_subscription_rel" {
		t.Errorf("Name=%q, want pg_subscription_rel", rel.RelName)
	}
	if rel.RelType != 83 {
		t.Errorf("RelType=%d, want 83 (no SubscriptionRelRelation_Rowtype_Id in PG18)", rel.RelType)
	}
	if rel.RelKind != 'r' {
		t.Errorf("RelKind=%q, want r", rel.RelKind)
	}
	if rel.RelNatts != 4 {
		t.Errorf("RelNatts=%d, want 4 (Natts_pg_subscription_rel == 4)", rel.RelNatts)
	}
	if rel.IsShared {
		t.Errorf("IsShared=true, want false (pg_subscription_rel is per-database)")
	}
	wantAttrs := []nailedAttr{
		{Name: "srsubid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "srrelid", TypeOID: 26, Num: 2, Len: 4, NotNull: true},
		{Name: "srsubstate", TypeOID: 18, Num: 3, Len: 1, NotNull: true},
		{Name: "srsublsn", TypeOID: 3220, Num: 4, Len: 8, NotNull: false},
	}
	if len(rel.Attrs) != len(wantAttrs) {
		t.Fatalf("Attrs len=%d, want %d", len(rel.Attrs), len(wantAttrs))
	}
	for i, w := range wantAttrs {
		got := rel.Attrs[i]
		if got != w {
			t.Errorf("Attrs[%d]=%+v, want %+v", i, got, w)
		}
	}
}

// TestNailedLocalRelsContainsPgSubscriptionRelSrrelidSrsubidIndex pins the
// flattenRels-derived index entry for OID 6117 — the sole index on
// pg_subscription_rel — with RelKind='i' and the correct indnatts so
// RelationInitIndexAccessInfo's relnatts/indnatts check
// (relcache.c:1492) is satisfied.
func TestNailedLocalRelsContainsPgSubscriptionRelSrrelidSrsubidIndex(t *testing.T) {
	const wantOID uint32 = 6117
	const wantName = "pg_subscription_rel_srrelid_srsubid_index"
	var rel *nailedRel
	all := append([]nailedRel{}, nailedSharedRels...)
	all = append(all, nailedLocalRels...)
	for i := range all {
		if all[i].OID == wantOID {
			rel = &all[i]
			break
		}
	}
	if rel == nil {
		t.Fatalf("nailed-rel list missing OID %d (%s)", wantOID, wantName)
	}
	if rel.RelName != wantName {
		t.Errorf("Name=%q, want %s", rel.RelName, wantName)
	}
	if rel.RelKind != 'i' {
		t.Errorf("RelKind=%q, want i", rel.RelKind)
	}
	if rel.RelNatts != 2 {
		t.Errorf("RelNatts=%d, want 2 (btree(srrelid, srsubid))", rel.RelNatts)
	}
}

// TestPgSubscriptionRelIndexInitialEntries pins the pgIndexInitialEntries
// row for OID 6117 (pg_subscription_rel_srrelid_srsubid_index). IndKey /
// IndClass / IndCollation / IsUnique / IsPrimary verbatim from
// `postgres/src/include/catalog/pg_subscription_rel.h:52`:
// DECLARE_UNIQUE_INDEX_PKEY(pg_subscription_rel_srrelid_srsubid_index,
//
//	6117, SubscriptionRelSrrelidSrsubidIndexId, pg_subscription_rel,
//	btree(srrelid oid_ops, srsubid oid_ops));
//
// pg_subscription_rel attnums: 1=srsubid, 2=srrelid. Index leads on
// srrelid, so IndKey = {2, 1}.
func TestPgSubscriptionRelIndexInitialEntries(t *testing.T) {
	const oidOpsID uint32 = 1981
	var got *pgIndexEntry
	for _, e := range pgIndexInitialEntries() {
		if e.IndexRelid == 6117 {
			eCopy := e
			got = &eCopy
			break
		}
	}
	if got == nil {
		t.Fatal("pgIndexInitialEntries missing OID 6117 (pg_subscription_rel_srrelid_srsubid_index)")
	}
	if got.IndRelid != 6102 {
		t.Errorf("IndRelid=%d, want 6102 (pg_subscription_rel)", got.IndRelid)
	}
	if !int16SliceEqual(got.IndKey, []int16{2, 1}) {
		t.Errorf("IndKey=%v, want [2 1] (srrelid, srsubid)", got.IndKey)
	}
	if !uint32SliceEqual(got.IndClass, []uint32{oidOpsID, oidOpsID}) {
		t.Errorf("IndClass=%v, want [oid_ops oid_ops]=[%d %d]", got.IndClass, oidOpsID, oidOpsID)
	}
	if !uint32SliceEqual(got.IndCollation, []uint32{0, 0}) {
		t.Errorf("IndCollation=%v, want [0 0] (oid_ops carries no collation)", got.IndCollation)
	}
	if !got.IsUnique {
		t.Errorf("IsUnique=false, want true (DECLARE_UNIQUE_INDEX_PKEY)")
	}
	if !got.IsPrimary {
		t.Errorf("IsPrimary=false, want true (_PKEY variant)")
	}
}

// TestPgSubscriptionRelAttrsTypeOIDsMatchPG18 pins the exact TypeOIDs and
// Len values for every column. Catches silent drift in
// pgSubscriptionRelAttrs.
func TestPgSubscriptionRelAttrsTypeOIDsMatchPG18(t *testing.T) {
	attrs := pgSubscriptionRelAttrs()
	want := []struct {
		Name    string
		TypeOID uint32
		Len     int16
		NotNull bool
	}{
		{"srsubid", 26, 4, true},
		{"srrelid", 26, 4, true},
		{"srsubstate", 18, 1, true},
		{"srsublsn", 3220, 8, false},
	}
	if len(attrs) != len(want) {
		t.Fatalf("pgSubscriptionRelAttrs: len=%d, want %d", len(attrs), len(want))
	}
	for i, w := range want {
		a := attrs[i]
		if a.Name != w.Name || a.TypeOID != w.TypeOID || a.Len != w.Len || a.NotNull != w.NotNull {
			t.Errorf("attrs[%d]={Name:%q TypeOID:%d Len:%d NotNull:%v}, want {Name:%q TypeOID:%d Len:%d NotNull:%v}",
				i, a.Name, a.TypeOID, a.Len, a.NotNull,
				w.Name, w.TypeOID, w.Len, w.NotNull)
		}
	}
}

// TestPgLsnTypeHelpersMatchPG18 pins the type-helper outputs for pg_lsn
// (TypeOID 3220) per `postgres/src/include/catalog/pg_type.dat:410-413`:
// typbyval = FLOAT8PASSBYVAL (true on 64-bit), typalign = 'd',
// typstorage = 'p' (PLAIN — default). Without these, the pg_attribute row
// for pg_subscription_rel.srsublsn and pg_subscription.subskiplsn would
// silently emit attbyval=false/attalign='i', mismatching PG-standby's
// rd_att TupleDesc reconstruction. M0106-0010 Step 3cg.
func TestPgLsnTypeHelpersMatchPG18(t *testing.T) {
	if !pgTypeByVal(3220) {
		t.Errorf("pgTypeByVal(3220)=false, want true (FLOAT8PASSBYVAL on 64-bit)")
	}
	if got := pgTypeAlignChar(3220); got != "d" {
		t.Errorf("pgTypeAlignChar(3220)=%q, want \"d\"", got)
	}
	if got := pgTypeStorageChar(3220); got != "p" {
		t.Errorf("pgTypeStorageChar(3220)=%q, want \"p\" (PLAIN default)", got)
	}
}
