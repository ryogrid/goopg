package executor

// alter_table_drop_if_exists_test.go — M0134-0002 C2 slice 7: `ALTER TABLE ...
// DROP COLUMN IF EXISTS` / `DROP CONSTRAINT IF EXISTS` on a missing object must
// emit PG's NOTICE `<object> "<name>" of relation "t" does not exist, skipping`
// and succeed instead of raising 42703/42704. The NOTICE text is byte-exact PG
// (ATExecDropColumn, postgres/src/backend/commands/tablecmds.c:9326-9328;
// ATExecDropConstraint, postgres/src/backend/commands/tablecmds.c:14060-14062).
// A real constraint of another kind must NOT be falsely skipped.

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

func TestAlterTableDropColumnIfExistsMissing(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t(a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	// DROP COLUMN IF EXISTS on a missing column: PG-exact NOTICE, no 42703,
	// table unchanged.
	if err := runDDL(t, ctx, "ALTER TABLE t DROP COLUMN IF EXISTS missing"); err != nil {
		t.Fatalf("DROP COLUMN IF EXISTS (missing): %v", err)
	}
	if len(ctx.Notices) != 1 || ctx.Notices[0] != `column "missing" of relation "t" does not exist, skipping` {
		t.Errorf("notices = %v, want [column \"missing\" of relation \"t\" does not exist, skipping]", ctx.Notices)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "t"})
	if !ok {
		t.Fatal("table t missing from catalog")
	}
	if len(tbl.Columns) != 1 || tbl.Columns[0].Name != "a" {
		t.Errorf("t.Columns = %+v, want exactly [a] (unchanged)", tbl.Columns)
	}

	// Without IF EXISTS the missing column must still raise 42703 (unchanged).
	ctx.Notices = nil
	err := runDDL(t, ctx, "ALTER TABLE t DROP COLUMN missing")
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42703" {
		t.Errorf("DROP COLUMN (missing, no IF EXISTS): err = %v (%T), want ExecError Code 42703", err, err)
	}
	if len(ctx.Notices) != 0 {
		t.Errorf("notices = %v, want none for the non-IF-EXISTS missing-column error", ctx.Notices)
	}
}

func TestAlterTableDropConstraintIfExistsMissing(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t(a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	// DROP CONSTRAINT IF EXISTS on a missing constraint: PG-exact NOTICE, no
	// 42704.
	if err := runDDL(t, ctx, "ALTER TABLE t DROP CONSTRAINT IF EXISTS missing"); err != nil {
		t.Fatalf("DROP CONSTRAINT IF EXISTS (missing): %v", err)
	}
	if len(ctx.Notices) != 1 || ctx.Notices[0] != `constraint "missing" of relation "t" does not exist, skipping` {
		t.Errorf("notices = %v, want [constraint \"missing\" of relation \"t\" does not exist, skipping]", ctx.Notices)
	}
	if _, ok := cat.LookupTable(parser.ObjectName{Name: "t"}); !ok {
		t.Fatal("table t missing from catalog")
	}

	// Without IF EXISTS the missing constraint must still raise 42704 (unchanged).
	ctx.Notices = nil
	err := runDDL(t, ctx, "ALTER TABLE t DROP CONSTRAINT missing")
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("DROP CONSTRAINT (missing, no IF EXISTS): err = %v (%T), want ExecError Code 42704", err, err)
	}
	if len(ctx.Notices) != 0 {
		t.Errorf("notices = %v, want none for the non-IF-EXISTS missing-constraint error", ctx.Notices)
	}
}

// TestAlterTableDropConstraintIfExistsRealConstraintNotSkipped — a real
// constraint of another kind must be dropped, not falsely skipped: the
// NOTICE-skip is only valid at the end-of-search fall-through.
func TestAlterTableDropConstraintIfExistsRealConstraintNotSkipped(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t(a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER TABLE t ADD CONSTRAINT uq_t UNIQUE (a)"); err != nil {
		t.Fatalf("ADD CONSTRAINT UNIQUE: %v", err)
	}
	if _, ok := cat.LookupIndex(parser.ObjectName{Name: "uq_t"}); !ok {
		t.Fatal("uq_t index not found before drop")
	}

	// IF EXISTS + a real UNIQUE constraint: dropped for real, no NOTICE.
	if err := runDDL(t, ctx, "ALTER TABLE t DROP CONSTRAINT IF EXISTS uq_t"); err != nil {
		t.Fatalf("DROP CONSTRAINT IF EXISTS (real): %v", err)
	}
	if len(ctx.Notices) != 0 {
		t.Errorf("notices = %v, want none when a real constraint is dropped", ctx.Notices)
	}
	if _, ok := cat.LookupIndex(parser.ObjectName{Name: "uq_t"}); ok {
		t.Error("uq_t index still present after DROP CONSTRAINT IF EXISTS")
	}

	// Dropping the now-missing constraint again emits the NOTICE and succeeds.
	if err := runDDL(t, ctx, "ALTER TABLE t DROP CONSTRAINT IF EXISTS uq_t"); err != nil {
		t.Fatalf("DROP CONSTRAINT IF EXISTS (missing 2nd time): %v", err)
	}
	if len(ctx.Notices) != 1 || ctx.Notices[0] != `constraint "uq_t" of relation "t" does not exist, skipping` {
		t.Errorf("notices = %v, want [constraint \"uq_t\" of relation \"t\" does not exist, skipping]", ctx.Notices)
	}
}
