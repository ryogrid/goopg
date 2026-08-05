package planner

// M0127-P5.9-g — the decorrelated GROUP BY key was recorded in the scope it
// was FOUND in, not the scope it is READ in.
//
// TPC-H Q2's WHERE carries a correlated scalar aggregate
// (`ps_supplycost = (select min(ps_supplycost) from partsupp, supplier,
// nation, region where p_partkey = ps_partkey and ...)`). `unnestSubquery`
// (unnest.go) rewrites it into a hash join whose inner side is a
// HashAggregate over a clone of the subquery body, GROUPED BY the correlation
// column — so the group key is evaluated against `agg.Child`'s OUTPUT.
//
// Where the key's coordinate came from is a different matter. With PK indexes
// on the TPC-H tables the inner planner folds `ps_partkey = p_partkey` into an
// `*IndexScan` probe, and `harvestIndexKeyParams` records `SubCol` as the
// column's position in THAT SCAN's schema — `ps_partkey/0`. The harvest walk
// descends through joins and projects without accumulating an offset, so the
// number is leaf-relative and only agrees with the aggregate's input when the
// column's relation happens to sit at the same offset in both.
//
// It did agree for years: left-deep and unprojected, partsupp is the first
// relation of Q2's subquery body. Under `GOOPG_PGSHAPED_DP` the search
// boundary publishes a rotated coordinate map (P5.9-c) and a reordering
// Project lands partsupp at offset 14, behind region/nation/supplier.
// `ps_partkey/0` then reads `r_regionkey`: every European row groups under the
// single key 3, `part.p_partkey = 3` matches nothing, and Q2 returned 0 rows
// against 455 — a WRONG ANSWER, not an error, which is why five acceptance
// runs' worth of row-count gates were needed to see it at all.
//
// Live confirmation on a 5-table Q2 fixture (bench-free, port 5533, ~1 s):
// with PK indexes present, flag ON returned 0 rows against the flag-OFF
// control's 18; after the fix both arms return byte-identical output, and PG
// 18.3 on the same fixture returns the same 18 tuples in the same order.
// Without the indexes the arms already agreed — the correlation stayed in a
// Filter, whose coordinate space IS the aggregate's input. Design:
// leftdeep-joins/09 §5.22.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// jsgQ2SQL is TPC-H Q2 verbatim (internal/testutil/tpch Queries()[2]).
const jsgQ2SQL = `select s_acctbal, s_name, n_name, p_partkey, p_mfgr, s_address, s_phone, s_comment ` +
	`from part, supplier, partsupp, nation, region ` +
	`where p_partkey = ps_partkey and s_suppkey = ps_suppkey and p_size = 15 ` +
	`and p_type like '%BRASS' and s_nationkey = n_nationkey and n_regionkey = r_regionkey ` +
	`and r_name = 'EUROPE' and ps_supplycost = ( ` +
	`select min(ps_supplycost) from partsupp, supplier, nation, region ` +
	`where p_partkey = ps_partkey and s_suppkey = ps_suppkey ` +
	`and s_nationkey = n_nationkey and n_regionkey = r_regionkey and r_name = 'EUROPE') ` +
	`order by s_acctbal desc, n_name, s_name, p_partkey`

