package optimizer

// Sublink pull-up admissibility — M0142-0008a-3i-route-a step 1.
//
// This file answers ONE question about an unplanned sublink body: may it be
// flattened into the enclosing query's join list, or must it stay a plan of
// its own? It is goopg's port of `is_simple_subquery`
// (postgres/src/backend/optimizer/prep/prepjointree.c:1807), the gate
// `pull_up_subqueries` (:1083) consults before pulling a subquery RTE into
// the parent's range table.
//
// Nothing calls it yet, by design. Step 1 of the route task is the
// prerequisite pair — retain the parse tree (ExistsExpr.Subquery) and be able
// to classify it — and both halves must be provably inert before the splice
// that consumes them is attempted. The repository has landed groundwork under
// exactly this posture before: `pathkeysCountContainedIn` (M0141-S7) and
// `costIncrementalSort` (M0141-S7b) both shipped with zero production callers
// and a unit test pinning the formula against upstream.

import "github.com/goopg/goopg/internal/parser"

// sublinkBodyIsSimple reports whether an unplanned sublink body can be pulled
// up into the enclosing join list.
//
// Every refusal below is one of upstream's, in upstream's order. The
// upstream comment explains the shape of the rule and it holds here verbatim:
// a body that groups, aggregates, sorts, limits, de-duplicates or locks
// computes something the enclosing query's join search cannot reproduce by
// merely joining the body's relations, so its result is not substitutable for
// its inputs.
//
//	if (subquery->setOperations) return false;
//	if (subquery->hasAggs || subquery->hasWindowFuncs ||
//	    subquery->hasTargetSRFs || subquery->groupClause ||
//	    subquery->groupingSets || subquery->havingQual ||
//	    subquery->sortClause || subquery->distinctClause ||
//	    subquery->limitOffset || subquery->limitCount ||
//	    subquery->hasForUpdate || subquery->cteList) return false;
//
// Two upstream arms are NOT ported, and their absence is safe here rather
// than merely unimplemented:
//
//   - `rte->security_barrier`. That flag lives on the range-table entry PG
//     built for a subquery-in-FROM. A sublink body reaching this predicate
//     came from `EXISTS (...)` / `IN (...)` in a qual, where upstream's own
//     `convert_EXISTS_sublink_to_join` (subselect.c:1450) synthesises the RTE
//     rather than inheriting a view's — so the flag is false by construction
//     in the case this predicate serves. A body that reads a security-barrier
//     view still has that view's own RTE INSIDE it, which this pull-up never
//     touches.
//   - the `rte->lateral` block. Same reasoning: the RTE is synthesised and
//     not marked LATERAL. goopg's correlation references are
//     `OuterColumnRef{Level:1}` in the PLANNED body, a representation this
//     predicate never sees — it reads the parse tree. Re-basing those
//     references is step 2's obligation and is recorded as such on the task;
//     it is deliberately not disguised as an admissibility question.
//
// The VALUES case is refused rather than accepted: `VALUES` bodies carry no
// FROM items to splice, so "simple" would be true but vacuous, and the caller
// that does not yet exist would have nothing to do with the answer.
//
// Step 2 adds two production arms on top of upstream's list:
//
//   - every FROM item must be a plain table reference (`sublinkBodyFromIsFlat`)
//     — the splice re-emits the body's leaves as base relations of the outer
//     search, so a derived table, a table function, a LATERAL item or an
//     outer/USING join inside the body has no leaf to stand in for;
//   - the body's resolved quals must not contain another sublink
//     (`flatBodyTreeOK`'s conjunct scan) — a nested sublink's `.Plan` reads
//     `OuterColumnRef{Level:1}` in body-concat coordinates that only stay
//     valid while the body evaluates as one unit, which pooling its quals
//     onto individual leaves would break.
func sublinkBodyIsSimple(s *parser.SelectStmt) bool {
	if s == nil {
		return false
	}
	// A grouping node stands for `( operand )` and holds no clauses of its
	// own; its value is the operand's. Refuse rather than descend — the
	// trailing ORDER BY/LIMIT it may carry belongs to the node ABOVE the
	// parenthesised query (see parser.SelectStmt.SetOpOperand), so the
	// answer for the pair is not the answer for the operand.
	if s.SetOpOperand != nil {
		return false
	}
	// `subquery->setOperations`.
	if s.SetOp != nil {
		return false
	}
	// `subquery->cteList`.
	if s.With != nil {
		return false
	}
	// `subquery->groupClause` / `groupingSets` / `havingQual`.
	if len(s.GroupBy) > 0 || s.Having != nil {
		return false
	}
	// `subquery->sortClause`.
	if len(s.OrderBy) > 0 {
		return false
	}
	// `subquery->distinctClause` — goopg splits plain DISTINCT from
	// DISTINCT ON across two fields; upstream has one clause for both.
	if s.Distinct || len(s.DistinctOn) > 0 {
		return false
	}
	// `subquery->limitCount` / `limitOffset`. WithTies cannot be set without
	// a Limit, but it is checked anyway so the arm does not silently depend
	// on that invariant holding.
	if s.Limit != nil || s.Offset != nil || s.WithTies {
		return false
	}
	// `subquery->hasForUpdate`. Upstream's comment: pulling up would make
	// the locking occur semantically higher than it should.
	if len(s.Locking) > 0 {
		return false
	}
	// A VALUES body has no FROM items to splice — see the header.
	if len(s.ValuesRows) > 0 {
		return false
	}
	// A body with no relation at all (`SELECT 1` with no FROM) likewise
	// offers the join search nothing.
	if len(s.From) == 0 && len(s.FromExprs) == 0 {
		return false
	}
	// `subquery->hasAggs` / `hasWindowFuncs` / `hasTargetSRFs`. goopg's
	// parser sets no such summary flags, so they are derived here from the
	// only two places a sublink body can carry one: its target list and its
	// WHERE. (GROUP BY/HAVING/ORDER BY are already refused above, so an
	// aggregate hiding in one of those cannot reach this point.) The SRF arm
	// is deliberately conservative — see selectListOrQualDisqualifies.
	if selectListOrQualDisqualifies(s) {
		return false
	}
	// Step 2: every FROM item must be a plain table reference — see
	// sublinkBodyFromIsFlat.
	if !sublinkBodyFromIsFlat(s) {
		return false
	}
	return true
}

