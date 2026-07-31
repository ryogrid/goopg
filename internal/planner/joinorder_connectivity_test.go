package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0125-0034, C1's WITH-reference arm. A comma-FROM list that names a
// WITH reference has no catalog row count for that item, which used to
// make reorderCommaFromByCardinality decline outright — so the source
// order survived and `planFromClause` built a left-deep CROSS chain
// with the equi-predicates demoted to a Filter above it. These tests
// pin the connectivity objective that replaces the cardinality one on
// exactly those lists, and pin the three ways it must stay out of the
// way. Shapes are taken from TPC-DS Q30/Q81 (one cross) and Q64 (four).
//
// See docs/design/0125-0034a-comma-from-connectivity-order.md.

// tpcdsConnCatalog holds the base relations the shapes below join. The
// WITH references are deliberately absent: "the catalog does not know
// this name" is precisely how the pass recognises one.
func tpcdsConnCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	mk := func(name string, rows int64, cols ...string) {
		defs := make([]catalog.Column, len(cols))
		for i, col := range cols {
			defs[i] = catalog.Column{Name: col, Type: catalog.Type{Name: "numeric"}}
		}
		tbl, err := c.CreateTable(parser.ObjectName{Name: name}, defs)
		if err != nil {
			t.Fatalf("CreateTable %s: %v", name, err)
		}
		c.SetTableStats(tbl, &catalog.TableStats{RowCount: rows})
	}
	mk("customer_address", 50000, "ca_address_sk", "ca_state")
	mk("customer", 100000, "c_customer_sk", "c_current_addr_sk",
		"c_first_sales_date_sk", "c_first_shipto_date_sk")
	mk("date_dim", 73049, "d_date_sk", "d_year")
	mk("store_sales", 2880404, "ss_sold_date_sk", "ss_customer_sk", "ss_item_sk")
	mk("item", 18000, "i_item_sk", "i_color")
	return c
}

func fromNames(t *testing.T, rvs []parser.RangeVar) []string {
	t.Helper()
	out := make([]string, len(rvs))
	for i, rv := range rvs {
		out[i] = rv.Name
		if rv.Alias != "" {
			out[i] = rv.Alias
		}
	}
	return out
}

func assertOrder(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("FROM length %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("FROM order %v, want %v", got, want)
		}
	}
}

// TestConnectivityOrderQ30Shape is TPC-DS Q30/Q81's outer query. The
// source order `ctr1, customer_address, customer` has no predicate
// joining the first two, so the first Join is a Cartesian product of
// the WITH body against customer_address; both of its equi-predicates
// reach `customer`, which sits last. Moving customer up one position
// makes both joins INNER. Note the edge to `ctr1` is found through its
// alias while `c_customer_sk` resolves as a bare column — the mixed
// form the query actually uses.
func TestConnectivityOrderQ30Shape(t *testing.T) {
	c := tpcdsConnCatalog(t)
	sql := `with customer_total_return as (select 1 as ctr_customer_sk, 2 as ctr_state)
	        select 1 from customer_total_return ctr1, customer_address, customer
	         where ctr1.ctr_customer_sk = c_customer_sk
	           and ca_address_sk = c_current_addr_sk
	           and ca_state = 'AR'`
	stmt := parseOne(t, sql).(*parser.SelectStmt)
	_, newFR, rewrote := reorderCommaFromByCardinality(stmt, c)
	if !rewrote {
		t.Fatalf("expected the WITH-reference list to be reordered")
	}
	assertOrder(t, fromNames(t, newFR), []string{"ctr1", "customer", "customer_address"})
}

