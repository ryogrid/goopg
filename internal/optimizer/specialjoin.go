package optimizer

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// SpecialJoinInfo records the restriction properties of an outer/semi/anti join
// — PG 18.3's struct of the same name (pathnodes.h:3031-3053). It is built
// bottom-up during jointree deconstruction and stored on the joinlistItem;
// the search consults it via join_is_legal (P1.2+).
//
// PG reference: postgres/src/include/nodes/pathnodes.h:3031-3053.
// Consumer: joinrels.c:350 (join_is_legal), :1066 (have_join_order_restriction).
// Construction: initsplan.c deconstruct_recurse → make_outerjoininfo.

type SpecialJoinInfo struct {
	MinLefthand  RelSet // base+OJ relids in minimum LHS for join
	MinRighthand RelSet // base+OJ relids in minimum RHS for join
	SynLefthand  RelSet // base+OJ relids syntactically within LHS
	SynRighthand RelSet // base+OJ relids syntactically within RHS
	Jointype     parser.JoinType
	Ojrelid      int // outer join's RT index; 0 if none (SEMI/ANTI, or no RT entry yet)

	// commute_above_l/r and commute_below_l/r replace PG≤15's delay_upper_joins
	// flag (removed upstream in PG 18). They record which lower/higher outer
	// joins this one can commute with, discovered bottom-up during
	// make_outerjoininfo's ordered scan of the already-built join_info_list.
	CommuteAboveL RelSet // commuting OJs above this one, if LHS
	CommuteAboveR RelSet // commuting OJs above this one, if RHS
	CommuteBelowL RelSet // commuting OJs in this one's LHS
	CommuteBelowR RelSet // commuting OJs in this one's RHS

	LhsStrict bool // join clause is strict for some LHS rel

	// Semi/anti fields — meaningful only for JOIN_SEMI; populated later (P1.4).
	SemiCanBtree bool        // true if semi_operators are all btree
	SemiCanHash  bool        // true if semi_operators are all hash
	SemiOperators []uint32   // OIDs of equality join operators
	SemiRhsExprs  []Expr     // righthand-side expressions of these ops
}

// makeSpecialJoinInfo builds a SpecialJoinInfo for an outer/semi/anti join
// during jointree deconstruction. It is the goopg analogue of PG's
// make_outerjoininfo (initsplan.c:1707), pared down for P1.1: the full
// commutativity analysis and clause-strictness walk arrive in P1.2 when the
// search actually consults these entries.
//
// M0128-P1.4: populates semi_can_hash/semi_can_btree optimistically from the
// join qual's equality conjuncts. Per-operator checking (PG's
// compute_semijoin_info, initsplan.c) is deferred — ledger row.
//
// left/right are the joinlists for the two sides, and joinQual is the ON/USING
// clause (nil for NATURAL and comma joins — never called for those).
func makeSpecialJoinInfo(jointype parser.JoinType, left, right joinlist, joinQual parser.Expr) *SpecialJoinInfo {
	return makeSpecialJoinInfoScoped(jointype, left, right, joinQual, nil, 0, nil)
}