// sublinkBodyFromIsFlat reports whether every FROM item in the body is a bare
// catalog table — the only thing step 2's splice can re-emit as a real outer
// join-list leaf. This is upstream's `pull_up_subqueries` shape constraint
// restated for goopg's AST: upstream walks the subquery's jointree and pulls
// each `RTE_RELATION` up, so anything that is not a bare relation entry —
// derived table, table function, LATERAL item — is not pullable.
//
// A USING/NATURAL join inside the body is refused for a goopg-specific
// reason: merged join columns share one output slot (`JOIN USING` drops the
// right column from the combined schema), so the body's own leaf-concat
// coordinates no longer line up with a plain scan-per-leaf decomposition.
// An OUTER join inside the body is refused because pooling its ON quals into
// a flat conjunct list would silently convert it to an inner join.
func sublinkBodyFromIsFlat(s *parser.SelectStmt) bool {
	for _, rv := range s.From {
		if !rangeVarIsPlainTable(rv) {
			return false
		}
	}
	for _, f := range s.FromExprs {
		if !rangeVarIsPlainTable(f.Base) {
			return false
		}
		for _, j := range f.Joins {
			if j.Type != parser.JoinInner && j.Type != parser.JoinCross {
				return false
			}
			if j.Natural || len(j.Using) > 0 {
				return false
			}
			if !rangeVarIsPlainTable(j.Right) {
				return false
			}
		}
	}
	return true
}

// rangeVarIsPlainTable is true only for `name [AS alias]` — a real relation
// reference. Anything else that can occupy a RangeVar slot (derived table,
// table function, LATERAL marker) disqualifies the whole body.
func rangeVarIsPlainTable(rv parser.RangeVar) bool {
	return rv.Name != "" && rv.Subquery == nil && rv.TableFunc == nil && !rv.Lateral
}