// TestConnectivityOrderQ64DateDimShape is Q64's inner CTE reduced to
// the defect M0125-0035a §6 isolated: `date_dim d2` and `d3` are placed
// before `customer`, the relation their equi-predicates need, so both
// become crosses and the predicates are demoted two levels up. The
// connectivity walk defers d2/d3 until customer is placed and takes
// them immediately afterwards — the source order is otherwise kept.
func TestConnectivityOrderQ64DateDimShape(t *testing.T) {
	c := tpcdsConnCatalog(t)
	sql := `with cs_ui as (select 1 as cs_item_sk)
	        select 1 from store_sales, cs_ui, date_dim d1, date_dim d2, date_dim d3,
	                     customer, item
	         where ss_sold_date_sk = d1.d_date_sk
	           and ss_customer_sk = c_customer_sk
	           and ss_item_sk = i_item_sk
	           and ss_item_sk = cs_ui.cs_item_sk
	           and c_first_sales_date_sk = d2.d_date_sk
	           and c_first_shipto_date_sk = d3.d_date_sk`
	stmt := parseOne(t, sql).(*parser.SelectStmt)
	_, newFR, rewrote := reorderCommaFromByCardinality(stmt, c)
	if !rewrote {
		t.Fatalf("expected the d2/d3-before-customer list to be reordered")
	}
	got := fromNames(t, newFR)
	assertOrder(t, got, []string{
		"store_sales", "cs_ui", "d1", "customer", "d2", "d3", "item",
	})
	// The property that matters is stronger than the exact
	// permutation: every item after the first must have an edge to
	// something already placed.
	assertNoAvoidableCross(t, stmt, c, newFR)
}

// assertNoAvoidableCross re-derives the join graph over the permuted
// list and checks the connectivity invariant directly, so the test
// fails for the right reason if the tie-break rule is ever retuned.
func assertNoAvoidableCross(t *testing.T, stmt *parser.SelectStmt, c catalog.Catalog, order []parser.RangeVar) {
	t.Helper()
	indexByKey := map[string]int{}
	tables := make([]*catalog.Table, len(order))
	for i, rv := range order {
		for _, k := range relKeys(rv) {
			if _, dup := indexByKey[k]; !dup {
				indexByKey[k] = i
			}
		}
		if tbl, ok := c.LookupTable(parser.ObjectName{Schema: rv.Schema, Name: rv.Name}); ok {
			tables[i] = tbl
		}
	}
	edges := collectEqualityEdges(stmt.Where, indexByKey, buildBareColumnIndex(tables), len(order))
	for i := 1; i < len(order); i++ {
		connected := false
		for k := range edges[i] {
			if k < i {
				connected = true
				break
			}
		}
		if !connected {
			t.Fatalf("position %d (%s) has no edge to the prefix: avoidable cross",
				i, order[i].Name)
		}
	}
}

// TestConnectivityOrderInertWhenAlreadyConnected pins the inertness
// guarantee that bounds this pass's blast radius: a source order in
// which every item already has an edge to its prefix is a fixed point
// of the walk, so the caller's identity check declines the rewrite and
// the query plans exactly as it did before. Without this, every
// WITH-bearing comma FROM list in the corpus would be re-permuted.
func TestConnectivityOrderInertWhenAlreadyConnected(t *testing.T) {
	c := tpcdsConnCatalog(t)
	sql := `with ctr as (select 1 as ctr_customer_sk)
	        select 1 from ctr, customer, customer_address
	         where ctr.ctr_customer_sk = c_customer_sk
	           and ca_address_sk = c_current_addr_sk`
	stmt := parseOne(t, sql).(*parser.SelectStmt)
	if _, _, rewrote := reorderCommaFromByCardinality(stmt, c); rewrote {
		t.Fatalf("a cross-free source order must not be rewritten")
	}
}

// TestConnectivityOrderDeclinesDerivedTable pins the LATERAL bound.
// The parser accepts LATERAL and discards it, so a derived table in
// the FROM list may or may not reference an earlier item and nothing
// in the AST says which. Permuting one could change what the query
// means, so the pass declines the whole list — which is why TPC-DS
// Q65, whose two inputs are derived aggregates rather than WITH
// references, keeps its cross. Deferral ledger, M0125-0034.
func TestConnectivityOrderDeclinesDerivedTable(t *testing.T) {
	c := tpcdsConnCatalog(t)
	sql := `select 1 from customer_address, item,
	             (select ss_store_sk, ss_item_sk from store_sales) sc
	         where i_item_sk = sc.ss_item_sk
	           and ca_address_sk = sc.ss_store_sk`
	stmt := parseOne(t, sql).(*parser.SelectStmt)
	if _, _, rewrote := reorderCommaFromByCardinality(stmt, c); rewrote {
		t.Fatalf("a derived table may be LATERAL; the list must not be reordered")
	}
}

