// Cost-based join-order reordering for the v0 planner.
//
// The default `planFromClause` builds a left-deep CROSS-join chain
// in source order, then `pushPredicatesIntoCrossJoins` pushes WHERE
// equalities down to their qualifying Join. That lifts the
// Cartesian explosion away from the Filter, but the *order* still
// follows the SQL text. For a TPC-H query like
//
//   SELECT n_name, ... FROM customer, orders, lineitem, supplier,
//                            nation, region
//   WHERE c_custkey = o_custkey AND l_orderkey = o_orderkey
//     AND l_suppkey = s_suppkey AND s_nationkey = n_nationkey
//     AND n_regionkey = r_regionkey AND r_name = 'ASIA'
//
// the source order joins `customer ⋈ orders ⋈ lineitem` first —
// three large fact tables — before any of the small dimension
// tables filter the result down. Even with hash join, the
// intermediate row count balloons.
//
// This pass runs *before* column-resolution: it permutes the
// parser-level FROM list so small relations are joined first.
// Operating at the parser level means we don't have to remap any
// resolved ColumnRef.Index downstream — the entire planner sees
// the new order as if the user had written it that way.
//
// Algorithm: greedy nearest-neighbour by cardinality. Start with
// the smallest table by `Stats.RowCount`. At each step, pick the
// next table that:
//   1. has an equality edge to some already-joined table (so the
//      next Join is INNER, not CROSS), and
//   2. minimises the cumulative joined cardinality estimate.
//
// If no edge-connected table remains, fall back to picking the
// smallest unjoined table (preserves the FROM list completeness;
// the residual CROSS joins still sit on the inside of the chain
// where their explosion is bounded by earlier filtering).
//
// Preconditions for applying the rewrite:
//   - All tables in FROM have catalog statistics (ANALYZE has run).
//   - Pure comma-FROM (no `JOIN ... ON` clauses, no derived tables).
//   - len(FROM) >= 3 (nothing to reorder for two-way joins; the
//     hash-join-build-side selector already handles the binary case).
//
// See docs/design/0003-0016-join-order-reordering.md for the full
// design and trade-offs.
package planner

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// reorderCommaFromByCardinality returns a permutation of s.FromExprs
// + s.From that places small-cardinality tables earlier. When any
// precondition is not met, returns the original lists unchanged.
// Caller is responsible for replacing the slices on a *copy* of the
// SelectStmt — this function does not mutate s.
func reorderCommaFromByCardinality(s *parser.SelectStmt, cat catalog.Catalog) ([]parser.FromExpr, []parser.RangeVar, bool) {
	if s == nil {
		return s.FromExprs, s.From, false
	}
	// Only the pure comma-FROM shape is eligible. Mixed FROM with
	// explicit JOIN ... ON would require honouring the user's
	// stated join order and predicate placement.
	if len(s.FromExprs) < 3 {
		return s.FromExprs, s.From, false
	}
	rels := make([]parser.RangeVar, len(s.FromExprs))
	for i, fe := range s.FromExprs {
		if len(fe.Joins) != 0 {
			return s.FromExprs, s.From, false
		}
		rv := fe.Base
		if rv.Subquery != nil {
			return s.FromExprs, s.From, false
		}
		rels[i] = rv
	}
	// Look up stats. Without a row count for every relation, the
	// reorder has no signal to work with — keep source order.
	rowCounts := make([]int64, len(rels))
	tables := make([]*catalog.Table, len(rels))
	for i, rv := range rels {
		tbl, ok := cat.LookupTable(parser.ObjectName{Schema: rv.Schema, Name: rv.Name})
		if !ok || tbl == nil {
			return s.FromExprs, s.From, false
		}
		if tbl.Stats == nil || tbl.Stats.RowCount <= 0 {
			return s.FromExprs, s.From, false
		}
		rowCounts[i] = tbl.Stats.RowCount
		tables[i] = tbl
	}
	// Build qualifier → relation-index map. Aliases / unqualified
	// table names point to the FROM position; bare column names
	// also flow through this map when the column appears in
	// exactly one of the FROM tables (TPC-H queries use bare refs
	// like `c_custkey = o_custkey` rather than `c.c_custkey =
	// o.o_custkey`).
	indexByKey := make(map[string]int, len(rels))
	for i, rv := range rels {
		for _, k := range relKeys(rv) {
			if _, exists := indexByKey[k]; !exists {
				indexByKey[k] = i
			}
		}
	}
	colToRel := buildBareColumnIndex(tables)
	edges := collectEqualityEdges(s.Where, indexByKey, colToRel, len(rels))

	// Greedy NN. Seed with the smallest-cardinality relation.
	order := make([]int, 0, len(rels))
	used := make([]bool, len(rels))
	first := smallestUnused(rowCounts, used)
	order = append(order, first)
	used[first] = true
	for len(order) < len(rels) {
		next, ok := pickNextByEdge(order, used, edges, rowCounts)
		if !ok {
			next = smallestUnused(rowCounts, used)
		}
		order = append(order, next)
		used[next] = true
	}
	// If the greedy pick is the source order, skip the rewrite —
	// no point in copying slices.
	if isIdentityPermutation(order) {
		return s.FromExprs, s.From, false
	}
	newFromExprs := make([]parser.FromExpr, len(rels))
	newFrom := make([]parser.RangeVar, len(rels))
	for i, idx := range order {
		newFromExprs[i] = s.FromExprs[idx]
		newFrom[i] = s.From[idx]
	}
	return newFromExprs, newFrom, true
}