// jsgCatalog builds Q2's five relations at their real TPC-H widths and row
// counts, WITH the primary-key indexes. Both halves matter: the widths decide
// the offsets the rotated map produces, and the indexes are what push the
// correlation out of a Filter and into an index probe — without them the
// defect is unreachable and the arms agree.
func jsgCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	cat := catalog.NewInMemory()
	textCols := map[string]bool{
		"p_name": true, "p_mfgr": true, "p_brand": true, "p_type": true,
		"p_container": true, "p_comment": true, "s_name": true, "s_address": true,
		"s_phone": true, "s_comment": true, "n_name": true, "n_comment": true,
		"r_name": true, "r_comment": true, "ps_comment": true,
	}
	mk := func(name string, rows int64, cols ...string) {
		t.Helper()
		cs := make([]catalog.Column, len(cols))
		for i, c := range cols {
			ty := "int4"
			if textCols[c] {
				ty = "text"
			}
			cs[i] = catalog.Column{Name: c, Type: catalog.Type{Name: ty}}
		}
		tbl, err := cat.CreateTable(parser.ObjectName{Name: name}, cs)
		if err != nil {
			t.Fatalf("CreateTable(%s): %v", name, err)
		}
		tbl.Stats = &catalog.TableStats{RowCount: rows, Pages: int(rows / 100), Analyzed: true}
	}
	mk("part", 200000, "p_partkey", "p_name", "p_mfgr", "p_brand", "p_type",
		"p_size", "p_container", "p_retailprice", "p_comment")
	mk("supplier", 10000, "s_suppkey", "s_name", "s_address", "s_nationkey",
		"s_phone", "s_acctbal", "s_comment")
	mk("partsupp", 800000, "ps_partkey", "ps_suppkey", "ps_availqty", "ps_supplycost", "ps_comment")
	mk("nation", 25, "n_nationkey", "n_name", "n_regionkey", "n_comment")
	mk("region", 5, "r_regionkey", "r_name", "r_comment")

	ix := func(name, tbl string, cols ...string) {
		t.Helper()
		tb, ok := cat.LookupTable(parser.ObjectName{Name: tbl})
		if !ok {
			t.Fatalf("lookup %s: not found", tbl)
		}
		if _, err := cat.CreateIndex(parser.ObjectName{Name: name}, tb, cols, true, "btree", false); err != nil {
			t.Fatalf("CreateIndex(%s): %v", name, err)
		}
	}
	ix("part_pk", "part", "p_partkey")
	ix("supplier_pk", "supplier", "s_suppkey")
	ix("partsupp_pk", "partsupp", "ps_partkey", "ps_suppkey")
	ix("nation_pk", "nation", "n_nationkey")
	ix("region_pk", "region", "r_regionkey")
	return cat
}

// jsgDecorrelatedAgg returns the *Aggregate `unnestSubquery` spliced in as the
// inner side of a join, or nil when the plan kept the correlated SubPlan
// instead (which is what flag OFF does on this shape, and is also correct).
func jsgDecorrelatedAgg(n Node) *Aggregate {
	var walk func(Node) *Aggregate
	walk = func(cur Node) *Aggregate {
		switch x := cur.(type) {
		case nil:
			return nil
		case *Join:
			if a, ok := x.Right.(*Aggregate); ok {
				return a
			}
			if a := walk(x.Left); a != nil {
				return a
			}
			return walk(x.Right)
		case *Project:
			return walk(x.Child)
		case *Sort:
			return walk(x.Child)
		case *Filter:
			return walk(x.Child)
		case *Aggregate:
			return walk(x.Child)
		}
		return nil
	}
	return walk(n)
}

// TestQ2DecorrelatedGroupKeyResolvesInAggregateInput states the invariant the
// defect broke, without pinning the offset the search happens to choose: the
// GROUP BY key must name the correlation column IN THE SCHEMA IT IS READ
// FROM. With the bug the key was 0 and named `r_regionkey`; the assertion
// below fails on exactly that, at whatever offset a future search produces.
func TestQ2DecorrelatedGroupKeyResolvesInAggregateInput(t *testing.T) {
	for _, on := range []bool{false, true} {
		name := "pgshaped=false"
		if on {
			name = "pgshaped=true"
		}
		t.Run(name, func(t *testing.T) {
			saved := pgShapedDP
			pgShapedDP = on
			defer func() { pgShapedDP = saved }()

			stmts, err := parser.Parse(jsgQ2SQL)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			plan, err := Plan(stmts[0], jsgCatalog(t))
			if err != nil {
				t.Fatalf("plan (pgshaped=%v): %v", on, err)
			}
			agg := jsgDecorrelatedAgg(plan)
			if agg == nil {
				// Flag OFF keeps the SubPlan on this shape. Nothing to
				// check, and nothing wrong: the SubPlan path re-evaluates
				// the aggregate per outer row and was never affected.
				if on {
					t.Fatalf("pgshaped=true no longer decorrelates Q2's scalar aggregate; " +
						"the coordinate invariant below is untested — re-derive the shape")
				}
				return
			}
			if len(agg.GroupExprs) != 1 {
				t.Fatalf("decorrelated aggregate has %d group exprs, want 1", len(agg.GroupExprs))
			}
			gk, ok := agg.GroupExprs[0].(*ColumnRef)
			if !ok {
				t.Fatalf("group expr is %T, want *ColumnRef", agg.GroupExprs[0])
			}
			in := agg.Child.Output()
			if gk.Index < 0 || gk.Index >= len(in) {
				t.Fatalf("group key %s/%d is out of range for a %d-wide aggregate input",
					gk.Name, gk.Index, len(in))
			}
			if got := in[gk.Index].Name; got != "ps_partkey" {
				t.Errorf("group key %s/%d reads %q from the aggregate's input, want ps_partkey "+
					"(the harvested index-probe coordinate is leaf-relative; see P5.9-g)",
					gk.Name, gk.Index, got)
			}
			// The aggregate's own argument was always resolved against this
			// same schema — the two must agree, which is the whole point.
			if agg.Aggs[0].Arg != nil {
				if ar, ok := agg.Aggs[0].Arg.(*ColumnRef); ok {
					if ar.Index < 0 || ar.Index >= len(in) || in[ar.Index].Name != "ps_supplycost" {
						t.Errorf("aggregate arg %s/%d does not read ps_supplycost from the "+
							"aggregate's input — key and argument are in different scopes", ar.Name, ar.Index)
					}
				}
			}
		})
	}
}

