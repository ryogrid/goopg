package initdb

import "testing"

// TestNailedSharedRelsContainsPgReplicationOrigin guards M0106-0010 step 3ca.
// PG-standby boot opens `pg_replication_origin` (OID 6000, BKI_SHARED_RELATION)
// during early backend startup once Step 3bz cleared the pg_range family.
// Without a pg_class row, every forked backend FATALs with `could not open
// relation with OID 6000`. The fix adds an entry to `nailedSharedRels`; this
// test rejects silent removal that would re-introduce the FATAL and pins
// every column against `postgres/src/include/catalog/pg_replication_origin.h`
// (PG18) and `pg_replication_origin_d.h` authoritative definitions.
func TestNailedSharedRelsContainsPgReplicationOrigin(t *testing.T) {
	const replicationOriginOID = 6000

	var got *nailedRel
	for i := range nailedSharedRels {
		if nailedSharedRels[i].OID == replicationOriginOID {
			got = &nailedSharedRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedSharedRels missing OID %d (pg_replication_origin) — step 3ca regression", replicationOriginOID)
	}
	if got.RelName != "pg_replication_origin" {
		t.Fatalf("OID %d: RelName=%q want %q", replicationOriginOID, got.RelName, "pg_replication_origin")
	}
	if got.RelKind != 'r' {
		t.Fatalf("OID %d: RelKind=%q want 'r'", replicationOriginOID, got.RelKind)
	}
	if !got.IsShared {
		t.Fatalf("OID %d: IsShared=false want true (pg_replication_origin is BKI_SHARED_RELATION)", replicationOriginOID)
	}
	// PG18 pg_replication_origin_d.h: Natts_pg_replication_origin = 2.
	if got.RelNatts != 2 {
		t.Fatalf("OID %d: RelNatts=%d want 2 (PG18 pg_replication_origin column count)", replicationOriginOID, got.RelNatts)
	}
	if len(got.Attrs) != 2 {
		t.Fatalf("OID %d: len(Attrs)=%d want 2", replicationOriginOID, len(got.Attrs))
	}
	// RelType=83 placeholder is safe because pg_replication_origin is not
	// formrdesc'd (no ReplicationOriginRelation_Rowtype_Id constant in PG18
	// headers; only pg_database/pg_authid/pg_auth_members/pg_shseclabel/
	// pg_subscription are formrdesc'd shared rels). Confirm we did not
	// accidentally pick up a real rowtype OID — anything other than 83
	// would suggest a formrdesc tie-in that needs deeper inspection.
	if got.RelType != 83 {
		t.Fatalf("OID %d: RelType=%d want 83 (placeholder, not formrdesc'd)", replicationOriginOID, got.RelType)
	}

	// Pin per-attribute (name, TypeOID, Num, Len, NotNull) against
	// pg_replication_origin.h authoritative definitions. roident is `Oid`
	// (4 bytes, NotNull) — upstream comment notes the *value* fits into
	// uint16 because it's allocated manually, but storage is full 4-byte
	// Oid. roname is text BKI_FORCE_NOT_NULL.
	type want struct {
		Name    string
		TypeOID uint32
		Num     int16
		Len     int16
		NotNull bool
	}
	wantAttrs := []want{
		{"roident", 26, 1, 4, true},  // oid (manually allocated)
		{"roname", 25, 2, -1, true},  // text BKI_FORCE_NOT_NULL
	}
	for i, w := range wantAttrs {
		a := got.Attrs[i]
		if a.Name != w.Name || a.TypeOID != w.TypeOID || a.Num != w.Num || a.Len != w.Len || a.NotNull != w.NotNull {
			t.Errorf("Attrs[%d]=%+v want {%s %d %d %d %v}", i, a, w.Name, w.TypeOID, w.Num, w.Len, w.NotNull)
		}
	}
}
