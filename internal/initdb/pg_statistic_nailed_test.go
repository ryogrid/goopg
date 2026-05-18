package initdb

import (
	"testing"
)

// TestNailedLocalRelsContainsPgStatistic pins the heap entry for
// pg_statistic (OID 2619) plus every column descriptor. Verbatim
// against PG18 `postgres/src/include/catalog/pg_statistic.h:29`
// (`CATALOG(pg_statistic,2619,StatisticRelationId)`) and
// `pg_statistic_d.h` (Anum_pg_statistic_* 1..31, Natts == 31).
// M0106-0010 Step 3ce.
func TestNailedLocalRelsContainsPgStatistic(t *testing.T) {
	var rel *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == 2619 {
			rel = &nailedLocalRels[i]
			break
		}
	}
	if rel == nil {
		t.Fatal("nailedLocalRels missing OID 2619 (pg_statistic)")
	}
	if rel.RelName != "pg_statistic" {
		t.Errorf("Name=%q, want pg_statistic", rel.RelName)
	}
	if rel.RelType != 83 {
		t.Errorf("RelType=%d, want 83 (no StatisticRelation_Rowtype_Id in PG18)", rel.RelType)
	}
	if rel.RelKind != 'r' {
		t.Errorf("RelKind=%q, want r", rel.RelKind)
	}
	if rel.RelNatts != 31 {
		t.Errorf("RelNatts=%d, want 31 (Natts_pg_statistic == 31)", rel.RelNatts)
	}
	if rel.IsShared {
		t.Errorf("IsShared=true, want false (pg_statistic is per-database)")
	}
	wantAttrs := []nailedAttr{
		{Name: "starelid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "staattnum", TypeOID: 21, Num: 2, Len: 2, NotNull: true},
		{Name: "stainherit", TypeOID: 16, Num: 3, Len: 1, NotNull: true},
		{Name: "stanullfrac", TypeOID: 700, Num: 4, Len: 4, NotNull: true},
		{Name: "stawidth", TypeOID: 23, Num: 5, Len: 4, NotNull: true},
		{Name: "stadistinct", TypeOID: 700, Num: 6, Len: 4, NotNull: true},
		{Name: "stakind1", TypeOID: 21, Num: 7, Len: 2, NotNull: true},
		{Name: "stakind2", TypeOID: 21, Num: 8, Len: 2, NotNull: true},
		{Name: "stakind3", TypeOID: 21, Num: 9, Len: 2, NotNull: true},
		{Name: "stakind4", TypeOID: 21, Num: 10, Len: 2, NotNull: true},
		{Name: "stakind5", TypeOID: 21, Num: 11, Len: 2, NotNull: true},
		{Name: "staop1", TypeOID: 26, Num: 12, Len: 4, NotNull: true},
		{Name: "staop2", TypeOID: 26, Num: 13, Len: 4, NotNull: true},
		{Name: "staop3", TypeOID: 26, Num: 14, Len: 4, NotNull: true},
		{Name: "staop4", TypeOID: 26, Num: 15, Len: 4, NotNull: true},
		{Name: "staop5", TypeOID: 26, Num: 16, Len: 4, NotNull: true},
		{Name: "stacoll1", TypeOID: 26, Num: 17, Len: 4, NotNull: true},
		{Name: "stacoll2", TypeOID: 26, Num: 18, Len: 4, NotNull: true},
		{Name: "stacoll3", TypeOID: 26, Num: 19, Len: 4, NotNull: true},
		{Name: "stacoll4", TypeOID: 26, Num: 20, Len: 4, NotNull: true},
		{Name: "stacoll5", TypeOID: 26, Num: 21, Len: 4, NotNull: true},
		{Name: "stanumbers1", TypeOID: 1021, Num: 22, Len: -1, NotNull: false},
		{Name: "stanumbers2", TypeOID: 1021, Num: 23, Len: -1, NotNull: false},
		{Name: "stanumbers3", TypeOID: 1021, Num: 24, Len: -1, NotNull: false},
		{Name: "stanumbers4", TypeOID: 1021, Num: 25, Len: -1, NotNull: false},
		{Name: "stanumbers5", TypeOID: 1021, Num: 26, Len: -1, NotNull: false},
		{Name: "stavalues1", TypeOID: 2277, Num: 27, Len: -1, NotNull: false},
		{Name: "stavalues2", TypeOID: 2277, Num: 28, Len: -1, NotNull: false},
		{Name: "stavalues3", TypeOID: 2277, Num: 29, Len: -1, NotNull: false},
		{Name: "stavalues4", TypeOID: 2277, Num: 30, Len: -1, NotNull: false},
		{Name: "stavalues5", TypeOID: 2277, Num: 31, Len: -1, NotNull: false},
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

// TestNailedLocalRelsContainsPgStatisticRelidAttInhIndex pins the
// flattenRels-derived index entry for OID 2696 — the sole index on
// pg_statistic — with RelKind='i' and the correct indnatts so
// RelationInitIndexAccessInfo's relnatts/indnatts check
// (relcache.c:1492) is satisfied.
func TestNailedLocalRelsContainsPgStatisticRelidAttInhIndex(t *testing.T) {
	const wantOID uint32 = 2696
	const wantName = "pg_statistic_relid_att_inh_index"
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
	if rel.RelNatts != 3 {
		t.Errorf("RelNatts=%d, want 3 (btree(starelid, staattnum, stainherit))", rel.RelNatts)
	}
}

// TestPgStatisticIndexInitialEntries pins the pgIndexInitialEntries row
// for OID 2696 (pg_statistic_relid_att_inh_index). IndKey / IndClass /
// IndCollation / IsUnique / IsPrimary verbatim from
// `postgres/src/include/catalog/pg_statistic.h:139`:
// DECLARE_UNIQUE_INDEX_PKEY(pg_statistic_relid_att_inh_index, 2696,
//
//	StatisticRelidAttnumInhIndexId, pg_statistic,
//	btree(starelid oid_ops, staattnum int2_ops, stainherit bool_ops));
func TestPgStatisticIndexInitialEntries(t *testing.T) {
	const (
		oidOpsID  uint32 = 1981
		int2OpsID uint32 = 1979
		boolOpsID uint32 = 1984
	)
	var got *pgIndexEntry
	for _, e := range pgIndexInitialEntries() {
		if e.IndexRelid == 2696 {
			eCopy := e
			got = &eCopy
			break
		}
	}
	if got == nil {
		t.Fatal("pgIndexInitialEntries missing OID 2696 (pg_statistic_relid_att_inh_index)")
	}
	if got.IndRelid != 2619 {
		t.Errorf("IndRelid=%d, want 2619 (pg_statistic)", got.IndRelid)
	}
	if !int16SliceEqual(got.IndKey, []int16{1, 2, 3}) {
		t.Errorf("IndKey=%v, want [1 2 3] (starelid, staattnum, stainherit)", got.IndKey)
	}
	if !uint32SliceEqual(got.IndClass, []uint32{oidOpsID, int2OpsID, boolOpsID}) {
		t.Errorf("IndClass=%v, want [oid_ops int2_ops bool_ops]=[%d %d %d]", got.IndClass, oidOpsID, int2OpsID, boolOpsID)
	}
	if !uint32SliceEqual(got.IndCollation, []uint32{0, 0, 0}) {
		t.Errorf("IndCollation=%v, want [0 0 0] (no collations on these opclasses)", got.IndCollation)
	}
	if !got.IsUnique {
		t.Errorf("IsUnique=false, want true (DECLARE_UNIQUE_INDEX_PKEY)")
	}
	if !got.IsPrimary {
		t.Errorf("IsPrimary=false, want true (_PKEY variant)")
	}
}

// TestPgStatisticAttrsTypeOIDsMatchPG18 pins the exact TypeOIDs and Len
// values for every column. Catches silent drift in pgStatisticAttrs.
func TestPgStatisticAttrsTypeOIDsMatchPG18(t *testing.T) {
	attrs := pgStatisticAttrs()
	want := []struct {
		Name    string
		TypeOID uint32
		Len     int16
		NotNull bool
	}{
		{"starelid", 26, 4, true},
		{"staattnum", 21, 2, true},
		{"stainherit", 16, 1, true},
		{"stanullfrac", 700, 4, true},
		{"stawidth", 23, 4, true},
		{"stadistinct", 700, 4, true},
		{"stakind1", 21, 2, true},
		{"stakind2", 21, 2, true},
		{"stakind3", 21, 2, true},
		{"stakind4", 21, 2, true},
		{"stakind5", 21, 2, true},
		{"staop1", 26, 4, true},
		{"staop2", 26, 4, true},
		{"staop3", 26, 4, true},
		{"staop4", 26, 4, true},
		{"staop5", 26, 4, true},
		{"stacoll1", 26, 4, true},
		{"stacoll2", 26, 4, true},
		{"stacoll3", 26, 4, true},
		{"stacoll4", 26, 4, true},
		{"stacoll5", 26, 4, true},
		{"stanumbers1", 1021, -1, false},
		{"stanumbers2", 1021, -1, false},
		{"stanumbers3", 1021, -1, false},
		{"stanumbers4", 1021, -1, false},
		{"stanumbers5", 1021, -1, false},
		{"stavalues1", 2277, -1, false},
		{"stavalues2", 2277, -1, false},
		{"stavalues3", 2277, -1, false},
		{"stavalues4", 2277, -1, false},
		{"stavalues5", 2277, -1, false},
	}
	if len(attrs) != len(want) {
		t.Fatalf("pgStatisticAttrs: len=%d, want %d", len(attrs), len(want))
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
