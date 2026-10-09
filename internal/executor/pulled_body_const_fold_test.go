package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestPulledSublinkBodyFoldsConstants pins M0146-0042 against PG 18.3: PG
// runs eval_const_expressions over a sublink body before pull_up_sublinks,
// so a `m BETWEEN 3 AND 3+3` bound is `m <= 6` by the time order_qual_clauses
// sorts the scan's quals — one operator, sorted before the equivalence-class
// equality `y = 2001` that generate_base_implied_equalities appends last.
// goopg folded the statement's own WHERE but not a pulled EXISTS / IN body's,
// so the unfolded two-operator bound sorted after the equality (TPC-DS Q10,
// Q69: `(d_moy >= 3) AND (d_year = 2001) AND (d_moy <= (3 + 3))`).
func TestPulledSublinkBodyFoldsConstants(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"create table fc (id int)", "create table fs (cid int, did int)", "create table fd (did int, y int, m int)",
		"insert into fc select g from generate_series(1,500) g",
		"insert into fs select g % 500, g % 3000 from generate_series(1,20000) g",
		"insert into fd select g, 1998 + g / 365, g % 12 + 1 from generate_series(1,3000) g",
		"analyze fc", "analyze fs", "analyze fd",
	} {
		runSQL(t, ctx, q)
	}
	const want = "Filter: ((m >= 3) AND (m <= 6) AND (y = 2001))"
	body := "select * from fs, fd where fc.id = fs.cid and fs.did = fd.did and fd.y = 2001 and fd.m between 3 and 3+3"
	for _, q := range []string{
		"select count(*) from fc where exists (" + body + ")",
		"select count(*) from fc where exists (" + body + ") and (exists (select * from fs, fd where fc.id = fs.did and fs.did = fd.did and fd.y = 2001 and fd.m between 3 and 3+3) or exists (" + body + "))",
		"select count(*) from fc where id in (select fs.cid from fs, fd where fs.did = fd.did and fd.y = 2001 and fd.m between 3 and 3+3)",
	} {
		lines := explainLines(t, ctx, optimizer.DefaultPlannerSettings(), "EXPLAIN (COSTS OFF) "+q)
		n := 0
		for _, l := range lines {
			if strings.Contains(l, "(m >= 3)") {
				n++
				if strings.TrimSpace(l) != want {
					t.Errorf("%s:\n  got  %s\n  want PG's %s", q, strings.TrimSpace(l), want)
				}
			}
		}
		if n == 0 {
			t.Errorf("%s: no date filter printed:\n%s", q, strings.Join(lines, "\n"))
		}
	}
}
