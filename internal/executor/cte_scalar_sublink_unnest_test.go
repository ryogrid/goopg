package executor

// cte_scalar_sublink_unnest_test.go — M0125-0041, rebased by M0147-0013.
//
// TPC-DS Q30 and Q81 put a correlated scalar-aggregate sublink over a WITH
// item: `ctr1.ctr_total_return > (select avg(ctr_total_return)*1.2 from
// customer_total_return ctr2 where ctr1.ctr_state = ctr2.ctr_state)`.
// M0125-0041 taught goopg's scalar decorrelation to rewrite that shape; the
// decorrelation (GOOPG_SCALAR_UNNEST) was default off from M0145-0008y and
// M0147-0013 deleted it. PG never decorrelates an EXPR_SUBLINK
// (pull_up_sublinks converts only ANY/EXISTS), so the shape stays a SubPlan,
// and the test now pins that plan and its answer.

import (
	"strings"
	"testing"
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

func TestCTEScalarSublinkStaysSubPlan(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	setupQ30Shape(t, ctx)

	plan := runExplainRows(t, ctx, "EXPLAIN "+q30ShapeSQL)
	sawSubPlan := false
	for _, line := range plan {
		if strings.Contains(line, "SubPlan") {
			sawSubPlan = true
		}
	}
	if !sawSubPlan {
		t.Fatalf("correlated scalar sublink over a CTE was decorrelated; PG keeps it a SubPlan:\n%s",
			strings.Join(plan, "\n"))
	}

	// AR's average total is (100+900+400+50)/4 = 362.5, so only C20 (900)
	// clears 435; TN's single member cannot clear 1.2 x its own average, and
	// the NULL state's correlation group is empty, so avg() is NULL there.
	got := renderRows(runSQL(t, ctx, q30ShapeSQL))
	if want := []string{"C20|900"}; !equalStrings(got, want) {
		t.Errorf("got %v, want %v\nplan:\n%s", got, want, strings.Join(plan, "\n"))
	}
}
