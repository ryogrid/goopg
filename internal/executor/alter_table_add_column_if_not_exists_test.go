package executor

// alter_table_add_column_if_not_exists_test.go — M0134-0002 C2: `ALTER TABLE ...
// ADD COLUMN IF NOT EXISTS <coldef>` must parse, and re-adding an existing column
// emits PG's NOTICE `column "c" of relation "r" already exists, skipping` and
// skips instead of raising 42701. The NOTICE text is byte-exact PG
// (check_for_column_name_collision's if_not_exists branch,
// postgres/src/backend/commands/tablecmds.c:7677-7684; the NOTICE lines in
// postgres/src/test/regress/expected/alter_table.out, e.g. line 3734).

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

func TestAlterTableAddColumnIfNotExists(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t(a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	// Re-add the existing column: PG-exact NOTICE, no 42701, no duplicate.
	if err := runDDL(t, ctx, "ALTER TABLE t ADD COLUMN IF NOT EXISTS a int"); err != nil {
		t.Fatalf("ADD COLUMN IF NOT EXISTS (existing): %v", err)
	}
	if len(ctx.Notices) != 1 || ctx.Notices[0] != `column "a" of relation "t" already exists, skipping` {
		t.Errorf("notices = %v, want [column \"a\" of relation \"t\" already exists, skipping]", ctx.Notices)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "t"})
	if !ok {
		t.Fatal("table t missing from catalog")
	}
	if len(tbl.Columns) != 1 {
		t.Fatalf("t.Columns = %+v, want exactly 1 column (no duplicate added)", tbl.Columns)
	}

	// A genuinely new column: added normally, no NOTICE.
	ctx.Notices = nil
	if err := runDDL(t, ctx, "ALTER TABLE t ADD COLUMN IF NOT EXISTS b int"); err != nil {
		t.Fatalf("ADD COLUMN IF NOT EXISTS (new): %v", err)
	}
	if len(ctx.Notices) != 0 {
		t.Errorf("notices = %v, want none for a new column", ctx.Notices)
	}
	if len(tbl.Columns) != 2 || tbl.Columns[1].Name != "b" {
		t.Errorf("t.Columns = %+v, want [a b]", tbl.Columns)
	}

	// Without IF NOT EXISTS the duplicate must still raise 42701 (unchanged).
	ctx.Notices = nil
	err := runDDL(t, ctx, "ALTER TABLE t ADD COLUMN a int")
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42701" {
		t.Errorf("ADD COLUMN (duplicate, no IF NOT EXISTS): err = %v (%T), want ExecError Code 42701", err, err)
	}
	if len(ctx.Notices) != 0 {
		t.Errorf("notices = %v, want none for the non-IF-NOT-EXISTS duplicate error", ctx.Notices)
	}
}