// selectListOrQualDisqualifies reports whether the body's target list or WHERE
// clause carries parse-tree evidence for one of upstream's three summary
// flags: `hasAggs`, `hasWindowFuncs` or `hasTargetSRFs`.
//
// The first two are decided precisely — an aggregate name, an `OVER`, a
// `WITHIN GROUP` or a `FILTER` decoration.
//
// `hasTargetSRFs` is decided CONSERVATIVELY, and the asymmetry is deliberate.
// goopg has no `proretset` equivalent for built-in functions: user routines
// carry `catalog.Routine.ReturnsSet`, but every built-in set-returning
// function (`generate_series`, `unnest`, the `json_*` family, …) is handled by
// name in the planner, and the only name predicate that exists
// (`isNestedSRFName`, planner.go:5364) covers `generate_series` alone. There
// is therefore no lookup — with or without a catalog argument — that can
// positively classify an arbitrary call as non-set-returning.
//
// So ANY function call in the TARGET LIST disqualifies the body. That is
// blunter than upstream, which pulls up `select upper(a) from t` happily, and
// the cost is real but one-directional: goopg forgoes pull-up opportunities
// it could legally take, and never pulls up a body whose row count the
// enclosing join cannot reproduce. A wrong answer in the other direction
// would silently change results, which is not a trade this predicate is
// allowed to make.
//
// The WHERE clause is NOT subject to the blunt rule — a set-returning call in
// a qual is an error in PG, so the only thing to look for there is the
// aggregate/window evidence.
//
// The narrowing fix, when someone needs it: a built-in SRF registry that
// `isNestedSRFName` and this predicate share. Tracked in the deferral ledger.
func selectListOrQualDisqualifies(s *parser.SelectStmt) bool {
	found := false
	aggOrWindow := func(fc *parser.FuncCall) error {
		if fc == nil {
			return nil
		}
		if fc.Over != nil || len(fc.WithinGroup) > 0 || fc.Filter != nil || isAggregateFuncName(fc) {
			found = true
		}
		return nil
	}
	// Target list: any call at all, for the reason in the header.
	anyCall := func(fc *parser.FuncCall) error {
		if fc != nil {
			found = true
		}
		return nil
	}
	for _, t := range s.Targets {
		_ = walkExpr(t.Expr, anyCall)
		if found {
			return true
		}
	}
	_ = walkExpr(s.Where, aggOrWindow)
	return found
}

// ---------------------------------------------------------------------------
// M0142-0008a-3i-route-a step 2 — flattening decomposition.
//
// When `sublinkBodyIsSimple` accepts a retained body, the unnest rewrites the
// sublink into a SEMI/ANTI `*Join` marked `FlattenedRHS`. The marker licenses
// `extractSearchLeaves` (joinsearchseam.go) to decompose the join's Right
// subtree into REAL scan leaves and a pool of body quals — PG's
// `pull_up_subqueries` outcome: the body's relations become participants of
// the enclosing join search instead of one opaque planned subtree.
//
// `decomposeFlatBodyTree` is the shared predicate both sides consult: the
// unnest sets the marker only when it reports ok, and the seam calls it again
// to obtain the leaves and the pooled conjuncts. The tree it accepts is the
// shape a planned simple body takes after `unwrapTrivialWrappers` —
// `Filter(body quals)` over `Join{Inner|Cross}` links over `*SeqScan` leaves,
// plus an optional root `*Project` carrying the IN-subquery's one target.
//
// Anything else declines, which is always safe: the join simply keeps its
// opaque RHS and today's SubPlan-equivalent plan is what runs.
// ---------------------------------------------------------------------------

