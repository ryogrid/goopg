package initdb

import "testing"

// TestNailedNameTypedIndexesHaveNameDescriptor (M0106-0010 batched-36
// loop 6) pins the Attrs overrides for every name-typed single-key
// UNIQUE btree in nailedLocalRels. These indexes all share the same
// SIGSEGV class that crashed PG18 standby backends in
// TestE2E_FailoverGoopgToPG/async on OID 2684
// (pg_namespace_nspname_index) prior to loop 5:
//
//   Without an Attrs override flattenRels falls back to
//   indexKeyAttrs(N)'s oid-stamped descriptor (attlen=4, attbyval=true),
//   which bootstrapPgAttributeTuples writes verbatim into pg_attribute
//   for (attrelid=<idx>, attnum=1). When PG18 reads that row during
//   RelationCacheInitializePhase3 the resulting TupleDesc claims the
//   leading key is a 4-byte by-value scalar; _bt_compare→index_getattr
//   then loads the first 4 inline NameData bytes of the leaf
//   IndexTuple as a by-val Datum and passes them to btnamecmp as a
//   pointer, which is dereferenced by __strncmp_avx2 → SIGSEGV.
//
// The fix supplies an explicit attr with TypeOID=19 (name), Len=64,
// which makes pgAttributeRow emit attlen=64 / attbyval=false /
// attalign='c' — the contract btnamecmp expects.
//
// OIDs covered (see relcache_init.go for the PG18 catalog header
// references):
//
//	3467  pg_event_trigger_evtname_index           evtname
//	3081  pg_extension_name_index                  extname
//	548   pg_foreign_data_wrapper_name_index       fdwname
//	549   pg_foreign_server_name_index             srvname
//	2681  pg_language_name_index                   lanname
//	3997  pg_statistic_ext_name_index              stxname (leading)
//
// 2684 (pg_namespace_nspname_index) is covered by its dedicated test
// in pg_namespace_index_test.go.
func TestNailedNameTypedIndexesHaveNameDescriptor(t *testing.T) {
	want := []struct {
		OID  uint32
		Name string
		Attr string
	}{
		{3467, "pg_event_trigger_evtname_index", "evtname"},
		{3081, "pg_extension_name_index", "extname"},
		{548, "pg_foreign_data_wrapper_name_index", "fdwname"},
		{549, "pg_foreign_server_name_index", "srvname"},
		{2681, "pg_language_name_index", "lanname"},
		{3997, "pg_statistic_ext_name_index", "stxname"},
	}

	byOID := map[uint32]*nailedRel{}
	for i := range nailedLocalRels {
		byOID[nailedLocalRels[i].OID] = &nailedLocalRels[i]
	}

	for _, w := range want {
		rel, ok := byOID[w.OID]
		if !ok {
			t.Fatalf("nailedLocalRels: OID %d (%s) missing", w.OID, w.Name)
		}
		if rel.RelKind != 'i' {
			t.Errorf("OID %d: RelKind=%q, want 'i'", w.OID, rel.RelKind)
		}
		if rel.RelName != w.Name {
			t.Errorf("OID %d: RelName=%q, want %q", w.OID, rel.RelName, w.Name)
		}
		if len(rel.Attrs) == 0 {
			t.Fatalf("OID %d (%s): Attrs is empty — name-typed override missing", w.OID, w.Name)
		}
		a := rel.Attrs[0]
		if a.TypeOID != 19 {
			t.Errorf("OID %d (%s): Attrs[0].TypeOID=%d, want 19 (name)", w.OID, w.Name, a.TypeOID)
		}
		if a.Len != 64 {
			t.Errorf("OID %d (%s): Attrs[0].Len=%d, want 64 (NAMEDATALEN)", w.OID, w.Name, a.Len)
		}
		if a.Name != w.Attr {
			t.Errorf("OID %d (%s): Attrs[0].Name=%q, want %q", w.OID, w.Name, a.Name, w.Attr)
		}

		// pg_attribute heap row coverage: the bytes that actually fix the
		// SIGSEGV. RelationBuildTupleDesc reads these and must emit
		// attlen=64 / attbyval=false / attalign='c'.
		row := pgAttributeRow(w.OID, a)
		if got := row[2].Int; got != 19 {
			t.Errorf("OID %d (%s): pgAttributeRow[atttypid]=%d, want 19", w.OID, w.Name, got)
		}
		if got := row[3].Int; got != 64 {
			t.Errorf("OID %d (%s): pgAttributeRow[attlen]=%d, want 64", w.OID, w.Name, got)
		}
		if row[7].BoolValue() {
			t.Errorf("OID %d (%s): pgAttributeRow[attbyval]=true, want false", w.OID, w.Name)
		}
		if got := row[8].StringValue(); got != "c" {
			t.Errorf("OID %d (%s): pgAttributeRow[attalign]=%q, want \"c\"", w.OID, w.Name, got)
		}
	}

	// pg_statistic_ext_name_index is composite (stxname, stxnamespace).
	// Verify the trailing oid_ops descriptor as well.
	rel := byOID[3997]
	if rel == nil {
		t.Fatal("OID 3997 missing — covered above; control flow bug")
	}
	if rel.RelNatts != 2 {
		t.Errorf("OID 3997: RelNatts=%d, want 2 (composite stxname/stxnamespace)", rel.RelNatts)
	}
	if len(rel.Attrs) != 2 {
		t.Fatalf("OID 3997: Attrs len=%d, want 2", len(rel.Attrs))
	}
	a2 := rel.Attrs[1]
	if a2.TypeOID != 26 {
		t.Errorf("OID 3997: Attrs[1].TypeOID=%d, want 26 (oid)", a2.TypeOID)
	}
	if a2.Len != 4 {
		t.Errorf("OID 3997: Attrs[1].Len=%d, want 4", a2.Len)
	}
	if a2.Name != "stxnamespace" {
		t.Errorf("OID 3997: Attrs[1].Name=%q, want \"stxnamespace\"", a2.Name)
	}
}
