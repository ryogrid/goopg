package optimizer

// M0127-P5.6-a — the join-selectivity substrate: PG's `examine_variable`,
// `get_variable_numdistinct` and `eqjoinsel`'s no-MCV arm, expressed over the
// join search's own `restrictInfo` operands.
//
// PG oracle: `eqjoinsel_inner`'s else-branch and `neqjoinsel`
// (utils/adt/selfuncs.c), `get_variable_numdistinct` (same file),
// `clause_selectivity_ext`'s `s1 = 0.5` fall-through
// (optimizer/path/clausesel.c:691), and the DEFAULT_* constants in
// include/utils/selfuncs.h. Design: leftdeep-joins
// [04](../../docs/design/leftdeep-joins/04-cost-and-cardinality.md) §3.2.
//
// WHY THIS IS THE FIRST SLICE OF P5.6. The Q9 estimate chain
// (1,250 → 37M → 1.5e11 → 1.1e15 → 5.9e15 against an actual 175) is compounding
// ndistinct error, and the compounding has a specific source: the legacy DP
// divides |L|·|R| by the PRODUCT of every spanning edge's per-side NDV
// (`estimateJoinCost`, bushy.go:1266-1301), where PG divides by ONE ndistinct —
// the larger of the two sides' — per clause. Two clauses of the same
// equivalence class therefore charge twice in the old model and once in PG's;
// that is the same double-count 04 §5 says the ×2.0 `inferredEdgePenalty` was
// papering over, in the cost dimension instead of the cardinality one where the
// error lives. `selectivityClauses` (joinrestrict.go) already picks the one
// member per class; this file is what prices the members it hands back.
//
// The estimator is deliberately NOT the whole `calcJoinrelSize`: the sizer also
// owns the FK-superkey override, the FK clamp and the fallback caps (04 §3.1,
// §3.3), each of which decides which clauses ever reach these functions. Those
// are P5.6-b's and P5.6-c's; landing the per-clause primitive first means the
// override can be tested against a general path that already works, rather than
// against nothing.
//
// SIBLING NOTE (hard-won rule #2). The legacy `sideKeyDistinct` /
// `uniqueNoFanoutRawCount` family in bushy.go is NOT a sibling of this one that
// must be kept in sync: it serves the subset-bitmask DP that P6.3 deletes, it
// answers in int64 saturating arithmetic, and its fallback (the table's largest
// per-column sample NDistinct) is not a PG behaviour at all. The two coexist
// because they belong to two different cost models, which 04 §1 forbids mixing
// inside one comparison — not because either is a copy of the other.
//
// No longer inert (M0127-P5.9, 2026-08-06): `GOOPG_PGSHAPED_DP` is ON by
// default, `sizeJoinRel` landed at P5.6-b, and `planSelect` calls the search —
// these estimates price production joins. Validated in isolation by
// `joinselectivity_test.go`.