// C-01 P3-01: name → relid resolution for SpecialJoinInfo population.
//
// makeSpecialJoinInfoScoped is make_outerjoininfo's clause-analysis half
// (initsplan.c:1780-1792, 1794-1808): clause_relids (PG pull_varnos) and
// strict_relids (PG find_nonnullable_rels) over the ON/USING qual, with the
// lower-outer-join ordering scan (initsplan.c:1823-1958, grow steps only).
//
// The blocker the take3 design names (08 §6.1) is that deconstruction runs on
// raw parser.FromExprs: parser.ColumnRef carries names, not relation indexes,
// and the legacy makeSpecialJoinInfo receives no catalog, no bindings and no
// resolver — so every entry degenerated to min = syn. The scope threads
// exactly what planFromItem already resolved against (the current comma item's
// leaves in leaf order, plus the catalog) into deconstruction, so the qual can
// be mapped to leaf bits the same way production resolution maps it.
//
// Safety contract (TODO_ALL.md C-01): min sets only ever SHRINK from syn
// toward PG's values on fully-resolved evidence; ANY uncertainty (unknown
// qualifier, ambiguous/unresolvable column, unhandled expression node,
// subquery/tablefunc/CTE leaves under an unqualified ref) falls back to syn.
// An underestimate would permit a reordering PG forbids (wrong answers); an
// overestimate only withholds one (missed optimisation). LhsStrict likewise
// defaults to false (joinIsLegal's mustBeLeftJoin gate then declines rather
// than allows). FULL keeps PG's exact early-return (min = syn); RIGHT keeps
// min = syn because PG rewrites RIGHT→LEFT before make_outerjoininfo and
// goopg's flat chain can only flip the first join (reduce_outer_joins.go S9.4)
// — mirroring the computation for a surviving deeper RIGHT is future work.
//
// Caller contract: a non-nil scope must carry quals that production
// resolution already accepted (planFromItem aborts on error before
// deconstruction runs in planFromClause) — the qualified arm maps a
// uniquely-matched qualifier straight to its leaf with no column-existence
// re-check. Do not reuse this with pre-validation quals.
//
// Deliberately NOT populated (evidence in the C-01 probe):
//   - Ojrelid stays 0: goopg has no RT indexes for join RTEs (RelSet is
//     base-leaves only); the ojrelid adds in PG's scan have no domain here.
//   - CommuteAbove/Below stay empty: PG keys them by ojrelid (nonexistent
//     here), and no goopg consumer reads them (joinIsLegal,
//     joinOrderRestricted, hasJoinRestriction, buildJoinRelRestrictList
//     consult only Min/Syn/Jointype/LhsStrict). PG's identity-3 shrink step,
//     which feeds those sets, is skipped for the same reason — skipping a
//     shrink is the safe direction.
//   - SemiOperators/SemiRhsExprs stay empty: PG sets them only for SEMI
//     (compute_semijoin_info returns early otherwise), and SEMI never reaches
//     deconstruction — the parser produces only INNER/LEFT/RIGHT/FULL/CROSS
//     from SQL (select.go:1246); SEMI/ANTI are planner-internal, and only
//     ANTI arrives here (via reduceOuterJoins LEFT→ANTI demotion, which runs
//     before deconstruction in planFromClause).
//   - PlaceHolderVar handling is vacuous: goopg has no placeholder machinery.
//   - PG's FOR UPDATE-on-nullable-side error is not replicated: goopg plans
//     row marks without that check (planner.go:1874+) — a pre-existing,
//     orthogonal gap, not SJI population.
// reduceRightLink is `reduce_outer_joins`' RIGHT→LEFT flip
// (prepjointree.c:3360-3376) applied to ONE link's sides rather than to the
// jointree: `left RIGHT JOIN right` is `right LEFT JOIN left`, so the reduced
// link is a LEFT join whose preserved side is the syntactic RIGHT input and
// whose nullable side is the syntactic LEFT input. C-04b.
//
// Both producers of a link description go through it — `makeSpecialJoinInfoScoped`
// for `root->join_info_list` and `extractSearchLeaves` for the seam's
// `outerChainLink` — so the two cannot disagree about which side a RIGHT link
// null-extends; `outerLinksHaveSJInfos` matches them hand for hand.
func reduceRightLink(left, right RelSet) (jointype parser.JoinType, preserved, nullable RelSet) {
	return parser.JoinLeft, right, left
}

