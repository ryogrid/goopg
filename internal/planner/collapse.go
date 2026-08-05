package planner

// M0127-P5.8 — what enters ONE join search problem: collapse limits with PG's
// actual semantics.
//
// Design: leftdeep-joins 03 §6 (this file's contract), 03 §7 (the search-size
// ceiling), 03 §4.4 (why outer joins stay pinned), 08 §2 (the sub-flag).
// Upstream: `deconstruct_recurse` (initsplan.c:1148-1452) — the joinlist half
// of `deconstruct_jointree`, which is the ONLY thing this file ports. Qual
// distribution, join domains and `SpecialJoinInfo` construction, the other
// three quarters of that function, are not here (see "What is NOT ported").
//
// # The finding: `join_collapse_limit` is not a search-size cap
//
// Both limits have been registered GUCs since M0097-0069
// (`config/defaults.go:1060`/`:1066`, boot value 8) and read by nothing, and
// the obvious way to "wire them up" — cap the number of relations the DP is
// allowed to enumerate over — is the one reading upstream does NOT have. PG
// applies them to the JOIN TREE's SHAPE, before any rel exists:
//
//   - a flat comma-FROM list is ALWAYS one search problem, no matter how wide.
//     Every single-baserel FROM item collapses into the parent joinlist
//     unconditionally (`sub_members <= 1`, initsplan.c:1233-1238), so
//     `FROM a,b,…,o` is one 15-way DP in PG with both limits at their default
//     8. `from_collapse_limit` governs merging of pulled-up SUB-JOINLISTS —
//     items that are themselves multi-relation problems — and nothing else;
//   - `join_collapse_limit` governs explicit `JOIN` constructs only.
//
// That distinction is load-bearing for goopg specifically, because reading the
// limits as a search-size cap would have re-introduced the greedy pre-reorder
// for wide comma lists, which is the documented Q2 failure mode (03 §6). A cap
// belongs at the `RelSet` ceiling (`maxSearchRels`, 03 §7), which is a
// representation limit, not a user knob.
//
// # =1 is a pin, and it is a pin only three relations in
//
// `join_collapse_limit = 1` is PG's standard "plan my JOINs in the order I
// wrote them" escape hatch, which goopg has never had. Following the recursion
// shows the pin is weaker than the folklore: at a two-way `a JOIN b` the
// "cannot combine" branch emits `[a, b]` — IDENTICAL to the collapsed answer,
// because unwrapping a one-element side (initsplan.c:1428-1436, "avoid creating
// useless 1-element sublists") leaves both members at top level. The pin first
// bites at the third relation: `(a JOIN b) JOIN c` emits `[[a,b], c]`, forcing
// a∪b to be built before c is joined. So =1 restricts the ORDER of the
// syntactic tree's own nodes, not the commutation of any single join.
//
// # Outer joins are pinned harder than PG pins them, on purpose
//
// PG flattens LEFT/SEMI/ANTI into the enclosing problem and constrains the
// resulting search with `SpecialJoinInfo` ordering restrictions; only FULL
// forces its order outright (initsplan.c:1414-1418). goopg v1 has no
// `join_is_legal` constraint inference (03 §4.4), so a flattened outer join
// would let the search emit an ILLEGAL order rather than a merely bad one.
// Until that inference lands, every outer join takes the FULL treatment:
// `pinnedJoinlist` reproduces `list_make1(list_make2(l, r))` verbatim. The
// difference from upstream is a plan-quality loss, never a correctness one,
// and it is recorded as a deferral rather than approximated.
//
// # What is NOT ported
//
// `deconstruct_recurse` also builds `JoinDomain`s, `qualscope` /
// `inner_join_rels` / `nonnullable_rels` relid sets, and the `JoinTreeItem`
// list that phase 2 (`deconstruct_distribute`) walks to place quals. None of
// that is here: goopg places quals in the pre-search pipeline
// (`planJoinPredicate`, `inner_join_qual_pushdown.go`) and its outer joins do
// not enter the search at all, so the sets would have no reader. This file
// ports the joinlist RESULT and only that — which is exactly the part
// `make_rel_from_joinlist` (allpaths.c:3391) consumes.
//
// # Still inert
//
// `deconstructJointree` runs on every planned SELECT (it is computed beside the
// bindings in `planFromClause`, which is the one place the leaf numbering and
// the binding order are guaranteed to agree), but nothing READS the result yet:
// `GOOPG_PGSHAPED_DP` is OFF and no `planSelect` call site hands a joinlist to
// the search. P5.9 is the wiring. The pass is therefore pure, allocation-light
// and cannot fail — it has no error return because there is no malformed FROM
// clause it could reject that the parser would have accepted.

