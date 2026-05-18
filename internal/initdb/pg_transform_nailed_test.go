package initdb

import "testing"

// TestNailedLocalRelsContainsPgTransform pins the heap entry for pg_transform
// (OID 3576) plus every column descriptor. Verbatim against PG18
// `postgres/src/include/catalog/pg_transform.h:29-36`
// (`CATALOG(pg_transform,3576,TransformRelationId)`) and `pg_transform_d.h`
// (Anum_pg_transform_* 1..5, Natts_pg_transform == 5). M0106-0010 Step 3ci.
func TestNailedLocalRelsContainsPgTransform(t *testing.T) {
	var rel *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == 3576 {
			rel = &nailedLocalRels[i]
			break
		}
	}
	if rel == nil {
		t.Fatal("nailedLocalRels missing OID 3576 (pg_transform)")
	}
	if rel.RelName != "pg_transform" {
		t.Errorf("Name=%q, want pg_transform", rel.RelName)
	}
	if rel.RelType != 83 {
		t.Errorf("RelType=%d, want 83 (no TransformRelation_Rowtype_Id in PG18)", rel.RelType)
	}
	if rel.RelKind != 'r' {
		t.Errorf("RelKind=%q, want r", rel.RelKind)
	}
	if rel.RelNatts != 5 {
		t.Errorf("RelNatts=%d, want 5 (Natts_pg_transform == 5)", rel.RelNatts)
	}
	if rel.IsShared {
		t.Errorf("IsShared=true, want false (pg_transform is per-database)")
	}
	wantAttrs := []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "trftype", TypeOID: 26, Num: 2, Len: 4, NotNull: true},
		{Name: "trflang", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "trffromsql", TypeOID: 24, Num: 4, Len: 4, NotNull: true},
		{Name: "trftosql", TypeOID: 24, Num: 5, Len: 4, NotNull: true},
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

// TestNailedLocalRelsContainsPgTransformIndexes pins both declared indexes of
// pg_transform as nailed local rels. PG's load_critical_index pass opens
// every declared index of a nailed rel; without these entries
// RelationIdGetRelation(3574)/(3575) FATALs because no pg_class row gets
// seeded.
//
//	3574 pg_transform_oid_index        : UNIQUE PRIMARY btree(oid oid_ops)
//	3575 pg_transform_type_lang_index  : UNIQUE         btree(trftype oid_ops, trflang oid_ops)
//
// Authoritative: postgres/src/include/catalog/pg_transform.h:43-44.
func TestNailedLocalRelsContainsPgTransformIndexes(t *testing.T) {
	cases := []struct {
		oid      uint32
		name     string
		relnatts int16
	}{
		{3574, "pg_transform_oid_index", 1},
		{3575, "pg_transform_type_lang_index", 2},
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

// TestPgTransformIndexInitialEntries pins the pgIndexInitialEntries rows for
// OID 3574 and 3575. IndKey / IndClass / IndCollation / IsUnique / IsPrimary
// verbatim from postgres/src/include/catalog/pg_transform.h:
//
//	DECLARE_UNIQUE_INDEX_PKEY(pg_transform_oid_index, 3574,
//	    TransformOidIndexId, pg_transform,
//	    btree(oid oid_ops));
//	DECLARE_UNIQUE_INDEX(pg_transform_type_lang_index, 3575,
//	    TransformTypeLangIndexId, pg_transform,
//	    btree(trftype oid_ops, trflang oid_ops));
func TestPgTransformIndexInitialEntries(t *testing.T) {
	const (
		oidOpsID uint32 = 1981
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
		3574: {3576, []int16{1}, []uint32{oidOpsID}, []uint32{0}, true, true},
		3575: {3576, []int16{2, 3}, []uint32{oidOpsID, oidOpsID}, []uint32{0, 0}, true, false},
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

// TestPgTransformAttrsTypeOIDsMatchPG18 pins the exact TypeOIDs and Len values
// for every column. Catches silent drift in pgTransformAttrs.
func TestPgTransformAttrsTypeOIDsMatchPG18(t *testing.T) {
	attrs := pgTransformAttrs()
	want := []struct {
		Name    string
		TypeOID uint32
		Len     int16
		NotNull bool
	}{
		{"oid", 26, 4, true},
		{"trftype", 26, 4, true},
		{"trflang", 26, 4, true},
		{"trffromsql", 24, 4, true},
		{"trftosql", 24, 4, true},
	}
	if len(attrs) != len(want) {
		t.Fatalf("pgTransformAttrs: len=%d, want %d", len(attrs), len(want))
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
