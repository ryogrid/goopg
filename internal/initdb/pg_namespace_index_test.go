package initdb

import "testing"

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
