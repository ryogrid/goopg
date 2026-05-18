package initdb

import "testing"

// TestNailedLocalRelsContainsPgTsConfigMap pins the heap entry for
// pg_ts_config_map (OID 3603) plus every column descriptor. Verbatim against
// PG18 `postgres/src/include/catalog/pg_ts_config_map.h:30-45`
// (`CATALOG(pg_ts_config_map,3603,TSConfigMapRelationId)`) and
// `pg_ts_config_map_d.h` (Anum_pg_ts_config_map_* 1..4,
// Natts_pg_ts_config_map == 4). M0106-0010 Step 3cj.
func TestNailedLocalRelsContainsPgTsConfigMap(t *testing.T) {
	var rel *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == 3603 {
			rel = &nailedLocalRels[i]
			break
		}
	}
	if rel == nil {
		t.Fatal("nailedLocalRels missing OID 3603 (pg_ts_config_map)")
	}
	if rel.RelName != "pg_ts_config_map" {
		t.Errorf("Name=%q, want pg_ts_config_map", rel.RelName)
	}
	if rel.RelType != 83 {
		t.Errorf("RelType=%d, want 83 (no TSConfigMapRelation_Rowtype_Id in PG18)", rel.RelType)
	}
	if rel.RelKind != 'r' {
		t.Errorf("RelKind=%q, want r", rel.RelKind)
	}
	if rel.RelNatts != 4 {
		t.Errorf("RelNatts=%d, want 4 (Natts_pg_ts_config_map == 4)", rel.RelNatts)
	}
	if rel.IsShared {
		t.Errorf("IsShared=true, want false (pg_ts_config_map is per-database)")
	}
	wantAttrs := []nailedAttr{
		{Name: "mapcfg", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "maptokentype", TypeOID: 23, Num: 2, Len: 4, NotNull: true},
		{Name: "mapseqno", TypeOID: 23, Num: 3, Len: 4, NotNull: true},
		{Name: "mapdict", TypeOID: 26, Num: 4, Len: 4, NotNull: true},
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

// TestNailedLocalRelsContainsPgTsConfigMapIndexes pins the single declared
// index of pg_ts_config_map as a nailed local rel. PG's load_critical_index
// pass opens every declared index of a nailed rel; without this entry
// RelationIdGetRelation(3609) FATALs because no pg_class row gets seeded.
//
//	3609 pg_ts_config_map_index : UNIQUE PRIMARY
//	     btree(mapcfg oid_ops, maptokentype int4_ops, mapseqno int4_ops)
//
// Authoritative: postgres/src/include/catalog/pg_ts_config_map.h:48.
func TestNailedLocalRelsContainsPgTsConfigMapIndexes(t *testing.T) {
	cases := []struct {
		oid      uint32
		name     string
		relnatts int16
	}{
		{3609, "pg_ts_config_map_index", 3},
	}
	for _, c := range cases {
		var rel *nailedRel
		for i := range nailedLocalRels {
			if nailedLocalRels[i].OID == c.oid {
				rel = &nailedLocalRels[i]
				break
			}
		}
		if rel == nil {
			t.Errorf("nailedLocalRels missing OID %d (%s)", c.oid, c.name)
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
	}
}

// TestPgTsConfigMapIndexInitialEntries pins the pgIndexInitialEntries row for
// OID 3609. IndKey / IndClass / IndCollation / IsUnique / IsPrimary verbatim
// from postgres/src/include/catalog/pg_ts_config_map.h:
//
//	DECLARE_UNIQUE_INDEX_PKEY(pg_ts_config_map_index, 3609,
//	    TSConfigMapIndexId, pg_ts_config_map,
//	    btree(mapcfg oid_ops, maptokentype int4_ops, mapseqno int4_ops));
func TestPgTsConfigMapIndexInitialEntries(t *testing.T) {
	const (
		oidOpsID  uint32 = 1981
		int4OpsID uint32 = 1978
	)
	type want struct {
		indrelid  uint32
		indkey    []int16
		indclass  []uint32
		indcoll   []uint32
		isUnique  bool
		isPrimary bool
	}
	wantMap := map[uint32]want{
		3609: {3603, []int16{1, 2, 3}, []uint32{oidOpsID, int4OpsID, int4OpsID}, []uint32{0, 0, 0}, true, true},
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

// TestPgTsConfigMapAttrsTypeOIDsMatchPG18 pins the exact TypeOIDs and Len
// values for every column. Catches silent drift in pgTsConfigMapAttrs.
func TestPgTsConfigMapAttrsTypeOIDsMatchPG18(t *testing.T) {
	attrs := pgTsConfigMapAttrs()
	want := []struct {
		Name    string
		TypeOID uint32
		Len     int16
		NotNull bool
	}{
		{"mapcfg", 26, 4, true},
		{"maptokentype", 23, 4, true},
		{"mapseqno", 23, 4, true},
		{"mapdict", 26, 4, true},
	}
	if len(attrs) != len(want) {
		t.Fatalf("pgTsConfigMapAttrs: len=%d, want %d", len(attrs), len(want))
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
