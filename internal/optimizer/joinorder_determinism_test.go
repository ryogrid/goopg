package optimizer

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0125-0047: goopg's comma-FROM reorder was restart-NONDETERMINISTIC
// whenever two relations tied on row count.
//
// `pickNextByEdge` ranks candidates while iterating `edges[j]`, which
// is a `map[int]struct{}`, and its tie-break was a strict `<` on the
// row count. A strict comparison keeps whichever candidate the map
// happened to yield FIRST, so on a tie the winner was decided by Go's
// per-iteration map-order randomisation rather than by the query — a
// different permutation on every server start of the same binary.
//
// TPC-DS Q85 is the shape that exposes it: `customer_demographics` is
// scanned twice as `cd1`/`cd2`, so the two relations are the SAME table
// and necessarily carry identical statistics, and both are reached by
// an equality edge from `web_returns`. Three restarts of one binary
// produced cd2-first twice and cd1-first once
// (`analysis/m0125-0002-c4-plans-20260803/README.md` §"q85").
//
// This is a PG divergence — upstream's `add_path` tie-breaks are stable
// given identical inputs — and, worse, an INSTRUMENT hazard: every
// EXPLAIN-based A/B in this repo (plan-snapshot, the SF0.5 capture,
// `make plan-diff`) can report a phantom hunk on a Q85-shaped query,
// so a plan-shape commit cannot be accepted on a single sweep.
//
// The fix gives the tie-break a TOTAL order by comparing FROM indices
// last, which makes the result a pure function of the query text.

// tpcdsQ85Catalog holds Q85's eight base relations at SF=1 row counts.
// Only `customer_demographics` matters to the defect — it is the table
// the query names twice — but the others are what put the tie at the
// END of the greedy walk, where the two aliases are the only unplaced
// candidates left and the map holds both.
func tpcdsQ85Catalog(t *testing.T) catalog.Catalog {
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
	mk("web_sales", 719384, "ws_item_sk", "ws_order_number",
		"ws_web_page_sk", "ws_quantity")
	mk("web_returns", 71763, "wr_item_sk", "wr_order_number",
		"wr_refunded_cdemo_sk", "wr_returning_cdemo_sk",
		"wr_refunded_addr_sk", "wr_returned_date_sk", "wr_reason_sk",
		"wr_refunded_cash", "wr_fee")
	mk("web_page", 60, "wp_web_page_sk")
	mk("customer_demographics", 1920800, "cd_demo_sk",
		"cd_marital_status", "cd_education_status")
	mk("customer_address", 50000, "ca_address_sk", "ca_state")
	mk("date_dim", 73049, "d_date_sk", "d_year")
	mk("reason", 35, "r_reason_sk", "r_reason_desc")
	return c
}

// q85SQL is TPC-DS Q85's FROM list and its join predicates, with the
// selection/aggregation stripped: the reorder pass reads only the
// comma-FROM list and the WHERE conjunction, so the rest cannot change
// the permutation and would only make the fixture harder to read.
const q85SQL = `select 1
	from web_sales, web_returns, web_page,
	     customer_demographics cd1, customer_demographics cd2,
	     customer_address, date_dim, reason
	 where ws_item_sk = wr_item_sk
	   and ws_order_number = wr_order_number
	   and ws_web_page_sk = wp_web_page_sk
	   and wr_refunded_cdemo_sk = cd1.cd_demo_sk
	   and wr_returning_cdemo_sk = cd2.cd_demo_sk
	   and cd1.cd_marital_status = cd2.cd_marital_status
	   and cd1.cd_education_status = cd2.cd_education_status
	   and wr_refunded_addr_sk = ca_address_sk
	   and wr_returned_date_sk = d_date_sk
	   and r_reason_sk = wr_reason_sk`

// TestPlanQ85IsDeterministic runs the WHOLE planner on Q85 repeatedly and
// compares alias-bearing fingerprints: any map-order-dependent site in the
// planner fails here.
//
// It was written as the end-to-end guard behind two FROM-order tests that
// take2 P3-12 deleted with reorderCommaFromByCardinality. The property it
// guards — a plan does not depend on Go map iteration order — is independent
// of that reorder and outlives it, so this one stays.
func TestPlanQ85IsDeterministic(t *testing.T) {
	c := tpcdsQ85Catalog(t)
	var first string
	for i := 0; i < 100; i++ {
		node, err := Plan(parseOne(t, q85SQL), c)
		if err != nil {
			t.Fatalf("iteration %d: Plan: %v", i, err)
		}
		got := planFingerprint(node)
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("iteration %d: plan differs from the first iteration"+
				"\n--- first ---\n%s\n--- got ---\n%s", i, first, got)
		}
	}
}

// planFingerprint renders a whole plan tree as a stable string, by
// reflection rather than by a type switch, so it keeps working as node
// types are added and so it notices a reordering ANYWHERE in the tree —
// not only in the FROM permutation the tests above pin.
//
// Scans print their alias, which is the whole point: `cd1` and `cd2`
// name the same *catalog.Table, so a renderer keyed on the table name
// (planShapeString in predp_test.go) prints the two Q85 permutations
// identically and cannot see this defect at all.
func planFingerprint(n Node) string {
	var b strings.Builder
	var walk func(v reflect.Value, depth int)
	walk = func(v reflect.Value, depth int) {
		if depth > 40 || !v.IsValid() {
			return
		}
		switch v.Kind() {
		case reflect.Interface, reflect.Ptr:
			if v.IsNil() {
				return
			}
			// A *catalog.Table is a leaf: it is shared catalog state,
			// not plan structure, and recursing into it would walk the
			// whole schema on every scan.
			if tbl, ok := v.Interface().(*catalog.Table); ok {
				fmt.Fprintf(&b, "%stable=%s\n", strings.Repeat(" ", depth), tbl.Name)
				return
			}
			if _, ok := v.Interface().(Node); ok && v.Kind() == reflect.Ptr {
				fmt.Fprintf(&b, "%s%s\n", strings.Repeat(" ", depth), v.Elem().Type().Name())
			}
			walk(v.Elem(), depth+1)
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				f := v.Field(i)
				name := v.Type().Field(i).Name
				switch f.Kind() {
				case reflect.String:
					if f.String() != "" {
						fmt.Fprintf(&b, "%s%s=%q\n", strings.Repeat(" ", depth), name, f.String())
					}
				case reflect.Interface, reflect.Ptr, reflect.Slice, reflect.Struct:
					walk(f, depth+1)
				}
			}
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i), depth+1)
			}
		}
	}
	walk(reflect.ValueOf(n), 0)
	return b.String()
}
