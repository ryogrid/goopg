package initdb

import "testing"

// TestNailedPgNamespaceNspnameIndexHasNameDescriptor (M0106-0010
// batched-36 loop 5) pins the nailedRel Attrs override for
// pg_namespace_nspname_index (OID 2684). Same shape as
// pg_authid_rolname_index (Step 3dg) and pg_database_datname_index
// (Step 3dh): a 1-column UNIQUE btree on NameData (name_ops).
//
// Without this override, flattenRels falls back to the oid-stamped
// indexKeyAttrs(1) descriptor (attlen=4, attbyval=true), which
// bootstrapPgAttributeTuples writes verbatim into the pg_attribute
// heap row for (attrelid=2684, attnum=1). When a PG18 standby reads
// that row during RelationCacheInitializePhase3 the resulting
// TupleDesc says attlen=4 / attbyval=true; _bt_compare→index_getattr
// then loads the first 4 inline NameData bytes of the leaf
// IndexTuple as a by-val Datum and passes them to btnamecmp as a
// pointer → SIGSEGV (captured via the LD_PRELOAD shim:
// RDI=0x00000000745f6770 == LE "pg_t", the leading bytes of the
// first leaf entry "pg_catalog" / "pg_toast"). The crash fires on
// the first SELECT after promote in TestE2E_FailoverGoopgToPG/async.
//
// The fix supplies an explicit attr with TypeOID=19 (name), Len=64,
// which makes pgAttributeRow emit attlen=64 / attbyval=false /
// attalign='c' — the contract btnamecmp expects.
func TestNailedPgNamespaceNspnameIndexHasNameDescriptor(t *testing.T) {
	var rel *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == 2684 {
			rel = &nailedLocalRels[i]
			break
		}
	}
	if rel == nil {
		t.Fatal("nailedLocalRels: OID 2684 (pg_namespace_nspname_index) missing")
	}
	if rel.RelKind != 'i' {
		t.Errorf("RelKind=%q, want 'i'", rel.RelKind)
	}
	if rel.RelNatts != 1 {
		t.Errorf("RelNatts=%d, want 1", rel.RelNatts)
	}
	if len(rel.Attrs) != 1 {
		t.Fatalf("Attrs len=%d, want 1", len(rel.Attrs))
	}
	a := rel.Attrs[0]
	if a.TypeOID != 19 {
		t.Errorf("Attrs[0].TypeOID=%d, want 19 (name)", a.TypeOID)
	}
	if a.Len != 64 {
		t.Errorf("Attrs[0].Len=%d, want 64 (NAMEDATALEN)", a.Len)
	}
	if a.Name != "nspname" {
		t.Errorf("Attrs[0].Name=%q, want \"nspname\"", a.Name)
	}

	// Relcache init blob coverage (parity with Step 3dg/3dh tests even
	// though OID 2684 is not in criticalLocalIndexOIDs and therefore
	// not written to base/1/pg_internal.init).
	blob := buildPgAttributeBlob(a)
	if blob[82] != 0 {
		t.Errorf("buildPgAttributeBlob.attbyval=%d, want 0", blob[82])
	}
	if blob[83] != 'c' {
		t.Errorf("buildPgAttributeBlob.attalign=%q, want 'c'", blob[83])
	}
	if got := uint16(blob[72]) | uint16(blob[73])<<8; got != 64 {
		t.Errorf("buildPgAttributeBlob.attlen=%d, want 64", got)
	}

	// Heap-row coverage: this is the byte that actually fixes the SIGSEGV.
	// pg_attribute heap row at (attrelid=2684, attnum=1) must report the
	// name-typed shape so PG's RelationBuildTupleDesc emits attlen=64 /
	// attbyval=false / attalign='c'.
	row := pgAttributeRow(2684, a)
	// Columns (pgAttrColDefs order): 0=attrelid, 1=attname, 2=atttypid,
	// 3=attlen, 4=attnum, 5=atttypmod, 6=attndims, 7=attbyval, 8=attalign.
	if got := row[2].Int; got != 19 {
		t.Errorf("pgAttributeRow[atttypid]=%d, want 19", got)
	}
	if got := row[3].Int; got != 64 {
		t.Errorf("pgAttributeRow[attlen]=%d, want 64", got)
	}
	if got := row[7].BoolValue(); got {
		t.Errorf("pgAttributeRow[attbyval]=true, want false")
	}
	if got := row[8].StringValue(); got != "c" {
		t.Errorf("pgAttributeRow[attalign]=%q, want \"c\"", got)
	}
}

