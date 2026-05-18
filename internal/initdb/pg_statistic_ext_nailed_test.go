package initdb

import (
	"testing"
)

// TestNailedLocalRelsContainsPgStatisticExt pins the heap entry for
// pg_statistic_ext (OID 3381) plus every column descriptor. Verbatim
// against PG18 `postgres/src/include/catalog/pg_statistic_ext.h:33`
// (`CATALOG(pg_statistic_ext,3381,StatisticExtRelationId)`) and
// `pg_statistic_ext_d.h:28..38` (Anum_pg_statistic_ext_* 1..9,
// Natts == 9). M0106-0010 Step 3cd.
func TestNailedLocalRelsContainsPgStatisticExt(t *testing.T) {
	var rel *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == 3381 {
			rel = &nailedLocalRels[i]
			break
		}
	}
	if rel == nil {
		t.Fatal("nailedLocalRels missing OID 3381 (pg_statistic_ext)")
	}
	if rel.RelName != "pg_statistic_ext" {
		t.Errorf("Name=%q, want pg_statistic_ext", rel.RelName)
	}
	if rel.RelType != 83 {
		t.Errorf("RelType=%d, want 83 (no StatisticExtRelation_Rowtype_Id in PG18)", rel.RelType)
	}
	if rel.RelKind != 'r' {
		t.Errorf("RelKind=%q, want r", rel.RelKind)
	}
	if rel.RelNatts != 9 {
		t.Errorf("RelNatts=%d, want 9 (Natts_pg_statistic_ext == 9)", rel.RelNatts)
	}
	if rel.IsShared {
		t.Errorf("IsShared=true, want false (pg_statistic_ext is per-database)")
	}
	wantAttrs := []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "stxrelid", TypeOID: 26, Num: 2, Len: 4, NotNull: true},
		{Name: "stxname", TypeOID: 19, Num: 3, Len: 64, NotNull: true},
		{Name: "stxnamespace", TypeOID: 26, Num: 4, Len: 4, NotNull: true},
		{Name: "stxowner", TypeOID: 26, Num: 5, Len: 4, NotNull: true},
		{Name: "stxkeys", TypeOID: 22, Num: 6, Len: -1, NotNull: true},
		{Name: "stxstattarget", TypeOID: 21, Num: 7, Len: 2, NotNull: false},
		{Name: "stxkind", TypeOID: 1002, Num: 8, Len: -1, NotNull: true},
		{Name: "stxexprs", TypeOID: 194, Num: 9, Len: -1, NotNull: false},
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

// TestNailedLocalRelsContainsPgStatisticExtIndexes pins the
// flattenRels-derived index entries for OIDs 3380, 3997, 3379. Each
// must appear as RelKind='i' with the correct indnatts so
// RelationInitIndexAccessInfo's relnatts/indnatts check
// (relcache.c:1492) is satisfied.
func TestNailedLocalRelsContainsPgStatisticExtIndexes(t *testing.T) {
	want := map[uint32]struct {
		name  string
		natts int16
	}{
		3380: {"pg_statistic_ext_oid_index", 1},   // UNIQUE PRIMARY single oid_ops
		3997: {"pg_statistic_ext_name_index", 2},  // UNIQUE composite name_ops + oid_ops
		3379: {"pg_statistic_ext_relid_index", 1}, // NON-UNIQUE single oid_ops
	}
	seen := map[uint32]bool{}
	all := append([]nailedRel{}, nailedSharedRels...)
	all = append(all, nailedLocalRels...)
	for _, r := range all {
		w, ok := want[r.OID]
		if !ok {
			continue
		}
		seen[r.OID] = true
		if r.RelName != w.name {
			t.Errorf("OID %d: Name=%q, want %s", r.OID, r.RelName, w.name)
		}
		if r.RelKind != 'i' {
			t.Errorf("OID %d: RelKind=%q, want i", r.OID, r.RelKind)
		}
		if r.RelNatts != w.natts {
			t.Errorf("OID %d: RelNatts=%d, want %d", r.OID, r.RelNatts, w.natts)
		}
	}
	for oid := range want {
		if !seen[oid] {
			t.Errorf("nailed-rel list missing OID %d (%s)", oid, want[oid].name)
		}
	}
}

