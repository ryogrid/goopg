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
