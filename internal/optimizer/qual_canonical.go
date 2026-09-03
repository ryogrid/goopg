package optimizer

import "github.com/goopg/goopg/internal/parser"

// PG's qual canonicalisation — `canonicalize_qual` / `find_duplicate_ors` /
// `process_duplicate_ors` (postgres/src/backend/optimizer/prep/prepqual.c),
// which upstream runs from `preprocess_expression` on every WHERE clause
// BEFORE the qual is distributed to baserestrictinfo / joinquals.
//
// WHAT IT DOES. An OR whose every arm is an AND sharing some conjunct is
// rewritten by pure boolean algebra:
//
//	(A ∧ B) ∨ (A ∧ C)   →   A ∧ (B ∨ C)
//
// The hoisted conjuncts are `process_duplicate_ors`'s "winners". Where an arm
// degenerates to nothing — `(A ∧ B) ∨ A` — the whole OR reduces to `A`, since
// the extra conditions in the other arms cannot exclude anything A admits.
//
// WHY GOOPG NEEDS IT (M0127-P5.6-g-iv). TPC-H Q19's entire WHERE is one OR of
// three conjunctions, and each conjunction repeats the join clause
// `p_partkey = l_partkey`. Without this pass:
//
//   - The join clause never becomes a top-level qual. M0058-0004 worked around
//     that for JOIN-EDGE derivation only (`commonEquijoinsAcrossOr`,
//     joinorder.go), which is the same intersection computed for one purpose
//     and thrown away — the qual itself stayed an opaque OR, so the planner
//     emitted a Hash Join whose Hash Cond had no corresponding conjunct.
//   - The estimator therefore priced the join clause TWICE: once as the
//     equi-join key (`l·r/nd`, i.e. 1/200 000 here) and once more inside each
//     OR arm, where `eqOpSelectivity` sees two columns and no constant and
//     falls back to DEFAULT_EQ_SEL. Three arms at ~5·10⁻⁹ each drove
//     {lineitem, part} to the 1-row clamp against an actual 131 — a 131×
//     under-estimate where PG 18.3 estimates 116 against 112 (1.0×). That
//     126.5× excess is the parity violation P5.6-g-iii's instrument exposed and
//     this pass is the fix for.
//   - The conjuncts common to all three arms that reference ONE relation —
//     `l_shipmode IN ('AIR','AIR REG')`, `l_shipinstruct = 'DELIVER IN PERSON'`,
//     `p_size >= 1` — were likewise trapped inside the OR, so neither scan
//     could be filtered and neither restriction was priced anywhere. PG's own
//     Q19 plan shows all three hoisted onto the scans, exactly as this pass
//     produces them.
//
// WHAT IS DELIBERATELY NOT REPRODUCED. `find_duplicate_ors` also drops constant
// TRUE/FALSE/NULL inputs as it recurses, with different rules for WHERE quals
// and CHECK constraints (its `is_check` argument). goopg folds constants in a
// separate pass (`FoldConstants`, foldconst.go) whose NULL handling is already
// settled, and duplicating that logic here would give two passes an opportunity
// to disagree about three-valued logic. Ledgered 2026-08-05.
//
// The rewrite is semantics-preserving for three-valued logic as well as
// two-valued: distributing a conjunct over an OR neither adds nor removes rows,
// and it cannot turn NULL into FALSE, because `A ∧ (B ∨ C)` and
// `(A ∧ B) ∨ (A ∧ C)` are the same expression in Kleene logic.

// canonicalizeQual is `find_duplicate_ors` over goopg's BINARY And/Or tree.
//
// Upstream's AND/OR nodes are n-ary (`BoolExpr.args`) and it keeps them flat
// with `pull_ands`/`pull_ors`; goopg's `parser.BinaryOp` is strictly binary, so
// flatness is obtained by flattening on the way in (`flattenAndBranches`,
// `flattenOrBranches`) and re-associating on the way out. The two
// representations agree on every question this pass asks, because both are
// asking about the SET of conjuncts.
//
// Returns e unchanged when there is nothing to hoist, including the nil case,
// so callers can apply it unconditionally. It is idempotent: a second call on
// its own output finds no winners in any OR it already reduced.
func canonicalizeQual(e parser.Expr) parser.Expr {
	if e == nil {
		return nil
	}
	bin, ok := e.(*parser.BinaryOp)
	if !ok {
		return e
	}
	switch bin.Op {
	case parser.OpOr:
		branches := flattenOrBranches(bin)
		reduced := make([]parser.Expr, 0, len(branches))
		for _, br := range branches {
			// Recurse first, exactly as upstream does, so an inner OR is
			// already reduced when this level intersects the arms — and so a
			// sub-OR pulled up by that reduction is flattened by the
			// `flattenOrBranches` below rather than hiding a winner.
			reduced = append(reduced, canonicalizeQual(br))
		}
		flat := make([]parser.Expr, 0, len(reduced))
		for _, br := range reduced {
			flat = append(flat, flattenOrBranches(br)...)
		}
		return processDuplicateOrs(flat)
	case parser.OpAnd:
		l := canonicalizeQual(bin.Left)
		r := canonicalizeQual(bin.Right)
		if l == bin.Left && r == bin.Right {
			return bin
		}
		return &parser.BinaryOp{Op: parser.OpAnd, Left: l, Right: r}
	default:
		return e
	}
}