// TestPgNamespaceIndexesSeededFromInitialEntries pins Step 3t's catalog seed:
// pg_namespace_nspname_index (OID 2684) and pg_namespace_oid_index (OID 2685)
// must both appear in pgIndexInitialEntries with the PG18-canonical indkey /
// uniqueness flags. Without these rows the populated btree at file 2679
// cannot include their TIDs and PG's first SearchSysCache1(INDEXRELID, 2684)
// during RelationCacheInitializePhase3 FATALs with "could not open relation
// with OID 2684".
func TestPgNamespaceIndexesSeededFromInitialEntries(t *testing.T) {
	want := map[uint32]struct {
		IndRelid  uint32
		Key       []int16
		IsUnique  bool
		IsPrimary bool
	}{
		2684: {IndRelid: 2615, Key: []int16{2}, IsUnique: true, IsPrimary: false}, // nspname name_ops
		2685: {IndRelid: 2615, Key: []int16{1}, IsUnique: true, IsPrimary: true},  // oid oid_ops
	}
	got := map[uint32]pgIndexEntry{}
	for _, e := range pgIndexInitialEntries() {
		if _, ok := want[e.IndexRelid]; !ok {
			continue
		}
		got[e.IndexRelid] = e
	}
	for oid, w := range want {
		g, ok := got[oid]
		if !ok {
			t.Fatalf("pgIndexInitialEntries: OID %d missing (Step 3t)", oid)
		}
		if g.IndRelid != w.IndRelid {
			t.Errorf("OID %d: IndRelid=%d, want %d (pg_namespace heap OID)", oid, g.IndRelid, w.IndRelid)
		}
		if !int16SliceEqual(g.IndKey, w.Key) {
			t.Errorf("OID %d: IndKey=%v, want %v", oid, g.IndKey, w.Key)
		}
		if g.IsUnique != w.IsUnique {
			t.Errorf("OID %d: IsUnique=%t, want %t", oid, g.IsUnique, w.IsUnique)
		}
		if g.IsPrimary != w.IsPrimary {
			t.Errorf("OID %d: IsPrimary=%t, want %t", oid, g.IsPrimary, w.IsPrimary)
		}
	}
}

// TestNailedLocalRelsContainsPgNamespaceIndexes pins Step 3t's relcache
// nailed-rel seed: PG's RelationInitIndexAccessInfo consistency check
// (relnatts vs indnatts) and the init-file TupleDesc both require these
// to be present in nailedLocalRels.
func TestNailedLocalRelsContainsPgNamespaceIndexes(t *testing.T) {
	want := map[uint32]string{
		2684: "pg_namespace_nspname_index",
		2685: "pg_namespace_oid_index",
	}
	got := map[uint32]string{}
	for _, r := range nailedLocalRels {
		if _, ok := want[r.OID]; !ok {
			continue
		}
		got[r.OID] = r.RelName
		if r.RelKind != 'i' {
			t.Errorf("nailedLocalRels[%d] RelKind=%q, want 'i'", r.OID, r.RelKind)
		}
		if r.RelNatts != 1 {
			t.Errorf("nailedLocalRels[%d] RelNatts=%d, want 1 (single-column key)", r.OID, r.RelNatts)
		}
	}
	for oid, name := range want {
		g, ok := got[oid]
		if !ok {
			t.Fatalf("nailedLocalRels: OID %d (%s) missing — Step 3t", oid, name)
		}
		if g != name {
			t.Errorf("nailedLocalRels[%d] RelName=%q, want %q (PG18 label per pg_namespace.h)", oid, g, name)
		}
	}
}
