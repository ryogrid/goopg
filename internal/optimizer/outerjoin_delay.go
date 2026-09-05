package optimizer

import (
	"github.com/goopg/goopg/internal/parser"
)

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