func makeSpecialJoinInfoScoped(jointype parser.JoinType, left, right joinlist, joinQual parser.Expr, sc *sjiScope, item int, lower []*SpecialJoinInfo) *SpecialJoinInfo {
	sj := &SpecialJoinInfo{
		SynLefthand:  joinlistRelSet(left),
		SynRighthand: joinlistRelSet(right),
		Jointype:     jointype,
		// ojrelid stays 0 until goopg grows RT indexes for join RTEs.
	}

	// FULL: min = syn, by definition (PG's make_outerjoininfo returns early
	// for FULL with this exact assignment — initsplan.c:1772-1778).
	if jointype == parser.JoinFull {
		sj.MinLefthand = sj.SynLefthand
		sj.MinRighthand = sj.SynRighthand
		return sj
	}

	// RIGHT: PG never builds one. `reduce_outer_joins` rewrites JOIN_RIGHT as
	// JOIN_LEFT by swapping the JoinExpr's arms before deconstruction
	// (prepjointree.c:3360-3376), `deconstruct_recurse` has no JOIN_RIGHT arm
	// (initsplan.c:1403) and `make_outerjoininfo` asserts it never sees one
	// (initsplan.c:1728). goopg's S9.4 flip can swap only the FIRST link of a
	// chain — `parser.FromExpr` is a flat left-deep list and cannot spell a
	// nested right arm — so a deeper RIGHT reaches here as itself, and C-04b
	// performs the same reduction on the SJI's HANDS instead of on the tree:
	// the link's syntactic left side is its nullable side, which is a LEFT
	// join's SynRighthand. Everything downstream (`join_is_legal`'s LEFT-only
	// association rule, `jointypeForDirection`, the executor's LEFT arms) then
	// sees exactly the SJI PG would have built. The reduction is spelled once,
	// in `reduceRightLink`, because the seam has to apply it to the plan-side
	// link in the same way for `outerLinksHaveSJInfos` to match the two.
	//
	// min = syn, deliberately. The reduced RHS is the whole left prefix, which
	// may contain inner joins, and PG's min_righthand includes
	// inner_join_rels precisely so the outer join cannot commute with any of
	// them (initsplan.c:1804-1805); the LEFT narrowing below assumes an
	// inner-join-free RHS (see the note there) and does not apply. The LHS is
	// a single leaf, so min = syn is exact for it. LhsStrict stays false
	// (declines LHS-strict association = safe).
	if jointype == parser.JoinRight {
		sj.Jointype, sj.SynLefthand, sj.SynRighthand = reduceRightLink(sj.SynLefthand, sj.SynRighthand)
		sj.MinLefthand = sj.SynLefthand
		sj.MinRighthand = sj.SynRighthand
		return sj
	}

	// LEFT/SEMI/ANTI general path. Without a scope there is no resolver, so
	// min = syn (the legacy conservative overestimate).
	if sc == nil {
		sj.MinLefthand = sj.SynLefthand
		sj.MinRighthand = sj.SynRighthand
	} else if clause, strict, ok := sjiClauseRelids(joinQual, sc, item); ok {
		sj.LhsStrict = relsOverlap(strict, sj.SynLefthand)
		minL := clause & sj.SynLefthand
		// inner_join_rels (PG's third min_righthand input) is provably empty
		// here: goopg's chain is strictly left-deep with a single fresh base
		// leaf on the right (collapse.go deconstructFromItem), and a fresh
		// leaf cannot participate in an inner join below. Subquery-internal
		// joins live in that subquery's own deconstruction.
		minR := clause & sj.SynRighthand
		// Lower-outer-join ordering scan (initsplan.c:1823-1958), grow steps
		// only: FULL barrier expansion and the LHS/RHS preserve-ordering
		// adds. Identity-3 removal is skipped (shrink = unsafe direction to
		// get wrong; the commute sets it feeds are unpopulated — see above).
		for _, other := range lower {
			if other.Jointype == parser.JoinFull {
				// A full join is an optimisation barrier (initsplan.c:1829).
				if relsOverlap(sj.SynLefthand, other.SynLefthand|other.SynRighthand) {
					minL |= other.SynLefthand | other.SynRighthand
				}
				if relsOverlap(sj.SynRighthand, other.SynLefthand|other.SynRighthand) {
					minR |= other.SynLefthand | other.SynRighthand
				}
				continue
			}
			// Lower OJ in our LHS (initsplan.c:1909-1933, preserve arm).
			if relsOverlap(sj.SynLefthand, other.SynRighthand) {
				if relsOverlap(clause, other.SynRighthand) &&
					(jointype == parser.JoinSemi || jointype == parser.JoinAnti ||
						!relsOverlap(strict, other.MinRighthand)) {
					minL |= other.SynLefthand | other.SynRighthand
				}
			}
			// Lower OJ in our RHS (initsplan.c:1951-1990, preserve arm).
			if relsOverlap(sj.SynRighthand, other.SynRighthand) {
				if relsOverlap(clause, other.SynRighthand) ||
					!relsOverlap(clause, other.MinLefthand) ||
					jointype == parser.JoinSemi || jointype == parser.JoinAnti ||
					other.Jointype == parser.JoinSemi || other.Jointype == parser.JoinAnti ||
					!other.LhsStrict {
					minR |= other.SynLefthand | other.SynRighthand
				}
			}
		}
		// PG's punt (initsplan.c:2007-2013): an empty min side means "no
		// constraint found", not "no relations required" — fall back to the
		// full syntactic side. This is also what makes a nil/USING qual
		// (no ColumnRefs) safely land on syn.
		if minL == 0 {
			minL = sj.SynLefthand
		}
		if minR == 0 {
			minR = sj.SynRighthand
		}
		sj.MinLefthand = minL
		sj.MinRighthand = minR
	} else {
		// Unresolvable qual: syn fallback (safe default), LhsStrict stays
		// false (safe default).
		sj.MinLefthand = sj.SynLefthand
		sj.MinRighthand = sj.SynRighthand
	}

	// M0128-P1.4: populate semi fields for SEMI joins (PG's
	// compute_semijoin_info sets them only for JOIN_SEMI — initsplan.c). The
	// legacy code set them optimistically for ANTI too; PG leaves ANTI at
	// false/false, so C-01 aligns ANTI with upstream. Both flags remain
	// unread by path generation (splitJoinClauses filters per-operator), and
	// SEMI never reaches deconstruction (see above), so this is inert today.
	if jointype == parser.JoinSemi {
		sj.SemiCanBtree, sj.SemiCanHash = semiQualCapabilities(joinQual)
	}

	return sj
}

