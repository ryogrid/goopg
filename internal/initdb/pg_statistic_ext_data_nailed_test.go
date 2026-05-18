package initdb

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestNailedLocalRelsContainsPgStatisticExtData pins the heap entry for
// pg_statistic_ext_data (OID 3429) plus every column descriptor. Verbatim
// against PG18 `postgres/src/include/catalog/pg_statistic_ext_data.h:31` and
// PostgreSQL 18.3 runtime pg_attribute lookup. M0106-0010 Step 3cc.
func TestNailedLocalRelsContainsPgStatisticExtData(t *testing.T) {
	var rel *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == 3429 {
			rel = &nailedLocalRels[i]
			break
		}
	}
	if rel == nil {
		t.Fatal("nailedLocalRels missing OID 3429 (pg_statistic_ext_data)")
	}
	if rel.RelName != "pg_statistic_ext_data" {
		t.Errorf("Name=%q, want pg_statistic_ext_data", rel.RelName)
	}
	if rel.RelType != 83 {
		t.Errorf("RelType=%d, want 83 (no StatisticExtDataRelation_Rowtype_Id in PG18)", rel.RelType)
	}
	if rel.RelKind != 'r' {
		t.Errorf("RelKind=%q, want r", rel.RelKind)
	}
	if rel.RelNatts != 6 {
		t.Errorf("RelNatts=%d, want 6", rel.RelNatts)
	}
	if rel.IsShared {
		t.Errorf("IsShared=true, want false (pg_statistic_ext_data is per-database)")
	}
	wantAttrs := []nailedAttr{
		{Name: "stxoid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "stxdinherit", TypeOID: 16, Num: 2, Len: 1, NotNull: true},
		{Name: "stxdndistinct", TypeOID: 3361, Num: 3, Len: -1, NotNull: false},
		{Name: "stxddependencies", TypeOID: 3402, Num: 4, Len: -1, NotNull: false},
		{Name: "stxdmcv", TypeOID: 5017, Num: 5, Len: -1, NotNull: false},
		{Name: "stxdexpr", TypeOID: 10028, Num: 6, Len: -1, NotNull: false},
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

// TestNailedLocalRelsContainsPgStatisticExtDataStxoidInhIndex pins the
// flattenRels-derived index entry for OID 3433. RelKind='i', RelNatts=2
// (composite key — first multi-column nailed index seeded in M0106-0010).
func TestNailedLocalRelsContainsPgStatisticExtDataStxoidInhIndex(t *testing.T) {
	all := append([]nailedRel{}, nailedSharedRels...)
	all = append(all, nailedLocalRels...)
	for _, r := range all {
		if r.OID != 3433 {
			continue
		}
		if r.RelName != "pg_statistic_ext_data_stxoid_inh_index" {
			t.Errorf("Name=%q, want pg_statistic_ext_data_stxoid_inh_index", r.RelName)
		}
		if r.RelKind != 'i' {
			t.Errorf("RelKind=%q, want i", r.RelKind)
		}
		if r.RelNatts != 2 {
			t.Errorf("RelNatts=%d, want 2 (composite index: stxoid + stxdinherit)", r.RelNatts)
		}
		return
	}
	t.Fatal("nailed-rel list missing OID 3433 (pg_statistic_ext_data_stxoid_inh_index)")
}

// TestBootstrapMappedLocalCatalogHeapsIncludesPgStatisticExtData pins
// that an 8-KiB initialised heap page is written at base/1/3429 and
// base/5/3429.
func TestBootstrapMappedLocalCatalogHeapsIncludesPgStatisticExtData(t *testing.T) {
	dir := t.TempDir()
	if err := bootstrapMappedLocalCatalogHeaps(dir); err != nil {
		t.Fatalf("bootstrapMappedLocalCatalogHeaps: %v", err)
	}
	for _, db := range []string{"base/1", "base/5"} {
		path := filepath.Join(dir, db, strconv.FormatUint(3429, 10))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if len(data) != storage.BlockSize {
			t.Fatalf("%s: len=%d, want %d", path, len(data), storage.BlockSize)
		}
		// Reject a zeroed page — InitPage must have stamped the header
		// so PG's mdopen returns a valid PageHeader.
		allZero := true
		for _, b := range data {
			if b != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			t.Fatalf("%s: page is all zero — InitPage was not applied", path)
		}
	}
}

// TestPgStatisticExtDataStxoidInhIndexInitialEntry pins the
// pgIndexInitialEntries row for OID 3433: composite (stxoid oid_ops,
// stxdinherit bool_ops) UNIQUE PRIMARY KEY over pg_statistic_ext_data
// heap OID 3429. First non-single-column nailed index seeded.
func TestPgStatisticExtDataStxoidInhIndexInitialEntry(t *testing.T) {
	for _, e := range pgIndexInitialEntries() {
		if e.IndexRelid != 3433 {
			continue
		}
		if e.IndRelid != 3429 {
			t.Errorf("IndRelid=%d, want 3429 (pg_statistic_ext_data)", e.IndRelid)
		}
		if len(e.IndKey) != 2 || e.IndKey[0] != 1 || e.IndKey[1] != 2 {
			t.Errorf("IndKey=%v, want [1 2] (stxoid + stxdinherit)", e.IndKey)
		}
		// 1981 = oid_ops, 1984 = bool_ops (btree).
		if len(e.IndClass) != 2 || e.IndClass[0] != 1981 || e.IndClass[1] != 1984 {
			t.Errorf("IndClass=%v, want [1981 1984] (oid_ops + bool_ops)", e.IndClass)
		}
		if len(e.IndCollation) != 2 || e.IndCollation[0] != 0 || e.IndCollation[1] != 0 {
			t.Errorf("IndCollation=%v, want [0 0]", e.IndCollation)
		}
		if !e.IsUnique {
			t.Error("IsUnique=false, want true")
		}
		if !e.IsPrimary {
			t.Error("IsPrimary=false, want true (DECLARE_UNIQUE_INDEX_PKEY)")
		}
		return
	}
	t.Fatal("pgIndexInitialEntries missing OID 3433 (pg_statistic_ext_data_stxoid_inh_index)")
}

// TestPgStatisticExtDataAttrsTypeOIDsMatchPG18 pins the exact TypeOIDs and
// Len values for every column against PostgreSQL 18.3 runtime pg_attribute
// lookup. A drift here would silently produce a wrong relcache init file.
func TestPgStatisticExtDataAttrsTypeOIDsMatchPG18(t *testing.T) {
	attrs := pgStatisticExtDataAttrs()
	want := []struct {
		Name    string
		TypeOID uint32
		Len     int16
		NotNull bool
	}{
		{"stxoid", 26, 4, true},
		{"stxdinherit", 16, 1, true},
		{"stxdndistinct", 3361, -1, false},
		{"stxddependencies", 3402, -1, false},
		{"stxdmcv", 5017, -1, false},
		{"stxdexpr", 10028, -1, false},
	}
	if len(attrs) != len(want) {
		t.Fatalf("pgStatisticExtDataAttrs: len=%d, want %d", len(attrs), len(want))
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

// TestPgTypeAlignAndStorageFor_pg_statisticArray pins that _pg_statistic
// (TypeOID 10028) reports typalign='d' and typstorage='x' to match PG18's
// runtime pg_type row. Drift here would corrupt the attstorage/attalign
// bytes of the stxdexpr column in pg_attribute.
func TestPgTypeAlignAndStorageFor_pg_statisticArray(t *testing.T) {
	if got := pgTypeAlignChar(10028); got != "d" {
		t.Errorf("pgTypeAlignChar(10028)=%q, want d", got)
	}
	if got := pgTypeStorageChar(10028); got != "x" {
		t.Errorf("pgTypeStorageChar(10028)=%q, want x", got)
	}
	for _, oid := range []uint32{3361, 3402, 5017} {
		if got := pgTypeStorageChar(oid); got != "x" {
			t.Errorf("pgTypeStorageChar(%d)=%q, want x (varlena EXTENDED)", oid, got)
		}
		if got := pgTypeAlignChar(oid); got != "i" {
			t.Errorf("pgTypeAlignChar(%d)=%q, want i", oid, got)
		}
	}
}
