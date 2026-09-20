package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// parseSelectForPullup parses one SELECT and returns it. Tests here read the
// PARSE tree, which is the representation sublinkBodyIsSimple is defined
// over — constructing parser.SelectStmt literals instead would let a test
// assert a shape the grammar cannot actually produce.
func parseSelectForPullup(t *testing.T, sql string) *parser.SelectStmt {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	if len(stmts) != 1 {
		t.Fatalf("parse %q: got %d statements, want 1", sql, len(stmts))
	}
	sel, ok := stmts[0].(*parser.SelectStmt)
	if !ok {
		t.Fatalf("parse %q: got %T, want *parser.SelectStmt", sql, stmts[0])
	}
	return sel
}

// TestSublinkBodyIsSimpleAcceptsTheCorpusWitnesses pins the case the route
// task exists for: every one of the five TPC-DS census witnesses' EXISTS
// bodies is pull-up-admissible. If this ever goes false, the route's premise
// (m0142-0008a-3i-lateral-route-recon.md §5) has stopped holding and step 2
// should not be attempted until it is re-derived.
func TestSublinkBodyIsSimpleAcceptsTheCorpusWitnesses(t *testing.T) {
	cases := []struct {
		name string
		sql  string
	}{
		{
			// Q16 / Q94, verbatim shape: one base relation, one equi and one
			// non-equi correlation qual.
			"q16-q94-catalog-sales",
			`select * from catalog_sales cs2
			 where cs1.cs_order_number = cs2.cs_order_number
			   and cs1.cs_warehouse_sk <> cs2.cs_warehouse_sk`,
		},
		{
			// Q35 / Q10 / Q69, verbatim shape: two base relations, a
			// correlation qual and two local quals.
			"q35-q10-q69-store-sales-date-dim",
			`select * from store_sales, date_dim
			 where c.c_customer_sk = ss_customer_sk
			   and ss_sold_date_sk = d_date_sk
			   and d_year = 1999
			   and d_qoy < 4`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !sublinkBodyIsSimple(parseSelectForPullup(t, tc.sql)) {
				t.Fatal("sublinkBodyIsSimple = false, want true (a plain SELECT over base relations)")
			}
		})
	}
}

// TestSublinkBodyIsSimpleRefusals walks every refusal arm, one case each, so
// a later edit that drops an arm fails here rather than silently widening
// what step 2 would splice. Each case names the upstream field it stands for
// (is_simple_subquery, prepjointree.c:1807).
func TestSublinkBodyIsSimpleRefusals(t *testing.T) {
	cases := []struct {
		name     string
		upstream string
		sql      string
	}{
		{"setop", "subquery->setOperations",
			`select a from t1 union all select a from t2`},
		{"cte", "subquery->cteList",
			`with w as (select a from t2) select a from w`},
		{"group-by", "subquery->groupClause",
			`select a from t1 group by a`},
		{"having", "subquery->havingQual",
			`select a from t1 group by a having count(*) > 1`},
		{"order-by", "subquery->sortClause",
			`select a from t1 order by a`},
		{"distinct", "subquery->distinctClause",
			`select distinct a from t1`},
		{"distinct-on", "subquery->distinctClause",
			`select distinct on (a) a from t1`},
		{"limit", "subquery->limitCount",
			`select a from t1 limit 1`},
		{"offset", "subquery->limitOffset",
			`select a from t1 offset 1`},
		{"for-update", "subquery->hasForUpdate",
			`select a from t1 for update`},
		{"aggregate-in-target", "subquery->hasAggs",
			`select count(*) from t1`},
		{"aggregate-in-qual", "subquery->hasAggs",
			`select a from t1 where sum(b) > 0`},
		{"window-function", "subquery->hasWindowFuncs",
			`select row_number() over (order by a) from t1`},
		{"values", "(goopg) no FROM items to splice",
			`values (1), (2)`},
		{"no-from", "(goopg) no FROM items to splice",
			`select 1`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if sublinkBodyIsSimple(parseSelectForPullup(t, tc.sql)) {
				t.Fatalf("sublinkBodyIsSimple = true, want false (upstream refuses on %s)", tc.upstream)
			}
		})
	}
}

// TestSublinkBodyIsSimpleNilIsRefused pins the nil answer rather than leaving
// it to a caller's guard: a sublink whose parse tree was not retained must
// read as "not pull-up-able", never as "simple".
func TestSublinkBodyIsSimpleNilIsRefused(t *testing.T) {
	if sublinkBodyIsSimple(nil) {
		t.Fatal("sublinkBodyIsSimple(nil) = true, want false")
	}
}

