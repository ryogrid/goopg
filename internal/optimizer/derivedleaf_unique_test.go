package optimizer

// M0146-0009e — PG's examine_simple_variable RTE_SUBQUERY/RTE_CTE arm for
// FROM-clause derived leaves (selfuncs.c:5830-5912). Two facts ride on a Var
// operand over a derived leaf even though no catalog statistics exist:
//
//   - vardata->rel = find_base_rel(varno) — unconditional (selfuncs.c:5331),
//     so the leaf's OWN row estimate is the variable's tuples; and
//   - isunique when the probed output column is the body top query's lone
//     GROUP BY or DISTINCT(ON) key (:5865-5883).
//
// Q83 (TPC-DS) is the witness: two lone-GROUP-BY derived leaves joined on
// the key estimated rows=1 (DEFAULT_NUM_DISTINCT=200 selectivity) where PG
// estimates rows=10. Everything below pins the two halves plus the fail-
// closed punts upstream also makes (multi-key grouping, set ops, non-Var
// outputs, base scans).

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// loneGroupBody builds `SELECT k, sum(v) FROM <in> GROUP BY k` the way the
// production planner emits it: Project over an Aggregate whose output is
// [group key, agg col].
func loneGroupBody(keyCols int) Node {
	aggSchema := Schema{{Name: "k", Type: catalog.Type{Name: "int4"}}, {Name: "s", Type: catalog.Type{Name: "int8"}}}
	if keyCols == 2 {
		aggSchema = append(aggSchema[:1:1], SchemaColumn{Name: "k2", Type: catalog.Type{Name: "int4"}},
			SchemaColumn{Name: "s", Type: catalog.Type{Name: "int8"}})
	}
	groups := []Expr{&ColumnRef{Index: 0, Name: "k", Type: catalog.Type{Name: "int4"}}}
	if keyCols == 2 {
		groups = append(groups, &ColumnRef{Index: 1, Name: "k2", Type: catalog.Type{Name: "int4"}})
	}
	agg := &Aggregate{
		Child:      &SeqScan{schema: Schema{{Name: "k"}, {Name: "k2"}, {Name: "v"}}},
		GroupExprs: groups,
		Aggs:       []AggregateCall{{Name: "sum", Arg: &ColumnRef{Index: 2, Name: "v"}}},
		schema:     aggSchema,
	}
	targets := make([]Expr, len(aggSchema))
	out := make(Schema, len(aggSchema))
	for i := range aggSchema {
		targets[i] = &ColumnRef{Index: i, Name: aggSchema[i].Name, Type: aggSchema[i].Type}
		out[i] = aggSchema[i]
	}
	return &Project{Child: agg, Targets: targets, schema: out}
}

func mkCTEScanLeaf(t *testing.T, name string, leafSchema Schema, body Node) *CTEScan {
	t.Helper()
	cols := make([]catalog.Column, len(leafSchema))
	for i, c := range leafSchema {
		cols[i] = catalog.Column{Name: c.Name, Type: c.Type}
	}
	cte := &plannedCTE{
		name:   name,
		body:   body,
		schema: leafSchema,
		table:  &catalog.Table{Name: name, Columns: cols},
	}
	return &CTEScan{Name: name, Child: body, schema: leafSchema, cte: cte}
}