// sjiLeaf is one FROM range variable in deconstruct leaf order.
type sjiLeaf struct {
	alias  string
	name   string
	schema string // RangeVar schema, or the catalog table's for base relations
	table  *catalog.Table // nil for subquery/tablefunc/CTE/shadowed/unknown
}

// sjiScope is the name → leaf map threaded into deconstruction (take3 08
// §6.1, first route: the smaller one, keeping phase order). leaves holds every
// comma item's range variables consecutively in the same depth-first order
// deconstructJointree numbers leaves in (Base, then Joins[0].Right, …), so a
// scope index IS a leaf/RelSet bit. items bounds each FromExpr's slice.
type sjiScope struct {
	leaves   []sjiLeaf
	items    [][2]int // per-FromExpr half-open leaf range
	cat      catalog.Catalog
	tableMap map[string]*catalog.Table // qualifier → table, for strictness
}

// newSjiScope builds the resolution scope for a statement's FROM clause. cat
// may be nil (tests): then every leaf is table-less — qualified refs still
// resolve structurally, unqualified ones fall back to syn.
func newSjiScope(from []parser.FromExpr, cat catalog.Catalog) *sjiScope {
	sc := &sjiScope{cat: cat, tableMap: make(map[string]*catalog.Table)}
	for i := range from {
		start := len(sc.leaves)
		sc.addLeaf(from[i].Base, cat)
		for _, j := range from[i].Joins {
			sc.addLeaf(j.Right, cat)
		}
		sc.items = append(sc.items, [2]int{start, len(sc.leaves)})
	}
	return sc
}

