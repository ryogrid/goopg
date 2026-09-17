package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestLikeIncludingIndexesCopiesPK verifies that LIKE source INCLUDING INDEXES
// copies the primary key index from the source table to the new table.
func TestLikeIncludingIndexesCopiesPK(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	// Create source table with a primary key.
	if err := runDDL(t, ctx, `CREATE TABLE src (id int PRIMARY KEY, val text)`); err != nil {
		t.Fatalf("CREATE TABLE src: %v", err)
	}

	// Create new table using LIKE INCLUDING INDEXES — should inherit the PK.
	if err := runDDL(t, ctx, `CREATE TABLE dst (extra text, LIKE src INCLUDING INDEXES)`); err != nil {
		t.Fatalf("CREATE TABLE dst: %v", err)
	}

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not InMemory")
	}
	dstTbl, ok := cat.LookupTable(parser.ObjectName{Name: "dst"})
	if !ok {
		t.Fatal("dst table not found")
	}
	if !im.HasPrimaryKey(dstTbl) {
		t.Error("dst should have a primary key after LIKE INCLUDING INDEXES")
	}
}

// TestLikeIncludingIndexesCopiesPKDeferrable verifies that LIKE source
// INCLUDING INDEXES also copies the source PK's DEFERRABLE INITIALLY DEFERRED
// property onto the cloned PK index, mirroring PG's generateClonedIndexStmt
// (postgres/src/backend/parser/parse_utilcmd.c:1804-1805):
//
//	index->deferrable = conrec->condeferrable;
//	index->initdeferred = conrec->condeferred;
//
// Before the fix, operators_ddl.go's LIKE INCLUDING INDEXES PK-copy branch
// appended idx.Columns to s.PrimaryKey but never set
// s.PrimaryKeyDeferrable/s.PrimaryKeyInitiallyDeferred, so the cloned PK
// index was always created non-deferrable.
func TestLikeIncludingIndexesCopiesPKDeferrable(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE src (id int, PRIMARY KEY (id) DEFERRABLE INITIALLY DEFERRED)`); err != nil {
		t.Fatalf("CREATE TABLE src: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE dst (extra text, LIKE src INCLUDING INDEXES)`); err != nil {
		t.Fatalf("CREATE TABLE dst: %v", err)
	}

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not InMemory")
	}
	dstTbl, ok := cat.LookupTable(parser.ObjectName{Name: "dst"})
	if !ok {
		t.Fatal("dst table not found")
	}
	idxs := im.IndexesOnTable(dstTbl, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
	var pk *catalog.Index
	for i := range idxs {
		if idxs[i].Primary {
			pk = idxs[i]
			break
		}
	}
	if pk == nil {
		t.Fatal("dst has no PK index after LIKE INCLUDING INDEXES")
	}
	if !pk.Deferrable {
		t.Errorf("cloned PK index %q: Deferrable=false, want true (source PK is DEFERRABLE)", pk.Name)
	}
	if !pk.InitiallyDeferred {
		t.Errorf("cloned PK index %q: InitiallyDeferred=false, want true (source PK is INITIALLY DEFERRED)", pk.Name)
	}
}

