package planner

import (
	"github.com/goopg/goopg/internal/parser"
)

// M0075-0001: equivalence-class inference for transitive
// equality predicates. PostgreSQL's planner identifies
// `a = b AND b = c` and synthesises `a = c` so the join-
// order DP gets the additional edge in its search graph.
// goopg's planner did NOT have this; Q5's
// `c.nationkey = s.nationkey AND s.nationkey = n.nationkey`
// missed the implied `c.nationkey = n.nationkey` and the
// bushy DP couldn't consider the alternative join orders
// it enables.
//
// This file lives at the planner-side hook point (called
// by tryBushyDP after splitAnd, before buildJoinGraph).
// See docs/design/0075-0001-q5-equivalence-class-inference.md.

// columnIdent uniquely identifies a column for
// equivalence-class tracking. Source-table-aware so
// `a.id` and `b.id` (different aliases of the same table
// or same name across tables) don't get incorrectly merged.
//
// Type-aware (typeName) so cross-type equalities don't
// merge classes — int4 = int8 with implicit cast stays
// as a literal predicate.
type columnIdent struct {
	name           string
	sourceTableIdx int16
	schemaIndex    int
	typeName       string
}

// equivClasses is a union-find structure tracking which
// columnIdents are in the same equivalence class via
// observed equality predicates. Used only for
// inferTransitiveEqualities — not retained.
type equivClasses struct {
	parent map[columnIdent]columnIdent
	rank   map[columnIdent]int
}

func newEquivClasses() *equivClasses {
	return &equivClasses{
		parent: make(map[columnIdent]columnIdent),
		rank:   make(map[columnIdent]int),
	}
}

func (ec *equivClasses) find(c columnIdent) columnIdent {
	if _, ok := ec.parent[c]; !ok {
		ec.parent[c] = c
		return c
	}
	if ec.parent[c] != c {
		ec.parent[c] = ec.find(ec.parent[c])
	}
	return ec.parent[c]
}

func (ec *equivClasses) union(a, b columnIdent) {
	ra := ec.find(a)
	rb := ec.find(b)
	if ra == rb {
		return
	}
	if ec.rank[ra] < ec.rank[rb] {
		ra, rb = rb, ra
	}
	ec.parent[rb] = ra
	if ec.rank[ra] == ec.rank[rb] {
		ec.rank[ra]++
	}
}

// classes returns each equivalence class as a slice of
// its members. Classes with only one member are skipped
// (no closure to synthesise from a singleton).
func (ec *equivClasses) classes() map[columnIdent][]columnIdent {
	result := make(map[columnIdent][]columnIdent)
	for k := range ec.parent {
		root := ec.find(k)
		result[root] = append(result[root], k)
	}
	// Filter out singletons.
	for root, members := range result {
		if len(members) < 2 {
			delete(result, root)
		}
	}
	return result
}