// TestConnectivityOrderRunsWithoutStats pins the mode split. An
// un-ANALYZEd base table still cancels *cardinality* mode, because
// there the missing row count is the whole signal. In connectivity
// mode row counts are never read, so the same table is no reason to
// decline — the WITH reference already made cardinality ordering
// impossible by construction.
func TestConnectivityOrderRunsWithoutStats(t *testing.T) {
	c := catalog.NewInMemory()
	mk := func(name string, cols ...string) {
		defs := make([]catalog.Column, len(cols))
		for i, col := range cols {
			defs[i] = catalog.Column{Name: col, Type: catalog.Type{Name: "numeric"}}
		}
		if _, err := c.CreateTable(parser.ObjectName{Name: name}, defs); err != nil {
			t.Fatalf("CreateTable %s: %v", name, err)
		}
	}
	mk("customer_address", "ca_address_sk")
	mk("customer", "c_customer_sk", "c_current_addr_sk")
	sql := `with ctr as (select 1 as ctr_customer_sk)
	        select 1 from ctr, customer_address, customer
	         where ctr.ctr_customer_sk = c_customer_sk
	           and ca_address_sk = c_current_addr_sk`
	stmt := parseOne(t, sql).(*parser.SelectStmt)
	_, newFR, rewrote := reorderCommaFromByCardinality(stmt, c)
	if !rewrote {
		t.Fatalf("connectivity mode must not depend on ANALYZE")
	}
	assertOrder(t, fromNames(t, newFR), []string{"ctr", "customer", "customer_address"})
}

// TestConnectivityOrderDisconnectedGraph: `item` has no equality edge
// at all, so one cross is unavoidable and no permutation removes it.
// The walk must still connect everything that can be connected, emit
// every item exactly once, and sink the unreachable relation to the
// end rather than looping or dropping one. Note this also shows the
// pass is not fooled into calling an unavoidable cross a defect: with
// `item` in source position 1 the source order is not a fixed point,
// but the rewrite it produces is the one that pays.
func TestConnectivityOrderDisconnectedGraph(t *testing.T) {
	c := tpcdsConnCatalog(t)
	sql := `with ctr as (select 1 as ctr_customer_sk)
	        select 1 from ctr, item, customer, customer_address
	         where ctr.ctr_customer_sk = c_customer_sk
	           and ca_address_sk = c_current_addr_sk`
	stmt := parseOne(t, sql).(*parser.SelectStmt)
	_, newFR, rewrote := reorderCommaFromByCardinality(stmt, c)
	if !rewrote {
		t.Fatalf("expected the disconnected list to still hoist the connected run")
	}
	got := fromNames(t, newFR)
	assertOrder(t, got, []string{"ctr", "customer", "customer_address", "item"})
	seen := map[string]bool{}
	for _, n := range got {
		if seen[n] {
			t.Fatalf("relation %q emitted twice: %v", n, got)
		}
		seen[n] = true
	}
	if len(seen) != 4 {
		t.Fatalf("expected 4 distinct relations, got %v", seen)
	}
}

// TestConnectivityOrderUnavoidableCrossIsAFixedPoint is the companion:
// when the disconnected relation already sits last, the source order is
// as good as any permutation and the pass must decline. An unavoidable
// cross is not a defect, and re-permuting for it would churn plans for
// nothing.
func TestConnectivityOrderUnavoidableCrossIsAFixedPoint(t *testing.T) {
	c := tpcdsConnCatalog(t)
	sql := `with ctr as (select 1 as ctr_customer_sk)
	        select 1 from ctr, customer, customer_address, item
	         where ctr.ctr_customer_sk = c_customer_sk
	           and ca_address_sk = c_current_addr_sk`
	stmt := parseOne(t, sql).(*parser.SelectStmt)
	if _, _, rewrote := reorderCommaFromByCardinality(stmt, c); rewrote {
		t.Fatalf("an unavoidable cross already in place must not be rewritten")
	}
}