// addLeaf appends one range variable's leaf metadata. Catalog lookup mirrors
// planScanRangeVar's precedence: subqueries/tablefuncs never hit the catalog,
// and a schemeless name owned by a CTE (lookupPlannedCTE, with.go:428 — the
// same state planScanRangeVar consults) must not resolve to a same-named
// catalog table, or unqualified attribution could map a CTE column onto the
// wrong leaf. A missed lookup leaves table nil, which only ever widens toward
// syn — never narrows.
func (sc *sjiScope) addLeaf(rv parser.RangeVar, cat catalog.Catalog) {
	lf := sjiLeaf{alias: rv.Alias, name: rv.Name, schema: rv.Schema}
	if rv.Subquery == nil && rv.TableFunc == nil && cat != nil {
		if rv.Schema == "" && lookupPlannedCTE(rv.Name) != nil {
			// CTE-owned name: structural (alias) matching only.
		} else if tbl, ok := cat.LookupTable(parser.ObjectName{Schema: rv.Schema, Name: rv.Name}); ok {
			lf.table = tbl
			lf.schema = tbl.Schema
		}
	}
	if lf.table != nil {
		key := lf.alias
		if key == "" {
			key = lf.name
		}
		sc.tableMap[key] = lf.table
	}
	sc.leaves = append(sc.leaves, lf)
}

// sjiLeafMatchesQualifier mirrors bindingMatchesRelation (planner.go:15188):
// an aliased relation is referenceable ONLY by its alias (EqualFold);
// otherwise by its relation name; a written schema must match the leaf's.
func sjiLeafMatchesQualifier(lf sjiLeaf, table, schema string) bool {
	if schema != "" && !strings.EqualFold(schema, lf.schema) {
		return false
	}
	if lf.alias != "" {
		return strings.EqualFold(table, lf.alias)
	}
	return strings.EqualFold(table, lf.name)
}

// resolveSjiColumn maps one ON-clause ColumnRef to its leaf bit, mirroring
// production resolution (resolveColumnRefAt, planner.go:14899) restricted to
// the current comma item's scope — which is exactly the scope planFromItem
// resolves that ON against (mergedCtx, planner.go:2945: left leaves plus the
// current right leaf; lateral/outer scopes excluded). ok=false on ANY
// uncertainty (no match, ambiguous match, table-less leaf under an
// unqualified ref): the caller falls back to syn.
func resolveSjiColumn(ref *parser.ColumnRef, sc *sjiScope, item int) (int, bool) {
	rng := sc.items[item]
	if ref.Table != "" || ref.Schema != "" {
		match := -1
		for i := rng[0]; i < rng[1]; i++ {
			if !sjiLeafMatchesQualifier(sc.leaves[i], ref.Table, ref.Schema) {
				continue
			}
			if match != -1 {
				return 0, false // ambiguous qualifier; production errors
			}
			match = i
		}
		if match == -1 {
			return 0, false
		}
		// No column-existence re-check: production already resolved this
		// exact ON successfully (planFromItem aborts on error before
		// deconstruction runs), so a uniquely-matched qualifier IS the
		// production leaf. System columns (tableoid/ctid) and whole-row
		// refs flow through the same arm — pull_varnos counts the rel.
		return match, true
	}
	// Unqualified: mirror the unqualified branch (planner.go:15017-15087) —
	// column scan first, then the single-candidate tableoid/ctid and
	// whole-row-alias rules. A table-less leaf (subquery/tablefunc/CTE) in
	// scope makes the column scan inconclusive: it might hold the column,
	// so any attribution is a guess → syn fallback. (USING-hidden columns
	// likewise bail via the multi-candidate arm — safe.)
	for i := rng[0]; i < rng[1]; i++ {
		if sc.leaves[i].table == nil {
			return 0, false
		}
	}
	match := -1
	for i := rng[0]; i < rng[1]; i++ {
		if _, ok := sc.cat.LookupColumn(sc.leaves[i].table, ref.Column); !ok {
			continue
		}
		if match != -1 {
			return 0, false // ambiguous; production errors
		}
		match = i
	}
	if match != -1 {
		return match, true
	}
	// Whole-row alias match (planner.go:15088): bare name equals a binding
	// alias (or the unaliased table name) — a composite row Var on that rel.
	for i := rng[0]; i < rng[1]; i++ {
		name := sc.leaves[i].alias
		if name == "" {
			name = sc.leaves[i].name
		}
		if strings.EqualFold(ref.Column, name) {
			if match != -1 {
				return 0, false
			}
			match = i
		}
	}
	if match == -1 {
		return 0, false
	}
	return match, true
}