// relKeys returns the lookup keys a parser.ColumnRef qualifier may
// resolve to for this RangeVar. The alias takes precedence when
// present; the unaliased table name is also accepted (matches
// upstream's column-resolution rules).
func relKeys(rv parser.RangeVar) []string {
	keys := make([]string, 0, 2)
	if rv.Alias != "" {
		keys = append(keys, strings.ToLower(rv.Alias))
	}
	if rv.Name != "" {
		keys = append(keys, strings.ToLower(rv.Name))
	}
	return keys
}

// collectEqualityEdges walks the WHERE conjunction collecting
// undirected `t1.col = t2.col` edges. Edges are stored as a
// symmetric adjacency: edges[i] is the set of relation indices
// connected to i. Column refs are resolved either by their
// table qualifier (alias / table name) or, when bare, by
// looking up the column name in colToRel — TPC-H queries use
// bare refs so the latter is the common case.
func collectEqualityEdges(where parser.Expr, indexByKey map[string]int, colToRel map[string]int, n int) []map[int]struct{} {
	out := make([]map[int]struct{}, n)
	for i := range out {
		out[i] = map[int]struct{}{}
	}
	walkConjuncts(where, func(c parser.Expr) {
		bin, ok := c.(*parser.BinaryOp)
		if !ok || bin.Op != "=" {
			return
		}
		l, lok := bin.Left.(*parser.ColumnRef)
		r, rok := bin.Right.(*parser.ColumnRef)
		if !lok || !rok {
			return
		}
		li, lOk := resolveRefToRel(l, indexByKey, colToRel)
		ri, rOk := resolveRefToRel(r, indexByKey, colToRel)
		if !lOk || !rOk || li == ri {
			return
		}
		out[li][ri] = struct{}{}
		out[ri][li] = struct{}{}
	})
	return out
}

// resolveRefToRel maps a parser.ColumnRef to the FROM-list index
// of the relation it belongs to. A qualified ref (`alias.col` or
// `table.col`) wins via indexByKey; otherwise we fall back to the
// bare-column index, which is populated only for columns that
// appear in exactly one FROM relation (ambiguous bare names are
// dropped — they wouldn't form a clean edge anyway).
func resolveRefToRel(c *parser.ColumnRef, indexByKey, colToRel map[string]int) (int, bool) {
	if c.Table != "" {
		idx, ok := indexByKey[strings.ToLower(c.Table)]
		return idx, ok
	}
	idx, ok := colToRel[strings.ToLower(c.Column)]
	return idx, ok
}

// buildBareColumnIndex maps a column name (lower-cased) to the
// FROM-list index of the unique table that owns it. Columns
// present in two or more tables map to -1 internally and are
// excluded from the returned map so callers see "not unique" as
// "not in map" — same shape as a missing entry.
func buildBareColumnIndex(tables []*catalog.Table) map[string]int {
	count := map[string]int{}
	owner := map[string]int{}
	for i, tbl := range tables {
		if tbl == nil {
			continue
		}
		for _, col := range tbl.Columns {
			name := strings.ToLower(col.Name)
			count[name]++
			owner[name] = i // overwritten on dup; resolved below
		}
	}
	out := make(map[string]int, len(owner))
	for name, n := range count {
		if n == 1 {
			out[name] = owner[name]
		}
	}
	return out
}

// walkConjuncts descends `AND`-trees and visits each leaf
// conjunct. The WHERE clause `a AND (b AND c)` produces three
// callbacks: a, b, c. NULL input is a no-op.
func walkConjuncts(e parser.Expr, visit func(parser.Expr)) {
	if e == nil {
		return
	}
	if bin, ok := e.(*parser.BinaryOp); ok && strings.ToUpper(bin.Op) == "AND" {
		walkConjuncts(bin.Left, visit)
		walkConjuncts(bin.Right, visit)
		return
	}
	visit(e)
}

// smallestUnused returns the index of the unused relation with
// the lowest row count. Ties broken by lowest index.
func smallestUnused(rowCounts []int64, used []bool) int {
	best := -1
	for i, rc := range rowCounts {
		if used[i] {
			continue
		}
		if best == -1 || rc < rowCounts[best] {
			best = i
		}
	}
	return best
}

// pickNextByEdge returns the unused relation with the lowest row
// count that's connected by an equality edge to any relation in
// the already-joined set. ok=false when no such relation exists —
// the caller falls back to the smallest unused relation.
func pickNextByEdge(joined []int, used []bool, edges []map[int]struct{}, rowCounts []int64) (int, bool) {
	best := -1
	for _, j := range joined {
		for k := range edges[j] {
			if used[k] {
				continue
			}
			if best == -1 || rowCounts[k] < rowCounts[best] {
				best = k
			}
		}
	}
	if best == -1 {
		return 0, false
	}
	return best, true
}

func isIdentityPermutation(order []int) bool {
	for i, v := range order {
		if i != v {
			return false
		}
	}
	return true
}
