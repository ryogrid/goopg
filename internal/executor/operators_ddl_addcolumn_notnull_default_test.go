package executor

// operators_ddl_addcolumn_notnull_default_test.go — M0134-0085 (fast_default.sql):
// pins two independent bugs surfaced by that regress case.

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestAddColumnNotNullDefaultAllowedOnNonEmptyTable pins the ADD COLUMN
// NOT NULL DEFAULT half of M0134-0085. PostgreSQL's ATExecAddColumn
// (postgres/src/backend/commands/tablecmds.c:7217+) has no "non-empty table"
// restriction for `ADD COLUMN ... NOT NULL DEFAULT <const>` at all — with a
// constant default it records attmissingval and every pre-existing row is
// satisfied via the fast-default backfill, no rewrite or per-row check
// needed. goopg instead unconditionally rejected `ADD COLUMN ... NOT NULL`
// with 0A000 "... is only supported on empty tables" whenever the table had
// any rows on disk, even when a usable constant DEFAULT was given — the
// check ran before the fast-default (MissingValue) datum was ever computed.
// Fixed by gating the block-count check on "no fast-default value
// available" instead of "table is non-empty".
func TestAddColumnNotNullDefaultAllowedOnNonEmptyTable(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE t (id int PRIMARY KEY, a int)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO t (id, a) VALUES (1, 2)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	// Pre-fix this raised: ERROR: ALTER TABLE ADD COLUMN ... NOT NULL is
	// only supported on empty tables (0A000) — a message that does not even
	// exist anywhere in PostgreSQL's own source.
	if err := runDDL(t, ctx, `ALTER TABLE t ADD COLUMN x int NOT NULL DEFAULT 4`); err != nil {
		t.Fatalf("ALTER TABLE ADD COLUMN x int NOT NULL DEFAULT 4 on a non-empty table: %v", err)
	}
	rows := runQuery(t, ctx, `SELECT id, a, x FROM t`)
	if len(rows) != 1 {
		t.Fatalf("SELECT after ADD COLUMN NOT NULL DEFAULT = %v rows, want 1", rows)
	}
	if rows[0][2].Kind != KindInt || rows[0][2].Int != 4 {
		t.Fatalf("pre-existing row's new column x = %+v, want fast-default 4", rows[0][2])
	}

	// Regression guard: NOT NULL with NO default on a non-empty table must
	// still be rejected (no fast-default value exists to satisfy the
	// constraint for the pre-existing row).
	if err := runDDL(t, ctx, `ALTER TABLE t ADD COLUMN y int NOT NULL`); err == nil {
		t.Fatal("ALTER TABLE ADD COLUMN y int NOT NULL (no default, non-empty table) unexpectedly succeeded")
	}
}

// TestCreateIndexAndCreateTriggerHonourSearchPath pins the second
// M0134-0085 bug: execCreateIndex and execCreateTrigger both called
// o.ctx.Catalog.LookupTable(s.Table, ...) directly with no search_path
// fallback for an unqualified table name, unlike every sibling DDL path
// (SELECT, DROP TABLE, execAlterTable, ALTER TABLE ... INHERIT — the latter
// pinned by the near-identical TestInheritsUnqualifiedParentHonoursSearchPath,
// M0134-0024b). `SET search_path = s; CREATE INDEX ON t(...)` /
// `CREATE TRIGGER ... ON t ...` raised a spurious 42P01 even though `t`
// resolved fine in schema s for every other statement type. Fixed by
// switching both call sites to the existing lookupTableWithSearch helper.
func TestCreateIndexAndCreateTriggerHonourSearchPath(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	searchPath := "sp85"
	ctx.GetSetting = func(name string) (string, bool) {
		if name == "search_path" {
			return searchPath, true
		}
		return "", false
	}

	if err := runDDL(t, ctx, `CREATE SCHEMA sp85`); err != nil {
		t.Fatalf("CREATE SCHEMA sp85: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE sp85.t (id int PRIMARY KEY, a int)`); err != nil {
		t.Fatalf("CREATE TABLE sp85.t: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE FUNCTION sp85.trig_fn() RETURNS trigger LANGUAGE plpgsql AS $$ begin return NEW; end; $$`); err != nil {
		t.Fatalf("CREATE FUNCTION sp85.trig_fn: %v", err)
	}

	// Pre-fix both of the following raised 42P01 "relation \"t\" does not
	// exist" even though `t` lives in the search_path schema sp85.
	if err := runDDL(t, ctx, `CREATE INDEX ix85 ON t (a)`); err != nil {
		t.Fatalf("CREATE INDEX ix85 ON t (a) [unqualified, search_path=sp85]: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TRIGGER trg85 BEFORE UPDATE ON t FOR EACH ROW EXECUTE PROCEDURE trig_fn()`); err != nil {
		t.Fatalf("CREATE TRIGGER trg85 ON t [unqualified, search_path=sp85]: %v", err)
	}

	if _, ok := cat.LookupIndex(parser.ObjectName{Schema: "sp85", Name: "ix85"}); !ok {
		t.Fatal("sp85.ix85 not found in catalog after CREATE INDEX")
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: "sp85", Name: "t"})
	if !ok {
		t.Fatal("sp85.t not found in catalog")
	}
	found := false
	for _, tr := range tbl.Triggers {
		if tr.Name == "trg85" {
			found = true
		}
	}
	if !found {
		t.Fatalf("trigger trg85 not registered on sp85.t, got triggers: %+v", tbl.Triggers)
	}
}
