package initdb

import "testing"

// TestNailedCompositeNameIndexesAutoDerivedFromHeap (M0106-0010
// batched-36 loop 7) pins the flattenRels auto-derivation that
// generalises the loop-5/6 explicit Attrs overrides to every nailed
// index whose indkey references a name-typed (or other non-oid) heap
// column. Without auto-derivation, an idxSpec without an explicit
// Attrs override falls back to indexKeyAttrs(natts)'s all-OID
// descriptor (attlen=4, attbyval=true) — the same SIGSEGV vector that
// crashed PG18 on OID 2684 in TestE2E_FailoverGoopgToPG/async.
//
// These composite indexes are on the parse-analyze path for any user-
// table SELECT that goes through schema/relation/column/operator
// lookup; a wrong-tupdesc on the FIRST name-typed key column makes
// _bt_compare → index_getattr load the inline NameData bytes as a
// by-val Datum and pass them to btnamecmp as a bogus pointer.
//
// Coverage list (OID → expected leading name-typed attr):
//
//	2663  pg_class_relname_nsp_index               relname        (col 1, attnum 2)
//	2658  pg_attribute_relid_attnam_index          attname        (col 2, attnum 2)
//	2691  pg_proc_proname_args_nsp_index           proname        (col 1, attnum 2)
//	2704  pg_type_typname_nsp_index                typname        (col 1, attnum 2)
//	2693  pg_rewrite_rel_rulename_index            rulename       (col 2, attnum 2)
//	2686  pg_opclass_am_name_nsp_index             opcname        (col 2, attnum 3)
//	2754  pg_opfamily_am_name_nsp_index            opfname        (col 2, attnum 3)
//	2689  pg_operator_oprname_l_r_n_index          oprname        (col 1, attnum 2)
//	3164  pg_collation_name_enc_nsp_index          collname       (col 1, attnum 2)
//	2669  pg_conversion_name_nsp_index             conname        (col 1, attnum 2)
func TestNailedCompositeNameIndexesAutoDerivedFromHeap(t *testing.T) {
	type want struct {
		OID    uint32
		Name   string
		// Col is 1-based index-column position; AttrName is the heap
		// column name we expect to flow through.
		Col      int
		AttrName string
	}
	cases := []want{
		{2663, "pg_class_relname_nsp_index", 1, "relname"},
		{2658, "pg_attribute_relid_attnam_index", 2, "attname"},
		{2691, "pg_proc_proname_args_nsp_index", 1, "proname"},
		{2704, "pg_type_typname_nsp_index", 1, "typname"},
		{2693, "pg_rewrite_rel_rulename_index", 2, "rulename"},
		// NOTE: 2701 (pg_trigger_tgrelid_tgname_index) is intentionally NOT
		// covered here. pgIndexInitialEntries says indkey={2, 4} matching
		// PG18's 23-column pg_trigger, but goopg's pgTriggerAttrs() uses an
		// 8-column reduced schema where tgname is at attnum 3 (attnum 4 is
		// tgfoid). Auto-derivation correctly resolves heap attnum 4 →
		// tgfoid, exposing this pre-existing PG18-vs-goopg schema mismatch
		// (separate from the loop-7 auto-derivation scope).
		{2686, "pg_opclass_am_name_nsp_index", 2, "opcname"},
		{2754, "pg_opfamily_am_name_nsp_index", 2, "opfname"},
		{2689, "pg_operator_oprname_l_r_n_index", 1, "oprname"},
		{3164, "pg_collation_name_enc_nsp_index", 1, "collname"},
		{2669, "pg_conversion_name_nsp_index", 1, "conname"},
	}

	byOID := map[uint32]*nailedRel{}
	for i := range nailedLocalRels {
		byOID[nailedLocalRels[i].OID] = &nailedLocalRels[i]
	}

	for _, c := range cases {
		rel, ok := byOID[c.OID]
		if !ok {
			t.Errorf("nailedLocalRels: OID %d (%s) missing", c.OID, c.Name)
			continue
		}
		if rel.RelKind != 'i' {
			t.Errorf("OID %d (%s): RelKind=%q, want 'i'", c.OID, c.Name, rel.RelKind)
		}
		if c.Col < 1 || c.Col > len(rel.Attrs) {
			t.Errorf("OID %d (%s): want column %d but Attrs len=%d", c.OID, c.Name, c.Col, len(rel.Attrs))
			continue
		}
		a := rel.Attrs[c.Col-1]
		if a.Name != c.AttrName {
			t.Errorf("OID %d (%s): Attrs[%d].Name=%q, want %q", c.OID, c.Name, c.Col-1, a.Name, c.AttrName)
		}
		if a.TypeOID != 19 {
			t.Errorf("OID %d (%s): Attrs[%d].TypeOID=%d, want 19 (name)", c.OID, c.Name, c.Col-1, a.TypeOID)
		}
		if a.Len != 64 {
			t.Errorf("OID %d (%s): Attrs[%d].Len=%d, want 64 (NAMEDATALEN)", c.OID, c.Name, c.Col-1, a.Len)
		}

		// Heap-row coverage: pg_attribute row for (attrelid=OID, attnum=c.Col)
		// must report the name-typed shape so PG's RelationBuildTupleDesc
		// emits attlen=64 / attbyval=false / attalign='c'.
		row := pgAttributeRow(c.OID, a)
		if got := row[2].Int; got != 19 {
			t.Errorf("OID %d (%s): pgAttributeRow[atttypid]=%d, want 19", c.OID, c.Name, got)
		}
		if got := row[3].Int; got != 64 {
			t.Errorf("OID %d (%s): pgAttributeRow[attlen]=%d, want 64", c.OID, c.Name, got)
		}
		if row[7].BoolValue() {
			t.Errorf("OID %d (%s): pgAttributeRow[attbyval]=true, want false", c.OID, c.Name)
		}
		if got := row[8].StringValue(); got != "c" {
			t.Errorf("OID %d (%s): pgAttributeRow[attalign]=%q, want \"c\"", c.OID, c.Name, got)
		}
	}
}