import (
	"os"

	"github.com/goopg/goopg/internal/parser"
)

// PG 18 boot values for the two collapse GUCs (guc_tables.c; registered by
// `config/defaults.go:1060`/`:1066`). `TestCollapseLimitsMatchConfigDefaults`
// pins the two against each other — the drift guard `defaultCostParams` has for
// the cost GUCs, and for the same reason: a planner that plans against a
// different number than `SHOW` reports is a bug nobody can see.
const (
	defaultFromCollapseLimit = 8
	defaultJoinCollapseLimit = 8
)

// collapseLimits is the pair of GUCs `deconstructJointree` reads, threaded as a
// value for the same reason `costParams` is: cost/shape decisions must be taken
// against ONE snapshot of the settings, never re-read mid-plan.
//
// The per-session values do not reach here yet — `Plan` (planner.go:89) takes
// no session, the same gap `costParams.workMem` and `ParallelSettings` record —
// so `defaultCollapseLimits` is what production would pass. That gap is what
// makes `SET join_collapse_limit = 1` still a no-op in a real session, and it
// is ledgered rather than papered over: the SEMANTICS of the pin are
// implemented and tested here, so the plumbing is a one-line change when a
// session becomes reachable.
type collapseLimits struct {
	// fromCollapseLimit is `from_collapse_limit`: the widest joinlist a
	// MULTI-relation sub-problem may be merged into. Single-relation items
	// ignore it entirely (see `deconstructJointree`).
	fromCollapseLimit int
	// joinCollapseLimit is `join_collapse_limit`: the widest joinlist two
	// explicit-JOIN sides may be combined into. 1 pins syntactic order.
	joinCollapseLimit int
}

func defaultCollapseLimits() collapseLimits {
	return collapseLimits{
		fromCollapseLimit: defaultFromCollapseLimit,
		joinCollapseLimit: defaultJoinCollapseLimit,
	}
}

// pgShapedCollapse gates explicit INNER JOIN flattening, and it is a SEPARATE
// flag from `GOOPG_PGSHAPED_DP` because 08 §2 soaks the two changes
// independently: the enumerator changes which of the orders goopg already
// considers wins, while collapse changes WHICH ORDERS EXIST — a query written
// `FROM a JOIN b ON … JOIN c ON …` has never been reordered by goopg at all.
// Turning both on at once would leave a plan change with two possible causes.
//
// OFF (the default) reproduces today's behaviour exactly: an explicit JOIN node
// forces its own order, so a JOIN chain enters the search as one opaque item.
// Note what the flag does NOT touch — a flat comma-FROM list is one search
// problem either way, because that collapse is unconditional in upstream too.
//
// Read once at process start, like `pgShapedDP`, so a plan cannot change shape
// mid-statement.
var pgShapedCollapse = pgShapedCollapseFromEnv(os.Getenv("GOOPG_PGSHAPED_COLLAPSE"))

// pgShapedCollapseFromEnv is the flag's polarity, factored out so the
// provenance table (flaglabels.go) can render the unset default from the same
// function production resolves it with — mirrors pgShapedDPFromEnv.
func pgShapedCollapseFromEnv(v string) bool { return v == "1" }

// pgShapedCollapseEnabled reports whether explicit INNER JOIN chains flatten
// into the enclosing search problem. Exposed as a function so the flag keeps a
// single read site.
func pgShapedCollapseEnabled() bool { return pgShapedCollapse }