// TestResolveSubColInSchema states the helper's contract directly: identity
// when the recorded index is already right (so no working path's ColumnRef
// changes), by-name resolution when a rotation moved the column, and a nil
// BAIL — never a guess — when the name is absent or ambiguous.
func TestResolveSubColInSchema(t *testing.T) {
	sc := func(name string, st int16) SchemaColumn {
		return SchemaColumn{Name: name, Type: catalog.Type{Name: "int4"}, SourceTableIdx: st}
	}
	rotated := Schema{sc("r_regionkey", 1), sc("r_name", 1), sc("ps_partkey", 4), sc("ps_suppkey", 4)}

	t.Run("identity when already correct", func(t *testing.T) {
		got := resolveSubColInSchema(rotated, &ColumnRef{Index: 2, Name: "ps_partkey", SourceTableIdx: 4})
		if got == nil || got.Index != 2 {
			t.Fatalf("got %v, want index 2 unchanged", got)
		}
	})
	t.Run("resolves a leaf-relative index onto the rotated schema", func(t *testing.T) {
		// Q2 exactly: harvested at 0 from partsupp's own scan schema.
		got := resolveSubColInSchema(rotated, &ColumnRef{Index: 0, Name: "ps_partkey", SourceTableIdx: 4})
		if got == nil || got.Index != 2 {
			t.Fatalf("got %v, want index 2 (ps_partkey), not the leaf-relative 0 (r_regionkey)", got)
		}
	})
	t.Run("bails when the column is absent", func(t *testing.T) {
		if got := resolveSubColInSchema(rotated, &ColumnRef{Index: 0, Name: "l_partkey", SourceTableIdx: 7}); got != nil {
			t.Fatalf("got %v, want nil — an absent column must not resolve to a neighbour", got)
		}
	})
	t.Run("bails when two candidates match", func(t *testing.T) {
		// A self-join of partsupp with no usable SourceTableIdx to separate
		// the two instances: guessing either one is a coin flip on the
		// answer, so the caller must keep the SubPlan.
		selfJoin := Schema{sc("ps_partkey", 0), sc("ps_suppkey", 0), sc("ps_partkey", 0)}
		if got := resolveSubColInSchema(selfJoin, &ColumnRef{Index: 9, Name: "ps_partkey"}); got != nil {
			t.Fatalf("got %v, want nil — ambiguous name must bail", got)
		}
	})
	t.Run("SourceTableIdx separates a self-join", func(t *testing.T) {
		selfJoin := Schema{sc("ps_partkey", 4), sc("ps_suppkey", 4), sc("ps_partkey", 6)}
		got := resolveSubColInSchema(selfJoin, &ColumnRef{Index: 0, Name: "ps_partkey", SourceTableIdx: 6})
		if got == nil || got.Index != 2 {
			t.Fatalf("got %v, want index 2 — the second partsupp instance", got)
		}
	})
}
