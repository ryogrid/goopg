package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestWindowFunctionKeepsNeededSet pins M0146-0225: a window function reads
// its arguments plus its window's PARTITION BY and ORDER BY expressions, the
// columns PG's build_base_rel_tlists adds for it, and nothing else. The
// collector used to decline every statement with an OVER clause or a WINDOW
// clause, so no relation of such a statement could get an index-only path.
// TPC-DS SF1 Q53/Q63 then probed `store` with a full-width Index Scan under
// Memoize where PG reads store_pkey index-only.
//
// Both the needed set and the above-tree output set must account for every
// column the window reads. Dropping one would let an index-only scan, or a
// narrowed leaf, omit a column the WindowAgg sorts or partitions on.
func TestWindowFunctionKeepsNeededSet(t *testing.T) {
	sel := func(sql string) *parser.SelectStmt {
		t.Helper()
		stmts, err := parser.Parse(sql)
		if err != nil {
			t.Fatal(err)
		}
		return stmts[0].(*parser.SelectStmt)
	}
	cases := []struct {
		name string
		sql  string
		want []string
	}{
		{"inline window", `select i_manufact_id, sum(ss_sales_price),
			avg(sum(ss_sales_price)) over (partition by i_brand order by d_qoy)
			from item, store_sales, date_dim, store
			where ss_item_sk = i_item_sk and ss_store_sk = s_store_sk and ss_sold_date_sk = d_date_sk
			group by i_manufact_id, i_brand, d_qoy`,
			[]string{"i_manufact_id", "ss_sales_price", "i_brand", "d_qoy", "ss_store_sk", "s_store_sk"}},
		{"named window", `select rank() over w from item window w as (partition by i_class order by i_current_price)`,
			[]string{"i_class", "i_current_price"}},
		{"frame offset", `select sum(i_current_price) over (order by i_item_sk rows between 2 preceding and current row) from item`,
			[]string{"i_current_price", "i_item_sk"}},
	}
	for _, c := range cases {
		stmt := sel(c.sql)
		for _, arm := range []struct {
			name string
			fn   func(*parser.SelectStmt) (map[string]bool, bool)
		}{{"needed", neededColumnNames}, {"output", outputColumnNames}} {
			got, known := arm.fn(stmt)
			if !known {
				t.Errorf("%s/%s: a window function voided the set", c.name, arm.name)
				continue
			}
			for _, col := range c.want {
				// The output set holds only above-tree reads, so a join key
				// such as ss_store_sk is not expected there.
				if arm.name == "output" && (col == "ss_store_sk" || col == "s_store_sk") {
					continue
				}
				if !got[col] {
					t.Errorf("%s/%s: %q is read by the window but missing", c.name, arm.name, col)
				}
			}
			if got["s_store_name"] {
				t.Errorf("%s/%s: an unread column is in the set", c.name, arm.name)
			}
		}
	}
}