// TestSublinkParseTreeIsRetained pins step 1's whole deliverable: after
// planning, the EXISTS/IN node carries the SAME parser body its `Plan` was
// built from. Without it, step 2 has nothing to flatten — the recon
// established that the parse tree was previously dropped at
// `planExistsExpr`/`planInExpr` and unreachable from every later stage.
//
// The sublinks sit in the target list, not WHERE, deliberately: a WHERE
// EXISTS over an eligible shape is rewritten to a pinned Semi join by
// `unnestSubqueriesInPlan` and the ExistsExpr no longer exists to inspect.
// What is being pinned here is the resolver's assignment, which is the same
// call either way.
func TestSublinkParseTreeIsRetained(t *testing.T) {
	cat := sublinkPullupCatalog(t)

	t.Run("exists", func(t *testing.T) {
		sql := `select (exists (select 1 from orders where o_custkey = c_custkey)) from customer`
		stmts, err := parser.Parse(sql)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		want := existsBodyFromParse(t, stmts[0])
		node, err := Plan(stmts[0], cat)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		got := findExistsExpr(node)
		if got == nil {
			t.Fatal("no *ExistsExpr in the planned tree")
		}
		if got.Subquery == nil {
			t.Fatal("ExistsExpr.Subquery is nil — the parse tree was not retained")
		}
		if got.Subquery != want {
			t.Fatalf("ExistsExpr.Subquery = %p, want the parser body %p", got.Subquery, want)
		}
		if got.Plan == nil {
			t.Fatal("ExistsExpr.Plan is nil — retaining the parse tree must not displace the plan")
		}
	})

	t.Run("in", func(t *testing.T) {
		sql := `select (c_custkey in (select o_custkey from orders)) from customer`
		stmts, err := parser.Parse(sql)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		want := inBodyFromParse(t, stmts[0])
		node, err := Plan(stmts[0], cat)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		got := findInExpr(node)
		if got == nil {
			t.Fatal("no *InExpr in the planned tree")
		}
		if got.Subquery == nil {
			t.Fatal("InExpr.Subquery is nil — the parse tree was not retained")
		}
		if got.Subquery != want {
			t.Fatalf("InExpr.Subquery = %p, want the parser body %p", got.Subquery, want)
		}
		if got.Plan == nil {
			t.Fatal("InExpr.Plan is nil — retaining the parse tree must not displace the plan")
		}
	})
}

func sublinkPullupCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	cat := catalog.NewInMemory()
	mk := func(name string, cols ...string) {
		t.Helper()
		cs := make([]catalog.Column, len(cols))
		for i, c := range cols {
			cs[i] = catalog.Column{Name: c, Type: catalog.Type{Name: "int4"}}
		}
		if _, err := cat.CreateTable(parser.ObjectName{Name: name}, cs); err != nil {
			t.Fatalf("CreateTable(%s): %v", name, err)
		}
	}
	mk("customer", "c_custkey", "c_name")
	mk("orders", "o_orderkey", "o_custkey")
	return cat
}

// existsBodyFromParse and inBodyFromParse dig the sublink body out of the
// PARSED statement, so the retention test compares against the POINTER the
// parser produced. Comparing against a re-parse would compare two
// equal-looking but distinct trees and prove nothing about retention.
func existsBodyFromParse(t *testing.T, stmt parser.Stmt) *parser.SelectStmt {
	t.Helper()
	sel, ok := stmt.(*parser.SelectStmt)
	if !ok {
		t.Fatalf("statement is %T, want *parser.SelectStmt", stmt)
	}
	for _, tgt := range sel.Targets {
		if ex, ok := tgt.Expr.(*parser.ExistsExpr); ok {
			return ex.Subquery
		}
	}
	t.Fatal("no *parser.ExistsExpr in the target list")
	return nil
}

func inBodyFromParse(t *testing.T, stmt parser.Stmt) *parser.SelectStmt {
	t.Helper()
	sel, ok := stmt.(*parser.SelectStmt)
	if !ok {
		t.Fatalf("statement is %T, want *parser.SelectStmt", stmt)
	}
	for _, tgt := range sel.Targets {
		if in, ok := tgt.Expr.(*parser.InExpr); ok {
			return in.Subquery
		}
	}
	t.Fatal("no *parser.InExpr in the target list")
	return nil
}