// joinlistItem is one member of a joinlist: upstream's `RangeTblRef` (a leaf
// FROM item) or `List` (a sub-joinlist to be planned as its own subproblem),
// which C models as a tagged Node and Go models as a two-arm struct.
//
// `rel` is an index into the FLAT FROM-item order — the same order
// `resolveContext.bindings` is in, because `planFromClause` walks the FROM
// clause once and appends one binding per range variable in exactly the
// depth-first order this file numbers leaves in. That correspondence is the
// whole value of the joinlist to a caller: `rel` is directly a `bindings` /
// `scans` / `relInfos` subscript for `buildInitialRels`, with no name matching
// anywhere. `TestJoinlistLeavesMatchBindings` pins it.
type joinlistItem struct {
	// rel is the leaf's FROM-item index when sub == nil; ignored otherwise.
	rel int
	// sub is the subproblem when this item is a sub-joinlist; nil for a leaf.
	sub joinlist
}

// joinlist is `make_rel_from_joinlist`'s input: items to be joined in an order
// the search chooses, where a sub-joinlist item is a subproblem planned
// separately and then treated as a single relation by the enclosing problem.
type joinlist []joinlistItem

func leafItem(rel int) joinlistItem     { return joinlistItem{rel: rel} }
func subItem(sub joinlist) joinlistItem { return joinlistItem{rel: -1, sub: sub} }
func (it joinlistItem) isLeaf() bool    { return it.sub == nil }

// nrels is the number of base relations at or below this joinlist — the size of
// the search problem it describes, which is what 03 §7's `maxSearchRels`
// ceiling is compared against.
func (jl joinlist) nrels() int {
	n := 0
	for _, it := range jl {
		if it.isLeaf() {
			n++
			continue
		}
		n += it.sub.nrels()
	}
	return n
}

// leaves appends every leaf FROM-item index at or below this joinlist, in
// depth-first order. Used by the wiring tests and by any caller that needs the
// relation set of a subproblem.
func (jl joinlist) leaves(dst []int) []int {
	for _, it := range jl {
		if it.isLeaf() {
			dst = append(dst, it.rel)
			continue
		}
		dst = it.sub.leaves(dst)
	}
	return dst
}

// deconstructJointree computes the joinlist for a whole FROM clause: upstream's
// `deconstruct_recurse` on the query's top `FromExpr` (initsplan.c:1190-1248),
// whose `fromlist` is goopg's comma-separated `[]parser.FromExpr`.
//
// `collapseJoins` is `pgShapedCollapseEnabled()` at the production call site and
// an explicit argument here so the two regimes are testable without touching
// process state.
//
// The merge rule is upstream's verbatim, and both halves of it matter:
//
//	if (sub_members <= 1 ||
//	    list_length(joinlist) + sub_members + remaining <= from_collapse_limit)
//
// `sub_members <= 1` is why a flat comma list is always one problem; the
// `remaining` term is why the decision is made against the joinlist's FINAL
// width rather than its width so far, so the outcome does not depend on which
// item happens to be processed first.
func deconstructJointree(from []parser.FromExpr, lim collapseLimits, collapseJoins bool) joinlist {
	var jl joinlist
	remaining := len(from)
	nextRel := 0
	for i := range from {
		sub := deconstructFromItem(from[i], nextRel, lim, collapseJoins)
		nextRel += fromItemRels(from[i])
		subMembers := len(sub)
		remaining--
		if subMembers <= 1 || len(jl)+subMembers+remaining <= lim.fromCollapseLimit {
			jl = append(jl, sub...)
		} else {
			jl = append(jl, subItem(sub))
		}
	}
	return jl
}

// deconstructRangeVars is `deconstructJointree` for the JOIN-free spelling of a
// FROM clause (`planFromRangeVars`, planner.go:1968), which the parser produces
// as a bare `[]parser.RangeVar` rather than as single-base `FromExpr`s.
//
// It takes no limits because there is nothing for a limit to govern: every item
// is a single base relation, so upstream's `sub_members <= 1` arm fires for all
// of them and the answer is one flat problem of `len(from)` leaves regardless of
// what either GUC is set to. Writing it as its own function rather than
// synthesising `FromExpr`s keeps that "the limits cannot change this" fact
// visible at the call site instead of buried in a loop that appears to consult
// them.
func deconstructRangeVars(n int) joinlist {
	jl := make(joinlist, n)
	for i := range jl {
		jl[i] = leafItem(i)
	}
	return jl
}

// fromItemRels is the number of base relations one comma-separated FROM item
// contributes: its base range variable plus one per JOIN in its chain. Exactly
// the number of `rangeBinding`s `planFromItem` appends for the same item
// (planner.go:2101-2117 — one `planScanRangeVar` for the base and one per
// `item.Joins` entry), which is what keeps leaf numbering and binding order in
// step.
func fromItemRels(item parser.FromExpr) int { return 1 + len(item.Joins) }