import (
	"math"
	"math/bits"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

const (
	// defaultNumDistinct is DEFAULT_NUM_DISTINCT (selfuncs.h): the ndistinct
	// assumed for a column with no usable statistics. It is deliberately a
	// small number — PG's comment calls it "a plausible value for a column with
	// no stats" — and its effect on a join is a selectivity of 1/200, i.e.
	// DEFAULT_EQ_SEL, which is why an equality between two unanalysed columns
	// lands on exactly PG's default rather than on a second, differently-chosen
	// constant.
	defaultNumDistinct = 200.0

	// defaultIneqJoinSel is DEFAULT_INEQ_SEL (selfuncs.h), the value PG's
	// `scalarltjoinsel` and its siblings return unconditionally: upstream has
	// no join-selectivity model for an ordering comparison at all
	// (selfuncs.c:2908 is a one-line `PG_RETURN_FLOAT8(DEFAULT_INEQ_SEL)`).
	defaultIneqJoinSel = 0.3333333333333333

	// defaultUnhandledClauseSel is `clause_selectivity_ext`'s initialiser
	// (clausesel.c:691, "default for any unhandled clause type"). It is 0.5,
	// NOT the 1/3 `defaultGenericSelectivity` the pre-search estimator uses for
	// a baserel qual (cardinality.go:31) — the two are different constants in
	// upstream too, and mixing them here would import a goopg-only number into
	// the currency 04 §1 defines as PG's.
	defaultUnhandledClauseSel = 0.5
)

// joinVarStats is `VariableStatData` (selfuncs.h) reduced to the fields these
// two estimators actually read: the column's ANALYZE statistics, the RAW row
// count of the relation it belongs to, and whether it is a boolean column.
//
// The zero value is the "could not resolve this operand" case, and it is a
// legitimate answer rather than a failure — PG reaches the same state whenever
// `examine_variable` is handed a non-Var expression, and every field below is
// read defensively so the estimate degrades to DEFAULT_NUM_DISTINCT instead of
// to a wrong number.
//
// `tuples` is the relation's raw (pre-filter) row count, PG's
// `vardata->rel->tuples`, and NOT the joinrel's or even the baserel's
// post-filter `rows`. That is upstream's choice and it is load-bearing: the
// distinct-value count of a column is a property of the stored column, so
// scaling a stored fraction by a FILTERED row count would report fewer distinct
// values merely because a WHERE clause removed rows the fraction was measured
// over, and a join's selectivity would then improve every time an unrelated
// qual was added.
type joinVarStats struct {
	stats  *catalog.ColumnStats
	tuples float64
	// rows is the relation's FILTERED (post-qual) row count, PG's
	// `vardata->rel->rows`. The bucket scaler (B-16) prices the rows actually
	// hashed, while `tuples` stays raw. Zero when the context was hand-built
	// without it; the scaler reads that as "no filter info" and skips.
	rows   float64
	isBool bool

	// typeName is the column's catalog type. Histogram bounds are stored as
	// STRINGS, so every comparison against them needs the type to know whether
	// to compare numerically or byte-wise — `histCmp` falls back to
	// strings.Compare without it, which orders "10" before "9". take2 P2-12.
	typeName string
}

// examineJoinVar resolves ONE operand of a join clause to its base relation's
// statistics — PG's `examine_variable` (selfuncs.c) at goopg's fidelity level.
//
// `relids` is the operand's own relset, which for a canonical equijoin is the
// `leftRelids`/`rightRelids` split `restrictInfo` already carries. The operand
// resolves only when that set names exactly ONE base relation and the
// expression is a bare column of it; both halves are required and neither is a
// formality:
//
//   - a multi-rel operand (`b.y + c.z` in `a.x = b.y + c.z`) has no single
//     relation whose statistics could describe it, which is exactly the case
//     upstream leaves `vardata->rel` NULL for;
//   - an expression operand over one relation (`upper(b.y)`) could in principle
//     be described by an expression index's statistics, which goopg's catalog
//     does not have.
//
// Resolution is by column NAME, not by `ColumnRef.Index`. The search's clauses
// are written in the pre-search CONCATENATION's coordinate space (03 §10), so
// `Index` is a global offset into `Left ++ Right ++ …` and indexing
// `Stats.Columns` — which is positional over the base table's own columns —
// with it would silently read a different column's statistics whenever the
// relation is not the first one in the FROM list. `columnStatsByName`
// (pathparamindex.go) is the same door the parameterised index paths resolve
// through, for the same reason.
func (s *searchCtx) examineJoinVar(key Expr, relids RelSet) joinVarStats {
	var v joinVarStats
	i, cr, ok := s.resolveJoinVarColumn(key, relids)
	if ok && i >= 0 && i < len(s.relInfos) && s.relInfos[i].table != nil && cr != nil {
		for _, c := range s.relInfos[i].table.Columns {
			if c.Name == cr.Name {
				v.typeName = c.Type.Name
				break
			}
		}
	}
	if cr != nil {
		// PG's BOOLOID arm of `get_variable_numdistinct`: a boolean column has
		// two values whether or not anyone has analysed it. Recorded even for
		// an operand that did NOT resolve to a relation, because the type is on
		// the operand rather than on the statistics.
		v.isBool = cr.Type.Name == "bool"
	}
	if !ok {
		return v
	}
	info := s.relInfos[i]
	v.tuples = float64(info.baseRows)
	v.rows = float64(info.filteredRows)
	v.stats = columnStatsByName(info.table, cr.Name)
	return v
}

// resolveJoinVarColumn is the operand-RESOLUTION half of `examine_variable`:
// which base relation of the search an operand belongs to, and which column of
// it. Split out from `examineJoinVar` because the sizer's superkey test
// (joinrelsize.go, P5.6-b) needs the same answer in the (relation, column)
// form rather than as statistics, and two resolutions that could disagree
// about which relation an operand belongs to would let a uniqueness proof fire
// against a different relation's statistics (hard-won rule #2).
//
// It answers `ok` only when the relset names exactly ONE base relation, that
// relation has a catalog table, and the expression is a bare named column.
// Every requirement is a real case rather than a formality:
//
//   - a multi-rel operand (`b.y + c.z` in `a.x = b.y + c.z`) has no single
//     relation whose statistics could describe it, which is exactly the case
//     upstream leaves `vardata->rel` NULL for; a zero relset (a Const, or an
//     operand the clause builder could not attribute) takes the same door;
//   - a subquery / CTE / VALUES leaf IS a relation of the search
//     (`buildInitialRels` admits every FROM item) but has no catalog table
//     behind it, so there is no per-column statistic to read. PG digs into the
//     subquery's own targetlist here (`examine_simple_variable`'s RTE_SUBQUERY
//     arm); goopg's leaf is an already-planned opaque subtree. Ledgered.
//   - an expression operand over one relation (`upper(b.y)`) could in
//     principle be described by an expression index's statistics, which
//     goopg's catalog does not have.
//
// The `*ColumnRef` is returned even when resolution fails, because the caller
// may still want the operand's TYPE (the boolean arm above) — a fact that does
// not depend on which relation the column came from.
func (s *searchCtx) resolveJoinVarColumn(key Expr, relids RelSet) (int, *ColumnRef, bool) {
	if s == nil || key == nil {
		return -1, nil, false
	}
	cr, ok := key.(*ColumnRef)
	if !ok {
		return -1, nil, false
	}
	// Exactly one base relation, i.e. a power of two.
	if relids == 0 || relids&(relids-1) != 0 {
		return -1, cr, false
	}
	i := bits.TrailingZeros32(uint32(relids))
	if i >= len(s.relInfos) {
		return -1, cr, false
	}
	if s.relInfos[i].table == nil || cr.Name == "" {
		return -1, cr, false
	}
	return i, cr, true
}

// getVariableNumDistinct is `get_variable_numdistinct` (selfuncs.c), including
// the order of its branches, which is the part that matters: an ABSOLUTE
// statistic wins outright, a RELATIVE one is scaled by the relation's raw row
// count, and only a relation whose size is unknown falls to the default.
//
// goopg stores upstream's one signed `stadistinct` as two fields; the reduction
// back to PG's convention is `ColumnStats.StaDistinct` (internal/catalog), so
// this estimator and the two catalog paths that publish `stadistinct` /
// `pg_stats.n_distinct` to the user cannot disagree about which field wins.
//
// The second return value is PG's `*isdefault` — "this is a guess, not a
// measurement". Nothing in this slice branches on it; `calcJoinrelSize`
// (P5.6-b) is where it earns its keep, because PG's `set_joinrel_size_estimates`
// caller chain uses it to decide whether an estimate may be trusted enough to
// skip a clamp.
func getVariableNumDistinct(v joinVarStats) (float64, bool) {
	stadistinct := 0.0
	switch {
	case v.stats != nil:
		stadistinct = v.stats.StaDistinct()
	case v.isBool:
		stadistinct = 2.0
	}

	// An absolute estimate is used as-is, whatever the relation's size.
	if stadistinct > 0 {
		return clampRowEst(stadistinct), false
	}
	// PG punts when `vardata->rel == NULL` or the relation has no known size;
	// both reach here as a non-positive `tuples`.
	if v.tuples <= 0 {
		return defaultNumDistinct, true
	}
	// A relative estimate scales with the relation.
	if stadistinct < 0 {
		return clampRowEst(-stadistinct * v.tuples), false
	}
	// No statistic at all: for a small relation PG assumes every row is
	// distinct, which cannot be off by more than the relation's own size, and
	// only for a large one does it fall back to the constant.
	if v.tuples < defaultNumDistinct {
		return clampRowEst(v.tuples), false
	}
	return defaultNumDistinct, true
}

// eqJoinSelectivity is `eqjoinsel_inner`'s no-MCV branch (selfuncs.c):
//
//	selectivity = (1 - nullfrac_l) · (1 - nullfrac_r) / max(nd_l, nd_r)
//
// Upstream's derivation, reproduced because the MAX reads backwards until it is
// spelled out: a non-null tuple of rel1 joins either zero rows of rel2 or
// N2·(1-nullfrac2)/nd2 of them, so the join cannot produce more than
// N1·N2·(1-nullfrac1)·(1-nullfrac2)/nd2 rows — a selectivity bound of
// (1-nf1)(1-nf2)/nd2. The symmetric argument bounds it by the same expression
// over nd1. Both are upper bounds, so the tighter one — the one with the LARGER
// nd in the denominator — is the estimate. Dividing by the larger nd is
// therefore not "picking the pessimistic side"; it is taking the MINIMUM of two
// bounds, and PG's own comment describes it as estimating from the point of
// view of the relation with the SMALLER nd.
//
// This is the single most consequential difference from the legacy DP, which
// divides by the PRODUCT of every spanning clause's per-side NDV: over a chain
// of correlated equalities the product compounds one restriction several times,
// which is the mechanism behind Q9's 13-order-of-magnitude estimate error.
func eqJoinSelectivity(v1, v2 joinVarStats) float64 {
	sel, _ := eqJoinSelectivityExt(v1, v2)
	return sel
}

// eqJoinSelectivityExt is the same estimate with PG's `*isdefault` carried out
// to the caller (P5.6-c): "the number in the denominator was a guess, not a
// measurement".
//
// The flag reported is the one belonging to the side whose ndistinct ACTUALLY
// ended up in the denominator, not the disjunction of the two. That is the only
// reading that matches what the flag is used for: an equality between a column
// with 1,000,000 measured distinct values and an unanalysed one divides by
// 1,000,000 — a measured number — and the presence of the unanalysed operand
// changed nothing about the answer. Reporting "default" there would make
// `calcJoinrelSize` clamp an estimate it has every reason to trust.
//
// Upstream computes both flags in `eqjoinsel` and uses them in the MCV and semi
// arms rather than in this one; carrying it out of the no-MCV branch is goopg's
// own, and exists because 04 §3.3's fallback cap has to know whether the
// estimate it is about to cap was derived from statistics at all.
func eqJoinSelectivityExt(v1, v2 joinVarStats) (float64, bool) {
	nd1, isdefault1 := getVariableNumDistinct(v1)
	nd2, isdefault2 := getVariableNumDistinct(v2)
	nullfrac1, nullfrac2 := 0.0, 0.0
	if v1.stats != nil {
		nullfrac1 = v1.stats.NullFrac
	}
	if v2.stats != nil {
		nullfrac2 = v2.stats.NullFrac
	}
	selec := (1.0 - nullfrac1) * (1.0 - nullfrac2)
	isdefault := isdefault2
	if nd1 > nd2 {
		selec /= nd1
		isdefault = isdefault1
	} else {
		selec /= nd2
	}
	return clampSelectivity(selec), isdefault
}

// joinClauseSelectivity is `clause_selectivity_ext`'s join arm for the clause
// shapes the search can produce: the per-clause factor `calcJoinrelSize`
// (P5.6-b) multiplies into a joinrel's row estimate.
//
// The dispatch is on the clause's OPERATOR, not on `restrictInfo.isEquijoin`,
// and the distinction is the kind of thing that silently under-counts if it is
// got wrong. `isEquijoin` answers "does this equality split into two one-sided
// operands a hash join could key on" — `a.x = b.y + c.z` is an equality that
// answers NO, because no single column of `b` is being equated to anything. PG
// nonetheless prices it with `eqjoinsel`, since it is still an equality and
// still restrictive; treating it as an unhandled clause would charge it 0.5
// where upstream charges 0.005, a 100× over-estimate of every joinrel above it.
// What the flag does govern is where the OPERANDS come from: an equijoin has a
// canonical, relset-attributed split to examine, and any other clause has only
// the raw operand expressions, which resolve to the unknown-variable case — and
// two unknown variables give 1/DEFAULT_NUM_DISTINCT, PG's DEFAULT_EQ_SEL.
//
// `<>` is `neqjoinsel`'s inner-join arm (selfuncs.c): the negator's selectivity
// subtracted from one. Its semi/anti arm (`1 - nullfrac`) is not reachable
// while 03 §4.4's pin keeps special joins out of the search.
func (s *searchCtx) joinClauseSelectivity(ri *restrictInfo) float64 {
	sel, _ := s.joinClauseSelectivityExt(ri)
	return sel
}

// joinClauseSelectivityExt is the same dispatch with the "this factor is a
// guess" bit `calcJoinrelSize`'s fallback cap reads (04 §3.3).
//
// Every arm that returns a CONSTANT is a guess by construction: DEFAULT_INEQ_SEL
// is what upstream returns unconditionally for an ordering comparison
// (selfuncs.c:2908 has no model at all), and the unhandled-clause 0.5 is a
// literal in `clause_selectivity_ext`. Only the equality arms can be
// measurements, and only when the ndistinct they divided by came from ANALYZE.
//
// `<>` inherits its operand's flag rather than being called a guess outright:
// `1 - eqjoinsel` over two measured ndistincts is as measured as the equality
// it negates.
//
// P6-08 (take3 08 §9): the result is memoised on the `restrictInfo` as PG's
// `norm_selec`. The DP asks for the same clause's selectivity once per joinrel
// pair that can evaluate it — O(2^n) times for a clause low in the tree — and
// the answer moves with neither the pair nor anything else that changes during
// a search (see the cache contract on `restrictInfo`). Planning-speed only:
// the cached value is bit-identical to the computed one, so plans do not move.
func (s *searchCtx) joinClauseSelectivityExt(ri *restrictInfo) (float64, bool) {
	if ri != nil && ri.normSelecValid {
		return ri.normSelec, ri.normSelecDefault
	}
	sel, isdefault := s.joinClauseSelectivityExtUncached(ri)
	if ri != nil && s != nil {
		ri.normSelec, ri.normSelecDefault, ri.normSelecValid = sel, isdefault, true
	}
	return sel, isdefault
}

// joinClauseSelectivityExtUncached is the body `joinClauseSelectivityExt`
// memoises; every caller should go through the memo.
func (s *searchCtx) joinClauseSelectivityExtUncached(ri *restrictInfo) (float64, bool) {
	if ri == nil || ri.clause == nil {
		return defaultUnhandledClauseSel, true
	}
	bo, ok := ri.clause.(*BinaryOp)
	if !ok {
		return defaultUnhandledClauseSel, true
	}
	switch bo.Op {
	case parser.OpEq:
		return eqJoinSelectivityExt(s.joinClauseOperands(ri, bo))
	case parser.OpNe:
		sel, isdefault := eqJoinSelectivityExt(s.joinClauseOperands(ri, bo))
		return clampSelectivity(1.0 - sel), isdefault
	case parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe:
		return defaultIneqJoinSel, true
	default:
		return defaultUnhandledClauseSel, true
	}
}

// joinClauseOperands examines both sides of an equality clause, preferring the
// `restrictInfo`'s canonical operand split when it has one.
//
// Pairing an operand with the WRONG relset is the failure this exists to
// prevent: `restrictInfo.leftKey` is the canonical LEFT of the equijoin split,
// which the clause builder is free to have taken from the clause's RIGHT-hand
// side, so reading `bo.Left` with `ri.leftRelids` would attribute one
// relation's column to another relation's statistics. Reading the split as a
// pair, or the raw operands with no relset at all, are the only two safe
// combinations.
func (s *searchCtx) joinClauseOperands(ri *restrictInfo, bo *BinaryOp) (joinVarStats, joinVarStats) {
	if ri.isEquijoin {
		return s.examineJoinVar(ri.leftKey, ri.leftRelids), s.examineJoinVar(ri.rightKey, ri.rightRelids)
	}
	return s.examineJoinVar(bo.Left, 0), s.examineJoinVar(bo.Right, 0)
}

// joinClauseSelectivityForJoin is `clause_selectivity_ext`'s join arm WITH the
// jointype PG threads through it (`clauselist_selectivity(root, clauses, 0,
// jointype, sjinfo)`, costsize.c:5555-5580): the per-clause factor C-05's
// `calcJoinrelSize` multiplies into `jselec`.
//
// INNER, LEFT, RIGHT and FULL dispatch to the inner-join arms above —
// `eqjoinsel` (selfuncs.c:2280) takes `eqjoinsel_inner` for every jointype
// except SEMI and ANTI, and `scalarltjoinsel` and the unhandled-clause default
// have no jointype at all. SEMI and ANTI are the jointypes whose selectivity is
// DEFINED differently ("the fraction of LHS rows that have matches",
// costsize.c:5592-5593), and for them:
//
//   - `=` is `eqjoinsel_semi`, over operands ORIENTED so that v1 is the outer
//     (preserved) side. Upstream does that swap through `get_join_variables`'
//     `join_is_reversed` (selfuncs.c:2312); goopg's `leftKey`/`rightKey` split
//     is canonical rather than oriented (joinrestrict.go:54), so the sizer
//     orients by which side `leftRelids` falls in. An operand that resolves to
//     neither side (a three-relation equality keyed across the pair) keeps
//     list order — the same "no rel" degradation `examineJoinVar` already
//     yields.
//   - `<>` is `neqjoinsel`'s semi arm (selfuncs.c:2843-2861): `1 - nullfrac`
//     of the OUTER variable, on the argument that with more than one distinct
//     value on the inside every non-null outer row finds a non-equal partner.
//     Reported as measured when the outer operand resolved to statistics —
//     the number is then the column's own null fraction, not a constant.
//   - everything else is the same selfuncs.h constant as for INNER.
//
// `outer`/`inner` are the sizer's post-swap inputs (`makeJoinRel`, C-03b:
// `outer` covers the SJI's LHS), and `inner.Rows` is `eqjoinsel_semi`'s
// `inner_rel->rows` clamp on nd2 — the only pathway by which a restriction on
// the inner side reaches a SEMI/ANTI estimate, since the inner's row count is
// otherwise unused by the SEMI/ANTI arms.
func (s *searchCtx) joinClauseSelectivityForJoin(ri *restrictInfo, jt parser.JoinType, outer, inner *RelOptInfo) (float64, bool) {
	if jt != parser.JoinSemi && jt != parser.JoinAnti {
		return s.joinClauseSelectivityExt(ri)
	}
	if ri == nil || ri.clause == nil {
		return defaultUnhandledClauseSel, true
	}
	bo, ok := ri.clause.(*BinaryOp)
	if !ok {
		return defaultUnhandledClauseSel, true
	}
	switch bo.Op {
	case parser.OpEq:
		v1, v2 := s.semiJoinOperands(ri, bo, outer)
		return eqJoinSelectivitySemi(v1, v2, relRows(inner))
	case parser.OpNe:
		v1, _ := s.semiJoinOperands(ri, bo, outer)
		nullfrac1 := 0.0
		if v1.stats != nil {
			nullfrac1 = v1.stats.NullFrac
		}
		return clampSelectivity(1.0 - nullfrac1), v1.stats == nil
	case parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe:
		return defaultIneqJoinSel, true
	default:
		return defaultUnhandledClauseSel, true
	}
}

// semiJoinOperands is `joinClauseOperands` oriented for the semi arms: the
// first result is the operand on the OUTER side of the join whenever the
// clause's canonical split lets that be decided.
func (s *searchCtx) semiJoinOperands(ri *restrictInfo, bo *BinaryOp, outer *RelOptInfo) (joinVarStats, joinVarStats) {
	v1, v2 := s.joinClauseOperands(ri, bo)
	if ri.isEquijoin && outer != nil && !relsSubset(ri.leftRelids, outer.Relids) && relsSubset(ri.rightRelids, outer.Relids) {
		return v2, v1
	}
	return v1, v2
}

// relRows is a nil-tolerant `rel.Rows`, zero meaning "no clamp".
func relRows(r *RelOptInfo) float64 {
	if r == nil {
		return 0
	}
	return r.Rows
}

// eqJoinSelectivitySemi is `eqjoinsel_semi` (selfuncs.c:2642) over the
// search's statistics: the fraction of OUTER rows (v1's side) that have at
// least one partner on the inner side, with `*isdefault` carried out as
// `eqJoinSelectivityExt` does for the inner arm.
//
// The two nd2 clamps are BOTH ported (selfuncs.c:2668-2681):
//
//	if (nd2 >= vardata2->rel->rows) nd2 = vardata2->rel->rows;   // the base rel's post-filter rows
//	if (nd2 >= inner_rel->rows)     nd2 = inner_rel->rows;       // the whole inner side's rows
//
// and each turns `isdefault2` OFF — a clamped nd2 is a measurement of the
// relation's size, not a guess about its column. Upstream's reason for
// clamping nd2 and NOT nd1 is load-bearing and asymmetric: this is the only
// pathway by which a restriction on the inner relation reaches a SEMI/ANTI
// estimate, while clamping nd1 as well would double-count the outer's own
// restrictions, which are already in `outer_rows`. `joinVarStats.rows` is the
// first clamp's operand (the same post-filter count B-16's bucket scaler
// reads); `innerRows` is the second's.
//
// The arithmetic after the clamps is `eqjoinselSemiCore` (cardinality.go),
// shared with the plan-node estimator's `semiPairMatchFraction` — one body,
// two callers that differ only in where their nd and clamps come from.
func eqJoinSelectivitySemi(v1, v2 joinVarStats, innerRows float64) (float64, bool) {
	nd1, isdefault1 := getVariableNumDistinct(v1)
	nd2, isdefault2 := getVariableNumDistinct(v2)
	if v2.rows > 0 && nd2 >= v2.rows {
		nd2 = v2.rows
		isdefault2 = false
	}
	if innerRows > 0 && nd2 >= innerRows {
		nd2 = innerRows
		isdefault2 = false
	}
	nullfrac1 := 0.0
	if v1.stats != nil {
		nullfrac1 = v1.stats.NullFrac
	}
	sel := eqjoinselSemiCore(v1.stats, v2.stats, nd1, nd2, !isdefault1, !isdefault2, nullfrac1)
	// The nd arms are a guess when EITHER nd was (upstream's
	// `!isdefault1 && !isdefault2` gate picks the 0.5 branch otherwise); the
	// MCV arm is a measurement whatever the nds were, because the matched
	// frequency mass is measured.
	haveMCVs := v1.stats != nil && v2.stats != nil && len(v1.stats.MCV) > 0 && len(v2.stats.MCV) > 0
	return clampSelectivity(sel), !haveMCVs && (isdefault1 || isdefault2)
}

// clampSelectivity holds a selectivity inside [0, 1] and maps NaN to the
// unhandled-clause default. PG's `CLAMP_PROBABILITY` (selfuncs.h) does the
// range half; the NaN half is goopg's, because a NaN selectivity would
// propagate through every joinrel above it and compare false against every
// cost, silently disabling `add_path`'s pruning rather than producing a visibly
// wrong plan.
func clampSelectivity(s float64) float64 {
	if math.IsNaN(s) {
		return defaultUnhandledClauseSel
	}
	if s < 0 {
		return 0
	}
	if s > 1 {
		return 1
	}
	return s
}

// residualSelectivity is the combined selectivity of the join clauses a merge
// join CANNOT use as merge clauses — PG's qpquals for that path.
//
// It exists so the merge arm can recover `mergejointuples`: the joinrel's row
// count is what survives EVERY clause, while the merge operator emits what
// survives only the MERGE clauses and lets the residual filter the rest. The
// two differ exactly by this factor, and costing the operator on the smaller of
// them is the mispricing recorded in
// impl/FINDING-mergejoin-costed-on-postfilter-rows.md.
//
// Clauses are combined as independent conjuncts, the same assumption
// clauselist_selectivity makes and the same one calcJoinrelSize already makes
// for the join as a whole, so this cannot disagree with the row estimate it is
// dividing into.
func (s *searchCtx) residualSelectivity(residual []*restrictInfo) float64 {
	sel := 1.0
	for _, ri := range residual {
		sel *= s.joinClauseSelectivity(ri)
	}
	return clampSelectivity(sel)
}

// mergeJoinTuples is `final_cost_mergejoin`'s `mergejointuples`
// (costsize.c:3960-4045): the number of tuples the merge operator actually
// emits, before the non-merge quals filter them down to the joinrel's row
// count.
//
// Returns joinrelRows unchanged when there is no residual — the overwhelmingly
// common case, and the one where the old code was already right.
func (s *searchCtx) mergeJoinTuples(joinrelRows float64, residual []*restrictInfo, outerRows, innerRows float64) float64 {
	if len(residual) == 0 || joinrelRows <= 0 {
		return joinrelRows
	}
	sel := s.residualSelectivity(residual)
	if sel <= 0 {
		return joinrelRows
	}
	tuples := joinrelRows / sel
	// The merge can never emit more pairs than the cross product; the clamp
	// mirrors the one calcJoinrelSize applies to its own estimate.
	if cross := math.Max(outerRows, 1) * math.Max(innerRows, 1); tuples > cross {
		tuples = cross
	}
	if tuples < joinrelRows {
		return joinrelRows
	}
	return tuples
}

// estimateHashBucketSize is `estimate_hash_bucket_stats` (selfuncs.c:4060)
// reduced to the fraction it exists to produce: what share of the inner
// relation lands in the bucket an average outer probe walks. take2 P2-11.
//
// B-16 (P2-11b) landed the MCV half: the 1/ndistinct fraction is scaled up by
// the most-common-value frequency over the average frequency, clamped to
// [1e-6, 1], and a default-ndistinct key reports Max(0.1, mcv_freq) instead of
// being skipped.
//
// SCOPE: the 1/nbuckets arm is OUT. PG starts from 1/nbuckets when ndistinct
// exceeds the executor's bucket count and only then falls to 1/ndistinct;
// nbuckets is a build-geometry input that would have to be widened through
// `addHashJoinPath`'s signature to reach this site. Without it a
// highly-distinct key prices CHEAPER than upstream (1/nd < 1/nbuckets when
// nd > nbuckets).
//
// The point of the term is the one thing goopg's hash cost could not see: a
// hash join keyed on a LOW-ndistinct column has long buckets, so every probe
// walks many tuples, while a unique key gives one. Priced without it, the two
// cost the same — the degeneracy `reselectDegenerateHashKeys` was written to
// work around (Q78's collapsed bucket, M0125-0035b).
//
// Returns 0 when no operand resolves to a statistic. 0 means "no information"
// and the caller must skip the term entirely rather than substitute a guess:
// inventing a bucket size without stats would move plans on nothing, which is
// the failure this bundle keeps recording.
func (s *searchCtx) estimateHashBucketSize(clauses []*restrictInfo, innerRelids RelSet) float64 {
	// PG takes the SMALLEST bucketsize over the hash clauses: "we use the
	// smallest bucketsize estimated for any individual hashclause", because the
	// most selective key is the one that spreads the table.
	best := 0.0
	for _, ri := range clauses {
		if ri == nil {
			continue
		}
		// The operand on the INNER side is the one that was hashed.
		key := ri.rightKey
		relids := ri.rightRelids
		if !relsSubset(ri.rightRelids, innerRelids) {
			key, relids = ri.leftKey, ri.leftRelids
		}
		if key == nil || !relsSubset(relids, innerRelids) {
			continue
		}
		v := s.examineJoinVar(key, relids)
		if v.stats == nil && !v.isBool {
			continue
		}
		// `mcv_freq`: the first MCV entry is the most common value
		// (ColumnStats.MCV is stored Frequency-desc, catalog.go:1809).
		mcvFreq := 0.0
		if v.stats != nil && len(v.stats.MCV) > 0 {
			mcvFreq = v.stats.MCV[0].Frequency
		}
		nd, isDefault := getVariableNumDistinct(v)
		if isDefault || nd <= 0 {
			// PG's isdefault arm: Max(0.1, mcv_freq). An explicit decision,
			// not an oversight: the old code SKIPPED a default-ndistinct
			// clause so a guess could never steer the cost, but upstream
			// deliberately steers here — 0.1 discourages hashing a large
			// unknown inner, while a known-hot MCV steers harder. As one
			// smallest-wins candidate it can only lose to a measured
			// selective key; an all-default key set reports PG's 0.1
			// instead of "no information".
			frac := math.Max(0.1, mcvFreq)
			if best == 0 || frac < best {
				best = frac
			}
			continue
		}
		nullFrac := 0.0
		if v.stats != nil {
			nullFrac = v.stats.NullFrac
		}
		// `avgfreq` is over the RAW relation (unscaled ndistinct) — PG
		// computes it before the restriction-clause adjustment below.
		avgFreq := (1.0 - nullFrac) / nd
		// Restriction clauses are assumed to thin rows uniformly: scale the
		// distinct count by filtered/raw. Skipped when either count is
		// unknown (a zero `rows` is a hand-built context without filter
		// info, not a truly empty relation).
		if v.rows > 0 && v.tuples > 0 {
			nd = clampRowEst(nd * v.rows / v.tuples)
		}
		frac := 1.0 / nd
		if avgFreq > 0 && mcvFreq > avgFreq {
			frac *= mcvFreq / avgFreq
		}
		if frac < 1e-6 {
			frac = 1e-6
		} else if frac > 1 {
			frac = 1
		}
		if best == 0 || frac < best {
			best = frac
		}
	}
	return best
}

// mergeJoinScanSel is `mergejoinscansel` (selfuncs.c) reduced to its END
// selectivities: what FRACTION of each input a merge join actually consumes
// before the other side's key range is exhausted. take2 P2-12.
//
// A merge join over `a.k = b.k` stops as soon as one side passes the other's
// maximum key. When the two ranges overlap only partly — a fact-table scan
// joined to a filtered dimension, say — a large share of the bigger input is
// never read, and goopg charged a FULL pass over both.
//
// PG's start selectivities (the "skip to the first match" half) are NOT
// implemented here and both are reported as 0. They are the smaller term, and
// they model a seek goopg's merge does not perform: it consumes from the
// beginning of each sorted input. Reporting 0 makes the omission a no-op rather
// than an approximation.
//
// Returns (1, 1) — charge everything, i.e. today's behaviour — whenever either
// side's range cannot be established. That is the safe direction: this term can
// only ever REDUCE a merge join's cost, so an unknown must not.
//
// P6-08 (take3 08 §9): the two end selectivities are memoised on the clause,
// which is upstream's own arrangement — `mergejoinscansel`'s callers go through
// `cached_scansel` (costsize.c:3798), whose `MergeScanSelCache` list hangs off
// the `RestrictInfo`. The reason is the same in both planners: the estimate is
// re-asked once per merge path considered, the histogram walk behind it is the
// single most expensive thing in the join search on a statistics-bearing
// catalog (measured: `histCmp` 43.7% of planning CPU over TPC-H before this
// cache), and its answer moves with none of what varies between those calls.
//
// The memo is stored in the clause's OWN left/right orientation and rotated at
// return, so both directions of a commuted join share one computation. That is
// sound because the underlying question is symmetric: the pair
// (fraction of left at or below right's max, fraction of right at or below
// left's max) is a property of the clause, and which member is the "outer" end
// is decided entirely by `outerRelids` here at the return.
func (s *searchCtx) mergeJoinScanSel(clauses []*restrictInfo, outerRelids RelSet) (outerEnd, innerEnd float64) {
	if len(clauses) == 0 {
		return 1, 1
	}
	// PG keys the estimate on the FIRST merge clause: it is the leading sort
	// column, and it alone determines when the scan can stop.
	ri := clauses[0]
	if ri == nil || ri.leftKey == nil || ri.rightKey == nil {
		return 1, 1
	}
	// Which operand is on the outer side of THIS join. The left operand wins
	// when it qualifies, exactly as the pre-cache swap did.
	leftIsOuter := relsSubset(ri.leftRelids, outerRelids)
	if !leftIsOuter && !relsSubset(ri.rightRelids, outerRelids) {
		return 1, 1
	}
	leftEnd, rightEnd := s.mergeScanSelForClause(ri)
	if leftIsOuter {
		return leftEnd, rightEnd
	}
	return rightEnd, leftEnd
}

// mergeScanSelForClause is `mergeJoinScanSel`'s body in the clause's own
// orientation: (fraction of the LEFT operand at or below the right operand's
// maximum, fraction of the RIGHT operand at or below the left's). Memoised on
// the `restrictInfo` — PG's `MergeScanSelCache` — under the cache contract
// documented on that struct.
func (s *searchCtx) mergeScanSelForClause(ri *restrictInfo) (leftEnd, rightEnd float64) {
	if ri.scanSelValid {
		return ri.scanSelLeftEnd, ri.scanSelRightEnd
	}
	leftEnd, rightEnd = 1, 1
	lv := s.examineJoinVar(ri.leftKey, ri.leftRelids)
	rv := s.examineJoinVar(ri.rightKey, ri.rightRelids)
	lMax, lOK := histogramMax(lv.stats)
	rMax, rOK := histogramMax(rv.stats)
	if lOK && rOK {
		// The outer is scanned until it passes the INNER's maximum, and vice
		// versa: `leftend = scalarineqsel(left <= right_max)`.
		leftEnd = clampSelectivity(fractionAtMost(lv.stats, rMax, lv.typeName))
		rightEnd = clampSelectivity(fractionAtMost(rv.stats, lMax, rv.typeName))
	}
	if s != nil {
		ri.scanSelLeftEnd, ri.scanSelRightEnd, ri.scanSelValid = leftEnd, rightEnd, true
	}
	return leftEnd, rightEnd
}

// histogramMax is `get_variable_range`'s upper bound: the last histogram
// boundary, which ANALYZE stores in ascending order.
func histogramMax(cs *catalog.ColumnStats) (string, bool) {
	if cs == nil || len(cs.Histogram) == 0 {
		return "", false
	}
	return cs.Histogram[len(cs.Histogram)-1], true
}

// fractionAtMost is `scalarineqsel(<=)` over the column's histogram: the share
// of the column at or below `bound`.
func fractionAtMost(cs *catalog.ColumnStats, bound, typeName string) float64 {
	if cs == nil || len(cs.Histogram) < 2 || typeName == "" {
		// No type means histCmp would compare byte-wise, which misorders every
		// numeric column. Refuse rather than guess.
		return 1
	}
	return histogramOpSelectivity(parser.OpLe, cs.Histogram, bound, typeName)
}
