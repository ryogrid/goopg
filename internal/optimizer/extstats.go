// Planner consumption of extended statistics (functional dependencies).
//
// B-05b (take3 P1-23, TODO_ALL). Consumes B-05a, which builds the rows:
// dependency blobs are written to pg_statistic_ext_data (3429) by
// internal/executor's buildAndStoreExtStatistics, with attnums in the
// catalog Ordinal+1 convention and degrees in [0, 1].
//
// Oracle:
//   - postgres/src/backend/statistics/dependencies.c:
//     dependencies_clauselist_selectivity, clauselist_apply_dependencies,
//     find_strongest_dependency, dependency_is_compatible_clause.
//   - postgres/src/backend/statistics/extended_stats.c:
//     statext_clauselist_selectivity, choose_best_statistics.
//   - postgres/src/backend/optimizer/path/clausesel.c:
//     clauselist_selectivity_ext (the single call site: extended statistics
//     first, estimatedclauses bitmap out, per-clause fallback skipping
//     consumed clauses, range-query pairing over the remainder).
//
// LAYERING. This file is pure planner: it cannot import internal/executor
// (executor imports optimizer — a cycle), and the call sites that need it
// (clauseSelectivity, filterSelectivity, clauseSelectivityWithSource) carry
// no catalog or storage handle, only the plan subtree. So the decoded
// dependency data lives in a planner-side in-memory registry keyed by table
// OID, populated by tests now and by the ANALYZE/first-use loader later.
// The loader is a documented resume point: it decodes the 3429 heap rows
// with executor's DecodeStatisticExtDataPayloads + deserializeExtDependencies
// (executor already imports optimizer, so it can call RegisterPlannerExtStats
// without a cycle) and registers the result. No catalog reload path is
// invented here, and with an empty registry every entry point below reduces
// to exactly the pre-B-05b arithmetic.
//
// SCOPE. Functional dependencies only (STATS_EXT_DEPENDENCIES). MCV
// (statext_mcv_clauselist_selectivity) is B-05a-deferred behind the TOAST
// wall, so statextClauselistSelectivity is deps-only for now. Expression
// statistics are likewise deferred (B-05a skips HasExpr objects), so there
// is no expression arm: every compatible clause resolves to one user-column
// attnum, and any dependency mentioning an attnum outside the clause set is
// dropped as not fully matched, exactly as the oracle drops deps referencing
// unmatched expressions.
//
// JOIN PATH. The oracle gates the whole extended-statistics branch on
// find_single_rel_for_clauses, which returns NULL the moment one clause has
// a non-singleton clause_relids — a join clause has two relids by
// construction, so dependencies_clauselist_selectivity NEVER runs on a join
// list (repo analysis m0127-p56fiv §6). The analog here is the single-scan
// gate in dependenciesClauselistSelectivity: clauses resolving to different
// relation instances decline. calcJoinrelSize is therefore intentionally NOT
// wired: even if it were, every join clause would decline at the gate.
package optimizer

