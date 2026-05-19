package initdb

import "testing"

// TestPgShseclabelAttrsMatchesPG18FormPgShseclabel pins the 4-column
// PG18 pg_shseclabel schema. PG18's
// `postgres/src/include/catalog/pg_shseclabel.h` defines exactly:
// objoid (oid), classoid (oid), provider (text), label (text); and
// `Schema_pg_shseclabel` (schemapg.h) is the baked-in TupleDesc used
// by `formrdesc("pg_shseclabel", Natts_pg_shseclabel=4, …)` in
// `RelationCacheInitializePhase2`.
//
// Diagnostic context (M0106-0010 step 3cv): the FATAL
// `invalid attalign value:` during PG-standby InitPostgres was caused
// by goopg declaring a 6-column pg_shseclabel (with bogus oid +
// objsubid columns and provider/label in slots 5–6). The on-disk
// pg_class.relnatts=6 made the first user-backend's
// `write_relcache_init_file(true)` iterate 6 slots over a 4-element
// rd_att array (formrdesc allocated 4), writing two garbage
// CompactAttribute slots into `global/pg_internal.init`. Subsequent
// backends loading that init file hit the garbage slot first and
// FATALed at `populate_compact_attribute_internal, tupdesc.c:105`
// (attlen=488=sizeofRelationData, attalign=0x00, attstorage=0xa0).
func TestPgShseclabelAttrsMatchesPG18FormPgShseclabel(t *testing.T) {
	got := pgShseclabelAttrs()
	want := []nailedAttr{
		{Name: "objoid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "classoid", TypeOID: 26, Num: 2, Len: 4, NotNull: true},
		{Name: "provider", TypeOID: 25, Num: 3, Len: -1, NotNull: true},
		{Name: "label", TypeOID: 25, Num: 4, Len: -1, NotNull: true},
	}
	if len(got) != len(want) {
		t.Fatalf("pg_shseclabel attr count: got %d, want %d (PG18 schema)", len(got), len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.Name != w.Name || g.TypeOID != w.TypeOID || g.Num != w.Num || g.Len != w.Len || g.NotNull != w.NotNull {
			t.Errorf("attr[%d]: got %+v, want %+v", i, g, w)
		}
	}
}

// TestNailedSharedRelsPgShseclabelRelnattsIsFour pins the
// nailedSharedRels declaration for pg_shseclabel: relnatts MUST be 4,
// matching `Natts_pg_shseclabel` in
// `postgres/src/include/catalog/pg_shseclabel_d.h`. Diverging from
// PG18's static `Desc_pg_shseclabel` produces the
// load_relcache_init_file → populate_compact_attribute_internal FATAL
// path described in
// `TestPgShseclabelAttrsMatchesPG18FormPgShseclabel`.
func TestNailedSharedRelsPgShseclabelRelnattsIsFour(t *testing.T) {
	for _, rel := range nailedSharedRels {
		if rel.RelName != "pg_shseclabel" {
			continue
		}
		if rel.RelNatts != 4 {
			t.Errorf("pg_shseclabel relnatts: got %d, want 4 (PG18 Natts_pg_shseclabel)", rel.RelNatts)
		}
		if rel.OID != 3592 {
			t.Errorf("pg_shseclabel OID: got %d, want 3592 (SharedSecLabelRelationId)", rel.OID)
		}
		if rel.RelType != 4066 {
			t.Errorf("pg_shseclabel RelType: got %d, want 4066 (SharedSecLabelRelation_Rowtype_Id)", rel.RelType)
		}
		return
	}
	t.Fatal("pg_shseclabel not found in nailedSharedRels")
}
