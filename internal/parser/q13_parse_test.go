package parser

import "testing"

func TestQ13Parse(t *testing.T) {
	q := `select c_count, count(*) as custdist from ( select c_custkey, count(o_orderkey) from customer left outer join orders on c_custkey = o_custkey and o_comment not like '%special%requests%' group by c_custkey) as c_orders (c_custkey, c_count) group by c_count order by custdist desc, c_count desc`
	stmts, err := Parse(q)
	if err != nil {
		t.Fatalf("Q13 parse error: %v", err)
	}
	if len(stmts) == 0 {
		t.Fatal("Q13 produced no statements")
	}
	t.Logf("Q13 parsed OK: %T", stmts[0])
}