// deconstructFromItem computes the joinlist of ONE comma-separated FROM item —
// upstream's `deconstruct_recurse` over that item's `JoinExpr` chain
// (initsplan.c:1250-1441).
//
// goopg's parse shape makes the recursion an iteration: `parser.FromExpr` is a
// base range variable plus a flat `Joins` slice, which is a strictly LEFT-DEEP
// chain — `((base ⋈ j0) ⋈ j1) ⋈ …` — and the grammar admits no parenthesised
// right-hand join tree, so `j.Right` is always a single range variable and the
// right joinlist is always one leaf. Every combination decision still goes
// through `combineJoinlists`, which is written for the general two-sided case,
// so a future grammar that nests joins needs no change here beyond the
// recursion.
func deconstructFromItem(item parser.FromExpr, firstRel int, lim collapseLimits, collapseJoins bool) joinlist {
	left := joinlist{leafItem(firstRel)}
	next := firstRel + 1
	for _, j := range item.Joins {
		right := joinlist{leafItem(next)}
		next++
		left = combineJoinlists(joinPinned(j.Type, collapseJoins), left, right, lim.joinCollapseLimit)
	}
	return left
}

// joinPinned reports whether a JOIN node must force its own order rather than
// offer its sides to the enclosing problem.
//
// Outer joins (LEFT/RIGHT/FULL) are always pinned in v1: upstream pins only
// FULL and constrains the rest with `SpecialJoinInfo`, which goopg cannot yet
// infer (03 §4.4 — see the file header for why the difference is a quality loss
// and not a correctness one). RIGHT is included because goopg has no
// `reduce_outer_joins` pass to rewrite it into a LEFT with swapped sides, so it
// reaches here as itself.
//
// INNER and CROSS are the same case to upstream — `a CROSS JOIN b` parses as a
// `JoinExpr` with `jointype = JOIN_INNER` and no quals — and are pinned only
// while `GOOPG_PGSHAPED_COLLAPSE` is off.
func joinPinned(t parser.JoinType, collapseJoins bool) bool {
	switch t {
	case parser.JoinInner, parser.JoinCross:
		return !collapseJoins
	default:
		return true
	}
}

// combineJoinlists is the tail of `deconstruct_recurse`'s `JoinExpr` arm
// (initsplan.c:1410-1441): fold the two sides together unless the node forces
// its order or `join_collapse_limit` forbids it.
func combineJoinlists(pinned bool, left, right joinlist, joinCollapseLimit int) joinlist {
	if pinned {
		// `joinlist = list_make1(list_make2(leftjoinlist, rightjoinlist))`
		// (initsplan.c:1417). One item — so the enclosing FromExpr sees
		// `sub_members == 1` and always absorbs it — whose subproblem has
		// exactly two members, which is the forced order.
		//
		// Upstream does NOT unwrap a one-element side here, unlike the
		// limit branch below; `make_rel_from_joinlist` recurses on a
		// one-element sub-list and returns that rel unchanged
		// (allpaths.c:3391), so the extra level is inert. Kept verbatim
		// rather than normalised, because a joinlist that differs from
		// upstream's only by nesting depth is one nobody can diff.
		return joinlist{subItem(joinlist{subItem(left), subItem(right)})}
	}
	if len(left)+len(right) <= joinCollapseLimit {
		out := make(joinlist, 0, len(left)+len(right))
		out = append(out, left...)
		out = append(out, right...)
		return out
	}
	// "can't combine, but needn't force join order above here": the two
	// sides become two members of the enclosing problem, each a subproblem
	// of its own. One-element sides are unwrapped rather than wrapped
	// (initsplan.c:1428-1436) — the reason a two-way join is unaffected by
	// `join_collapse_limit = 1`, see the file header.
	return joinlist{soleItemOr(left), soleItemOr(right)}
}

// soleItemOr is `list_length(l) == 1 ? linitial(l) : (Node *) l` — upstream's
// "avoid creating useless 1-element sublists".
func soleItemOr(jl joinlist) joinlistItem {
	if len(jl) == 1 {
		return jl[0]
	}
	return subItem(jl)
}
