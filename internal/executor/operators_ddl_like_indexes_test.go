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

