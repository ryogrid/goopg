package optimizer

import (
	"sort"

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
//
// M0076-0004: each class's member slice is sorted by
// `compareColumnIdent` so the synthesised conjunct
// sequence in `inferTransitiveEqualities` is
// reproducible across runs (essential for plan-snapshot
// diff stability — Go's map iteration is intentionally
// nondeterministic).
func (ec *equivClasses) classes() map[columnIdent][]columnIdent {
	result := make(map[columnIdent][]columnIdent)
	for k := range ec.parent {
		root := ec.find(k)
		result[root] = append(result[root], k)
	}
	// Filter out singletons + sort each class.
	for root, members := range result {
		if len(members) < 2 {
			delete(result, root)
			continue
		}
		sort.SliceStable(members, func(i, j int) bool {
			return compareColumnIdent(members[i], members[j]) < 0
		})
		result[root] = members
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
// inferEquivClassConstants is `inferTransitiveEqualities` restricted to its
// CONSTANT-propagation half — take2 P1-20.
//
// The two halves are separated because their blast radii differ by an order of
// magnitude. Propagating `a = 42` to every member of a's class only ADDS
// restrictions, so a relation can be filtered earlier and no join order is
// re-opened. Synthesising the transitive `a = c` from `a = b, b = c` hands the
// search new JOIN CLAUSES, which changes which orders are legal and reshapes
// plans broadly — measured: it broke the pinned-semi-join layout
// TestPreDPPinnedSemiKeysResolveAfterDP asserts, on a query with no constants
// in it at all.
//
// The search therefore takes the constant half only. The transitive half keeps
// its single legacy caller (pushPredicatesIntoCrossJoins) until it is evaluated
// on its own, which is a separate item.
func inferEquivClassConstants(conjuncts []Expr) []Expr {
	return inferEqualitiesClosure(conjuncts, false)
}

func inferTransitiveEqualities(conjuncts []Expr) []Expr {
	return inferEqualitiesClosure(conjuncts, true)
}

func inferEqualitiesClosure(conjuncts []Expr, emitTransitive bool) []Expr {
	if len(conjuncts) == 0 {
		return nil
	}

	ec := newEquivClasses()
	seenPairs := make(map[[2]columnIdent]bool)
	columnRefByIdent := make(map[columnIdent]*ColumnRef)
	// take2 P1-20: constants seen against a class member, and the members a
	// constant has already been stated for. See constant propagation below.
	constByIdent := make(map[columnIdent]Expr)
	seenConst := make(map[columnIdent]bool)

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

	// Pass 1b: record `member = const` restrictions — take2 P1-20.
	//
	// PostgreSQL's equivalence classes carry constants, and equivclass.c
	// generates a `var = const` restriction for EVERY member of a class that
	// has one. goopg's closure synthesised only column-to-column equalities,
	// so a constant stated against one member never reached the others.
	//
	// Measured on the TPC-H bench clusters,
	// `customer, orders WHERE c_custkey = o_custkey AND c_custkey = 42`:
	//
	//	PG     Index Cond: (o_custkey = '42') -> 16 rows, total cost 13.30
	//	goopg  Parallel Index Only Scan on orders, rows=1500000, cost 32249.25
	//
	// A 2400x cost difference, and in execution the whole `orders` relation
	// scanned instead of one key's worth. This is one of the transformations
	// equivalence classes exist for.
	for _, c := range conjuncts {
		cr, konst, ok := isColumnRefConstEquality(c)
		if !ok {
			continue
		}
		id := identOf(cr)
		columnRefByIdent[id] = cr
		if _, dup := constByIdent[id]; !dup {
			constByIdent[id] = konst
		}
		// The restriction is already stated for this member; only the OTHER
		// members need one synthesised.
		seenConst[id] = true
	}

	// Pass 2: synthesise the closure — for each
	// equivalence class with ≥ 2 members, emit
	// `member[i] = member[j]` for every pair NOT in
	// seenPairs.
	//
	// M0076-0004: iterate classes in a deterministic
	// order (sorted by root columnIdent). Without this,
	// Go's map iteration order varies per run and the
	// synthesised conjunct sequence appended to the DP's
	// conjunct list shifts between runs, causing
	// non-reproducible plan choices.
	var added []Expr
	classMap := ec.classes()
	roots := make([]columnIdent, 0, len(classMap))
	for root := range classMap {
		roots = append(roots, root)
	}
	sort.SliceStable(roots, func(i, j int) bool {
		return compareColumnIdent(roots[i], roots[j]) < 0
	})
	for _, root := range roots {
		members := classMap[root]
		for i := 0; emitTransitive && i < len(members); i++ {
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

		// Constant propagation for this class. If ANY member is equated to a
		// constant, every other member is too — that is what makes the class
		// an equivalence class. Emitted in the members' own deterministic
		// order for the same reproducibility reason the pair loop above is
		// (M0076-0004).
		var konst Expr
		for _, m := range members {
			if k, ok := constByIdent[m]; ok {
				konst = k
				break
			}
		}
		if konst == nil {
			continue
		}
		for _, m := range members {
			if seenConst[m] {
				continue
			}
			ref := columnRefByIdent[m]
			if ref == nil {
				continue
			}
			added = append(added, &BinaryOp{
				Op:    parser.OpEq,
				Left:  ref,
				Right: konst,
			})
			seenConst[m] = true
		}
	}
	return added
}

// isColumnRefConstEquality recognises `col = const` and `const = col`, the
// shape that makes an equivalence class `ec_has_const` upstream.
//
// Constant-ness is `isConstExpr` (selectivity.go), reused rather than
// re-written: it already means "a literal the planner may reason about", and a
// second hand-written Expr switch is the defect class
// TestExprSwitchInventoryIsPinned exists to catch — it caught this one.
//
// The narrowness matters. Only a literal is propagated, so a volatile or
// parameterised expression is never duplicated onto another relation where it
// would be evaluated a second time and might not agree with itself.
func isColumnRefConstEquality(e Expr) (*ColumnRef, Expr, bool) {
	bo, ok := e.(*BinaryOp)
	if !ok || bo.Op != parser.OpEq {
		return nil, nil, false
	}
	if cr, isCol := bo.Left.(*ColumnRef); isCol && isConstExpr(bo.Right) {
		return cr, bo.Right, true
	}
	if cr, isCol := bo.Right.(*ColumnRef); isCol && isConstExpr(bo.Left) {
		return cr, bo.Left, true
	}
	return nil, nil, false
}


// smallAnchorRowsThreshold is the design 02 §5 small-anchor
// row-count limit. A relation whose post-filter rowcount fits
// in this many rows qualifies as an "anchor" — the bushy DP
// is allowed to synthesise equality edges from it to any
// non-anchor relation in its equivalence class. The initial
// value of 1024 is intentionally permissive; raising the bar
// (e.g., 256) is a safe regression-tightening knob for future
// tuning. (M0077-0004 / Slice D.)
const smallAnchorRowsThreshold = 1024

// inferAnchoredEqualities is the M0077-0004 (Slice D)
// selective alternative to `inferTransitiveEqualities`.
// Instead of the full transitive closure, it synthesises
// only edges that go FROM an anchor relation TO a
// non-anchor relation in the same equivalence class, where
// "anchor" means at least one of:
//
//  1. the relation is `SmallDimension`-flagged
//     (catalog hint — region / nation in TPC-H);
//  2. its local filter halved the relation
//     (`filteredRows*2 ≤ baseRows`);
//  3. its post-filter rowcount fits inside
//     `smallAnchorRowsThreshold`.
//
// The output edges are tagged inferred via
// `buildJoinGraph`'s `inferredCount` parameter, so
// M0076-0004's `inferredEdgePenalty` keeps them as a final
// tiebreaker against an explicit equivalent.
//
// Why this avoids the M0075-0001 / M0076-0001 Q9 regression
// mode: large-fact equivalence classes whose members are
// neither SmallDimension nor selectively filtered get NO
// synthesised edges. The Q9 hang at 380-600 s with 0 rows
// was caused by the global hook firing on exactly that
// shape; the anchored rule structurally excludes it.
//
// (M0077-0004 / Slice D per design 02 §5.)
func inferAnchoredEqualities(conjuncts []Expr, rels []baseRelInfo) []Expr {
	if len(conjuncts) == 0 || len(rels) == 0 {
		return nil
	}

	// Mark each relation as anchor / non-anchor and build a
	// SourceTableIdx → bindingIdx lookup (so ColumnRefs can
	// be mapped back to their relation when checking
	// anchor-ness during class iteration).
	isAnchor := make(map[int]bool, len(rels))
	srcToBinding := make(map[int16]int, len(rels))
	for _, ri := range rels {
		if ri.bindingIdx < 0 || ri.sourceIdx == 0 {
			continue
		}
		srcToBinding[ri.sourceIdx] = ri.bindingIdx
		switch {
		case ri.isSmallDimension:
			isAnchor[ri.bindingIdx] = true
		case ri.hasLocalFilter && ri.baseRows > 0 && ri.filteredRows > 0 &&
			ri.filteredRows*2 <= ri.baseRows:
			isAnchor[ri.bindingIdx] = true
		case ri.filteredRows > 0 && ri.filteredRows <= smallAnchorRowsThreshold:
			isAnchor[ri.bindingIdx] = true
		}
	}
	if len(isAnchor) == 0 {
		// No anchor exists in any class → nothing to
		// synthesise from.
		return nil
	}

	// Pass 1: build equivalence classes from explicit
	// `ColumnRef = ColumnRef` predicates (mirrors
	// inferTransitiveEqualities). Record explicit pairs to
	// avoid re-emitting them.
	ec := newEquivClasses()
	seenPairs := make(map[[2]columnIdent]bool)
	columnRefByIdent := make(map[columnIdent]*ColumnRef)
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

	// Pass 2: for each class, partition members into
	// anchors and non-anchors. Synthesise anchor →
	// non-anchor edges only. Skip pairs already seen
	// (explicit predicates). Cap synthesised edges per
	// (target, class) to one — the anchor brings the
	// non-anchor under selective control once, additional
	// edges to the same target add no new selectivity.
	//
	// Iterate classes in the same deterministic order as
	// inferTransitiveEqualities so synthesised conjunct
	// sequence is reproducible (M0076-0004 carry).
	classMap := ec.classes()
	roots := make([]columnIdent, 0, len(classMap))
	for root := range classMap {
		roots = append(roots, root)
	}
	sort.SliceStable(roots, func(i, j int) bool {
		return compareColumnIdent(roots[i], roots[j]) < 0
	})

	var added []Expr
	for _, root := range roots {
		members := classMap[root]
		// Split into anchors / non-anchors via SourceTableIdx → bindingIdx → isAnchor.
		anchors := make([]columnIdent, 0, len(members))
		nonAnchors := make([]columnIdent, 0, len(members))
		for _, m := range members {
			cr := columnRefByIdent[m]
			if cr == nil {
				continue
			}
			bIdx, ok := srcToBinding[cr.SourceTableIdx]
			if !ok {
				// SourceTableIdx unknown — treat as non-anchor.
				nonAnchors = append(nonAnchors, m)
				continue
			}
			if isAnchor[bIdx] {
				anchors = append(anchors, m)
			} else {
				nonAnchors = append(nonAnchors, m)
			}
		}
		if len(anchors) == 0 || len(nonAnchors) == 0 {
			continue
		}
		// One synthesised edge per non-anchor target. Prefer
		// the first anchor (already in deterministic order
		// from `classes()`).
		for _, target := range nonAnchors {
			source := anchors[0]
			pair := orderedPair(source, target)
			if seenPairs[pair] {
				continue
			}
			a := columnRefByIdent[source]
			b := columnRefByIdent[target]
			if a == nil || b == nil {
				continue
			}
			added = append(added, &BinaryOp{
				Op:    parser.OpEq,
				Left:  a,
				Right: b,
			})
			seenPairs[pair] = true
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