// TestPgStatisticExtIndexInitialEntries pins the pgIndexInitialEntries
// rows for the three pg_statistic_ext indexes. Each entry's IndKey /
// IndClass / IndCollation / IsUnique / IsPrimary is taken verbatim
// from `postgres/src/include/catalog/pg_statistic_ext.h:73..75`.
func TestPgStatisticExtIndexInitialEntries(t *testing.T) {
	const (
		oidOpsID    = uint32(1981)
		nameOpsID   = uint32(1986)
		cCollation  = uint32(950)
	)
	want := map[uint32]struct {
		indrelid   uint32
		key        []int16
		class      []uint32
		collation  []uint32
		isUnique   bool
		isPrimary  bool
	}{
		3380: {3381, []int16{1}, []uint32{oidOpsID}, []uint32{0}, true, true},
		3997: {3381, []int16{3, 4}, []uint32{nameOpsID, oidOpsID}, []uint32{cCollation, 0}, true, false},
		3379: {3381, []int16{2}, []uint32{oidOpsID}, []uint32{0}, false, false},
	}
	seen := map[uint32]bool{}
	for _, e := range pgIndexInitialEntries() {
		w, ok := want[e.IndexRelid]
		if !ok {
			continue
		}
		seen[e.IndexRelid] = true
		if e.IndRelid != w.indrelid {
			t.Errorf("OID %d: IndRelid=%d, want %d (pg_statistic_ext)", e.IndexRelid, e.IndRelid, w.indrelid)
		}
		if !int16SliceEqual(e.IndKey, w.key) {
			t.Errorf("OID %d: IndKey=%v, want %v", e.IndexRelid, e.IndKey, w.key)
		}
		if !uint32SliceEqual(e.IndClass, w.class) {
			t.Errorf("OID %d: IndClass=%v, want %v", e.IndexRelid, e.IndClass, w.class)
		}
		if !uint32SliceEqual(e.IndCollation, w.collation) {
			t.Errorf("OID %d: IndCollation=%v, want %v", e.IndexRelid, e.IndCollation, w.collation)
		}
		if e.IsUnique != w.isUnique {
			t.Errorf("OID %d: IsUnique=%v, want %v", e.IndexRelid, e.IsUnique, w.isUnique)
		}
		if e.IsPrimary != w.isPrimary {
			t.Errorf("OID %d: IsPrimary=%v, want %v", e.IndexRelid, e.IsPrimary, w.isPrimary)
		}
	}
	for oid := range want {
		if !seen[oid] {
			t.Errorf("pgIndexInitialEntries missing OID %d", oid)
		}
	}
}

// TestPgStatisticExtAttrsTypeOIDsMatchPG18 pins the exact TypeOIDs and
// Len values for every column against PostgreSQL 18.3 runtime
// pg_attribute. A drift would silently produce a wrong relcache init
// file.
func TestPgStatisticExtAttrsTypeOIDsMatchPG18(t *testing.T) {
	attrs := pgStatisticExtAttrs()
	want := []struct {
		Name    string
		TypeOID uint32
		Len     int16
		NotNull bool
	}{
		{"oid", 26, 4, true},
		{"stxrelid", 26, 4, true},
		{"stxname", 19, 64, true},
		{"stxnamespace", 26, 4, true},
		{"stxowner", 26, 4, true},
		{"stxkeys", 22, -1, true},
		{"stxstattarget", 21, 2, false},
		{"stxkind", 1002, -1, true},
		{"stxexprs", 194, -1, false},
	}
	if len(attrs) != len(want) {
		t.Fatalf("pgStatisticExtAttrs: len=%d, want %d", len(attrs), len(want))
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

func uint32SliceEqual(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
