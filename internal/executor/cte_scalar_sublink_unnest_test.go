package executor

// cte_scalar_sublink_unnest_test.go — M0125-0041.
//
// TPC-DS Q30 and Q81 put a correlated scalar-aggregate sublink over a WITH
// item: `ctr1.ctr_total_return > (select avg(ctr_total_return)*1.2 from
// customer_total_return ctr2 where ctr1.ctr_state = ctr2.ctr_state)`. Every
// pull-up gate accepts that shape, but the rewrite died inside
// clonePlanReplacingOuter, which had no arm for *planner.CTEScan — so the
// sublink silently stayed a per-outer-row SubPlan and the query timed out.
//
// The fix is only worth anything if the decorrelated plan answers exactly what
// the SubPlan path answered, so the test is an equivalence test: the same query
// over the same data, planned with the pull-up on and off, must return the same
// rows. A plan-shape assertion alone would have passed for a rewrite that
// dropped or duplicated rows.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// q30ShapeSQL is Q30 reduced to its planning-relevant skeleton: a grouped WITH
// item joined to two more relations, filtered by a correlated scalar aggregate
// over that same WITH item.
const q30ShapeSQL = `WITH ctr AS (
	SELECT r.cust AS ctr_cust, a.st AS ctr_state, sum(r.amt) AS ctr_total
	FROM q30_ret r, q30_addr a
	WHERE r.addr = a.ask
	GROUP BY r.cust, a.st)
SELECT c.cid, ctr1.ctr_total
FROM ctr ctr1, q30_addr, q30_cust c
WHERE ctr1.ctr_total > (SELECT avg(ctr_total) * 1.2 FROM ctr ctr2
                        WHERE ctr1.ctr_state = ctr2.ctr_state)
  AND q30_addr.ask = c.caddr
  AND q30_addr.st = 'AR'
  AND ctr1.ctr_cust = c.csk
ORDER BY c.cid, ctr1.ctr_total`

func setupQ30Shape(t *testing.T, ctx *Context) {
	t.Helper()
	runSQL(t, ctx, `CREATE TABLE q30_addr (ask int, st text)`)
	runSQL(t, ctx, `CREATE TABLE q30_cust (csk int, cid text, caddr int)`)
	runSQL(t, ctx, `CREATE TABLE q30_ret (cust int, addr int, amt int)`)

	// Three states plus one NULL state. NULL is the interesting one: on the
	// SubPlan path `ctr1.ctr_state = ctr2.ctr_state` is never true for a NULL
	// state, so avg() runs over an empty group, returns NULL, and the row is
	// filtered; the decorrelated form must drop it too (a NULL key never
	// matches a hash probe). The two paths agreeing on it is the point.
	runSQL(t, ctx, `INSERT INTO q30_addr VALUES (1,'AR'),(2,'AR'),(3,'TN'),(4,'AR'),(5,NULL),(6,'AR')`)
	runSQL(t, ctx, `INSERT INTO q30_cust VALUES (10,'C10',1),(20,'C20',2),(30,'C30',3),(40,'C40',4),(50,'C50',5),(60,'C60',6)`)
	// Per-state totals are deliberately spread so that some customers clear
	// their state's avg*1.2 and some do not, and one state ('TN') has a single
	// member (which can never exceed 1.2 × its own average).
	runSQL(t, ctx, `INSERT INTO q30_ret VALUES
		(10,1,100),(20,2,900),(40,4,400),(60,6,50),
		(30,3,700),
		(50,5,1000)`)
}

func rowsKey(rows []Row) string {
	var b strings.Builder
	for _, r := range rows {
		for i, d := range r {
			if i > 0 {
				b.WriteByte('|')
			}
			fmt.Fprintf(&b, "%v", d)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func TestCTEScalarSublinkDecorrelatesAndAgrees(t *testing.T) {
	// Plan shape first: with the pull-up enabled the Q30 shape must no longer
	// carry a SubPlan. This is what the timeout hinged on.
	ctxOn, _, cleanupOn := newDDLFixture(t)
	t.Cleanup(cleanupOn)
	setupQ30Shape(t, ctxOn)

	plan := runExplainRows(t, ctxOn, "EXPLAIN "+q30ShapeSQL)
	for _, line := range plan {
		if strings.Contains(line, "SubPlan") {
			t.Fatalf("correlated scalar sublink over a CTE stayed a SubPlan:\n%s",
				strings.Join(plan, "\n"))
		}
	}
	got := runSQL(t, ctxOn, q30ShapeSQL)

	// Same query, same data, pull-up disabled — the SubPlan path's answer is
	// the oracle here (it is the shape that shipped before this change).
	optimizer.SetSubqueryUnnestEnabled(false)
	defer optimizer.SetSubqueryUnnestEnabled(true)

	ctxOff, _, cleanupOff := newDDLFixture(t)
	t.Cleanup(cleanupOff)
	setupQ30Shape(t, ctxOff)

	offPlan := runExplainRows(t, ctxOff, "EXPLAIN "+q30ShapeSQL)
	sawSubPlan := false
	for _, line := range offPlan {
		if strings.Contains(line, "SubPlan") {
			sawSubPlan = true
		}
	}
	if !sawSubPlan {
		t.Fatalf("control arm did not take the SubPlan path, so it proves nothing:\n%s",
			strings.Join(offPlan, "\n"))
	}
	want := runSQL(t, ctxOff, q30ShapeSQL)

	if rowsKey(got) != rowsKey(want) {
		t.Errorf("decorrelated result differs from the SubPlan result:\n got:\n%s\nwant:\n%s\nplan:\n%s",
			rowsKey(got), rowsKey(want), strings.Join(plan, "\n"))
	}
	if len(want) == 0 {
		t.Fatalf("fixture produced no rows on either path — the comparison is vacuous")
	}
}
