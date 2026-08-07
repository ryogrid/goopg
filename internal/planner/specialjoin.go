package planner

import (
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
// left/right are the joinlists for the two sides, and joinQual is the ON/USING
// clause (nil for NATURAL and comma joins — never called for those).
func makeSpecialJoinInfo(jointype parser.JoinType, left, right joinlist, joinQual parser.Expr) *SpecialJoinInfo {
	sj := &SpecialJoinInfo{
		SynLefthand:  joinlistRelSet(left),
		SynRighthand: joinlistRelSet(right),
		Jointype:     jointype,
		// ojrelid stays 0 until goopg grows RT indexes for join RTEs.
	}

	// FULL: min = syn, by definition (PG's make_outerjoininfo returns early
	// for FULL with this exact assignment — initsplan.c:1772-1778).
	//
	// LEFT/SEMI/ANTI: the true min_{left,right}hand requires clause analysis
	// (which rels the qual actually mentions, which are strict). P1.2 adds
	// that; for P1.1 the syn_ sets are a conservative overestimate that keeps
	// the pin in force — the search will never consult these entries.
	if jointype == parser.JoinFull {
		sj.MinLefthand = sj.SynLefthand
		sj.MinRighthand = sj.SynRighthand
	} else {
		// LEFT/SEMI/ANTI: for now, min = the side that cannot lose rows.
		// LEFT: left side is preserved, right side is nullable → min_righthand
		//   must include right-side rels named in the qual (which is all of
		//   them until we analyse the qual). Conservatively: min = syn for
		//   both sides.
		// SEMI/ANTI: similar to LEFT.
		sj.MinLefthand = sj.SynLefthand
		sj.MinRighthand = sj.SynRighthand
	}

	return sj
}

// joinlistRelSet returns the set of base-relation FROM-item indices covered by
// a joinlist, as a RelSet bitmask. It is the goopg analogue of the Relids
// (Bitmapset) computation PG does during deconstruct_recurse for
// left_rels/right_rels.
func joinlistRelSet(jl joinlist) RelSet {
	var rs RelSet
	for _, leaf := range jl.leaves(nil) {
		if leaf >= 16 {
			// RelSet is uint16; a FROM clause with ≥16 base relations
			// would overflow. The search itself has the same ceiling
			// (maxSearchRels=14), so this is a producer invariant check.
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
