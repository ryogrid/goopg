package initdb

import "testing"

// TestNailedLocalRelsContainsPgTsTemplate pins the heap entry for pg_ts_template
// (OID 3764) plus every column descriptor. Verbatim against PG18
// `postgres/src/include/catalog/pg_ts_template.h:29-44`
// (`CATALOG(pg_ts_template,3764,TSTemplateRelationId)`) and
// `pg_ts_template_d.h` (Anum_pg_ts_template_* 1..5, Natts_pg_ts_template == 5).
// M0106-0010 Step 3co.
func TestNailedLocalRelsContainsPgTsTemplate(t *testing.T) {
	var rel *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == 3764 {
			rel = &nailedLocalRels[i]
			break
		}
	}
	if rel == nil {
		t.Fatal("nailedLocalRels missing OID 3764 (pg_ts_template)")
	}
	if rel.RelName != "pg_ts_template" {
		t.Errorf("Name=%q, want pg_ts_template", rel.RelName)
	}
	if rel.RelType != 83 {
		t.Errorf("RelType=%d, want 83 (no TSTemplateRelation_Rowtype_Id in PG18)", rel.RelType)
	}
	if rel.RelKind != 'r' {
		t.Errorf("RelKind=%q, want r", rel.RelKind)
	}
	if rel.RelNatts != 5 {
		t.Errorf("RelNatts=%d, want 5 (Natts_pg_ts_template == 5)", rel.RelNatts)
	}
	if rel.IsShared {
		t.Errorf("IsShared=true, want false (pg_ts_template is per-database)")
	}
	wantAttrs := []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "tmplname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},
		{Name: "tmplnamespace", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "tmplinit", TypeOID: 24, Num: 4, Len: 4, NotNull: true},
		{Name: "tmpllexize", TypeOID: 24, Num: 5, Len: 4, NotNull: true},
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

// TestNailedLocalRelsContainsPgTsTemplateIndexes pins the two declared indexes
// of pg_ts_template as nailed local rels. PG's load_critical_index pass opens
// every declared index of a nailed rel; without these entries
// RelationIdGetRelation(3766)/RelationIdGetRelation(3767) FATAL because no
// pg_class row gets seeded.
//
//	3766 pg_ts_template_tmplname_index : UNIQUE
//	     btree(tmplname name_ops, tmplnamespace oid_ops)
//	3767 pg_ts_template_oid_index      : UNIQUE PRIMARY
//	     btree(oid oid_ops)
//
// Authoritative: postgres/src/include/catalog/pg_ts_template.h:48-49.
func TestNailedLocalRelsContainsPgTsTemplateIndexes(t *testing.T) {
	cases := []struct {
		oid      uint32
		name     string
		relnatts int16
	}{
		{3766, "pg_ts_template_tmplname_index", 2},
		{3767, "pg_ts_template_oid_index", 1},
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

// TestPgTsTemplateIndexInitialEntries pins the pgIndexInitialEntries rows for
// OIDs 3766 / 3767. IndKey / IndClass / IndCollation / IsUnique / IsPrimary
// verbatim from postgres/src/include/catalog/pg_ts_template.h:
//
//	DECLARE_UNIQUE_INDEX(pg_ts_template_tmplname_index, 3766,
//	    TSTemplateNameNspIndexId, pg_ts_template,
//	    btree(tmplname name_ops, tmplnamespace oid_ops));
//	DECLARE_UNIQUE_INDEX_PKEY(pg_ts_template_oid_index, 3767,
//	    TSTemplateOidIndexId, pg_ts_template, btree(oid oid_ops));
func TestPgTsTemplateIndexInitialEntries(t *testing.T) {
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
		3766: {3764, []int16{2, 3}, []uint32{nameOpsID, oidOpsID}, []uint32{cCollation, 0}, true, false},
		3767: {3764, []int16{1}, []uint32{oidOpsID}, []uint32{0}, true, true},
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

// TestPgTsTemplateAttrsTypeOIDsMatchPG18 pins the exact TypeOIDs and Len values
// for every column. Catches silent drift in pgTsTemplateAttrs.
func TestPgTsTemplateAttrsTypeOIDsMatchPG18(t *testing.T) {
	attrs := pgTsTemplateAttrs()
	want := []struct {
		Name    string
		TypeOID uint32
		Len     int16
		NotNull bool
	}{
		{"oid", 26, 4, true},
		{"tmplname", 19, 64, true},
		{"tmplnamespace", 26, 4, true},
		{"tmplinit", 24, 4, true},
		{"tmpllexize", 24, 4, true},
	}
	if len(attrs) != len(want) {
		t.Fatalf("pgTsTemplateAttrs: len=%d, want %d", len(attrs), len(want))
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
