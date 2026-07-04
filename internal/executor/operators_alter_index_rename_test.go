package executor

// operators_alter_index_rename_test.go pins the DU-002 slice 443 fix:
// `ALTER INDEX name RENAME TO newname` on a real (non-TOAST) index was a
// functional no-op — the parser already routed it through the shared
// AlterTableStmt/AlterTableRenameTable action (M0118-0008 TOAST-exposure
// slice 4), but the executor's index branch (reached when the target isn't
// a heap table) only handled AlterTableAlterColumnSet/AlterTableSetStatistics/
// AlterIndexAttachPartition/AlterIndexSetReloptions — AlterTableRenameTable
// fell through to "Other ALTER actions on index: silently accept in v0." The
// CommandComplete tag reported "ALTER TABLE" (mistagged; should be "ALTER
// INDEX") and, worse, the rename never happened at all.

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

func TestAlterIndexRenameToApplies(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE idxsrc (a int, b int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX idx_ab ON idxsrc (b)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER INDEX idx_ab RENAME TO idx_ab_renamed"); err != nil {
		t.Fatalf("ALTER INDEX RENAME TO: %v", err)
	}
	idx, ok := cat.LookupIndex(parser.ObjectName{Name: "idx_ab_renamed"})
	if !ok {
		t.Fatalf("catalog has no index named idx_ab_renamed after rename")
	}
	if idx.Table == nil || idx.Table.Name != "idxsrc" {
		t.Fatalf("renamed index lost its owning table")
	}
	if len(idx.Columns) != 1 || idx.Columns[0] != "b" {
		t.Errorf("renamed index Columns = %v, want [b]", idx.Columns)
	}
	if _, stillThere := cat.LookupIndex(parser.ObjectName{Name: "idx_ab"}); stillThere {
		t.Errorf("catalog still has the old index name idx_ab after rename")
	}
	// A query against the owning table must still work post-rename.
	rows := runQueryRows(t, ctx, "SELECT a FROM idxsrc")
	_ = rows
}

func TestAlterIndexRenameToUnknownRelation(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	err := runDDL(t, ctx, "ALTER INDEX no_such_index RENAME TO whatever")
	if err == nil {
		t.Fatalf("ALTER INDEX RENAME TO on a nonexistent index: got nil error, want 42P01")
	}
}

func TestAlterIndexRenameToCollision(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE idxcoll (a int, b int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX idx_c1 ON idxcoll (a)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX idx_c2 ON idxcoll (b)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	err := runDDL(t, ctx, "ALTER INDEX idx_c1 RENAME TO idx_c2")
	if err == nil {
		t.Fatalf("ALTER INDEX RENAME TO an already-existing name: got nil error, want 42P07")
	}
}

// TestAlterIndexRenameToCommandTag confirms the CommandComplete tag is the
// PG-accurate "ALTER INDEX", not the generic "ALTER TABLE" the shared
// AlterTableStmt would otherwise report (dispatch.go's ddlTag reads
// AlterTableStmt.TagOverride).
func TestAlterIndexRenameToCommandTag(t *testing.T) {
	stmts, err := parser.Parse("ALTER INDEX idxtag1 RENAME TO idxtag2")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("Parse returned %d statements, want 1", len(stmts))
	}
	at, ok := stmts[0].(*parser.AlterTableStmt)
	if !ok {
		t.Fatalf("Parse = %T, want *parser.AlterTableStmt", stmts[0])
	}
	if at.TagOverride != "ALTER INDEX" {
		t.Errorf("TagOverride = %q, want %q", at.TagOverride, "ALTER INDEX")
	}
}