// inferTransitiveEqualities walks the WHERE conjuncts,
// identifies ColumnRef = ColumnRef equalities, builds
// equivalence classes, and returns synthesised closure
// predicates that are NOT already present in the input.
//
// E.g. given `[a=b, b=c]`, returns `[a=c]`. Given
// `[a=b, b=c, c=d]`, returns `[a=c, a=d, b=d]`.
//
// Type-aware: ColumnRef pairs of different types are NOT
// merged. SourceTableIdx-aware (M0071-0009): same-name
// columns from different aliases stay in different
// classes via the columnIdent disambiguation.
//
// The returned slice contains fresh `*BinaryOp{Op: OpEq}`
// nodes; the caller appends them to its conjunct list
// before buildJoinGraph / enumerateBushyPlans.
func inferTransitiveEqualities(conjuncts []Expr) []Expr {
	if len(conjuncts) == 0 {
		return nil
	}

	ec := newEquivClasses()
	seenPairs := make(map[[2]columnIdent]bool)
	columnRefByIdent := make(map[columnIdent]*ColumnRef)

	// Pass 1: build equivalence classes from explicit
	// `ColumnRef = ColumnRef` predicates; record explicit
	// pairs to avoid re-synthesising them.
	for _, c := range conjuncts {
		la, lb, ok := isColumnRefEquality(c)
		if !ok {
			continue
		}
		ia := identOf(la)
		ib := identOf(lb)
		if ia == ib {
			continue
		}
		ec.union(ia, ib)
		seenPairs[orderedPair(ia, ib)] = true
		columnRefByIdent[ia] = la
		columnRefByIdent[ib] = lb
	}

	// Pass 2: synthesise the closure — for each
	// equivalence class with ≥ 2 members, emit
	// `member[i] = member[j]` for every pair NOT in
	// seenPairs.
	var added []Expr
	for _, members := range ec.classes() {
		for i := 0; i < len(members); i++ {
			for j := i + 1; j < len(members); j++ {
				p := orderedPair(members[i], members[j])
				if seenPairs[p] {
					continue
				}
				a := columnRefByIdent[members[i]]
				b := columnRefByIdent[members[j]]
				if a == nil || b == nil {
					continue
				}
				added = append(added, &BinaryOp{
					Op:    parser.OpEq,
					Left:  a,
					Right: b,
				})
				seenPairs[p] = true
			}
		}
	}
	return added
}

// isColumnRefEquality returns the (left, right) ColumnRefs
// when expr is `ColRef = ColRef` with matching types.
// Otherwise (_, _, false).
//
// Type-matching is enforced here so the caller doesn't
// have to re-check; cross-type equality predicates are
// excluded from equivalence-class inference (they may
// involve implicit casts that change semantics).
func isColumnRefEquality(e Expr) (*ColumnRef, *ColumnRef, bool) {
	bo, ok := e.(*BinaryOp)
	if !ok || bo.Op != parser.OpEq {
		return nil, nil, false
	}
	l, lok := bo.Left.(*ColumnRef)
	r, rok := bo.Right.(*ColumnRef)
	if !lok || !rok {
		return nil, nil, false
	}
	// Type-aware: only merge classes when both sides have
	// the same SQL type. Cross-type equality (e.g.
	// int4 = int8 via implicit cast) stays as a literal
	// predicate; the join graph still picks it up via the
	// existing edge-building path.
	if l.Type.Name != r.Type.Name {
		return nil, nil, false
	}
	return l, r, true
}

// identOf builds a stable columnIdent key from a
// ColumnRef. SourceTableIdx is the M0071-0009
// disambiguation primitive; without it, self-joins
// (Q9 lineitem self-join) would collapse into a single
// equivalence class spuriously.
func identOf(c *ColumnRef) columnIdent {
	return columnIdent{
		name:           c.Name,
		sourceTableIdx: c.SourceTableIdx,
		schemaIndex:    c.Index,
		typeName:       c.Type.Name,
	}
}

// orderedPair returns (a, b) sorted by name then by
// sourceTableIdx so the seenPairs map keys are
// canonical regardless of which order the original
// predicate had its operands.
func orderedPair(a, b columnIdent) [2]columnIdent {
	if compareColumnIdent(a, b) <= 0 {
		return [2]columnIdent{a, b}
	}
	return [2]columnIdent{b, a}
}

func compareColumnIdent(a, b columnIdent) int {
	if a.name != b.name {
		if a.name < b.name {
			return -1
		}
		return 1
	}
	if a.sourceTableIdx != b.sourceTableIdx {
		if a.sourceTableIdx < b.sourceTableIdx {
			return -1
		}
		return 1
	}
	if a.schemaIndex != b.schemaIndex {
		if a.schemaIndex < b.schemaIndex {
			return -1
		}
		return 1
	}
	if a.typeName != b.typeName {
		if a.typeName < b.typeName {
			return -1
		}
		return 1
	}
	return 0
}
