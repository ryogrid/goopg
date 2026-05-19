package initdb

import "testing"

// TestNailedLocalRelsContainsPgTsDict pins the heap entry for pg_ts_dict
// (OID 3600) plus every column descriptor. Verbatim against PG18
// `postgres/src/include/catalog/pg_ts_dict.h:29-50`
// (`CATALOG(pg_ts_dict,3600,TSDictionaryRelationId)`) and `pg_ts_dict_d.h`
// (Anum_pg_ts_dict_* 1..6, Natts_pg_ts_dict == 6). M0106-0010 Step 3cm.
func TestNailedLocalRelsContainsPgTsDict(t *testing.T) {
	var rel *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == 3600 {
			rel = &nailedLocalRels[i]
			break
		}
	}
	if rel == nil {
		t.Fatal("nailedLocalRels missing OID 3600 (pg_ts_dict)")
	}
	if rel.RelName != "pg_ts_dict" {
		t.Errorf("Name=%q, want pg_ts_dict", rel.RelName)
	}
	if rel.RelType != 83 {
		t.Errorf("RelType=%d, want 83 (no TSDictionaryRelation_Rowtype_Id in PG18)", rel.RelType)
	}
	if rel.RelKind != 'r' {
		t.Errorf("RelKind=%q, want r", rel.RelKind)
	}
	if rel.RelNatts != 6 {
		t.Errorf("RelNatts=%d, want 6 (Natts_pg_ts_dict == 6)", rel.RelNatts)
	}
	if rel.IsShared {
		t.Errorf("IsShared=true, want false (pg_ts_dict is per-database)")
	}
	wantAttrs := []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "dictname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},
		{Name: "dictnamespace", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "dictowner", TypeOID: 26, Num: 4, Len: 4, NotNull: true},
		{Name: "dicttemplate", TypeOID: 26, Num: 5, Len: 4, NotNull: true},
		{Name: "dictinitoption", TypeOID: 25, Num: 6, Len: -1, NotNull: false},
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

// TestNailedLocalRelsContainsPgTsDictIndexes pins the two declared indexes
// of pg_ts_dict as nailed local rels. PG's load_critical_index pass opens
// every declared index of a nailed rel; without these entries
// RelationIdGetRelation(3604)/RelationIdGetRelation(3605) FATAL because no
// pg_class row gets seeded.
//
//	3604 pg_ts_dict_dictname_index : UNIQUE
//	     btree(dictname name_ops, dictnamespace oid_ops)
//	3605 pg_ts_dict_oid_index      : UNIQUE PRIMARY
//	     btree(oid oid_ops)
//
// Authoritative: postgres/src/include/catalog/pg_ts_dict.h:56-57.
func TestNailedLocalRelsContainsPgTsDictIndexes(t *testing.T) {
	cases := []struct {
		oid      uint32
		name     string
		relnatts int16
	}{
		{3604, "pg_ts_dict_dictname_index", 2},
		{3605, "pg_ts_dict_oid_index", 1},
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

// TestPgTsDictIndexInitialEntries pins the pgIndexInitialEntries rows for
// OIDs 3604 / 3605. IndKey / IndClass / IndCollation / IsUnique / IsPrimary
// verbatim from postgres/src/include/catalog/pg_ts_dict.h:
//
//	DECLARE_UNIQUE_INDEX(pg_ts_dict_dictname_index, 3604,
//	    TSDictionaryNameNspIndexId, pg_ts_dict,
//	    btree(dictname name_ops, dictnamespace oid_ops));
//	DECLARE_UNIQUE_INDEX_PKEY(pg_ts_dict_oid_index, 3605,
//	    TSDictionaryOidIndexId, pg_ts_dict, btree(oid oid_ops));
func TestPgTsDictIndexInitialEntries(t *testing.T) {
	const (
		oidOpsID   uint32 = 1981
		nameOpsID  uint32 = 1986
		cCollation uint32 = 950
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
		3604: {3600, []int16{2, 3}, []uint32{nameOpsID, oidOpsID}, []uint32{cCollation, 0}, true, false},
		3605: {3600, []int16{1}, []uint32{oidOpsID}, []uint32{0}, true, true},
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

// TestPgTsDictAttrsTypeOIDsMatchPG18 pins the exact TypeOIDs and Len values
// for every column. Catches silent drift in pgTsDictAttrs.
func TestPgTsDictAttrsTypeOIDsMatchPG18(t *testing.T) {
	attrs := pgTsDictAttrs()
	want := []struct {
		Name    string
		TypeOID uint32
		Len     int16
		NotNull bool
	}{
		{"oid", 26, 4, true},
		{"dictname", 19, 64, true},
		{"dictnamespace", 26, 4, true},
		{"dictowner", 26, 4, true},
		{"dicttemplate", 26, 4, true},
		{"dictinitoption", 25, -1, false},
	}
	if len(attrs) != len(want) {
		t.Fatalf("pgTsDictAttrs: len=%d, want %d", len(attrs), len(want))
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
