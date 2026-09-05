package optimizer

import (
	"github.com/goopg/goopg/internal/parser"
)

// planToParserJoinType maps a plan-tree join type onto the parser jointype
// domain SpecialJoinInfo speaks. The two enums name the same six shapes;
// LATERAL is not a type in either (it is a Join flag, declined upstream
// of delay).
func planToParserJoinType(t JoinType) parser.JoinType {
	switch t {
	case JoinTypeLeft:
		return parser.JoinLeft
	case JoinTypeRight:
		return parser.JoinRight
	case JoinTypeFull:
		return parser.JoinFull
	case JoinTypeCross:
		return parser.JoinCross
	case JoinTypeSemi:
		return parser.JoinSemi
	case JoinTypeAnti:
		return parser.JoinAnti
	default:
		return parser.JoinInner
	}
}

// outputRelSet unions the SourceTableIdx identities over a plan node's
// output schema into relset bits (srcIdx 1 → bit 0, …). ok=false on ANY
// gap: unknown (0) identities, identities past maxSearchRels, or an empty
// schema — the caller then keeps the legacy verdict and consults no
// delay proof. SourceTableIdx is the stable remap key (remapWithBindings
// derives Index FROM it), so this attribution survives every rewrite the
// copy pass runs after.
func outputRelSet(out Schema) (RelSet, bool) {
	if len(out) == 0 {
		return 0, false
	}
	var rs RelSet
	for _, c := range out {
		if c.SourceTableIdx <= 0 || int(c.SourceTableIdx) > maxSearchRels {
			return 0, false
		}
		rs |= 1 << (c.SourceTableIdx - 1)
	}
	return rs, true
}

// qualSrcRelSet attributes a conjunct to source-table identities via its
// ColumnRefs' SourceTableIdx. ok=false on anything the attribution cannot
// see exactly: unknown (0) identities, out-of-range identities,
// OuterColumnRef (outer scope — the pass declines these by hand already),
// FuncCall (the pass declines these for volatility), and any walk abort
// (sublinks via scopeVeto, unenumerated kinds — fail-closed). Constants
// contribute nothing, so an all-constant qual yields (0, true) —
// vacuously attributable — while an empty OUTPUT schema is
// unattributable (no identity to speak of): the asymmetry is deliberate.
// ok=false keeps the legacy verdict; it never allows a copy the legacy
// pass would decline.
func qualSrcRelSet(c Expr) (RelSet, bool) {
	var rs RelSet
	bad := false
	okWalk := walkExprRefs(c, scopeVeto, exprVisitor{
		Visit: func(e Expr) bool {
			if bad {
				return false
			}
			switch x := e.(type) {
			case *OuterColumnRef, *FuncCall:
				_ = x
				bad = true
				return false
			case *ColumnRef:
				if x.SourceTableIdx <= 0 || int(x.SourceTableIdx) > maxSearchRels {
					bad = true
					return false
				}
				rs |= 1 << (x.SourceTableIdx - 1)
			}
			return true
		},
	})
	if !okWalk || bad {
		return 0, false
	}
	return rs, true
}

// planJoinDelaySJI builds the plan-local SpecialJoinInfo for the delay
// test at one plan-tree join (C-02b infra; first consumer is the C-02c/d
// move logic): syntactic hands from the two inputs' Output identities,
// Min=Syn (delay consults syntactic sides only — null extension applies
// to the whole syntactic side), type mapped from the plan join type.
// ok=false when either side's attribution is incomplete; the caller then
// keeps the legacy verdict. No joinlist alignment is needed:
// null-extension sides come from the plan node's own type and outputs.
// NOTE (review): wiring this into the COPY pass is vacuous — the legacy
// side gates already decline every nullable-side qual, so a delay check
// there can only fire on Index-vs-SourceTableIdx disagreement. The proof
// becomes load-bearing at the MOVES (C-02c/d), where dropping the
// residual needs a positive placeability proof the legacy declines
// cannot supply.
func planJoinDelaySJI(j *Join) (*SpecialJoinInfo, bool) {
	left, ok := outputRelSet(j.Left.Output())
	if !ok {
		return nil, false
	}
	right, ok := outputRelSet(j.Right.Output())
	if !ok {
		return nil, false
	}
	jt := planToParserJoinType(j.Type)
	return &SpecialJoinInfo{
		SynLefthand:  left,
		SynRighthand: right,
		MinLefthand:  left,
		MinRighthand: right,
		Jointype:     jt,
	}, true
}

// C-02a (P3-02): the per-link delay test for qual placement.
//
// delayedAboveOJ reports whether a qual coming from ABOVE the outer-join
// link described by sj must be delayed to (at or above) that link rather
// than evaluated below it: delay iff the qual's relids reach the link's
// NULLABLE side, where evaluating it would test NULL-extended rows as if
// they were base rows (`WHERE a.x IS NULL` pushed below a RIGHT link).
//
// This is goopg's reduction of PG 18's `outerjoin_nonnullable` /
// `incompatible_relids` test
// (postgres/src/backend/optimizer/plan/initsplan.c:2400-2830).
// `check_outerjoin_delay` no longer exists upstream (removed in the
// nullingrels rework); strictness does NOT exempt a qual here — strictness
// feeds `reduceOuterJoins` demotion separately, which runs before
// deconstruction, so every link this ever sees is a surviving outer link.
//
// The test is per-link; callers apply it conjunctively over the ENTIRE
// crossed path (one delay verdict anywhere stops the descent and the
// residual keeps the conjunct). Nil sj = delay (fail-closed). FULL always
// delays in practice (both sides nullable). SEMI/ANTI delay fail-closed —
// the copy pass declines them before consulting delay anyway.
func delayedAboveOJ(qual RelSet, sj *SpecialJoinInfo) bool {
	if sj == nil {
		return true
	}
	var nullable RelSet
	switch sj.Jointype {
	case parser.JoinLeft:
		nullable = sj.SynRighthand
	case parser.JoinRight:
		nullable = sj.SynLefthand
	case parser.JoinFull:
		nullable = sj.SynLefthand | sj.SynRighthand
	case parser.JoinInner, parser.JoinCross:
		return false
	default:
		// SEMI/ANTI and anything future: fail-closed. Neither null-extends
		// today, but no caller descends into them, so delay costs nothing
		// and a future null-extending type defaults safe.
		return true
	}
	return qual&nullable != 0
}