// TestLikeIncludingIndexesMultiplePKError verifies that specifying two primary
// keys (one explicit and one via LIKE INCLUDING INDEXES) causes an error.
func TestLikeIncludingIndexesMultiplePKError(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE src (id int PRIMARY KEY, val text)`); err != nil {
		t.Fatalf("CREATE TABLE src: %v", err)
	}

	// Should fail: src brings a PK via LIKE, and PRIMARY KEY(extra) is explicit.
	err := runDDL(t, ctx, `CREATE TABLE dst (extra text, LIKE src INCLUDING INDEXES, PRIMARY KEY(extra))`)
	if err == nil {
		t.Fatal("expected error for multiple primary keys, got nil")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42P16" {
		t.Errorf("expected ExecError code 42P16, got %v", err)
	}
}

// TestLikeIncludingIndexesMarksUniqueAsConstraint verifies that a UNIQUE
// (non-PK) index cloned via LIKE ... INCLUDING INDEXES is marked
// constraint-backed (IsConstraint), matching every other UNIQUE-index
// creation path (inline column, table-level, named, PK auto-index). Before
// M0143-0003f this clone never set IsConstraint, so the resulting index
// never appeared in pg_constraint even without any restart — a live bug, not
// a restart-durability gap (see M0143-0003c/e's sibling fixes, which only
// address indexes that already carried IsConstraint/IsExclusion but lost it
// on reload).
func TestLikeIncludingIndexesMarksUniqueAsConstraint(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE src (id int PRIMARY KEY, val text UNIQUE)`); err != nil {
		t.Fatalf("CREATE TABLE src: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE dst (extra text, LIKE src INCLUDING INDEXES)`); err != nil {
		t.Fatalf("CREATE TABLE dst: %v", err)
	}

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not InMemory")
	}
	dstTbl, ok := cat.LookupTable(parser.ObjectName{Name: "dst"})
	if !ok {
		t.Fatal("dst table not found")
	}
	var uniqIdx *catalog.Index
	for _, idx := range im.IndexesOnTable(dstTbl, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)) {
		if idx.Unique && !idx.Primary {
			uniqIdx = idx
			break
		}
	}
	if uniqIdx == nil {
		t.Fatal("dst has no cloned UNIQUE (non-PK) index")
	}
	if !uniqIdx.IsConstraint {
		t.Errorf("cloned UNIQUE index %q: IsConstraint=false, want true (must appear in pg_constraint)", uniqIdx.Name)
	}
}

// TestPartitionOfInlineUniqueMarksAsConstraint verifies that a UNIQUE column
// constraint declared directly in a PARTITION OF child's column list
// (`CREATE TABLE child PARTITION OF parent (col UNIQUE) FOR VALUES …`,
// poc.UniqueColumns) is marked constraint-backed, matching every other
// UNIQUE-index creation path. Companion to
// TestLikeIncludingIndexesMarksUniqueAsConstraint (M0143-0003f).
func TestPartitionOfInlineUniqueMarksAsConstraint(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE rp (i int, j text) PARTITION BY RANGE (i)",
		"CREATE TABLE rp_1 PARTITION OF rp (j UNIQUE) FOR VALUES FROM (0) TO (100)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("runDDL(%q): %v", s, err)
		}
	}

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not InMemory")
	}
	childTbl, ok := cat.LookupTable(parser.ObjectName{Name: "rp_1"})
	if !ok {
		t.Fatal("rp_1 table not found")
	}
	var uniqIdx *catalog.Index
	for _, idx := range im.IndexesOnTable(childTbl, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)) {
		if idx.Unique && !idx.Primary {
			uniqIdx = idx
			break
		}
	}
	if uniqIdx == nil {
		t.Fatal("rp_1 has no UNIQUE (non-PK) index for its inline column-constraint")
	}
	if !uniqIdx.IsConstraint {
		t.Errorf("PARTITION OF inline UNIQUE index %q: IsConstraint=false, want true (must appear in pg_constraint)", uniqIdx.Name)
	}
}

// TestDropTableSkipsAlreadyCascadedChildren verifies that when a table is
// cascade-dropped as a child of an earlier table in the same DROP TABLE
// statement, it is not errored on when encountered explicitly later.
func TestDropTableSkipsAlreadyCascadedChildren(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	// parent INHERITS nothing; child INHERITS parent.
	if err := runDDL(t, ctx, `CREATE TABLE parent (id int)`); err != nil {
		t.Fatalf("CREATE TABLE parent: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE child () INHERITS (parent)`); err != nil {
		t.Fatalf("CREATE TABLE child: %v", err)
	}

	// Drop both parent (CASCADE) and child explicitly in one statement.
	// Without the fix this would fail with "table child does not exist".
	if err := runDDL(t, ctx, `DROP TABLE parent, child CASCADE`); err != nil {
		t.Fatalf("DROP TABLE parent, child CASCADE: %v", err)
	}
}

