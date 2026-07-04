package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestCreateIndexRecursesPartitionTree verifies that CREATE INDEX on a
// partitioned table fans out to every existing partition descendant, building a
// matching child index on each, auto-named `<partition>_<col>_idx` and deduped
// to `_idx1` when two ancestor indexes both reach the same leaf. This mirrors
// PostgreSQL's DefineIndex recursion and is the catalog half of the
// partition-drop-index-locking isolation spec (M0118-0008): the spec's
// s3getlocks output references `part_drop_index_locking_subpart_child_id_idx`
// and `..._id_idx1`, the two child indexes a leaf inherits from its grandparent
// and parent partitioned indexes respectively.
func TestCreateIndexRecursesPartitionTree(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatalf("expected *catalog.InMemory, got %T", cat)
	}

	for _, s := range []string{
		"CREATE TABLE part_dil (id int) PARTITION BY RANGE(id)",
		"CREATE TABLE part_dil_subpart PARTITION OF part_dil FOR VALUES FROM (1) TO (100) PARTITION BY RANGE(id)",
		"CREATE TABLE part_dil_subpart_child PARTITION OF part_dil_subpart FOR VALUES FROM (1) TO (100)",
		// Index on the top partitioned table: recurses subpart -> subpart_child.
		"CREATE INDEX part_dil_idx ON part_dil(id)",
		// Index on the intermediate sub-partition: recurses to subpart_child only.
		"CREATE INDEX part_dil_subpart_idx ON part_dil_subpart(id)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("runDDL(%q): %v", s, err)
		}
	}

	lookup := func(name string) (*catalog.Index, bool) {
		return cat.LookupIndex(parser.ObjectName{Name: name})
	}

	// Every auto-created child index must exist with the PG name.
	wantChildren := []struct {
		name   string
		parent string // expected immediate parent index name
	}{
		{"part_dil_subpart_id_idx", "part_dil_idx"},                  // grandparent -> subpart (intermediate)
		{"part_dil_subpart_child_id_idx", "part_dil_subpart_id_idx"}, // grandparent chain -> leaf
		{"part_dil_subpart_child_id_idx1", "part_dil_subpart_idx"},   // parent subpart index -> leaf (deduped)
	}
	for _, w := range wantChildren {
		child, ok := lookup(w.name)
		if !ok {
			t.Fatalf("expected child index %q to exist after CREATE INDEX recursion", w.name)
		}
		parent, ok := lookup(w.parent)
		if !ok {
			t.Fatalf("expected parent index %q to exist", w.parent)
		}
		if child.PartitionParentOID != parent.OID {
			t.Errorf("child %q PartitionParentOID = %d, want parent %q OID %d",
				w.name, child.PartitionParentOID, w.parent, parent.OID)
		}
	}

	// The parent index must register its direct child indexes so DROP/lock walks
	// can find them: part_dil_idx -> part_dil_subpart_id_idx (immediate child).
	topIdx, _ := lookup("part_dil_idx")
	directChildren := im.IndexPartitionChildren(topIdx.OID)
	if len(directChildren) != 1 || directChildren[0].Name != "part_dil_subpart_id_idx" {
		t.Errorf("part_dil_idx direct index children = %v, want [part_dil_subpart_id_idx]", indexNames(directChildren))
	}
}

func indexNames(idxs []*catalog.Index) []string {
	out := make([]string, len(idxs))
	for i, idx := range idxs {
		out[i] = idx.Name
	}
	return out
}

// TestCreateIndexRecursesPartitionTreeCarriesReloptions is the partition-child
// sibling of TestIndexReloptionsSurviveRestart (M0119-0004 index-reloptions
// follow-up): createPartitionChildIndexes previously copied HasPredicate/
// IncludeColumns/expression strings onto each auto-created child index but
// silently dropped the WITH-clause storage parameters (fillfactor,
// deduplicate_items, ...), so `CREATE INDEX ... WITH (fillfactor=N)` on a
// partitioned parent lost the option on every partition.
func TestCreateIndexRecursesPartitionTreeCarriesReloptions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE part_ro (id int) PARTITION BY RANGE(id)",
		"CREATE TABLE part_ro_child PARTITION OF part_ro FOR VALUES FROM (1) TO (100)",
		"CREATE INDEX part_ro_idx ON part_ro(id) WITH (fillfactor=60)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("runDDL(%q): %v", s, err)
		}
	}

	child, ok := cat.LookupIndex(parser.ObjectName{Name: "part_ro_child_id_idx"})
	if !ok {
		t.Fatal("expected child index part_ro_child_id_idx to exist after CREATE INDEX recursion")
	}
	if child.Fillfactor != 60 {
		t.Errorf("part_ro_child_id_idx.Fillfactor = %d, want 60 (parent's WITH-clause value)", child.Fillfactor)
	}
}
