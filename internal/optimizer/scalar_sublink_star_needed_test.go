package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestScalarSublinkStarKeepsNeededSet pins M0146-0122: a scalar sublink's
// unqualified `*` expands over the sublink's own FROM list (varlevelsup 0), so
// it reads no outer column and must not void the statement's needed set.
// TPC-DS Q23's best_ss_customer CTE —
// `HAVING sum(...) > 0.95 * (SELECT * FROM max_store_sales)` — lost every
// index-only path to it, and customer was seq-scanned where PG reads
// customer_pkey index-only. A qualified star may name an outer relation (a
// whole-row reference), and an IN sublink's star is its pulled-up join key;
// both still decline.
func TestScalarSublinkStarKeepsNeededSet(t *testing.T) {
	sel := func(sql string) *parser.SelectStmt {
		t.Helper()
		stmts, err := parser.Parse(sql)
		if err != nil {
			t.Fatal(err)
		}
		return stmts[0].(*parser.SelectStmt)
	}
	needed, known := neededColumnNames(sel(`select c_customer_sk, sum(ss_quantity) from store_sales, customer
		where ss_customer_sk = c_customer_sk group by c_customer_sk
		having sum(ss_quantity) > (select * from max_store_sales)`))
	if !known {
		t.Fatal("a scalar sublink's unqualified star voided the needed set")
	}
	for _, name := range []string{"c_customer_sk", "ss_quantity", "ss_customer_sk"} {
		if !needed[name] {
			t.Errorf("%q is read by the statement but missing from the needed set", name)
		}
	}
	if needed["c_email_address"] {
		t.Error("an unread column is in the needed set")
	}
	for _, sql := range []string{
		`select c_customer_sk from customer c where c_birth_year > (select c.* from max_store_sales)`,
		`select c_customer_sk from customer where c_customer_sk in (select * from best_ss_customer)`,
	} {
		if nc, ok := neededColumnNames(sel(sql)); ok || nc != nil {
			t.Errorf("%s: needed = (%v, %v), want the set declined", sql, nc, ok)
		}
	}
}
