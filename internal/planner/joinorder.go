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
//   - Pure comma-FROM (no `JOIN ... ON` clauses, no table functions,
//     no LATERAL derived tables; a non-lateral derived table is
//     admitted and forces connectivity mode, like a WITH reference).
//   - len(FROM) >= 3 (nothing to reorder for two-way joins; the
//     hash-join-build-side selector already handles the binary case).
//
// See docs/design/0003-0016-join-order-reordering.md for the full
// design and trade-offs.
//
// M0125-0034 (connectivity mode). The precondition "every FROM item is
// a base table with statistics" excluded a whole shape rather than a
// whole *cost regime*: a comma-FROM list that names a WITH reference
// has no row count for that item and never will, so the pass declined
// and the source order survived — CROSS and all. `tryBushyDP` declines
// on the same lists for an unrelated reason (its leaf whitelist admits
// only SeqScan / IndexScan / MultiHashJoin, because buildBindingsPosMap
// keys on scan identity), so nothing reordered them at all. That is the
// mechanism behind M0125-0026's class C1: TPC-DS Q30, Q64 and Q81 each
// join a WITH reference to base relations and each got a Cartesian
// product with the equi-predicate demoted to a Filter above it.
//
// A missing row count blocks ranking connected orders; it does not
// block telling a connected order from a disconnected one. So when the
// list holds a WITH reference — or, since the Q65 arm, a non-lateral
// derived table, which has the same standing (real join edges, no
// possible row count) — the pass switches objective, from "join small
// relations first" to "never emit an avoidable cross", and breaks ties
// on source order. See orderByConnectivity and
// docs/design/0125-0034a-comma-from-connectivity-order.md.
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
		return nil, nil, false
	}
	// Only the pure comma-FROM shape is eligible. Mixed FROM with
	// explicit JOIN ... ON would require honouring the user's
	// stated join order and predicate placement.
	if len(s.FromExprs) < 3 {
		return s.FromExprs, s.From, false
	}
	// The permutation writes both lists positionally, so it needs
	// them parallel. The parser builds a synthetic SelectStmt whose
	// `From` is a flattened JOIN tree while `FromExprs` holds one
	// item (select.go's join flattening); the `fe.Joins` guard below
	// catches that shape, but check the lengths too rather than
	// depend on a sibling invariant.
	if len(s.From) != len(s.FromExprs) {
		return s.FromExprs, s.From, false
	}
	rels := make([]parser.RangeVar, len(s.FromExprs))
	for i, fe := range s.FromExprs {
		if len(fe.Joins) != 0 {
			return s.FromExprs, s.From, false
		}
		rv := fe.Base
		// A table function in FROM may reference an earlier FROM item
		// even WITHOUT the LATERAL keyword — in PG the keyword is
		// noise before a function item — so no AST flag can prove one
		// independent and the whole reorder declines.
		if rv.TableFunc != nil {
			return s.FromExprs, s.From, false
		}
		// A LATERAL derived table references an earlier FROM item, so
		// moving it ahead of that item would change what the query
		// means. The parser records the keyword on the RangeVar
		// (M0125-0034's Q65 arm). A derived table WITHOUT it cannot
		// see its siblings — PG rejects the unmarked form with
		// "invalid reference to FROM-clause entry" — so it is admitted
		// and sized (or not) below like any other relation.
		if rv.Lateral {
			return s.FromExprs, s.From, false
		}
		rels[i] = rv
	}
	// Size what can be sized, and record *why* an item could not be.
	// The two reasons are not interchangeable:
	//
	//   unknown   — the item is a WITH reference (the catalog has no
	//               such relation) or a non-lateral derived table
	//               (TPC-DS Q65's two aggregate inputs). Either is a
	//               real relation with real join edges; only its
	//               cardinality is unavailable at parser level, and
	//               no ANALYZE will ever supply one. Cardinality
	//               ordering is impossible *by construction*, so the
	//               list runs in connectivity mode below.
	//   unstatted — a base table that simply has not been ANALYZEd.
	//               Here the cardinality signal is merely missing,
	//               and the pass declines exactly as it always has.
	rowCounts := make([]int64, len(rels))
	tables := make([]*catalog.Table, len(rels))
	unknown := false
	for i, rv := range rels {
		if rv.Subquery != nil {
			// Non-lateral derived table: never in the catalog, and
			// rv.Name is empty, so don't let the lookup below decide.
			unknown = true
			continue
		}
		tbl, ok := cat.LookupTable(parser.ObjectName{Schema: rv.Schema, Name: rv.Name})
		if !ok || tbl == nil {
			unknown = true
			continue
		}
		// Keep the table whether or not it has statistics: `tables`
		// feeds bare-column resolution, which needs the column list
		// and never looks at Stats. (Conflating the two cost this
		// pass its edges on an un-ANALYZEd list.)
		tables[i] = tbl
	}
	for i, tbl := range tables {
		if tbl == nil {
			continue // WITH reference — no row count exists to read
		}
		if tbl.Stats == nil || tbl.Stats.RowCount <= 0 {
			// Un-ANALYZEd base table. In cardinality mode that is the
			// whole signal missing and the pass declines, exactly as
			// it always has. In connectivity mode row counts are
			// never consulted, so it is not a reason to decline.
			if !unknown {
				return s.FromExprs, s.From, false
			}
			continue
		}
		rowCounts[i] = tbl.Stats.RowCount
	}
	// Build qualifier → relation-index map. Aliases / unqualified
	// table names point to the FROM position; bare column names
	// also flow through this map when the column appears in
	// exactly one of the FROM tables (TPC-H queries use bare refs
	// like `c_custkey = o_custkey` rather than `c.c_custkey =
	// o.o_custkey`). A WITH reference contributes its alias/name
	// key like any other relation — that is how the edges that
	// connect it are found — but contributes no bare-column entry,
	// because its output columns are not in the catalog.
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

	var order []int
	if unknown {
		order = orderByConnectivity(edges, len(rels))
	} else {
		order = orderByCardinality(edges, rowCounts)
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

// orderByCardinality is the historical greedy nearest-neighbour: seed
// with the smallest relation, then repeatedly take the smallest
// edge-connected relation, falling back to the smallest unused one
// when the graph runs out of edges.
func orderByCardinality(edges []map[int]struct{}, rowCounts []int64) []int {
	n := len(rowCounts)
	order := make([]int, 0, n)
	used := make([]bool, n)
	first := smallestUnused(rowCounts, used)
	order = append(order, first)
	used[first] = true
	for len(order) < n {
		next, ok := pickNextByEdge(order, used, edges, rowCounts)
		if !ok {
			next = smallestUnused(rowCounts, used)
		}
		order = append(order, next)
		used[next] = true
	}
	return order
}

// orderByConnectivity permutes a comma-FROM list so that every item
// after the first shares an equality edge with some item already
// placed — so the left-deep chain `planFromClause` builds contains no
// Cartesian product that the join graph could have avoided.
//
// It deliberately makes no claim about *which* connected order is
// cheapest. It runs only when the list holds a WITH reference or a
// non-lateral derived table, i.e.
// when at least one relation has no parser-level row count, so a cost
// ranking would be inventing a number rather than reading one. Ties
// therefore break on source order: the seed is the first FROM item and
// each step takes the lowest-numbered connected item still unplaced.
// That keeps the permutation minimal and gives it one useful property —
// a source order that is already cross-free is a fixed point, because
// at step i every unplaced item is ≥ i and item i is connected, so item
// i is chosen. The caller's identity check then declines the rewrite.
// In other words this pass rewrites the FROM list if and only if the
// source order contains a cross the join graph could have avoided.
//
// Which connected order is *fastest* is a cost question this pass does
// not answer; it belongs to M0125-0038 (no cost/cardinality propagation
// above base scans).
func orderByConnectivity(edges []map[int]struct{}, n int) []int {
	order := make([]int, 0, n)
	used := make([]bool, n)
	order = append(order, 0)
	used[0] = true
	for len(order) < n {
		next := -1
		for _, j := range order {
			for k := range edges[j] {
				if used[k] {
					continue
				}
				if next == -1 || k < next {
					next = k
				}
			}
		}
		if next == -1 {
			// The join graph is disconnected: the remaining items
			// have no edge to anything placed, so no permutation
			// removes this cross. Emit them in source order.
			next = firstUnused(used)
		}
		order = append(order, next)
		used[next] = true
	}
	return order
}

// firstUnused returns the lowest-numbered relation not yet placed.
func firstUnused(used []bool) int {
	for i, u := range used {
		if !u {
			return i
		}
	}
	return -1
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
	addEdge := func(c parser.Expr) {
		bin, ok := c.(*parser.BinaryOp)
		if !ok || bin.Op != parser.OpEq {
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
	}
	walkConjuncts(where, func(c parser.Expr) {
		// Direct AND-conjunct match.
		addEdge(c)
		// M0058-0004: Q19's WHERE clause is an OR of ANDs where every
		// branch shares the same equijoin (`p_partkey = l_partkey`).
		// Extract those common equijoins so the planner sees the join
		// edge and emits a Hash Join instead of `Nested Loop (CROSS)`.
		for _, eq := range commonEquijoinsAcrossOr(c) {
			addEdge(eq)
		}
	})
	return out
}

// commonEquijoinsAcrossOr inspects a top-level OR expression and
// returns the set of equality predicates `t1.col = t2.col` that
// appear (semantically) in every OR branch. The full OR predicate
// remains in the WHERE clause as a residual filter; this only adds
// join-edge information for cost-based ordering. (M0058-0004.)
func commonEquijoinsAcrossOr(e parser.Expr) []parser.Expr {
	bin, ok := e.(*parser.BinaryOp)
	if !ok || bin.Op != parser.OpOr {
		return nil
	}
	branches := flattenOrBranches(bin)
	if len(branches) < 2 {
		return nil
	}
	branchEqs := make([]map[string]parser.Expr, len(branches))
	for i, br := range branches {
		branchEqs[i] = map[string]parser.Expr{}
		walkConjuncts(br, func(c parser.Expr) {
			b, ok := c.(*parser.BinaryOp)
			if !ok || b.Op != parser.OpEq {
				return
			}
			lc, lok := b.Left.(*parser.ColumnRef)
			rc, rok := b.Right.(*parser.ColumnRef)
			if !lok || !rok {
				return
			}
			branchEqs[i][colRefPairKey(lc, rc)] = c
		})
	}
	// Intersect across branches.
	var common []parser.Expr
	for k, v := range branchEqs[0] {
		ok := true
		for j := 1; j < len(branchEqs); j++ {
			if _, present := branchEqs[j][k]; !present {
				ok = false
				break
			}
		}
		if ok {
			common = append(common, v)
		}
	}
	return common
}

// flattenOrBranches recursively splits an OR tree into a flat list of
// branch expressions. `(a OR b) OR c` returns [a, b, c]; non-OR
// inputs return [e].
func flattenOrBranches(e parser.Expr) []parser.Expr {
	bin, ok := e.(*parser.BinaryOp)
	if !ok || bin.Op != parser.OpOr {
		return []parser.Expr{e}
	}
	out := flattenOrBranches(bin.Left)
	out = append(out, flattenOrBranches(bin.Right)...)
	return out
}

// colRefPairKey produces a canonical (order-independent) key for a
// pair of ColumnRefs so equijoin equalities like `a.x = b.y` and
// `b.y = a.x` collapse onto the same key during set intersection.
func colRefPairKey(a, b *parser.ColumnRef) string {
	left := strings.ToLower(a.Table) + "." + strings.ToLower(a.Column)
	right := strings.ToLower(b.Table) + "." + strings.ToLower(b.Column)
	if left > right {
		left, right = right, left
	}
	return left + "==" + right
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
	if bin, ok := e.(*parser.BinaryOp); ok && bin.Op == parser.OpAnd {
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