// decomposeFlatBodyTree walks a planned sublink body and returns its scan
// leaves in walk order, every Filter / inner-join Predicate conjunct pooled
// (in the body's own leaf-concat column coordinates — the space the leaves'
// Output() schemas lay out), and, when `wantTarget` is set, the single target
// expression a root `*Project` carries (an IN body's comparison column).
//
// `body` is the subtree the flattening consumer should use as the RHS leaf
// source: `n` itself, minus a root non-identity `*Project` extracted for the
// target. The returned `body`'s Output() is verified position-identical to
// the leaf-concatenation — the coordinate convention the seam's
// `rebaseSemiAntiChainQual` assumes when it maps the link pred's inner
// operand (`outerWidth + i`) onto the flattened leaves' spans.
//
// ok is false for:
//   - any node that is not a SeqScan / Filter / Inner|Cross Join / Project
//     (IndexScan, NestedLoopIndexJoin, Gather, Aggregate, Sort, Limit, …) —
//     a non-trivial body plan stays an opaque leaf;
//   - a `Filter` marked `LeafLocal` whose child is not a bare `*SeqScan` —
//     leaf-local coordinates equal the subtree's own space only when the
//     subtree IS one leaf, so the same `+base` shift lifts it correctly;
//   - a Join carrying `Lateral` or a non-inner/cross type;
//   - a non-root `*Project` that is not a column-position identity — a
//     qual pooled above it would be written in its output space, which is
//     not the leaf-concat space every other conjunct uses;
//   - a pooled conjunct that still contains a sublink expression — its
//     `.Plan`'s `OuterColumnRef{Level:1}` reads the enclosing row in
//     body-concat coordinates, valid only while the body evaluates as one
//     unit (see the file-header step-2 note);
//   - `wantTarget` with a root Project that does not carry exactly one
//     target, or a non-Project root whose single output column is not
//     leaf-concat position 0 (the IN target cannot be recovered);
//   - `body`'s Output() not matching the leaf-concatenation column for
//     column — the seam's inner-operand convention would misattribute
//     every inner reference.
func decomposeFlatBodyTree(n Node, wantTarget bool) (leaves []Node, quals []Expr, target Expr, body Node, ok bool) {
	if n == nil {
		return nil, nil, nil, nil, false
	}
	body = n
	var walk func(cur Node, base int) bool
	// Every pooled conjunct is written in its own subtree's local
	// coordinates — a Filter's Predicate indexes its child's output, a
	// Join's Predicate indexes left.Output()++right.Output() — which is the
	// leaf-concat range [base, base+subtreeWidth) of THAT subtree. Shifting
	// each conjunct by the subtree's own base lands it in the body's global
	// leaf-concat space, for bushy bodies exactly as for left-deep ones.
	addQuals := func(pred Expr, base int) bool {
		for _, c := range splitAnd(pred) {
			if c == nil {
				continue
			}
			if b, isBool := c.(*BooleanConst); isBool && b.Value {
				continue
			}
			if exprHasSublinkPlan(c) {
				return false
			}
			shifted, okShift := rebaseChainQual(c, base)
			if !okShift {
				return false
			}
			quals = append(quals, shifted)
		}
		return true
	}
	walk = func(cur Node, base int) bool {
		switch x := cur.(type) {
		case *Filter:
			// A LeafLocal predicate indexes the leaf scan's own schema
			// ([0, leafWidth)) rather than its child's cumulative
			// output (M0077-0001). For a filter attached directly
			// above a bare leaf those are the same space — the subtree
			// IS one leaf — so the same `+base` shift lifts it. The
			// flag is only ever set on leaf-attached wrappers
			// (inner_join_qual_pushdown.go's innerJoinPushLeafScan);
			// a LeafLocal filter over anything else mixes spaces the
			// shift cannot repair — decline rather than guess.
			if x.LeafLocal {
				if _, isScan := x.Child.(*SeqScan); !isScan {
					return false
				}
			}
			if !addQuals(x.Predicate, base) {
				return false
			}
			return walk(x.Child, base)
		case *Join:
			if x.Lateral {
				return false
			}
			switch x.Type {
			case JoinTypeCross:
				if x.Predicate != nil {
					return false
				}
			case JoinTypeInner:
				if x.Predicate != nil && !addQuals(x.Predicate, base) {
					return false
				}
			default:
				return false
			}
			if !walk(x.Left, base) {
				return false
			}
			return walk(x.Right, base+len(x.Left.Output()))
		case *Project:
			// Non-root Projects (the root is peeled before the walk) must be
			// column-position identities — anything else makes its parents'
			// quals non-leaf-concat. The root's own non-identity Project is
			// handled by the caller-extraction path below.
			if !projectIsPositionalIdentity(x) {
				return false
			}
			return walk(x.Child, base)
		case *SeqScan:
			leaves = append(leaves, x)
			return true
		default:
			return false
		}
	}
	root := n
	if p, isProj := n.(*Project); isProj {
		if !wantTarget {
			// For EXISTS no target is needed, and a non-identity root
			// Project makes n.Output() diverge from the leaf-concat the
			// link pred's inner operands are written against — decline
			// rather than track a second space. The scope flag itself is
			// not disqualifying: the IN paths re-wrap a stripped body in
			// an IsolatedScope positional-identity Project so the RHS
			// keeps the NLI / pushdown protections the body's original
			// root project carried (the `outerWidth + i` inner-operand
			// convention only requires the OUTPUT to be leaf-concat,
			// which an identity project preserves). A non-identity or
			// renaming IsolatedScope project still fails the
			// schemaIsLeafConcat post-check below.
			if !projectIsPositionalIdentityAnyScope(p) {
				return nil, nil, nil, nil, false
			}
		} else {
			// The IN comparison column: exactly one target, written in the
			// child's (leaf-concat) coordinates — the rebase the caller
			// applies lands it in merged outer++inner space.
			if len(p.Targets) != 1 {
				return nil, nil, nil, nil, false
			}
			target = p.Targets[0]
			body = p.Child
		}
		root = p.Child
	}
	if !walk(root, 0) || len(leaves) == 0 {
		return nil, nil, nil, nil, false
	}
	if wantTarget && target == nil {
		// No root Project carried the comparison column — recoverable only
		// when the body's whole output is one leaf-concat column (a bare
		// single-column scan), where the target is that column itself.
		if len(body.Output()) != 1 {
			return nil, nil, nil, nil, false
		}
		target = &ColumnRef{Index: 0, Name: body.Output()[0].Name, Type: body.Output()[0].Type}
	}
	// The alignment post-check: body's Output() must be position-identical
	// to the leaf-concatenation. Anything else — a residual non-identity
	// projection, a merged USING column, a width that drifted — makes the
	// seam's `outerWidth + i` inner-operand convention misattribute every
	// inner reference, so the body stays opaque.
	if !schemaIsLeafConcat(body.Output(), leaves) {
		return nil, nil, nil, nil, false
	}
	return leaves, quals, target, body, true
}