func TestDerivedLeafUniqueCols(t *testing.T) {
	two := Schema{{Name: "item_id"}, {Name: "qty"}}

	cases := []struct {
		name string
		leaf Node
		want []string // unique leaf output names; nil = none
	}{
		{
			"cte-lone-group-key",
			mkCTEScanLeaf(t, "c", two, loneGroupBody(1)),
			[]string{"item_id"},
		},
		{
			"cte-two-group-keys",
			mkCTEScanLeaf(t, "c", Schema{{Name: "a"}, {Name: "b"}, {Name: "s"}}, loneGroupBody(2)),
			nil,
		},
		{
			// A column-alias list renames but never reorders: the leaf's
			// own output name is what a probed ColumnRef carries.
			"cte-alias-renamed-key",
			mkCTEScanLeaf(t, "c", Schema{{Name: "alias_k"}, {Name: "qty"}}, loneGroupBody(1)),
			[]string{"alias_k"},
		},
		{
			"subquery-lone-group-key",
			&SubqueryScan{Child: loneGroupBody(1), schema: two},
			[]string{"item_id"},
		},
		{
			"distinct-one-col",
			&SubqueryScan{Child: &Distinct{Child: &SeqScan{schema: Schema{{Name: "k"}}},
				schema: Schema{{Name: "k"}}}, schema: Schema{{Name: "k"}}},
			[]string{"k"},
		},
		{
			// Plain DISTINCT's distinctClause covers every output column,
			// so a two-column output is never a lone key (upstream's
			// list_length check).
			"distinct-two-cols",
			&SubqueryScan{Child: &Distinct{Child: &SeqScan{schema: two}, schema: two}, schema: two},
			nil,
		},
		{
			"distinct-on-key-col",
			&SubqueryScan{Child: &DistinctOn{Child: &SeqScan{schema: two},
				KeyCols: []int{1}, schema: two}, schema: two},
			[]string{"qty"},
		},
		{
			// A leaf-local filter shrinks rows; it cannot change which
			// output columns are unique (upstream's baserestrictinfo arm).
			"filter-wrapped-cte",
			&Filter{Child: mkCTEScanLeaf(t, "c", two, loneGroupBody(1)), LeafLocal: true},
			[]string{"item_id"},
		},
		{
			"base-scan-leaf",
			&SeqScan{schema: two},
			nil,
		},
		{
			// Set ops and unkeyed bodies punt, exactly as upstream's early
			// returns do.
			"values-leaf",
			&SubqueryScan{Child: &SeqScan{schema: two}, schema: two},
			nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := derivedLeafUniqueCols(tc.leaf)
			if len(got) != len(tc.want) {
				t.Fatalf("unique cols = %v, want %v", got, tc.want)
			}
			for _, w := range tc.want {
				if !got[w] {
					t.Fatalf("unique cols = %v, want key %q marked", got, w)
				}
			}
		})
	}
}

// TestExamineJoinVarDerivedLeaf pins the estimator half: a Var operand on a
// derived leaf sees the leaf's own tuples (rel=leaf) and the lone-key
// column's isunique marking, so `getVariableNumDistinct` answers the leaf's
// row count instead of DEFAULT_NUM_DISTINCT — Q83's rows=1 → rows=10.
func TestExamineJoinVarDerivedLeaf(t *testing.T) {
	leafCols := []catalog.Column{
		{Name: "item_id", Type: catalog.Type{Name: "int4"}},
		{Name: "qty", Type: catalog.Type{Name: "int8"}},
	}
	synthTable := &catalog.Table{Name: "sr_items", Columns: leafCols}
	s := &searchCtx{relInfos: []baseRelInfo{
		{
			table:         synthTable,
			leafTuples:    20,
			leafRows:      20,
			uniqueOutCols: map[string]bool{"item_id": true},
		},
	}}

	// The lone group key: isunique + leaf tuples → nd = 20, not 200.
	v := s.examineJoinVar(&ColumnRef{Name: "item_id", Type: catalog.Type{Name: "int4"}}, RelSet(1)<<0)
	if !v.isUnique {
		t.Fatalf("item_id: isUnique = false, want true")
	}
	if v.tuples != 20 {
		t.Fatalf("item_id: tuples = %v, want 20 (leaf rel->tuples)", v.tuples)
	}
	if nd, _ := getVariableNumDistinct(v); nd != 20 {
		t.Fatalf("item_id: nd = %v, want 20", nd)
	}

	// A non-key column gets the leaf tuples but no uniqueness: still a
	// better answer than tuples=0 (nd = min(rows, 200) upstream).
	v = s.examineJoinVar(&ColumnRef{Name: "qty", Type: catalog.Type{Name: "int8"}}, RelSet(1)<<0)
	if v.isUnique {
		t.Fatalf("qty: isUnique = true, want false")
	}
	if v.tuples != 20 {
		t.Fatalf("qty: tuples = %v, want 20", v.tuples)
	}
	if nd, _ := getVariableNumDistinct(v); nd != 20 {
		t.Fatalf("qty: nd = %v, want 20 (small-rel arm)", nd)
	}

	// A leaf WITHOUT the derived-leaf marking keeps today's zero-tuples
	// fallback — the catalog path is untouched for base relations.
	s2 := &searchCtx{relInfos: []baseRelInfo{{table: synthTable}}}
	v = s2.examineJoinVar(&ColumnRef{Name: "item_id"}, RelSet(1)<<0)
	if v.tuples != 0 || v.isUnique {
		t.Fatalf("unmarked leaf: tuples=%v isUnique=%v, want 0/false", v.tuples, v.isUnique)
	}
}
