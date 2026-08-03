package planner

// M0127-P5.4b-i — the parameterised-path DISCIPLINE (leftdeep-joins 03 §9), the
// rules that must be in place *before* a parameterised path may enter a
// pathlist. The paths themselves — NLI and Memoize (03 §5.2) — are P5.4b-ii's.
//
// The ordering is the point, not an accident of scheduling. A parameterised
// path is not a substitute for an unparameterised one: it only produces rows
// once some specific outer relation supplies its parameter. Every consumer that
// treats a pathlist as "ways to produce this relation" is therefore wrong for
// such a path unless it has been taught otherwise, and the failure mode is not
// a bad plan but an unbuildable one. The three rules of 03 §9 are the teaching,
// and each has a distinct home:
//
//	rule 1 — selection: setCheapest (path.go) picks cheapest-startup/total among
//	         UNPARAMETERISED paths only, and keeps the parameterised survivors
//	         in CheapestParameterized. Landed with this slice.
//	rule 2 — consumption: hash and merge inputs refuse a path parameterised by
//	         the rel they are being joined to — PG's PATH_PARAM_BY_REL
//	         (joinpath.c:43-47, used at :1398 for merge and :2297 for hash).
//	         `pathParamByRel` below; applied in joinpaths.go.
//	rule 3 — cardinality: a parameterised path carries its own row count
//	         (PG's ppi_rows) in Path.Rows, so the cost primitives must read the
//	         CHILD PATH's Rows and never child.Rel.Rows. See Path.Rows's comment
//	         and pathgen.go.
//
// And the fourth thing, which 03 §9 does not enumerate because in PG it is
// simply how the constructors work: a join path computes its OWN
// parameterisation from its children's. `calc_nestloop_required_outer`
// (pathnode.c:2592) is the interesting one — a nested loop SATISFIES an inner
// parameterised by the outer, so the join above it is *less* parameterised than
// its inner child, and frequently unparameterised outright.

// pathParamByRel is PG's PATH_PARAM_BY_REL macro (joinpath.c:46): does this
// path's parameterisation mention (any part of) `rel`?
//
// PG's macro is a disjunction with PATH_PARAM_BY_PARENT, which covers
// partitionwise joins where a child path is parameterised by the *top* parent's
// relids rather than the child's. goopg's search has no partitionwise
// counterpart — every relid in a RelSet is a top-level base relation — so the
// self test is the whole test here.
func pathParamByRel(p *Path, rel *RelOptInfo) bool {
	if p == nil || rel == nil {
		return false
	}
	return p.RequiredOuter&rel.Relids != 0
}

// calcNestloopRequiredOuter is `calc_nestloop_required_outer` (pathnode.c:2592):
// the parameterisation a nested loop still needs from ABOVE, given what its two
// children need.
//
// The rule that makes NLI work: the inner may require rels from the outer, and
// the nested loop is precisely the operator that supplies them, so those
// requirements are discharged here and must be subtracted. An index path over
// `b` parameterised by `a`, joined to `a` by a nested loop, yields a join path
// that is not parameterised at all — which is why it can then be handed to a
// hash join above without tripping rule 2.
//
// The converse never holds: an outer cannot require rels from the inner (the
// inner is rescanned per outer row, so it has produced nothing yet when the
// outer is evaluated). PG asserts it; the callers here enforce it by refusing
// such a pair outright (joinpaths.go), so it is a caller precondition rather
// than a silently-corrected input.
func calcNestloopRequiredOuter(outerRelids, outerParam, innerRelids, innerParam RelSet) RelSet {
	_ = innerRelids // present to mirror PG's signature and its assertion's operands
	if innerParam == 0 {
		return outerParam
	}
	return (outerParam | innerParam) &^ outerRelids
}

// calcNonNestloopRequiredOuter is `calc_non_nestloop_required_outer`
// (pathnode.c:2618) for a merge or hash join: neither side can supply the
// other's parameters — a hash build is materialised whole before the probe
// begins, so there is no per-outer-row binding to discharge — and the union
// therefore passes straight through to the join.
func calcNonNestloopRequiredOuter(outer, inner *Path) RelSet {
	var required RelSet
	if outer != nil {
		required |= outer.RequiredOuter
	}
	if inner != nil {
		required |= inner.RequiredOuter
	}
	return required
}