// schemaIsLeafConcat reports whether a node's output schema is exactly the
// concatenation of the decomposed leaves' output schemas, in walk order —
// the post-decomposition coordinate check decomposeFlatBodyTree owes the
// seam. Column identity is Name + SourceTableIdx, the same pair
// findColumnIndexByNameAndSource disambiguates by.
func schemaIsLeafConcat(out Schema, leaves []Node) bool {
	pos := 0
	for _, lf := range leaves {
		ls := lf.Output()
		if pos+len(ls) > len(out) {
			return false
		}
		for i, c := range ls {
			o := out[pos+i]
			if o.Name != c.Name || o.SourceTableIdx != c.SourceTableIdx {
				return false
			}
		}
		pos += len(ls)
	}
	return pos == len(out)
}

// projectIsPositionalIdentityAnyScope is projectIsPositionalIdentity
// minus the IsolatedScope refusal: the leaf-concat convention only
// requires that the project republish its child's columns 1:1, which an
// IsolatedScope identity project (the same shape planner.go's view-rename
// wrapper and the step-2 flat-body re-wrap both use) still does. Kept
// separate rather than parameterised so the strict variant's call sites —
// which use IsolatedScope as a "different scope, do not walk in" signal —
// keep their meaning.
func projectIsPositionalIdentityAnyScope(p *Project) bool {
	if p == nil || p.Child == nil {
		return false
	}
	out := p.Output()
	child := p.Child.Output()
	if len(out) == 0 || len(out) != len(child) || len(p.Targets) != len(out) {
		return false
	}
	for j, t := range p.Targets {
		cr, ok := t.(*ColumnRef)
		if !ok || cr.Index != j {
			return false
		}
	}
	return true
}

// flatBodyScopeProject re-wraps a stripped flat body in an
// IsolatedScope positional-identity Project — the same wrapper shape the
// body's own root Project carried before decomposeFlatBodyTree peeled it
// for the target, and the same one planner.go's view-rename wrapper
// emits. The re-wrap is NOT decorative: bare `Filter{SeqScan}` /
// `SeqScan` leaves are exactly the shape nl_index_join.go's SEMI/ANTI
// candidate detection unwraps, and before step 2 every IN-unnested
// inner plan was an IsolatedScope Project and therefore never
// NLI-eligible (Q16's NOT IN must keep its Hash Anti Join). The
// wrapper's positional targets make Output() the leaf-concat the
// executor's right-row pad and the seam's `outerWidth + i`
// inner-operand convention both assume, while the IsolatedScope flag
// restores the NLI / qual-pushdown / posmap protections the original
// project provided. decomposeFlatBodyTree's wantTarget=false root arm
// accepts it back through projectIsPositionalIdentityAnyScope.
func flatBodyScopeProject(body Node) *Project {
	out := body.Output()
	targets := make([]Expr, len(out))
	for i, c := range out {
		targets[i] = &ColumnRef{pos: body.Pos(), Index: i, Name: c.Name, Type: c.Type}
	}
	return &Project{
		pos:           body.Pos(),
		Child:         body,
		Targets:       targets,
		schema:        append(Schema(nil), out...),
		IsolatedScope: true,
	}
}

// exprHasSublinkPlan reports whether an expression contains any sublink
// node — the coordinate-sensitive case decomposeFlatBodyTree must refuse.
// A planned sublink's `.Plan` reads `OuterColumnRef{Level:1}` in
// body-concat coordinates that only stay valid while the body evaluates
// as one unit — once the body is decomposed into search leaves those
// coordinates have no frame. Detection goes through ExprSubplans (the
// exprChildSlots slotInnerPlan/slotSubqRow enumeration), so every sublink
// kind the traversal layer knows about is covered without a new
// hand-written type switch, while an `InExpr` that is merely an
// `IN (…)` list — same-scope slots only — correctly does not trip it.
func exprHasSublinkPlan(e Expr) bool {
	found := false
	walkExprTree(e, func(x Expr) {
		if len(ExprSubplans(x)) > 0 {
			found = true
		}
	})
	return found
}
