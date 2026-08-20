package executor

import (
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"testing"
)

// TestCollectAllViewTransitiveDepsExcludesStartOnCycle pins M0134-0042: the
// BFS in collectAllViewTransitiveDeps must seed its `seen` set with the
// start view before traversing, so that a circular view dependency (view A
// depends on view B which depends back on view A) never re-adds the start
// view into its own transitive-dependency result. Without the seed, `DROP
// VIEW lock_view3 CASCADE` over the cycle
// lock_view2 -> lock_view3 -> lock_view2 (via CREATE OR REPLACE VIEW
// lock_view2 AS SELECT * FROM lock_view3) wrongly re-discovers lock_view3
// itself, producing "drop cascades to 2 other objects" instead of PG's
// "drop cascades to view lock_view2" (postgres/src/test/regress/sql/lock.sql:
// lock_view2/lock_view3 cycle, expected/lock.out "drop cascades to view
// lock_view2").
func TestCollectAllViewTransitiveDepsExcludesStartOnCycle(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE lock_tbl1 (a BIGINT)`); err != nil {
		t.Fatalf("CREATE TABLE lock_tbl1: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE lock_tbl1a (a BIGINT)`); err != nil {
		t.Fatalf("CREATE TABLE lock_tbl1a: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE VIEW lock_view2 AS SELECT * FROM lock_tbl1, lock_tbl1a`); err != nil {
		t.Fatalf("CREATE VIEW lock_view2: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE VIEW lock_view3 AS SELECT * FROM lock_view2`); err != nil {
		t.Fatalf("CREATE VIEW lock_view3: %v", err)
	}
	// Redefine lock_view2 to depend on lock_view3, closing the cycle:
	// lock_view3 -> lock_view2 -> lock_view3.
	if err := runDDL(t, ctx, `CREATE OR REPLACE VIEW lock_view2 AS SELECT * FROM lock_view3`); err != nil {
		t.Fatalf("CREATE OR REPLACE VIEW lock_view2: %v", err)
	}

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatalf("catalog is %T, want *catalog.InMemory", cat)
	}

	deps := collectAllViewTransitiveDeps(im, parser.ObjectName{Name: "lock_view3"}, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))

	for _, d := range deps {
		if d.name.String() == "lock_view3" {
			t.Fatalf("collectAllViewTransitiveDeps(lock_view3) re-included the start view in its own result: %+v", deps)
		}
	}
	if len(deps) != 1 || deps[0].name.String() != "lock_view2" {
		t.Fatalf("collectAllViewTransitiveDeps(lock_view3) = %+v, want exactly [lock_view2]", deps)
	}
}
