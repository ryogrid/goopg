package initdb

import "testing"

// TestNailedIndexRelnattsAgreesWithIndnatts pins the M0106-0010 step 3j
// fix: pg_class.relnatts for every nailed index must equal the
// pg_index.indnatts of the matching pgIndexInitialEntries row. PG's
// RelationInitIndexAccessInfo asserts the two on every standby boot
// (postgres/src/backend/utils/cache/relcache.c:1492 — "relnatts
// disagrees with indnatts for index %u"), and the historical default
// of natts=2 in flattenRels silently corrupted every single-column
// index (e.g. pg_class_oid_index has 1 key column, not 2).
func TestNailedIndexRelnattsAgreesWithIndnatts(t *testing.T) {
	want := pgIndexNattsByOID()
	if len(want) == 0 {
		t.Fatal("pgIndexNattsByOID returned empty map — pg_index seed missing")
	}
	check := func(t *testing.T, label string, rels []nailedRel) {
		t.Helper()
		for _, rel := range rels {
			if rel.RelKind != 'i' {
				continue
			}
			indnatts, ok := want[rel.OID]
			if !ok {
				t.Errorf("%s: index %d (%s) has no pg_index seed row", label, rel.OID, rel.RelName)
				continue
			}
			if rel.RelNatts != indnatts {
				t.Errorf("%s: index %d (%s) relnatts=%d, want %d (indnatts)",
					label, rel.OID, rel.RelName, rel.RelNatts, indnatts)
			}
			if got := int16(len(rel.Attrs)); got != indnatts {
				t.Errorf("%s: index %d (%s) attr count=%d, want %d (indnatts)",
					label, rel.OID, rel.RelName, got, indnatts)
			}
		}
	}
	check(t, "nailedSharedRels", nailedSharedRels)
	check(t, "nailedLocalRels", nailedLocalRels)
}

// TestPgClassOidIndexHasSingleKeyColumn pins the specific regression
// caught by Step 3i's E2E re-run: pg_class_oid_index (OID 2662) is a
// unique btree on oid alone — relnatts must be 1, not the legacy
// hardcoded default of 2. Distinct from the generic agreement test
// above because pg_class_oid_index is the first index PG loads
// during standby boot and was the FATAL site for Step 3j.
func TestPgClassOidIndexHasSingleKeyColumn(t *testing.T) {
	for _, rel := range nailedLocalRels {
		if rel.OID != 2662 {
			continue
		}
		if rel.RelNatts != 1 {
			t.Fatalf("pg_class_oid_index relnatts=%d, want 1", rel.RelNatts)
		}
		if len(rel.Attrs) != 1 {
			t.Fatalf("pg_class_oid_index attr count=%d, want 1", len(rel.Attrs))
		}
		return
	}
	t.Fatal("pg_class_oid_index (OID 2662) not found in nailedLocalRels")
}
