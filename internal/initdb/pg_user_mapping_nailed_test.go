package initdb

import "testing"

// TestNailedLocalRelsContainsPgUserMapping pins the pg_user_mapping heap
// entry (OID 1418) in nailedLocalRels so PG-standby boot's
// `RelationBuildDesc(1418)` finds a pg_class row instead of FATALing with
// `could not open relation with OID 1418` (M0106-0010 step 3cp).
// Schema sourced verbatim from PG18
// `postgres/src/include/catalog/pg_user_mapping.h` +
// pg_user_mapping_d.h: 4 columns total, three fixed-width 4-byte NOT NULL
// oid columns (oid/umuser/umserver) plus a CATALOG_VARLEN text[]
// (umoptions, typeoid 1009 = _text, nullable).
func TestNailedLocalRelsContainsPgUserMapping(t *testing.T) {
	var rel *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == 1418 {
			rel = &nailedLocalRels[i]
			break
		}
	}
	if rel == nil {
		t.Fatalf("nailedLocalRels missing OID 1418 (pg_user_mapping); PG-standby boot will FATAL with `could not open relation with OID 1418`")
	}
	if rel.RelName != "pg_user_mapping" {
		t.Errorf("OID 1418: RelName=%q, want pg_user_mapping", rel.RelName)
	}
	if rel.RelKind != 'r' {
		t.Errorf("OID 1418: RelKind=%q, want 'r' (regular heap)", rel.RelKind)
	}
	if rel.RelNatts != 4 {
		t.Errorf("OID 1418: RelNatts=%d, want 4 (oid+umuser+umserver+umoptions)", rel.RelNatts)
	}
	// RelType=83 is safe because pg_user_mapping is not formrdesc'd —
	// no UserMappingRelation_Rowtype_Id constant in PG18 headers.
	if rel.RelType != 83 {
		t.Errorf("OID 1418: RelType=%d, want 83", rel.RelType)
	}

	wantAttrs := []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "umuser", TypeOID: 26, Num: 2, Len: 4, NotNull: true},
		{Name: "umserver", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "umoptions", TypeOID: 1009, Num: 4, Len: -1, NotNull: false},
	}
	if len(rel.Attrs) != len(wantAttrs) {
		t.Fatalf("OID 1418: %d attrs, want %d", len(rel.Attrs), len(wantAttrs))
	}
	for i, w := range wantAttrs {
		got := rel.Attrs[i]
		if got.Name != w.Name || got.TypeOID != w.TypeOID || got.Num != w.Num || got.Len != w.Len || got.NotNull != w.NotNull {
			t.Errorf("OID 1418 attr[%d]: got %+v, want %+v", i, got, w)
		}
	}
}

// TestNailedLocalRelsContainsPgUserMappingIndexes pins both nailed index
// rows (174 = pg_user_mapping_oid_index, 175 =
// pg_user_mapping_user_server_index). Without these entries
// `RelationIdGetRelation(174|175)` FATALs because no pg_class row gets
// seeded (M0106-0010 step 3cp).
func TestNailedLocalRelsContainsPgUserMappingIndexes(t *testing.T) {
	cases := []struct {
		oid     uint32
		name    string
		relnatts int16
	}{
		{174, "pg_user_mapping_oid_index", 1},
		{175, "pg_user_mapping_user_server_index", 2},
	}
	for _, tc := range cases {
		var rel *nailedRel
		for i := range nailedLocalRels {
			if nailedLocalRels[i].OID == tc.oid {
				rel = &nailedLocalRels[i]
				break
			}
		}
		if rel == nil {
			t.Errorf("nailedLocalRels missing OID %d (%s)", tc.oid, tc.name)
			continue
		}
		if rel.RelName != tc.name {
			t.Errorf("OID %d: RelName=%q, want %q", tc.oid, rel.RelName, tc.name)
		}
		if rel.RelKind != 'i' {
			t.Errorf("OID %d: RelKind=%q, want 'i'", tc.oid, rel.RelKind)
		}
		if rel.RelNatts != tc.relnatts {
			t.Errorf("OID %d: RelNatts=%d, want %d", tc.oid, rel.RelNatts, tc.relnatts)
		}
	}
}

// TestPgUserMappingIndexInitialEntries pins the two pgIndexInitialEntries
// rows for the pg_user_mapping family. PG18 authoritative source:
//
//	postgres/src/include/catalog/pg_user_mapping.h:52-53
//	  DECLARE_UNIQUE_INDEX_PKEY(pg_user_mapping_oid_index, 174,
//	    UserMappingOidIndexId, pg_user_mapping, btree(oid oid_ops));
//	  DECLARE_UNIQUE_INDEX(pg_user_mapping_user_server_index, 175,
//	    UserMappingUserServerIndexId, pg_user_mapping,
//	    btree(umuser oid_ops, umserver oid_ops));
//
// attnums (pg_user_mapping_d.h): 1=oid, 2=umuser, 3=umserver, 4=umoptions.
// 174 is UNIQUE PRIMARY KEY on oid (attnum 1); 175 is UNIQUE (NOT PRIMARY)
// on (umuser, umserver) (attnums 2, 3).
func TestPgUserMappingIndexInitialEntries(t *testing.T) {
	entries := pgIndexInitialEntries()
	get := func(oid uint32) *pgIndexEntry {
		for i := range entries {
			if entries[i].IndexRelid == oid {
				return &entries[i]
			}
		}
		return nil
	}

	e174 := get(174)
	if e174 == nil {
		t.Fatalf("pgIndexInitialEntries missing OID 174 (pg_user_mapping_oid_index)")
	}
	if e174.IndRelid != 1418 {
		t.Errorf("OID 174: IndRelid=%d, want 1418 (pg_user_mapping)", e174.IndRelid)
	}
	if !int16SliceEqual(e174.IndKey, []int16{1}) {
		t.Errorf("OID 174: IndKey=%v, want [1] (oid attnum)", e174.IndKey)
	}
	if !e174.IsUnique || !e174.IsPrimary {
		t.Errorf("OID 174: IsUnique=%v, IsPrimary=%v; want both true (UNIQUE PRIMARY KEY)", e174.IsUnique, e174.IsPrimary)
	}

	e175 := get(175)
	if e175 == nil {
		t.Fatalf("pgIndexInitialEntries missing OID 175 (pg_user_mapping_user_server_index)")
	}
	if e175.IndRelid != 1418 {
		t.Errorf("OID 175: IndRelid=%d, want 1418 (pg_user_mapping)", e175.IndRelid)
	}
	if !int16SliceEqual(e175.IndKey, []int16{2, 3}) {
		t.Errorf("OID 175: IndKey=%v, want [2,3] (umuser, umserver)", e175.IndKey)
	}
	if !e175.IsUnique || e175.IsPrimary {
		t.Errorf("OID 175: IsUnique=%v, IsPrimary=%v; want IsUnique=true, IsPrimary=false (UNIQUE non-PRIMARY)", e175.IsUnique, e175.IsPrimary)
	}
}