// sjiQualRelids is pull_varnos (PG: which base relids the qual mentions) over
// the ON expression, with a strict whitelist: transparent scalar containers
// are descended, constants contribute nothing, and EVERYTHING else (sublinks,
// array constructors/subscripts, EXTRACT, aggregates/windows, …) bails to
// ok=false. Bailing is always safe (syn fallback); silently skipping a node
// that hides a ColumnRef would underestimate.
func sjiQualRelids(e parser.Expr, sc *sjiScope, item int) (RelSet, bool) {
	if e == nil {
		return 0, true
	}
	switch x := e.(type) {
	case *parser.ColumnRef:
		leaf, ok := resolveSjiColumn(x, sc, item)
		if !ok {
			return 0, false
		}
		if leaf >= maxSearchRels {
			// Same producer invariant as joinlistRelSet: beyond the RelSet
			// ceiling the bit is unrepresentable; the side's syn set drops
			// it identically, so intersecting later stays consistent.
			return 0, true
		}
		return 1 << leaf, true
	case *parser.BinaryOp:
		l, ok := sjiQualRelids(x.Left, sc, item)
		if !ok {
			return 0, false
		}
		r, ok := sjiQualRelids(x.Right, sc, item)
		if !ok {
			return 0, false
		}
		return l | r, true
	case *parser.UnaryOp:
		return sjiQualRelids(x.Operand, sc, item)
	case *parser.IsNullExpr:
		return sjiQualRelids(x.Operand, sc, item)
	case *parser.IsBoolExpr:
		return sjiQualRelids(x.Operand, sc, item)
	case *parser.IsDistinctFromExpr:
		l, ok := sjiQualRelids(x.Left, sc, item)
		if !ok {
			return 0, false
		}
		r, ok := sjiQualRelids(x.Right, sc, item)
		if !ok {
			return 0, false
		}
		return l | r, true
	case *parser.CastExpr:
		return sjiQualRelids(x.Operand, sc, item)
	case *parser.CollateExpr:
		return sjiQualRelids(x.Operand, sc, item)
	case *parser.FuncCall:
		if x.Over != nil || x.Filter != nil || len(x.OrderBy) > 0 || len(x.WithinGroup) > 0 {
			return 0, false
		}
		var rs RelSet
		for _, a := range x.Args {
			r, ok := sjiQualRelids(a, sc, item)
			if !ok {
				return 0, false
			}
			rs |= r
		}
		return rs, true
	case *parser.CaseExpr:
		var rs RelSet
		if x.Operand != nil {
			r, ok := sjiQualRelids(x.Operand, sc, item)
			if !ok {
				return 0, false
			}
			rs |= r
		}
		for _, w := range x.Whens {
			r, ok := sjiQualRelids(w.When, sc, item)
			if !ok {
				return 0, false
			}
			rs |= r
			r, ok = sjiQualRelids(w.Then, sc, item)
			if !ok {
				return 0, false
			}
			rs |= r
		}
		if x.Else != nil {
			r, ok := sjiQualRelids(x.Else, sc, item)
			if !ok {
				return 0, false
			}
			rs |= r
		}
		return rs, true
	case *parser.RowExpr:
		var rs RelSet
		for _, el := range x.Elems {
			r, ok := sjiQualRelids(el, sc, item)
			if !ok {
				return 0, false
			}
			rs |= r
		}
		return rs, true
	case *parser.InExpr:
		// List-form IN (…)/ANY (ARRAY[…]) only; a subquery arm bails.
		if x.Subquery != nil {
			return 0, false
		}
		var rs RelSet
		r, ok := sjiQualRelids(x.Operand, sc, item)
		if !ok {
			return 0, false
		}
		rs |= r
		for _, el := range x.List {
			r, ok := sjiQualRelids(el, sc, item)
			if !ok {
				return 0, false
			}
			rs |= r
		}
		return rs, true
	case *parser.IntegerConst, *parser.StringConst, *parser.NumericConst,
		*parser.NullConst, *parser.BooleanConst, *parser.ParamRef,
		*parser.TypedStringLit, *parser.IntervalLit, *parser.DefaultMarker,
		*parser.StarExpr:
		return 0, true
	default:
		return 0, false
	}
}