// processDuplicateOrs is `process_duplicate_ors` (prepqual.c:359).
//
// The reference list is the SHORTEST arm's conjunct set — "obviously, any
// subclause not in this clause isn't in all the clauses" — and an arm that is
// not an AND at all is a one-element set, which necessarily wins as shortest
// and short-circuits the scan.
func processDuplicateOrs(branches []parser.Expr) parser.Expr {
	if len(branches) == 0 {
		return nil
	}
	if len(branches) == 1 {
		return branches[0]
	}

	arms := make([][]parser.Expr, len(branches))
	armKeys := make([]map[string]bool, len(branches))
	reference := -1
	for i, br := range branches {
		arms[i] = flattenAndBranches(br)
		armKeys[i] = make(map[string]bool, len(arms[i]))
		for _, c := range arms[i] {
			armKeys[i][strictParserExprKey(c)] = true
		}
		if len(arms[i]) == 1 {
			// A non-AND arm: nothing outside it can be common, and upstream
			// breaks out of the reference scan here for the same reason.
			reference = i
			break
		}
		if reference < 0 || len(arms[i]) < len(arms[reference]) {
			reference = i
		}
	}

	// Winners, in the reference arm's order (upstream builds the list the same
	// way, and the order decides the rebuilt AND's shape). Duplicates within
	// the reference arm are eliminated as `list_union` does upstream.
	var winners []parser.Expr
	seen := make(map[string]bool, len(arms[reference]))
	for _, ref := range arms[reference] {
		key := strictParserExprKey(ref)
		if seen[key] {
			continue
		}
		seen[key] = true
		win := true
		for i := range arms {
			if !armKeys[i][key] {
				win = false
				break
			}
		}
		if win {
			winners = append(winners, ref)
		}
	}
	if len(winners) == 0 {
		// Nothing common: the OR is returned as it came in. Rebuilding from
		// the (already recursed) branches rather than returning the original
		// node is what propagates any reduction an inner OR made.
		return orChain(branches)
	}

	// The reduced OR: each arm minus the winners. An arm that loses everything
	// is upstream's degenerate case — `(A ∧ B) ∨ A` is just `A` — and it
	// discards the OR entirely rather than leaving an empty disjunct, which
	// would be FALSE and would wrongly reject every row.
	winnerKeys := make(map[string]bool, len(winners))
	for _, w := range winners {
		winnerKeys[strictParserExprKey(w)] = true
	}
	var neworlist []parser.Expr
	degenerate := false
	for i := range arms {
		var rest []parser.Expr
		for _, c := range arms[i] {
			if !winnerKeys[strictParserExprKey(c)] {
				rest = append(rest, c)
			}
		}
		if len(rest) == 0 {
			degenerate = true
			break
		}
		neworlist = append(neworlist, andChain(rest))
	}

	out := winners
	if !degenerate {
		if len(neworlist) == 1 {
			out = append(out, neworlist[0])
		} else {
			out = append(out, orChain(neworlist))
		}
	}
	return andChain(out)
}

// flattenAndBranches splits an AND tree into its conjuncts. `(a AND b) AND c`
// returns [a, b, c]; a non-AND input returns [e]. The OR twin is
// `flattenOrBranches` (joinorder.go).
func flattenAndBranches(e parser.Expr) []parser.Expr {
	bin, ok := e.(*parser.BinaryOp)
	if !ok || bin.Op != parser.OpAnd {
		return []parser.Expr{e}
	}
	out := flattenAndBranches(bin.Left)
	return append(out, flattenAndBranches(bin.Right)...)
}

// andChain re-associates conjuncts into goopg's binary AND tree, left-deep, so
// `flattenAndBranches` round-trips the list it was given.
func andChain(parts []parser.Expr) parser.Expr {
	return boolChain(parts, parser.OpAnd)
}

// orChain is andChain's disjunctive twin.
func orChain(parts []parser.Expr) parser.Expr {
	return boolChain(parts, parser.OpOr)
}

func boolChain(parts []parser.Expr, op parser.OpCode) parser.Expr {
	if len(parts) == 0 {
		return nil
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out = &parser.BinaryOp{Op: op, Left: out, Right: p}
	}
	return out
}

// flattenOrBranches returns the operands of a left- or right-deep OR chain as a
// flat list, so `a OR b OR c` yields three branches rather than a nested pair.
//
// take2 P3-12 moved this here from joinorder.go, which was deleted with the
// pre-search greedy FROM reorder. This file is its only consumer.
func flattenOrBranches(e parser.Expr) []parser.Expr {
	bin, ok := e.(*parser.BinaryOp)
	if !ok || bin.Op != parser.OpOr {
		return []parser.Expr{e}
	}
	out := flattenOrBranches(bin.Left)
	out = append(out, flattenOrBranches(bin.Right)...)
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
