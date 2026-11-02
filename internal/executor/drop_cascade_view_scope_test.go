package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// M0148-0001h: DROP TABLE … CASCADE must not drop views over a different
// table of the same name. PG walks pg_depend by OID (findDependentObjects);
// goopg matches views by name, and its CASCADE path passed the raw
// unqualified name (empty schema = "any schema"), so `drop table t1 cascade`
// on public.t1 also dropped vs2's view over an explicitly qualified vs2.t1.
// The CASCADE path now scans by the resolved identity (as RESTRICT does,
// M0134-0069) and a qualified reference must name the dropped table's schema.
// (Unqualified references keep the old rule; the aggregates.sql case — views
// created unqualified under search_path testviewschm2,public, which goopg
// places in public — is ledgered with the CREATE VIEW placement defect.)
func TestDropTableCascadeLeavesOtherSchemasViews(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"create schema vs2",
		"create table vs2.t1 (num int)",
		"create view vs2.v_other as select * from vs2.t1",
		"create table t1 (a int)",
		"create view v_mine as select * from t1",
		"drop table t1 cascade",
	} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	got := strings.Join(renderRows(runSQLWith(t, ctx, "select relname from pg_class where relname in ('v_other', 'v_mine') order by 1", ps)), ",")
	if got != "v_other" {
		t.Errorf("after drop table t1 cascade: views left = %q, want only v_other (vs2.t1's view must survive)", got)
	}
}

// An unqualified CREATE VIEW lands in the first search_path schema
// (DefineView → RangeVarGetCreationNamespace), so DROP TABLE … CASCADE on
// that schema's table finds and drops it — create_function_sql's
// `CREATE VIEW functestv3 AS SELECT * FROM functest3` under
// `search_path = temp_func_test, public`. A view over a temporary table is
// implicitly temporary (view.c isQueryUsingTempRelation) and stays bare.
func TestCreateViewPlacedBySearchPath(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "create schema vs3")
	ctx.GetSetting = func(name string) (string, bool) {
		if name == "search_path" {
			return "vs3, public", true
		}
		return "", false
	}
	for _, q := range []string{
		"create table vs3.t3 (a int)",
		"create view v3 as select * from vs3.t3",
		"create temp table tt3 (a int)",
		"create view vtemp as select * from tt3",
	} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	get := func(q string) string { return strings.Join(renderRows(runSQLWith(t, ctx, q, ps)), ",") }
	if got := get("select n.nspname from pg_class c join pg_namespace n on n.oid = c.relnamespace where c.relname = 'v3'"); got != "vs3" {
		t.Errorf("v3 created under search_path vs3, public lives in %q, want vs3", got)
	}
	if got := get("select n.nspname from pg_class c join pg_namespace n on n.oid = c.relnamespace where c.relname = 'vtemp'"); got == "vs3" {
		t.Errorf("vtemp over a temp table was placed in vs3; a view over a temp relation is temporary")
	}
	runSQL(t, ctx, "drop table vs3.t3 cascade")
	if got := get("select relname from pg_class where relname = 'v3'"); got != "" {
		t.Errorf("drop table vs3.t3 cascade left view %q", got)
	}
}

// DROP SCHEMA … CASCADE drops the views placed in the schema. The schema used
// to be unregistered before its members were dropped, and the member-view
// drop (IF EXISTS, schema-qualified) then reported `schema "s" does not
// exist, skipping` and left each view behind — invisible until CREATE VIEW
// began honouring search_path, which put create_view.sql's views there.
func TestDropSchemaCascadeDropsItsViews(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "create schema vs4")
	ctx.GetSetting = func(name string) (string, bool) {
		if name == "search_path" {
			return "vs4, public", true
		}
		return "", false
	}
	runSQL(t, ctx, "create table vs4.t4 (a int)")
	runSQL(t, ctx, "create view v4 as select * from vs4.t4")
	ctx.TakeNotices()
	runSQL(t, ctx, "drop schema vs4 cascade")
	for _, n := range ctx.TakeNotices() {
		if strings.Contains(n, "does not exist") {
			t.Errorf("drop schema vs4 cascade reported %q", n)
		}
	}
	ps := optimizer.DefaultPlannerSettings()
	if got := strings.Join(renderRows(runSQLWith(t, ctx, "select relname from pg_class where relname in ('v4', 't4')", ps)), ","); got != "" {
		t.Errorf("drop schema vs4 cascade left %q", got)
	}
}
