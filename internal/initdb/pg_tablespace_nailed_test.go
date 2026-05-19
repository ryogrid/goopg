package initdb

import "testing"

// TestNailedSharedRelsContainsPgTablespace pins the heap entry for
// pg_tablespace (OID 1213) plus every column descriptor. Verbatim against
// PG18 `postgres/src/include/catalog/pg_tablespace.h:29-41`
// (`CATALOG(pg_tablespace,1213,TableSpaceRelationId) BKI_SHARED_RELATION`)
// and `pg_tablespace_d.h` (Anum_pg_tablespace_* 1..5,
// Natts_pg_tablespace == 5). M0106-0010 Step 3ch.
func TestNailedSharedRelsContainsPgTablespace(t *testing.T) {
	var rel *nailedRel
	for i := range nailedSharedRels {
		if nailedSharedRels[i].OID == 1213 {
			rel = &nailedSharedRels[i]
			break
		}
	}
	if rel == nil {
		t.Fatal("nailedSharedRels missing OID 1213 (pg_tablespace)")
	}
	if rel.RelName != "pg_tablespace" {
		t.Errorf("Name=%q, want pg_tablespace", rel.RelName)
	}
	if rel.RelType != 83 {
		t.Errorf("RelType=%d, want 83 (no TableSpaceRelation_Rowtype_Id in PG18)", rel.RelType)
	}
	if rel.RelKind != 'r' {
		t.Errorf("RelKind=%q, want r", rel.RelKind)
	}
	if rel.RelNatts != 5 {
		t.Errorf("RelNatts=%d, want 5 (Natts_pg_tablespace == 5)", rel.RelNatts)
	}
	if !rel.IsShared {
		t.Errorf("IsShared=false, want true (BKI_SHARED_RELATION)")
	}
	wantAttrs := []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "spcname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},
		{Name: "spcowner", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "spcacl", TypeOID: 1034, Num: 4, Len: -1, NotNull: false},
		{Name: "spcoptions", TypeOID: 1009, Num: 5, Len: -1, NotNull: false},
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

// TestNailedSharedRelsContainsPgTablespaceIndexes pins both declared
// indexes of pg_tablespace as nailed shared rels. PG's
// load_critical_index pass opens every declared index of a nailed rel;
// without these entries RelationIdGetRelation(2697)/(2698) FATALs because
// no pg_class row gets seeded.
//
//	2697 pg_tablespace_oid_index     : UNIQUE PRIMARY btree(oid oid_ops)
//	2698 pg_tablespace_spcname_index : UNIQUE         btree(spcname name_ops)
//
// Authoritative: postgres/src/include/catalog/pg_tablespace.h:52-53.
func TestNailedSharedRelsContainsPgTablespaceIndexes(t *testing.T) {
	cases := []struct {
		oid      uint32
		name     string
		relnatts int16
	}{
		{2697, "pg_tablespace_oid_index", 1},
		{2698, "pg_tablespace_spcname_index", 1},
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
	}
}

// TestPgTablespaceIndexInitialEntries pins the pgIndexInitialEntries rows
// for OID 2697 and 2698. IndKey / IndClass / IndCollation / IsUnique /
// IsPrimary verbatim from postgres/src/include/catalog/pg_tablespace.h:
//
//	DECLARE_UNIQUE_INDEX_PKEY(pg_tablespace_oid_index, 2697,
//	    TablespaceOidIndexId, pg_tablespace,
//	    btree(oid oid_ops));
//	DECLARE_UNIQUE_INDEX(pg_tablespace_spcname_index, 2698,
//	    TablespaceNameIndexId, pg_tablespace,
//	    btree(spcname name_ops));
func TestPgTablespaceIndexInitialEntries(t *testing.T) {
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
		2697: {1213, []int16{1}, []uint32{oidOpsID}, []uint32{0}, true, true},
		2698: {1213, []int16{2}, []uint32{nameOpsID}, []uint32{cCollation}, true, false},
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

// TestPgTablespaceAttrsTypeOIDsMatchPG18 pins the exact TypeOIDs and Len
// values for every column. Catches silent drift in pgTablespaceAttrs.
func TestPgTablespaceAttrsTypeOIDsMatchPG18(t *testing.T) {
	attrs := pgTablespaceAttrs()
	want := []struct {
		Name    string
		TypeOID uint32
		Len     int16
		NotNull bool
	}{
		{"oid", 26, 4, true},
		{"spcname", 19, 64, true},
		{"spcowner", 26, 4, true},
		{"spcacl", 1034, -1, false},
		{"spcoptions", 1009, -1, false},
	}
	if len(attrs) != len(want) {
		t.Fatalf("pgTablespaceAttrs: len=%d, want %d", len(attrs), len(want))
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
