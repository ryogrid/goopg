package optimizer

import (
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0134-0010 §4: NOT NULL-driven reduction of `IS NULL` / `IS NOT NULL`
// restriction quals, single-baserel case only. See
// docs/design/m0134-0010-notnull-qual-reduction.md for the full ruling
// design and the deferred (join-qual, inheritance) slices.
//
// The pass is invoked from planSelect (planner.go) at the single point
// where a plain single-table WHERE predicate becomes a `Filter` over the
// base-relation scan built by planScanRangeVar — the branch gated on
// `isSimpleSingle` that falls through when planIndexScanFromWhere declines.
// That gate is goopg's stand-in for upstream's `add_base_clause_to_rel`
// (`postgres/src/backend/optimizer/plan/initsplan.c:2979`), which restricts
// this reduction to plain base-rel quals (`!rte->inh`) — i.e. no joins, no
// inheritance children in scope.

// quantifiedTruth is the tri-state result of classifying whether a resolved
// predicate is provably TRUE, provably FALSE, or neither (must be kept as a
// runtime Filter conjunct) — the outcome of PG's
// restriction_is_always_true/_false pair for a single clause.
type quantifiedTruth int

const (
	truthUnknown quantifiedTruth = iota
	truthTrue
	truthFalse
)

// exprIsNonNullable mirrors PostgreSQL's expr_is_nonnullable
// (postgres/src/backend/optimizer/plan/initsplan.c:3054): true only when e is
// a resolved column reference of the SAME base table (srcIdx) whose
// catalog.Column.NotNull flag is set. Anything else — a different table's
// column, an expression, a constant, an outer-column reference — returns
// false, exactly as upstream's `if (!IsA(expr, Var)) return false;` does.
//
// Upstream additionally checks `var->varnullingrels` (nullability injected by
// an enclosing outer join) and `var->varattno < 0` (system columns are always
// non-nullable). goopg has no varnullingrels equivalent; the single-baserel
// gate this pass is called under means no outer join can be in scope, so that
// check is satisfied structurally — recorded as a deferral for the join-qual
// slice (design doc §5.2). System-column non-nullability (design doc §5.5) is
// also out of scope for this slice: a *ColumnRef never represents a system
// column (CTIDExpr / tableoid have their own node types), so the "return
// false" default already matches upstream's behavior for every case this
// slice can reach.
func exprIsNonNullable(e Expr, tbl *catalog.Table, srcIdx int) bool {
	if tbl == nil {
		return false
	}
	cr, ok := e.(*ColumnRef)
	if !ok {
		return false
	}
	if cr.SourceTableIdx != int16(srcIdx) {
		return false
	}
	if cr.Index < 0 || cr.Index >= len(tbl.Columns) {
		return false
	}
	return tbl.Columns[cr.Index].NotNull
}

// singleClauseTruth classifies one non-OR conjunct: mirrors the NullTest
// branch shared verbatim by restriction_is_always_true (initsplan.c:3091)
// and restriction_is_always_false (initsplan.c:3156). Only a `*IsNullExpr`
// (goopg's NullTest twin, plan.go:283 — Negated==true means IS NOT NULL) over
// a provably-non-nullable operand is ever classified true/false; everything
// else is truthUnknown and must be kept.
//
// Row-valued NullTest operands (`(a,b) IS NULL`, upstream's `argisrow`) are
// never folded by upstream. goopg's IsNullExpr has no row-operand
// representation at all (Operand is a single Expr, never a RowExpr wrapped
// specially for this purpose) — see notnull_qual_reduce_test.go for the
// assertion that a row-valued IS NULL simply falls through singleClauseTruth
// as an ordinary (non-Var) operand and is therefore never folded, matching
// upstream's guard without needing one.
func singleClauseTruth(e Expr, tbl *catalog.Table, srcIdx int) quantifiedTruth {
	nt, ok := e.(*IsNullExpr)
	if !ok {
		return truthUnknown
	}
	if !exprIsNonNullable(nt.Operand, tbl, srcIdx) {
		return truthUnknown
	}
	if nt.Negated {
		return truthTrue // IS NOT NULL over a non-nullable operand
	}
	return truthFalse // IS NULL over a non-nullable operand
}

// conjunctTruth classifies one top-level AND conjunct, recursing into OR
// sub-clauses exactly as restriction_is_always_true/_false do
// (initsplan.c:3122-3143 / :3187-3212 — both walk `orclause->args` and defer
// to the very same pair of functions per arm, not a separate AND-aware
// recursion).
func conjunctTruth(e Expr, tbl *catalog.Table, srcIdx int) quantifiedTruth {
	if bin, ok := e.(*BinaryOp); ok && bin.Op == parser.OpOr {
		return orClauseTruth(bin, tbl, srcIdx)
	}
	return singleClauseTruth(e, tbl, srcIdx)
}

// orClauseTruth applies PG's ASYMMETRIC OR quantifiers: the whole OR is
// dropped (truthTrue) the moment ANY arm is provably true; it folds to FALSE
// only when EVERY arm is provably false; otherwise it is kept byte-unmodified
// (truthUnknown) — upstream deliberately does not prune individual
// disprovable arms here (initsplan.c:3187-3196 comment, carried into the
// design doc §2).
func orClauseTruth(bin *BinaryOp, tbl *catalog.Table, srcIdx int) quantifiedTruth {
	arms := flattenOrConjuncts(bin)
	allFalse := true
	for _, a := range arms {
		switch conjunctTruth(a, tbl, srcIdx) {
		case truthTrue:
			return truthTrue
		case truthFalse:
			// still a candidate for "ALL false"
		default:
			allFalse = false
		}
	}
	if allFalse {
		return truthFalse
	}
	return truthUnknown
}

// reduceNotNullQuals mirrors initsplan.c's per-conjunct application of
// restriction_is_always_true/restriction_is_always_false to a single-baserel
// WHERE predicate, already split at add_base_clause_to_rel's AND boundaries
// (goopg splits at the same boundary here since it has no separate
// baserestrictinfo list to walk).
//
// Returns (rewritten, alwaysFalse):
//   - alwaysFalse==true: the whole predicate is provably FALSE (some
//     conjunct's restriction_is_always_false fired); rewritten is nil and
//     must be ignored — the caller wraps the scan in
//     Result{OneTimeFilter: BooleanConst{false}}.
//   - alwaysFalse==false, rewritten==nil: every conjunct was provably TRUE
//     (dropped) — the caller must emit a bare scan with NO Filter at all.
//   - alwaysFalse==false, rewritten!=nil: the surviving conjuncts,
//     re-assembled as an AND chain — the caller keeps the Filter, using
//     rewritten as its Predicate.
func reduceNotNullQuals(pred Expr, tbl *catalog.Table, srcIdx int) (Expr, bool) {
	conjuncts := flattenAndConjuncts(pred)
	kept := make([]Expr, 0, len(conjuncts))
	for _, c := range conjuncts {
		switch conjunctTruth(c, tbl, srcIdx) {
		case truthTrue:
			continue // dropped: restriction_is_always_true
		case truthFalse:
			return nil, true // restriction_is_always_false: whole predicate FALSE
		default:
			kept = append(kept, c)
		}
	}
	if len(kept) == 0 {
		return nil, false
	}
	return andConjuncts(kept), false
}

// identityResultTargets builds the pass-through ColumnRef list for a
// Result node that replaces a Filter over `schema` — used when
// reduceNotNullQuals finds the predicate provably FALSE and the scan must be
// wrapped in Result{OneTimeFilter: false} rather than a Filter, while still
// emitting the same output shape the Filter it replaces would have. Mirrors
// nodeResult.c's `outerPlan(plan)` variant already used by the const-arg
// min/max rewrite (planner.go, the `inner.Targets` built alongside
// `otf := &IsNullExpr{...}` for `SELECT max(100) FROM t`).
func identityResultTargets(schema Schema) []Expr {
	targets := make([]Expr, len(schema))
	for i, c := range schema {
		targets[i] = &ColumnRef{Index: i, Name: c.Name, Type: c.Type, SourceTableIdx: c.SourceTableIdx}
	}
	return targets
}

// flattenAndConjuncts splits an AND tree into its top-level conjuncts.
// `(a AND b) AND c` returns [a, b, c]; a non-AND input returns [e]. This is
// the resolved-Expr (post resolveExpr) twin of qual_canonical.go's
// flattenAndBranches, which operates on the pre-resolution parser.Expr tree.
func flattenAndConjuncts(e Expr) []Expr {
	bin, ok := e.(*BinaryOp)
	if !ok || bin.Op != parser.OpAnd {
		return []Expr{e}
	}
	out := flattenAndConjuncts(bin.Left)
	return append(out, flattenAndConjuncts(bin.Right)...)
}

// flattenOrConjuncts is flattenAndConjuncts's OR twin.
func flattenOrConjuncts(e Expr) []Expr {
	bin, ok := e.(*BinaryOp)
	if !ok || bin.Op != parser.OpOr {
		return []Expr{e}
	}
	out := flattenOrConjuncts(bin.Left)
	return append(out, flattenOrConjuncts(bin.Right)...)
}

// andConjuncts re-associates surviving conjuncts into a left-deep AND tree,
// the resolved-Expr twin of qual_canonical.go's andChain.
func andConjuncts(parts []Expr) Expr {
	if len(parts) == 0 {
		return nil
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out = &BinaryOp{Op: parser.OpAnd, Left: out, Right: p}
	}
	return out
}
