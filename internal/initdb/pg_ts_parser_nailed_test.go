package initdb

import "testing"

// TestNailedLocalRelsContainsPgTsParser pins the heap entry for pg_ts_parser
// (OID 3601) plus every column descriptor. Verbatim against PG18
// `postgres/src/include/catalog/pg_ts_parser.h:29-54`
// (`CATALOG(pg_ts_parser,3601,TSParserRelationId)`) and `pg_ts_parser_d.h`
// (Anum_pg_ts_parser_* 1..8, Natts_pg_ts_parser == 8). M0106-0010 Step 3cn.
func TestNailedLocalRelsContainsPgTsParser(t *testing.T) {
	var rel *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == 3601 {
			rel = &nailedLocalRels[i]
			break
		}
	}
	if rel == nil {
		t.Fatal("nailedLocalRels missing OID 3601 (pg_ts_parser)")
	}
	if rel.RelName != "pg_ts_parser" {
		t.Errorf("Name=%q, want pg_ts_parser", rel.RelName)
	}
	if rel.RelType != 83 {
		t.Errorf("RelType=%d, want 83 (no TSParserRelation_Rowtype_Id in PG18)", rel.RelType)
	}
	if rel.RelKind != 'r' {
		t.Errorf("RelKind=%q, want r", rel.RelKind)
	}
	if rel.RelNatts != 8 {
		t.Errorf("RelNatts=%d, want 8 (Natts_pg_ts_parser == 8)", rel.RelNatts)
	}
	if rel.IsShared {
		t.Errorf("IsShared=true, want false (pg_ts_parser is per-database)")
	}
	wantAttrs := []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "prsname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},
		{Name: "prsnamespace", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "prsstart", TypeOID: 24, Num: 4, Len: 4, NotNull: true},
		{Name: "prstoken", TypeOID: 24, Num: 5, Len: 4, NotNull: true},
		{Name: "prsend", TypeOID: 24, Num: 6, Len: 4, NotNull: true},
		{Name: "prsheadline", TypeOID: 24, Num: 7, Len: 4, NotNull: true},
		{Name: "prslextype", TypeOID: 24, Num: 8, Len: 4, NotNull: true},
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

// TestNailedLocalRelsContainsPgTsParserIndexes pins the two declared indexes
// of pg_ts_parser as nailed local rels. PG's load_critical_index pass opens
// every declared index of a nailed rel; without these entries
// RelationIdGetRelation(3606)/RelationIdGetRelation(3607) FATAL because no
// pg_class row gets seeded.
//
//	3606 pg_ts_parser_prsname_index : UNIQUE
//	     btree(prsname name_ops, prsnamespace oid_ops)
//	3607 pg_ts_parser_oid_index     : UNIQUE PRIMARY
//	     btree(oid oid_ops)
//
// Authoritative: postgres/src/include/catalog/pg_ts_parser.h:56-57.
func TestNailedLocalRelsContainsPgTsParserIndexes(t *testing.T) {
	cases := []struct {
		oid      uint32
		name     string
		relnatts int16
	}{
		{3606, "pg_ts_parser_prsname_index", 2},
		{3607, "pg_ts_parser_oid_index", 1},
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

// TestPgTsParserIndexInitialEntries pins the pgIndexInitialEntries rows for
// OIDs 3606 / 3607. IndKey / IndClass / IndCollation / IsUnique / IsPrimary
// verbatim from postgres/src/include/catalog/pg_ts_parser.h:
//
//	DECLARE_UNIQUE_INDEX(pg_ts_parser_prsname_index, 3606,
//	    TSParserNameNspIndexId, pg_ts_parser,
//	    btree(prsname name_ops, prsnamespace oid_ops));
//	DECLARE_UNIQUE_INDEX_PKEY(pg_ts_parser_oid_index, 3607,
//	    TSParserOidIndexId, pg_ts_parser, btree(oid oid_ops));
func TestPgTsParserIndexInitialEntries(t *testing.T) {
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
		3606: {3601, []int16{2, 3}, []uint32{nameOpsID, oidOpsID}, []uint32{cCollation, 0}, true, false},
		3607: {3601, []int16{1}, []uint32{oidOpsID}, []uint32{0}, true, true},
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

// TestPgTsParserAttrsTypeOIDsMatchPG18 pins the exact TypeOIDs and Len values
// for every column. Catches silent drift in pgTsParserAttrs.
func TestPgTsParserAttrsTypeOIDsMatchPG18(t *testing.T) {
	attrs := pgTsParserAttrs()
	want := []struct {
		Name    string
		TypeOID uint32
		Len     int16
		NotNull bool
	}{
		{"oid", 26, 4, true},
		{"prsname", 19, 64, true},
		{"prsnamespace", 26, 4, true},
		{"prsstart", 24, 4, true},
		{"prstoken", 24, 4, true},
		{"prsend", 24, 4, true},
		{"prsheadline", 24, 4, true},
		{"prslextype", 24, 4, true},
	}
	if len(attrs) != len(want) {
		t.Fatalf("pgTsParserAttrs: len=%d, want %d", len(attrs), len(want))
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
