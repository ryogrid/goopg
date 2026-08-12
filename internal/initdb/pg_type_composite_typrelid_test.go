package initdb

import "testing"

// TestPgTypeCompositeRowsCarryTyprelid pins the invariant M0131-S9.3g learned
// the expensive way: a bootstrapped pg_type row with typtype='c' MUST carry a
// valid typrelid, and that typrelid must name a nailed relation whose RelType
// points back at the type.
//
// PG treats typtype='c' as a promise rather than a label.
// insert_rel_type_cache_if_needed() asserts `OidIsValid(typentry->typrelid)`
// (postgres/src/backend/utils/cache/typcache.c:3082) and lookup_type_cache()
// dereferences the OID to build the record tupledesc — so a composite row with
// typrelid = 0 does not degrade, it kills the backend on the first planner path
// that type-caches the type. goopg shipped FIVE such rows: _pg_statistic
// (10028), which was not even a composite upstream (typtype 'b'), plus the four
// BKI_ROWTYPE_OID rows 71/75/81/83 generated out of pg_type.dat.
//
// The forward direction (every composite has a typrelid) is what stops the
// crash; the round-trip through nailedRel.RelType is what stops the two edits
// from drifting, since seeding a rowtype takes one edit in pgTypeCanonical /
// pgTypeRelidOverlay and another in nailedSharedRels / nailedLocalRels.
func TestPgTypeCompositeRowsCarryTyprelid(t *testing.T) {
	relTypeOf := map[uint32]uint32{} // rel OID -> declared RelType
	for _, rel := range append(append([]nailedRel{}, nailedSharedRels...), nailedLocalRels...) {
		relTypeOf[rel.OID] = rel.RelType
	}

	composites := 0
	for oid, e := range pgTypeBootstrapEntryMap() {
		if e.Type != 'c' {
			// Sanity in the other direction: a non-composite must NOT claim a
			// typrelid, or PG's get_typ_typrelid callers would resolve a
			// relation for a base type.
			if relid, ok := pgTypeRelidOverlay[oid]; ok {
				t.Errorf("pg_type %d (%s) is typtype=%q but pgTypeRelidOverlay "+
					"gives it typrelid=%d; only composites carry one",
					oid, e.Name, e.Type, relid)
			}
			continue
		}
		composites++
		relid, ok := pgTypeRelidOverlay[oid]
		if !ok || relid == 0 {
			t.Errorf("pg_type %d (%s) is a COMPOSITE with typrelid=0 — a hosted "+
				"PG trips Assert(\"OidIsValid(typentry->typrelid)\") "+
				"(typcache.c:3082) and dies. Add it to pgTypeRelidOverlay, or "+
				"correct typtype if the type is not really a composite (that was "+
				"_pg_statistic 10028, an ARRAY, which upstream types 'b').",
				oid, e.Name)
			continue
		}
		back, known := relTypeOf[relid]
		if !known {
			t.Errorf("pg_type %d (%s) has typrelid=%d, which is not a nailed "+
				"relation — nothing on disk describes the rowtype's columns",
				oid, e.Name, relid)
			continue
		}
		if back != oid {
			t.Errorf("pg_type %d (%s) has typrelid=%d, but that relation's "+
				"RelType is %d — pg_class.reltype and pg_type.typrelid must be "+
				"mutual inverses (relcache.c:4293 asserts the tupledesc side)",
				oid, e.Name, relid, back)
		}
	}
	if composites == 0 {
		t.Fatal("no composite pg_type rows in the bootstrap set — the guard is " +
			"vacuous; pgTypeBootstrapEntryMap or pgTypeCanonical changed shape")
	}
}