// sjiClauseRelids computes the (clause_relids, strict_relids) pair PG's
// make_outerjoininfo derives via pull_varnos + find_nonnullable_rels
// (initsplan.c:1780-1792). Strictness reuses the existing goopg analogue
// collectNonNullableTableNames (reduce_outer_joins.go:268 — comparison fast
// path plus catalog proisstrict, conservative elsewhere), translated from
// table names to leaf bits through the scope. The translation can only
// UNDER-approximate strict (unresolvable or ambiguous refs are skipped by the
// collector; unmapped names are ignored), and under-strict is the safe
// direction: LhsStrict false and the preserve-ordering arms firing are what
// withhold reorderings, never what permit them. Unqualified refs owned by
// exactly one scope table resolve through the catalog (unique ownership).
func sjiClauseRelids(qual parser.Expr, sc *sjiScope, item int) (clause, strict RelSet, ok bool) {
	clause, ok = sjiQualRelids(qual, sc, item)
	if !ok {
		return 0, 0, false
	}
	rng := sc.items[item]
	for name := range collectNonNullableTableNames(qual, sc.tableMap, sc.cat) {
		match := -1
		for i := rng[0]; i < rng[1]; i++ {
			lf := sc.leaves[i]
			key := lf.alias
			if key == "" {
				key = lf.name
			}
			if strings.EqualFold(name, key) {
				if match != -1 {
					match = -2
					break
				}
				match = i
			}
		}
		if match >= 0 {
			if match >= maxSearchRels {
				continue // same producer ceiling as the clause arm
			}
			strict |= 1 << match
		}
		// match -1 (outer-scope name) or -2 (ambiguous duplicate
		// alias/name keys across comma items): ignored — both are
		// under-strict = safe.
	}
	return clause, strict, true
}

// semiQualCapabilities scans a join qual for equality operators and returns
// whether the qual supports btree (merge) and hash join methods. P1.4:
// optimistically returns true when any equality conjunct is found; per-operator
// specificity (PG's compute_semijoin_info) is deferred.
func semiQualCapabilities(qual parser.Expr) (canBtree bool, canHash bool) {
	if qual == nil {
		return false, false
	}
	if hasEqualityOperator(qual) {
		return true, true
	}
	return false, false
}

// hasEqualityOperator walks a parser expression tree looking for an equality
// operator (=), descending into both arms of AND/OR and BinaryOp.
func hasEqualityOperator(e parser.Expr) bool {
	if e == nil {
		return false
	}
	switch x := e.(type) {
	case *parser.BinaryOp:
		if x.Op == parser.OpEq {
			return true
		}
		return hasEqualityOperator(x.Left) || hasEqualityOperator(x.Right)
	}
	return false
}

// joinlistRelSet returns the set of base-relation FROM-item indices covered by
// a joinlist, as a RelSet bitmask. It is the goopg analogue of the Relids
// (Bitmapset) computation PG does during deconstruct_recurse for
// left_rels/right_rels.
func joinlistRelSet(jl joinlist) RelSet {
	var rs RelSet
	for _, leaf := range jl.leaves(nil) {
		if leaf >= maxSearchRels {
			// A FROM clause with more base relations than RelSet can
			// represent would overflow the mask. The search carries the
			// same ceiling by construction, so this is a producer
			// invariant check. take2 P3-09.
			continue
		}
		rs |= 1 << leaf
	}
	return rs
}

// collectSpecialJoinInfos walks a joinlist and appends every SpecialJoinInfo
// to dst in bottom-up (post-order) order — the same order PG's
// root->join_info_list is built in, which is the order join_is_legal's
// commutativity scan depends on.
func (jl joinlist) collectSpecialJoinInfos(dst []*SpecialJoinInfo) []*SpecialJoinInfo {
	for _, it := range jl {
		if it.sub != nil {
			dst = it.sub.collectSpecialJoinInfos(dst)
		}
		if it.sjinfo != nil {
			dst = append(dst, it.sjinfo)
		}
	}
	return dst
}
