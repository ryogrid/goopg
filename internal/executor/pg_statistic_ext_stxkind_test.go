package executor

import (
	"strings"
	"testing"
)

// AI-20261102-000225-003 (M-NIGHTLY): psql's `\d <table>` reads extended
// statistics with `'d' = any(stxkind)`. The planner's pg_statistic_ext stub
// typed stxkind `text`, so once M0122-0015f (91bc8262a) enforced
// make_scalar_array_op's array-operand rule every `\d` failed with 42809 —
// regress index_including / index_including_gist went red. PG's stxkind is
// "char"[] holding the enabled kinds in d, f, m, e order (statscmds.c
// CreateStatistics); values below verified against PG 18.3.
func TestPgStatisticExtStxkindIsCharArray(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"create table sxt (a int primary key, b text)",
		"create statistics sx1 (ndistinct, dependencies) on a, b from sxt",
		"create statistics sx2 on a, b from sxt",
		"create statistics sx3 on (a + 1), b from sxt",
		"create statistics sx4 (mcv) on (lower(b)), a from sxt",
	} {
		runSQL(t, ctx, q)
	}
	got := strings.Join(renderRows(runSQL(t, ctx,
		"select stxname, stxkind::text, 'd' = any(stxkind), 'f' = any(stxkind), 'm' = any(stxkind), 'e' = any(stxkind) "+
			"from pg_catalog.pg_statistic_ext order by stxname")), ";")
	want := "sx1|{d,f}|t|t|f|f;sx2|{d,f,m}|t|t|t|f;sx3|{d,f,m,e}|t|t|t|t;sx4|{m,e}|f|f|t|t"
	if got != want {
		t.Errorf("stxkind:\n got %s\nwant %s", got, want)
	}
	// The `\d` query shape itself must plan (42809 regression).
	runSQL(t, ctx, "SELECT stxname, 'd' = any(stxkind) AS ndist_enabled, 'f' = any(stxkind) AS deps_enabled, "+
		"'m' = any(stxkind) AS mcv_enabled FROM pg_catalog.pg_statistic_ext WHERE stxrelid = 'sxt'::regclass ORDER BY stxname")
}
