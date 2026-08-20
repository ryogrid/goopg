package executor

// operators_ddl_add_constraint_using_index_test.go pins `ALTER TABLE t ADD
// CONSTRAINT c PRIMARY KEY|UNIQUE USING INDEX idx` (M0134-0005x). Both the
// parser and the executor previously discarded this form entirely — the
// index name was parsed and thrown away and the action downgraded to
// AlterTableNoOp (internal/parser/ddl.go), so the statement silently did
// nothing. tablecmds.c:ATExecAddIndexConstraint is the oracle: adopt the
// existing unique index, rename it to the constraint name when they differ
// (emitting the rename NOTICE), and — for PRIMARY KEY only — synthesize NOT
// NULL on every key column via the same path the column-list form uses
// (M0134-0005o).

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestAlterTableAddConstraintUsingIndex covers the `cnn_pk` regress fixture
// (constraints.sql:751-757): PRIMARY KEY USING INDEX promotes the existing
// index, renames it, marks the key column NOT NULL, and registers the
// synthesized not-null constraint.
func TestAlterTableAddConstraintUsingIndexPrimaryKey(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE cnn_pk (a int, b int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE UNIQUE INDEX cnn_uq ON cnn_pk (b)"); err != nil {
		t.Fatalf("CREATE UNIQUE INDEX: %v", err)
	}
	ctx.Notices = nil
	if err := runDDL(t, ctx, "ALTER TABLE cnn_pk ADD CONSTRAINT cnn_primarykey PRIMARY KEY USING INDEX cnn_uq"); err != nil {
		t.Fatalf("ALTER TABLE ADD CONSTRAINT ... PRIMARY KEY USING INDEX: %v", err)
	}

	// The rename NOTICE (tablecmds.c:9751-9754) fires because the constraint
	// name "cnn_primarykey" differs from the index name "cnn_uq".
	wantNotice := `ALTER TABLE / ADD CONSTRAINT USING INDEX will rename index "cnn_uq" to "cnn_primarykey"`
	if len(ctx.Notices) != 1 || ctx.Notices[0] != wantNotice {
		t.Errorf("notices = %v, want [%s]", ctx.Notices, wantNotice)
	}

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "cnn_pk"})
	if !ok {
		t.Fatal("cnn_pk not found after ALTER")
	}
	col, ok := cat.LookupColumn(tbl, "b")
	if !ok {
		t.Fatal("column b not found")
	}
	if !col.NotNull {
		t.Error("column b should be NOT NULL after PRIMARY KEY USING INDEX")
	}
	foundNN := false
	for _, nc := range tbl.NotNullConstraints {
		if nc.ColName == "b" {
			foundNN = true
		}
	}
	if !foundNN {
		t.Error("expected a synthesized NOT NULL constraint on column b")
	}

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}
	// The old name must be gone and the new one must carry indisprimary +
	// IsConstraint.
	if _, exists := im.LookupIndex(parser.ObjectName{Name: "cnn_uq"}, catalog.DefaultDBOid); exists {
		t.Error("old index name \"cnn_uq\" should no longer exist after the rename")
	}
	var renamed *catalog.Index
	for _, idx := range im.IndexesOnTable(tbl, catalog.DefaultDBOid) {
		if idx.Name == "cnn_primarykey" {
			renamed = idx
		}
	}
	if renamed == nil {
		t.Fatal("expected index \"cnn_primarykey\" after promotion")
	}
	if !renamed.Primary {
		t.Error("promoted index should have Primary=true")
	}
	if !renamed.IsConstraint {
		t.Error("promoted index should have IsConstraint=true")
	}
	if !cat.HasPrimaryKey(tbl) {
		t.Error("HasPrimaryKey should be true after PRIMARY KEY USING INDEX")
	}
}

// TestAlterTableAddConstraintUsingIndexUnique covers the sibling `cnn_uq`
// regress fixture (constraints.sql:1231-1234): ADD UNIQUE USING INDEX
// promotes the index to a unique constraint (IsConstraint=true, Primary
// stays false) with no NOT NULL synthesis and, since the index name already
// matches, no rename NOTICE.
func TestAlterTableAddConstraintUsingIndexUnique(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE cnn_uq2 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE UNIQUE INDEX cnn_uq2_idx ON cnn_uq2 (a)"); err != nil {
		t.Fatalf("CREATE UNIQUE INDEX: %v", err)
	}
	ctx.Notices = nil
	if err := runDDL(t, ctx, "ALTER TABLE cnn_uq2 ADD UNIQUE USING INDEX cnn_uq2_idx"); err != nil {
		t.Fatalf("ALTER TABLE ADD UNIQUE USING INDEX: %v", err)
	}
	if len(ctx.Notices) != 0 {
		t.Errorf("expected no rename NOTICE when the constraint has no explicit name, got %v", ctx.Notices)
	}

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "cnn_uq2"})
	if !ok {
		t.Fatal("cnn_uq2 not found after ALTER")
	}
	col, ok := cat.LookupColumn(tbl, "a")
	if !ok {
		t.Fatal("column a not found")
	}
	if col.NotNull {
		t.Error("ADD UNIQUE USING INDEX must NOT synthesize NOT NULL (unlike PRIMARY KEY)")
	}
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}
	var idx *catalog.Index
	for _, cand := range im.IndexesOnTable(tbl, catalog.DefaultDBOid) {
		if cand.Name == "cnn_uq2_idx" {
			idx = cand
		}
	}
	if idx == nil {
		t.Fatal("expected index \"cnn_uq2_idx\" to still exist (no rename)")
	}
	if !idx.IsConstraint {
		t.Error("adopted index should have IsConstraint=true")
	}
	if idx.Primary {
		t.Error("ADD UNIQUE USING INDEX must not set Primary")
	}
}

// TestAlterTableAddConstraintUsingIndexNonUnique rejects a non-unique index
// with PG's own message/SQLSTATE (parse_utilcmd.c:2456-2462): 42809 with the
// DETAIL "Cannot create a primary key or unique constraint using such an
// index."
func TestAlterTableAddConstraintUsingIndexNonUnique(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE cnn_nonuq (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX cnn_nonuq_idx ON cnn_nonuq (a)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	err := runDDL(t, ctx, "ALTER TABLE cnn_nonuq ADD CONSTRAINT cnn_pkey PRIMARY KEY USING INDEX cnn_nonuq_idx")
	if err == nil {
		t.Fatal("expected an error adopting a non-unique index as a primary key")
	}
	execErr, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("error is not *ExecError: %v (%T)", err, err)
	}
	if execErr.Code != "42809" {
		t.Errorf("SQLSTATE = %q, want 42809", execErr.Code)
	}
	wantMsg := `"cnn_nonuq_idx" is not a unique index`
	if execErr.Message != wantMsg {
		t.Errorf("message = %q, want %q", execErr.Message, wantMsg)
	}
	wantDetail := "Cannot create a primary key or unique constraint using such an index."
	if execErr.Detail != wantDetail {
		t.Errorf("detail = %q, want %q", execErr.Detail, wantDetail)
	}
}
