package executor

// operators_ddl_inherits_search_path_test.go — M0134-0024b: `CREATE TABLE
// child () INHERITS (parent)` and `ALTER TABLE child INHERIT parent` must
// resolve an unqualified parent name through the session search_path,
// exactly as every other unqualified relation reference does (mirrors
// tablecmds.c:868 DefineRelation's RangeVarGetRelid). Before the fix, both
// call sites used the raw o.ctx.Catalog.LookupTable, which only defaults an
// unqualified name to "public" (catalog.InMemory.LookupTable) — an
// unqualified parent living in any other schema failed with 42P01 even
// though `SELECT * FROM parent` in the same session resolved it fine via
// search_path.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestInheritsUnqualifiedParentHonoursSearchPath is the brief's named test
// (M0134-0024b): it covers all four acceptance-criteria DDL shapes (AC1-4)
// and then proves inheritance actually functions after the DDL succeeds
// (AC6) — the child inherits the parent's columns, and a row inserted into
// the child is visible via a SELECT on the parent.
func TestInheritsUnqualifiedParentHonoursSearchPath(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	searchPath := "q3plain"
	ctx.GetSetting = func(name string) (string, bool) {
		if name == "search_path" {
			return searchPath, true
		}
		return "", false
	}

	if err := runDDL(t, ctx, `CREATE SCHEMA q3plain`); err != nil {
		t.Fatalf("CREATE SCHEMA q3plain: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE q3plain.plain1 (a int PRIMARY KEY, b int)`); err != nil {
		t.Fatalf("CREATE TABLE q3plain.plain1: %v", err)
	}

	// AC1 (the brief's live reproduction): unqualified INHERITS resolves the
	// parent through search_path. Pre-fix this failed with:
	//   ERROR: relation "plain1" does not exist (42P01)
	// even though plain1 lives in the search_path schema q3plain.
	if err := runDDL(t, ctx, `CREATE TABLE plain1_1 () INHERITS (plain1)`); err != nil {
		t.Fatalf("CREATE TABLE plain1_1 () INHERITS (plain1) [unqualified, search_path=q3plain]: %v", err)
	}

	// AC2 (regression guard): qualified INHERITS still works.
	if err := runDDL(t, ctx, `CREATE TABLE q3plain.plain1_2 () INHERITS (q3plain.plain1)`); err != nil {
		t.Fatalf("CREATE TABLE q3plain.plain1_2 () INHERITS (q3plain.plain1) [qualified]: %v", err)
	}

	// AC3 (regression guard): unqualified INHERITS in public still works.
	searchPath = "public"
	if err := runDDL(t, ctx, `CREATE TABLE pub_parent (x int)`); err != nil {
		t.Fatalf("CREATE TABLE pub_parent: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE pub_child () INHERITS (pub_parent)`); err != nil {
		t.Fatalf("CREATE TABLE pub_child () INHERITS (pub_parent) [unqualified, search_path=public]: %v", err)
	}

	// AC4: ALTER TABLE ... INHERIT works unqualified outside public.
	searchPath = "q3plain"
	if err := runDDL(t, ctx, `CREATE TABLE plain2 (a int)`); err != nil {
		t.Fatalf("CREATE TABLE plain2: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE plain3 (a int)`); err != nil {
		t.Fatalf("CREATE TABLE plain3: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE plain3 INHERIT plain2`); err != nil {
		t.Fatalf("ALTER TABLE plain3 INHERIT plain2 [unqualified, search_path=q3plain]: %v", err)
	}

	// AC6: prove inheritance actually WORKS, not merely that the DDL
	// stopped erroring.
	parent, ok := cat.LookupTable(parser.ObjectName{Schema: "q3plain", Name: "plain1"})
	if !ok {
		t.Fatal("q3plain.plain1 not found in catalog")
	}
	child, ok := cat.LookupTable(parser.ObjectName{Schema: "q3plain", Name: "plain1_1"})
	if !ok {
		t.Fatal("q3plain.plain1_1 not found in catalog")
	}
	if len(child.Columns) != len(parent.Columns) {
		t.Fatalf("plain1_1 has %d columns, want %d inherited from plain1: %+v",
			len(child.Columns), len(parent.Columns), child.Columns)
	}
	for i, pc := range parent.Columns {
		if !strings.EqualFold(child.Columns[i].Name, pc.Name) {
			t.Fatalf("plain1_1 column[%d] = %q, want inherited column %q", i, child.Columns[i].Name, pc.Name)
		}
	}

	// A row inserted into the child must be visible via SELECT on the parent
	// (the whole point of INHERITS — a plain CREATE TABLE + no linkage would
	// be a worse bug than the one being fixed).
	if err := runDDL(t, ctx, `INSERT INTO q3plain.plain1_1 (a, b) VALUES (42, 99)`); err != nil {
		t.Fatalf("INSERT INTO q3plain.plain1_1: %v", err)
	}
	rows := runQuery(t, ctx, `SELECT a, b FROM q3plain.plain1`)
	found := false
	for _, r := range rows {
		if r[0].Kind == KindInt && r[0].Int == 42 && r[1].Kind == KindInt && r[1].Int == 99 {
			found = true
		}
	}
	if !found {
		t.Fatalf("SELECT a, b FROM q3plain.plain1 does not include the row inserted into child plain1_1: %v", rows)
	}
}