// TestFlattenRelsDeriveIndexAttrsFromHeap exercises the helper directly
// to pin its no-match fall-through behavior (used for expressional
// indexes and indexes whose heap relation is not in the same flattenRels
// call) and its happy-path heap-attr resolution.
func TestFlattenRelsDeriveIndexAttrsFromHeap(t *testing.T) {
	heaps := []nailedRel{
		{OID: 9999, RelName: "fake_heap", RelKind: 'r', RelNatts: 2, Attrs: []nailedAttr{
			{Name: "id", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
			{Name: "label", TypeOID: 19, Num: 2, Len: 64, NotNull: true},
		}},
	}
	heapAttrByOID := map[uint32]map[int16]nailedAttr{}
	for _, h := range heaps {
		m := make(map[int16]nailedAttr, len(h.Attrs))
		for _, a := range h.Attrs {
			m[a.Num] = a
		}
		heapAttrByOID[h.OID] = m
	}
	seedByOID := map[uint32]pgIndexEntry{
		// 2-column index (id, label) on fake_heap.
		8001: {IndexRelid: 8001, IndRelid: 9999, IndKey: []int16{1, 2}},
		// Expressional column: indkey=0.
		8002: {IndexRelid: 8002, IndRelid: 9999, IndKey: []int16{0}},
		// Heap not in heaps slice: indrelid 7777 unknown.
		8003: {IndexRelid: 8003, IndRelid: 7777, IndKey: []int16{1}},
		// natts/indkey-len mismatch.
		8004: {IndexRelid: 8004, IndRelid: 9999, IndKey: []int16{1, 2}},
	}

	if attrs, ok := deriveIndexAttrsFromHeap(8001, 2, seedByOID, heapAttrByOID); !ok {
		t.Errorf("8001: derive returned ok=false, want true")
	} else {
		if len(attrs) != 2 {
			t.Fatalf("8001: len=%d, want 2", len(attrs))
		}
		if attrs[0].TypeOID != 26 || attrs[0].Len != 4 {
			t.Errorf("8001 col0: got TypeOID=%d Len=%d, want 26/4", attrs[0].TypeOID, attrs[0].Len)
		}
		if attrs[1].TypeOID != 19 || attrs[1].Len != 64 {
			t.Errorf("8001 col1: got TypeOID=%d Len=%d, want 19/64", attrs[1].TypeOID, attrs[1].Len)
		}
		if attrs[0].Num != 1 || attrs[1].Num != 2 {
			t.Errorf("Num values: got %d/%d, want 1/2", attrs[0].Num, attrs[1].Num)
		}
	}

	if _, ok := deriveIndexAttrsFromHeap(8002, 1, seedByOID, heapAttrByOID); ok {
		t.Errorf("8002 (expressional indkey=0): derive returned ok=true, want false")
	}
	if _, ok := deriveIndexAttrsFromHeap(8003, 1, seedByOID, heapAttrByOID); ok {
		t.Errorf("8003 (heap not in heaps): derive returned ok=true, want false")
	}
	if _, ok := deriveIndexAttrsFromHeap(8004, 1, seedByOID, heapAttrByOID); ok {
		t.Errorf("8004 (natts mismatch): derive returned ok=true, want false")
	}
	if _, ok := deriveIndexAttrsFromHeap(7000, 1, seedByOID, heapAttrByOID); ok {
		t.Errorf("7000 (unknown OID): derive returned ok=true, want false")
	}
}
