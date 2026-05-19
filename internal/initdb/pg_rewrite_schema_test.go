package initdb

import "testing"

// TestPgRewriteAttrsMatchesPg18FormPgRewrite pins the pg_rewrite
// TupleDesc to the PG18 canonical 8-column layout from
// `postgres/src/include/catalog/pg_rewrite.h:32-44` (M0106-0010 Step 3dm
// phase A). The pre-Step-3dm 7-column layout reordered the columns and
// invented an `ev_owner` slot that PG18 does not have; the
// pg_rewrite_rel_rulename_index indkey `[3, 2]` already assumes the
// canonical layout (ev_class at column 3, rulename at column 2) so a
// PG-shipped reader cannot decode tuples written under the prior
// schema.
func TestPgRewriteAttrsMatchesPg18FormPgRewrite(t *testing.T) {
	got := pgRewriteAttrs()
	want := []struct {
		Name    string
		TypeOID uint32
		Num     int16
		Len     int16
		NotNull bool
	}{
		{"oid", 26, 1, 4, true},
		{"rulename", 19, 2, 64, true},
		{"ev_class", 26, 3, 4, true},
		{"ev_type", 18, 4, 1, true},
		{"ev_enabled", 18, 5, 1, true},
		{"is_instead", 16, 6, 1, true},
		{"ev_qual", 194, 7, -1, true},
		{"ev_action", 194, 8, -1, true},
	}
	if len(got) != len(want) {
		t.Fatalf("pgRewriteAttrs len=%d want %d (PG18 Form_pg_rewrite)", len(got), len(want))
	}
	for i, w := range want {
		a := got[i]
		if a.Name != w.Name || a.TypeOID != w.TypeOID || a.Num != w.Num || a.Len != w.Len || a.NotNull != w.NotNull {
			t.Fatalf("Attrs[%d]={%s,%d,%d,%d,%v} want {%s,%d,%d,%d,%v}",
				i, a.Name, a.TypeOID, a.Num, a.Len, a.NotNull,
				w.Name, w.TypeOID, w.Num, w.Len, w.NotNull)
		}
	}
}

// TestNailedLocalRelsPgRewriteRelnatts8 pins the pg_rewrite
// nailedLocalRels relnatts to 8 so the init-file TupleDesc agrees with
// the PG18 Form_pg_rewrite layout (M0106-0010 Step 3dm phase A). PG's
// relcache asserts `relnatts == descriptor natts` (relcache.c:1492) on
// load, so a mismatch crashes the standby before a single tuple is
// fetched.
func TestNailedLocalRelsPgRewriteRelnatts8(t *testing.T) {
	const pgRewriteOID = 2618
	var got *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == pgRewriteOID {
			got = &nailedLocalRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedLocalRels missing pg_rewrite (OID %d)", pgRewriteOID)
	}
	if got.RelName != "pg_rewrite" {
		t.Fatalf("OID %d: RelName=%q want %q", pgRewriteOID, got.RelName, "pg_rewrite")
	}
	if got.RelNatts != 8 {
		t.Fatalf("OID %d: RelNatts=%d want 8 (PG18 Form_pg_rewrite has 8 columns)", pgRewriteOID, got.RelNatts)
	}
	if len(got.Attrs) != 8 {
		t.Fatalf("OID %d: len(Attrs)=%d want 8", pgRewriteOID, len(got.Attrs))
	}
}
