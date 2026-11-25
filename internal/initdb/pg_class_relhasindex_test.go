package initdb

import (
	"testing"

	"github.com/goopg/goopg/internal/executor"
)

// TestPgClassRelhasindexMatchesBootstrapIndexes pins M0149-0001d: every
// bootstrapped catalog heap that has an index in pgIndexInitialEntries carries
// pg_class.relhasindex = true (shared: M0149-0001d; local: M0149-0001g). PostgreSQL does not re-derive the flag —
// ExecOpenIndices (execIndexing.c) and RelationGetIndexList (relcache.c)
// return no indexes when it is false — so a false here made every catalog row
// a PG 18.3 node wrote on a goopg directory skip index maintenance, and a
// database PG created could not be connected to.
func TestPgClassRelhasindexMatchesBootstrapIndexes(t *testing.T) {
	col := -1
	for i, c := range pgClassColDefs() {
		if c.Name == "relhasindex" {
			col = i
		}
	}
	if col < 0 {
		t.Fatal("pgClassColDefs has no relhasindex column")
	}
	rels := append(append([]nailedRel{}, nailedSharedRels...), nailedLocalRels...)
	rels = append(rels, nailedToastRels()...)
	checked := 0
	for _, rel := range rels {
		if rel.RelKind != 'r' && rel.RelKind != 't' {
			continue
		}
		want := bootstrapRelHasIndex(rel.OID) || rel.RelKind == 't'
		got := pgClassRow(rel)[col]
		if got.Kind != executor.KindBool || got.BoolValue() != want {
			t.Errorf("pg_class row for %s (%d): relhasindex = %v, want %v", rel.RelName, rel.OID, got, want)
		}
		checked++
	}
	for _, oid := range []uint32{1259, 1249, 1262, 1260} { // pg_class, pg_attribute, pg_database, pg_authid
		if !bootstrapRelHasIndex(oid) {
			t.Errorf("catalog %d has no bootstrap index; relhasindex would be false", oid)
		}
	}
	if checked < 50 {
		t.Fatalf("checked only %d catalog heaps", checked)
	}
}

// TestBootstrapIndexEntriesHavePgClassRows pins the other half of
// M0149-0001d: with relhasindex = true, PG opens every index pg_index lists
// for a catalog, so each bootstrap pg_index entry needs a pg_class row (2692,
// pg_rewrite_oid_index, had none).
func TestBootstrapIndexEntriesHavePgClassRows(t *testing.T) {
	have := map[uint32]bool{}
	rels := append(append([]nailedRel{}, nailedSharedRels...), nailedLocalRels...)
	rels = append(rels, nailedToastRels()...)
	rels = append(rels, informationSchemaDataTableRels()...)
	for _, r := range rels {
		have[r.OID] = true
	}
	for _, e := range pgIndexInitialEntries() {
		if !have[e.IndexRelid] {
			t.Errorf("pg_index entry %d (on %d) has no bootstrap pg_class row", e.IndexRelid, e.IndRelid)
		}
	}
}
