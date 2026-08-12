package initdb

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// pg18AttributeColumnOrder is FormData_pg_attribute as PG 18.3 declares it —
// postgres/src/include/catalog/pg_attribute.h, read top to bottom. Two details
// that trip people up and are the whole point of pinning it here:
//
//   - attcacheoff is ABSENT. PG18 dropped it from the catalog; a descriptor
//     that still carries it (goopg's pg_internal.init did until M0131-S14.1) is
//     one column out of step from attbyval onward.
//   - attstattarget sits at #21, AFTER attcollation, not at the #4 it occupied
//     through PG16. Upstream moved it when it became nullable.
var pg18AttributeColumnOrder = []string{
	"attrelid", "attname", "atttypid", "attlen", "attnum",
	"atttypmod", "attndims", "attbyval", "attalign", "attstorage",
	"attcompression", "attnotnull", "atthasdef", "atthasmissing", "attidentity",
	"attgenerated", "attisdropped", "attislocal", "attinhcount", "attcollation",
	"attstattarget", "attacl", "attoptions", "attfdwoptions", "attmissingval",
}

// TestPGAttributeSelfDescriptionIsPG18Canonical is the M0131-S14.1 tripwire.
//
// goopg describes pg_attribute in four places, and a hosted real PostgreSQL
// reads the result with its OWN compiled struct, so any disagreement is a
// silent misread rather than an error:
//
//   - initdb.pgAttrColDefs         — the heap rows for relid 1249, and the
//     physical encoding of every pg_attribute row
//   - initdb.pgAttributeAttrs      — the pg_internal.init nailed descriptor
//     (derives from pgAttrColDefs since S14.1)
//   - catalog.PGAttributeColumns   — the executor's decoder schema
//   - executor.pgAttributeColumnsPG18 — the runtime row builders' schema,
//     asserted from the executor package's own test (this one cannot import it)
//
// Before S14.1 the first three ordered the tail
// attacl/attoptions/attfdwoptions/attmissingval/attstattarget while PG18 orders
// it attstattarget/attacl/attoptions/attfdwoptions/attmissingval, and the
// fourth was a 24-column PG-11-era layout entirely. The permutation was
// invisible while all five were NULL — the null bitmap does not distinguish
// them — but `ALTER COLUMN … SET STATISTICS` writes attstattarget, and a hosted
// PG then read that int2 as attacl.
func TestPGAttributeSelfDescriptionIsPG18Canonical(t *testing.T) {
	assertOrder := func(what string, got []string) {
		t.Helper()
		if len(got) != len(pg18AttributeColumnOrder) {
			t.Fatalf("%s has %d columns, PG18 FormData_pg_attribute has %d: %v",
				what, len(got), len(pg18AttributeColumnOrder), got)
		}
		for i, want := range pg18AttributeColumnOrder {
			if got[i] != want {
				t.Errorf("%s column %d = %q, PG18 has %q (full order: %v)",
					what, i+1, got[i], want, got)
			}
		}
	}

	initdbNames := make([]string, 0, 25)
	for _, c := range pgAttrColDefs() {
		initdbNames = append(initdbNames, c.Name)
	}
	assertOrder("initdb.pgAttrColDefs", initdbNames)

	catalogNames := make([]string, 0, 25)
	for _, c := range catalog.PGAttributeColumns() {
		catalogNames = append(catalogNames, c.Name)
	}
	assertOrder("catalog.PGAttributeColumns", catalogNames)

	nailedNames := make([]string, 0, 25)
	for i, a := range pgAttributeAttrs() {
		nailedNames = append(nailedNames, a.Name)
		if a.Num != int16(i+1) {
			t.Errorf("pgAttributeAttrs[%d] %q carries Num=%d, want %d", i, a.Name, a.Num, i+1)
		}
	}
	assertOrder("initdb.pgAttributeAttrs", nailedNames)

	// The row builder must line up with the descriptor position-for-position;
	// it is the thing that actually writes bytes.
	row := pgAttributeRow(2684, nailedAttr{Name: "nspname", TypeOID: 19, Num: 1, Len: 64, NotNull: true})
	if len(row) != len(pg18AttributeColumnOrder) {
		t.Fatalf("pgAttributeRow emits %d datums, descriptor has %d",
			len(row), len(pg18AttributeColumnOrder))
	}

	// relnatts for pg_attribute itself must equal that column count. PG unlinks
	// goopg's pg_internal.init at startup (xlog.c RelationCacheInitFileRemove)
	// and rebuilds pg_attribute from its compiled descriptor, but
	// RelationCacheInitializePhase3 then copies rd_rel straight out of goopg's
	// pg_class tuple — so a short relnatts truncates real columns off the end.
	// 24 here is what made `SELECT attmissingval FROM pg_attribute` answer
	// 42703 on a hosted PG (M0131-S4 finding F3).
	var found bool
	for _, rel := range nailedLocalRels {
		if rel.OID != catalog.AttributeRelationId {
			continue
		}
		found = true
		if int(rel.RelNatts) != len(pg18AttributeColumnOrder) {
			t.Errorf("nailed pg_class row for pg_attribute carries relnatts=%d, want %d",
				rel.RelNatts, len(pg18AttributeColumnOrder))
		}
	}
	if !found {
		t.Fatalf("no nailed relation entry for pg_attribute (OID %d)", catalog.AttributeRelationId)
	}

	_ = executor.NullDatum
}
