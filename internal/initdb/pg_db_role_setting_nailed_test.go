package initdb

import "testing"

// M0106-0010 Step 3cu pins the pg_db_role_setting catalog seed.
//
// PG18 declares:
//   postgres/src/include/catalog/pg_db_role_setting.h:34
//     CATALOG(pg_db_role_setting, 2964, DbRoleSettingRelationId)
//       BKI_SHARED_RELATION
//       { Oid setdatabase; Oid setrole; #ifdef CATALOG_VARLEN text setconfig[1]; }
//
//   postgres/src/include/catalog/pg_db_role_setting.h:51
//     DECLARE_UNIQUE_INDEX_PKEY(pg_db_role_setting_databaseid_rol_index,
//       2965, DbRoleSettingDatidRolidIndexId, pg_db_role_setting,
//       btree(setdatabase oid_ops, setrole oid_ops));
//
// `process_settings(MyDatabaseId, GetSessionUserId())` runs at the end of
// InitPostgres (right after CheckMyDatabase returns) and opens this
// catalog. Without a pg_class row for 2964 every backend FATALs with
// `could not open relation with OID 2964`.

func TestNailedSharedRelsContainsPgDbRoleSetting(t *testing.T) {
	const oid = uint32(2964)
	var found nailedRel
	for _, r := range nailedSharedRels {
		if r.OID == oid {
			found = r
			break
		}
	}
	if found.OID == 0 {
		t.Fatalf("nailedSharedRels: OID %d (pg_db_role_setting) missing — Step 3cu", oid)
	}
	if found.RelName != "pg_db_role_setting" {
		t.Errorf("nailedSharedRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_db_role_setting")
	}
	if found.RelKind != 'r' {
		t.Errorf("nailedSharedRels[%d] RelKind=%q, want 'r'", oid, found.RelKind)
	}
	if found.RelNatts != 3 {
		t.Errorf("nailedSharedRels[%d] RelNatts=%d, want 3", oid, found.RelNatts)
	}
	if found.RelType != 83 {
		t.Errorf("nailedSharedRels[%d] RelType=%d, want 83 (pg_db_role_setting is not formrdesc'd)", oid, found.RelType)
	}
	// Spot-check schema against pg_db_role_setting_d.h authoritative attnums.
	want := []struct {
		Name    string
		TypeOID uint32
		Num     int16
		Len     int16
		NotNull bool
	}{
		{"setdatabase", 26, 1, 4, true},
		{"setrole", 26, 2, 4, true},
		{"setconfig", 1009, 3, -1, false}, // text[], CATALOG_VARLEN
	}
	if len(found.Attrs) != len(want) {
		t.Fatalf("nailedSharedRels[%d] Attrs len=%d, want %d", oid, len(found.Attrs), len(want))
	}
	for i, w := range want {
		got := found.Attrs[i]
		if got.Name != w.Name || got.TypeOID != w.TypeOID || got.Num != w.Num || got.Len != w.Len || got.NotNull != w.NotNull {
			t.Errorf("nailedSharedRels[%d] Attrs[%d] = %+v, want %+v", oid, i, got, w)
		}
	}
}

func TestNailedSharedRelsContainsPgDbRoleSettingDatabaseidRolIndex(t *testing.T) {
	const oid = uint32(2965)
	var found nailedRel
	for _, r := range nailedSharedRels {
		if r.OID == oid {
			found = r
			break
		}
	}
	if found.OID == 0 {
		t.Fatalf("nailedSharedRels: OID %d (pg_db_role_setting_databaseid_rol_index) missing — Step 3cu", oid)
	}
	if found.RelName != "pg_db_role_setting_databaseid_rol_index" {
		t.Errorf("nailedSharedRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_db_role_setting_databaseid_rol_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedSharedRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 2 {
		t.Errorf("nailedSharedRels[%d] RelNatts=%d, want 2 (btree(setdatabase, setrole))", oid, found.RelNatts)
	}
}

func TestPgDbRoleSettingDatabaseidRolIndexSeededFromInitialEntries(t *testing.T) {
	const oid = uint32(2965)
	var found pgIndexEntry
	for _, e := range pgIndexInitialEntries() {
		if e.IndexRelid == oid {
			found = e
			break
		}
	}
	if found.IndexRelid == 0 {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_db_role_setting_databaseid_rol_index) missing — Step 3cu", oid)
	}
	if found.IndRelid != 2964 {
		t.Errorf("OID %d: IndRelid=%d, want 2964 (pg_db_role_setting heap OID)", oid, found.IndRelid)
	}
	wantKey := []int16{1, 2}
	if len(found.IndKey) != len(wantKey) {
		t.Fatalf("OID %d: IndKey=%v, want %v", oid, found.IndKey, wantKey)
	}
	for i, k := range wantKey {
		if found.IndKey[i] != k {
			t.Errorf("OID %d: IndKey[%d]=%d, want %d", oid, i, found.IndKey[i], k)
		}
	}
	if !found.IsUnique {
		t.Errorf("OID %d: IsUnique=false, want true (DECLARE_UNIQUE_INDEX_PKEY)", oid)
	}
	if !found.IsPrimary {
		t.Errorf("OID %d: IsPrimary=false, want true (DECLARE_UNIQUE_INDEX_PKEY)", oid)
	}
}