import (
	"math"
	"sync"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// extStatsMaxDimensions mirrors STATS_MAX_DIMENSIONS (statistics.h:19),
// the same cap B-05a builds under. It seeds choose_best_statistics'
// "fewest keys" tie-break, so the two must agree.
const extStatsMaxDimensions = 8

// PlannerExtDependency is one functional dependency over a statistics
// object's columns: Attrs[0:len-1] functionally determine Attrs[len-1]
// with the given degree of validity in [0, 1]. Attnums are 1-based catalog
// attnums (column Ordinal+1), the same convention B-05a serializes. This
// mirrors executor.ExtDependency without importing it (import cycle — see
// the file header); the future 3429 loader translates one into the other
// field-for-field.
type PlannerExtDependency struct {
	Degree float64
	Attrs  []int16
}

// PlannerExtStatsObject is one dependencies-kind statistics object as the
// planner sees it: the full member key set (for choose_best_statistics'
// coverage counting) plus the built dependencies. Only non-inherited,
// dependencies-kind objects are ever registered — B-05a writes inh=false
// rows exclusively — so no kind/inherit fields are carried.
type PlannerExtStatsObject struct {
	StatsOID uint32
	Keys     []int16
	Deps     []PlannerExtDependency
}

var (
	plannerExtStatsMu      sync.RWMutex
	plannerExtStatsByTable = map[uint32][]PlannerExtStatsObject{}
)

// RegisterPlannerExtStats records one statistics object's dependencies for
// tableOID, replacing any previous entry with the same StatsOID. This is
// the in-memory accessory's write door: tests call it directly, and the
// future ANALYZE/first-use 3429 loader (executor side, which may import
// this package) decodes heap rows into PlannerExtStatsObject values and
// calls it per object.
func RegisterPlannerExtStats(tableOID uint32, obj PlannerExtStatsObject) {
	plannerExtStatsMu.Lock()
	defer plannerExtStatsMu.Unlock()
	objs := plannerExtStatsByTable[tableOID]
	for i := range objs {
		if objs[i].StatsOID == obj.StatsOID {
			objs[i] = obj
			plannerExtStatsByTable[tableOID] = objs
			return
		}
	}
	plannerExtStatsByTable[tableOID] = append(objs, obj)
}

// ClearPlannerExtStats drops the whole registry. Tests use it (via Cleanup)
// for isolation; production has no call site yet.
func ClearPlannerExtStats() {
	plannerExtStatsMu.Lock()
	defer plannerExtStatsMu.Unlock()
	plannerExtStatsByTable = map[uint32][]PlannerExtStatsObject{}
}

// plannerExtStatsForTable snapshots the registered objects for one table.
// Nil when nothing was registered — the caller declines.
func plannerExtStatsForTable(tableOID uint32) []PlannerExtStatsObject {
	plannerExtStatsMu.RLock()
	defer plannerExtStatsMu.RUnlock()
	return plannerExtStatsByTable[tableOID]
}

// plannerExtStatsLive reports whether anything is registered at all. The
// hot paths check this first so the common no-statistics case pays one
// mutex read and no per-clause resolution work.
func plannerExtStatsLive() bool {
	plannerExtStatsMu.RLock()
	defer plannerExtStatsMu.RUnlock()
	return len(plannerExtStatsByTable) != 0
}

// chooseBestPlannerExtStats ports choose_best_statistics
// (extended_stats.c:1206) for the dependencies kind: the object covering
// the most attributes of still-unestimated clauses wins, ties going to the
// object with fewer keys overall. clauseSets[i] holds clause i's attnums
// (a singleton — every compatible clause names exactly one column); nil
// means incompatible or already estimated. Returns -1 when nothing covers
// at least two attributes.
//
// The initial bestMatched=2 / bestKeys=9 reproduces the oracle's initial
// state verbatim, including its subtle consequence: a two-attribute cover
// wins only through the tie-break (2 == 2 with fewer than 9 keys), which is
// exactly how a two-column statistics object gets picked upstream.
func chooseBestPlannerExtStats(objs []PlannerExtStatsObject, clauseSets []map[int16]bool) int {
	best := -1
	bestMatched := 2
	bestKeys := extStatsMaxDimensions + 1
	for oi := range objs {
		keys := make(map[int16]bool, len(objs[oi].Keys))
		for _, k := range objs[oi].Keys {
			keys[k] = true
		}
		matched := map[int16]bool{}
		for _, s := range clauseSets {
			if s == nil {
				continue
			}
			if !attnumsSubset(s, keys) {
				continue
			}
			for a := range s {
				matched[a] = true
			}
		}
		nm := len(matched)
		nk := len(objs[oi].Keys)
		if nm > bestMatched || (nm == bestMatched && nk < bestKeys) {
			best, bestMatched, bestKeys = oi, nm, nk
		}
	}
	return best
}

// findStrongestPlannerDep ports find_strongest_dependency
// (dependencies.c:918): the fully-matched dependency over the most
// attributes wins; ties on width go to the higher degree (equal degrees
// replace, i.e. the later dependency wins — the oracle keeps the new best
// unless the old one is strictly stronger). Dependencies wider than the
// available attnum set can never match and are skipped first, as upstream.
func findStrongestPlannerDep(deps []PlannerExtDependency, attnums map[int16]bool) int {
	best := -1
	for i, d := range deps {
		if len(d.Attrs) > len(attnums) {
			continue
		}
		if best >= 0 {
			if len(d.Attrs) < len(deps[best].Attrs) {
				continue
			}
			if len(d.Attrs) == len(deps[best].Attrs) && deps[best].Degree > d.Degree {
				continue
			}
		}
		covered := make(map[int16]bool, len(d.Attrs))
		for _, a := range d.Attrs {
			covered[a] = true
		}
		if !attnumsSubset(covered, attnums) {
			continue
		}
		best = i
	}
	return best
}

func attnumsSubset(sub, super map[int16]bool) bool {
	for a := range sub {
		if !super[a] {
			return false
		}
	}
	return true
}

// extDepClauseColumn resolves the single base-table column a clause constrains,
// with the relation-instance identity the single-scan gate needs. It ports
// the Var half of dependency_is_compatible_clause (dependencies.c:741):
//
//   - `col = const` (either side) with an equality operator — the opclause
//     arm (Var = pseudoconstant, get_oprrest == F_EQSEL). Only OpEq: goopg
//     has no resa function indirection to consult, and OpEq is the only
//     operator whose estimator is the equality one.
//   - `col IN (const, ...)` value-list form — the ScalarArrayOpExpr arm
//     (useOr, Var IN Const, eqsel operator). NOT IN, x != ANY, non-equality
//     ANY/ALL and subquery forms decline, as upstream rejects ALL() and
//     non-eqsel operators.
//   - OR of same-column compatibilities — the is_orclause arm (every
//     argument compatible with the SAME attnum).
//   - NOT col — the is_notclause arm ("NOT x" as "x = false"): the operand
//     must be a bare column; NOT over a comparison is not unwrapped further.
//   - bare col — the boolean "x" as "x = true" fall-through.
//
// Anything else (ranges, joins, expressions, multi-column shapes) is
// incompatible. In particular a bare `col = col` join clause fails the const
// recogniser, so join lists can never contribute a compatible clause.
// NOTE on form: this classifier is deliberately written with type
// assertions rather than a type switch. The fail-closed default (unknown
// shape => incompatible => the base estimator prices the clause) is the
// whole correctness argument, identical to selectivity.go:isConstExpr —
// there is no traversal that could silently skip a new Expr type, so there
// is nothing for the walker census to count.
func extDepClauseColumn(clause Expr, child Node) (scan Node, table *catalog.Table, attnum int16, ok bool) {
	if b, isBin := clause.(*BinaryOp); isBin {
		switch b.Op {
		case parser.OpEq:
			cr, _, isCol := normalizeColumnConst(b.Left, b.Right)
			if !isCol {
				return nil, nil, 0, false
			}
			return resolveExtDepColumn(cr, child)
		case parser.OpOr:
			return extDepOrColumn(b, child)
		}
		return nil, nil, 0, false
	}
	if ie, isIn := clause.(*InExpr); isIn {
		if ie.Plan != nil || len(ie.List) == 0 || ie.Negated || ie.NotEqualAny {
			return nil, nil, 0, false
		}
		if ie.AnyOp != 0 && ie.AnyOp != parser.OpEq {
			return nil, nil, 0, false
		}
		cr, isCol := ie.Operand.(*ColumnRef)
		if !isCol {
			return nil, nil, 0, false
		}
		for _, el := range ie.List {
			if !isConstExpr(el) {
				return nil, nil, 0, false
			}
		}
		return resolveExtDepColumn(cr, child)
	}
	if u, isUnary := clause.(*UnaryOp); isUnary {
		if u.Op != parser.OpNot {
			return nil, nil, 0, false
		}
		cr, isCol := u.Operand.(*ColumnRef)
		if !isCol {
			return nil, nil, 0, false
		}
		return resolveExtDepColumn(cr, child)
	}
	if cr, isCol := clause.(*ColumnRef); isCol {
		return resolveExtDepColumn(cr, child)
	}
	return nil, nil, 0, false
}

// extDepOrColumn ports the is_orclause recursion: every disjunct must be
// compatible with the same (scan, table, attnum). Disjuncts are flattened
// through nested ORs; a disjunct that is itself an OR flattens naturally,
// while AND/anything else under an OR fails compatibility and poisons the
// whole clause, as upstream returns false on the first incompatible argument.
func extDepOrColumn(or *BinaryOp, child Node) (Node, *catalog.Table, int16, bool) {
	disjuncts := splitDisjuncts(or, nil)
	if len(disjuncts) == 0 {
		return nil, nil, 0, false
	}
	var (
		firstScan  Node
		firstTable *catalog.Table
		firstAtt   int16
	)
	for i, d := range disjuncts {
		// An OR arm must be a plain equality-or-IN shape: recurse only into
		// what extDepClauseColumn accepts minus further ORs is wrong — nested
		// ORs are fine (they flatten), so call the top entry but reject
		// bare boolean/NOT arms? No: upstream recurses into the SAME function,
		// so every arm shape is allowed as long as attnums agree. Recurse.
		s, t, a, ok := extDepClauseColumnFlat(d, child)
		if !ok {
			return nil, nil, 0, false
		}
		if i == 0 {
			firstScan, firstTable, firstAtt = s, t, a
			continue
		}
		if a != firstAtt || t != firstTable || s != firstScan {
			return nil, nil, 0, false
		}
	}
	return firstScan, firstTable, firstAtt, true
}

// extDepClauseColumnFlat is extDepClauseColumn with nested ORs flattened
// first, so extDepOrColumn's per-disjunct recursion terminates on the
// non-OR shapes. (Top-level callers pass already-AND-split conjuncts, so
// this indirection only matters under OR.)
func extDepClauseColumnFlat(clause Expr, child Node) (Node, *catalog.Table, int16, bool) {
	if b, isBin := clause.(*BinaryOp); isBin && b.Op == parser.OpOr {
		return extDepOrColumn(b, child)
	}
	return extDepClauseColumn(clause, child)
}

// splitDisjuncts flattens an OR tree, mirroring splitConjuncts' AND
// flattening in rangequery.go.
func splitDisjuncts(e Expr, out []Expr) []Expr {
	if b, ok := e.(*BinaryOp); ok && b.Op == parser.OpOr {
		out = splitDisjuncts(b.Left, out)
		return splitDisjuncts(b.Right, out)
	}
	return append(out, e)
}

// resolveExtDepColumn maps one ColumnRef to its base relation instance and
// 1-based attnum. Resolution goes through resolveBaseColumn — the same door
// the rest of the estimator family uses — so Project/Filter wrappers above
// the scan behave identically here. The attnum is Ordinal+1 over the base
// table's own columns, matching what B-05a stores; dropped columns never
// match (B-05a cannot build on them either).
func resolveExtDepColumn(cr *ColumnRef, child Node) (Node, *catalog.Table, int16, bool) {
	if cr == nil {
		return nil, nil, 0, false
	}
	ref, isOK := resolveBaseColumn(cr.Index, child)
	if !isOK || ref.table == nil {
		return nil, nil, 0, false
	}
	for i := range ref.table.Columns {
		col := &ref.table.Columns[i]
		if col.Name == ref.col && !col.Dropped {
			return ref.scan, ref.table, int16(col.Ordinal + 1), true
		}
	}
	return nil, nil, 0, false
}

// extSelFunc prices one clause for the dependency combine. The numeric path
// passes clauseSelectivity (unconditionally reliable); the WithSource twin
// passes clauseSelectivityWithSource and propagates reliability.
type extSelFunc func(c Expr, child Node) (float64, bool)

// dependenciesClauselistSelectivity ports dependencies_clauselist_selectivity
// (dependencies.c:1370) for an AND clause list. It returns the combined
// selectivity of the consumed clauses (1.0 when nothing applies) with the
// estimatedclauses bitmap: estimated[i] is set for every clause whose
// selectivity is included, so the caller prices the REMAINDER with the base
// estimator exactly as clauselist_selectivity_ext skips consumed clauses.
//
// Combination math (clauselist_apply_dependencies): per-attribute
// selectivities first, then the greedy strongest-dependency chain applied
// backwards as conditional probabilities — P(b|a) replaces P(b) — with
// P(a,b) = f*Min(P(a),P(b)) + (1-f)*P(a)*P(b) per step. The README's
// P(a,b) = P(a)*(d + (1-d)*P(b)) is the same formula when the implying
// selectivity does not exceed the implied one (the s1 <= s2 branch below).
func dependenciesClauselistSelectivity(conjuncts []Expr, child Node) (float64, []bool) {
	sel, estimated, _ := dependenciesClauselistSelectivityInner(conjuncts, child,
		func(c Expr, n Node) (float64, bool) { return clauseSelectivity(c, n), true })
	return sel, estimated
}

// dependenciesClauselistSelectivityWithSource is the reliability-tracking
// twin: same consumption, but every consumed clause must be reliably
// estimated or the combined answer reports unreliable (so
// applyLocalFilterSelectivity keeps the pre-filter count rather than
// trusting fallback constants through the dependency math).
func dependenciesClauselistSelectivityWithSource(conjuncts []Expr, child Node) (selectivityEstimate, []bool) {
	sel, estimated, reliable := dependenciesClauselistSelectivityInner(conjuncts, child,
		func(c Expr, n Node) (float64, bool) {
			sub := clauseSelectivityWithSource(c, n)
			return sub.value, sub.reliable
		})
	return selectivityEstimate{value: sel, reliable: reliable}, estimated
}

func dependenciesClauselistSelectivityInner(conjuncts []Expr, child Node, price extSelFunc) (float64, []bool, bool) {
	estimated := make([]bool, len(conjuncts))
	decline := func() (float64, []bool, bool) { return 1.0, estimated, true }
	if len(conjuncts) == 0 || child == nil || !plannerExtStatsLive() {
		return decline()
	}

	// Per-clause attnums + the single-relation gate
	// (find_single_rel_for_clauses analog: every compatible clause must
	// resolve to the same relation instance; different instances — e.g. the
	// two sides of a join — decline the whole list).
	type clauseCol struct {
		scan   Node
		table  *catalog.Table
		attnum int16
	}
	cols := make([]clauseCol, len(conjuncts))
	compat := make([]bool, len(conjuncts))
	var firstScan Node
	var firstTable *catalog.Table
	haveFirst := false
	for i, c := range conjuncts {
		s, t, a, ok := extDepClauseColumnFlat(c, child)
		if !ok {
			continue
		}
		if !haveFirst {
			firstScan, firstTable, haveFirst = s, t, true
		} else if s != firstScan || t != firstTable {
			return decline()
		}
		cols[i] = clauseCol{scan: s, table: t, attnum: a}
		compat[i] = true
	}
	if !haveFirst {
		return decline()
	}
	clauseAttnums := map[int16]bool{}
	for i := range conjuncts {
		if compat[i] {
			clauseAttnums[cols[i].attnum] = true
		}
	}
	// Fewer than two distinct attnums: no dependency can be fully matched
	// (the oracle's BMS_MULTIPLE gate on clauses_attnums).
	if len(clauseAttnums) < 2 {
		return decline()
	}
	registered := plannerExtStatsForTable(firstTable.OID)
	if len(registered) == 0 {
		return decline()
	}

	// Candidate objects: at least two member keys among the clause attnums
	// (the oracle's nmatched + nexprs < 2 skip; expressions cannot occur
	// here — see the file header).
	candidates := make([]PlannerExtStatsObject, 0, len(registered))
	for _, obj := range registered {
		nmatched := 0
		for _, k := range obj.Keys {
			if clauseAttnums[k] {
				nmatched++
			}
		}
		if nmatched >= 2 {
			candidates = append(candidates, obj)
		}
	}
	if len(candidates) == 0 {
		return decline()
	}

	// Best-object claiming loop over choose_best_statistics: pick the object
	// covering the most still-uncovered clause attributes (ties to fewest
	// keys), claim its covered clauses, repeat until nothing covers two
	// uncovered attributes. A single object covering everything — the common
	// case — claims once and the pooled dependencies below are exactly the
	// oracle's.
	//
	// Deviation note: the oracle pools dependencies from EVERY object
	// matching two clause attributes without claiming; when two objects
	// overlap on the same clause columns with different dependency sets the
	// pools can differ. Single-object and disjoint-object inputs are
	// identical.
	sets := make([]map[int16]bool, len(conjuncts))
	for i := range conjuncts {
		if compat[i] {
			sets[i] = map[int16]bool{cols[i].attnum: true}
		}
	}
	var pooled []PlannerExtDependency
	for {
		best := chooseBestPlannerExtStats(candidates, sets)
		if best < 0 {
			break
		}
		pooled = append(pooled, candidates[best].Deps...)
		keys := make(map[int16]bool, len(candidates[best].Keys))
		for _, k := range candidates[best].Keys {
			keys[k] = true
		}
		for i := range sets {
			if sets[i] != nil && attnumsSubset(sets[i], keys) {
				sets[i] = nil
			}
		}
	}

	// Keep only dependencies fully matched by the clause attnums (the
	// oracle's expression-remap filter reduces to this without expressions),
	// with corrupt-registry guards the oracle never needs: NaN or negative
	// degrees cannot come out of B-05a's builder, but a hand-populated
	// registry could smuggle one in and poison every estimate above it.
	var matched []PlannerExtDependency
	for _, d := range pooled {
		if len(d.Attrs) < 2 {
			continue
		}
		if math.IsNaN(d.Degree) || d.Degree < 0 {
			continue
		}
		covered := make(map[int16]bool, len(d.Attrs))
		for _, a := range d.Attrs {
			covered[a] = true
		}
		if !attnumsSubset(covered, clauseAttnums) {
			continue
		}
		matched = append(matched, d)
	}

	// Greedy strongest-first ordering (the oracle's while loop): each round
	// takes the strongest dependency fully matched by the still-unimplied
	// attnums, then removes its implied attribute so chains order
	// correctly (a -> b -> c applies b -> c first — the backwards traversal
	// in the combine step depends on this order).
	working := make(map[int16]bool, len(clauseAttnums))
	for a := range clauseAttnums {
		working[a] = true
	}
	var ordered []PlannerExtDependency
	for {
		strongest := findStrongestPlannerDep(matched, working)
		if strongest < 0 {
			break
		}
		ordered = append(ordered, matched[strongest])
		implied := matched[strongest].Attrs[len(matched[strongest].Attrs)-1]
		delete(working, implied)
	}
	if len(ordered) == 0 {
		return decline()
	}

	// Per-attribute selectivities over the dependency-mentioned attributes,
	// marking every compatible clause on those attributes estimated (the
	// oracle prices attr_clauses with clauselist_selectivity_ext and marks
	// them in estimatedclauses; per-clause products here equal that call
	// because every compatible clause is an equality shape).
	attrUnion := map[int16]bool{}
	for _, d := range ordered {
		for _, a := range d.Attrs {
			attrUnion[a] = true
		}
	}
	attrSel := make(map[int16]float64, len(attrUnion))
	reliable := true
	for a := range attrUnion {
		s := 1.0
		for i, c := range conjuncts {
			if !compat[i] || cols[i].attnum != a {
				continue
			}
			v, rel := price(c, child)
			s *= v
			if !rel {
				reliable = false
			}
			estimated[i] = true
		}
		attrSel[a] = s
	}

	// Backwards conditional-probability combine
	// (clauselist_apply_dependencies): P(b|a) = P(a,b)/P(a), i.e.
	// f*Min(s1,s2)/s1 + (1-f)*s2, with the s1 <= s2 branch avoiding the
	// division (it reduces to f + (1-f)*s2). The final answer is the product
	// of unimplied selectivities and implied conditionals.
	for i := len(ordered) - 1; i >= 0; i-- {
		dep := ordered[i]
		s1 := 1.0
		for _, a := range dep.Attrs[:len(dep.Attrs)-1] {
			s1 *= attrSel[a]
		}
		implied := dep.Attrs[len(dep.Attrs)-1]
		s2 := attrSel[implied]
		f := dep.Degree
		if f > 1 {
			f = 1
		}
		if s1 <= s2 {
			attrSel[implied] = f + (1-f)*s2
		} else {
			attrSel[implied] = f*s2/s1 + (1-f)*s2
		}
	}
	sel := 1.0
	for _, v := range attrSel {
		sel *= v
	}
	return clampProbability(sel), estimated, reliable
}

// statextClauselistSelectivity ports statext_clauselist_selectivity
// (extended_stats.c:1981) for implicit-AND lists: MCV first, functional
// dependencies over the remainder. MCV is B-05a-deferred (no stxdmcv is ever
// written), so this is dependencies-only; the two-stage shape is kept so the
// MCV arm has a place to land. OR lists never reach here — the only callers
// pass AND lists, matching the oracle's is_or=false restriction path (for
// OR clauses the oracle runs MCV alone and returns, and dependencies never
// apply to ORs).
func statextClauselistSelectivity(conjuncts []Expr, child Node) (float64, []bool) {
	return dependenciesClauselistSelectivity(conjuncts, child)
}

// statextClauselistSelectivityWithSource is the reliability-tracking twin,
// consumed by clauseSelectivityWithSource's AND arm.
func statextClauselistSelectivityWithSource(conjuncts []Expr, child Node) (selectivityEstimate, []bool) {
	return dependenciesClauselistSelectivityWithSource(conjuncts, child)
}
