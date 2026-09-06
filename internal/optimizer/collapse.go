package optimizer

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
// # Live since M0127-P5.9 (2026-08-06) — but read the split carefully
//
// `deconstructJointree` runs on every planned SELECT (it is computed beside the
// bindings in `planFromClause`, which is the one place the leaf numbering and
// the binding order are guaranteed to agree). The result is now READ:
// `tryPGShapedJoinSearch` consults `ctx.joinlist` (joinsearchseam.go:162,170)
// and `GOOPG_PGSHAPED_DP` defaults ON, so the header's former claim that
// "nothing READS the result yet" is false.
//
// What was still off until take2 P0-13 is the narrower thing —
// `pgShapedCollapse` (`GOOPG_PGSHAPED_COLLAPSE`, `=0` opts out, default on
// since P0-13) gates only explicit INNER JOIN FLATTENING. With it on, an
// explicit JOIN chain flattens into the enclosing search problem instead of
// entering as one opaque item. Do not read the pre-flip history ("the
// collapse flag is off") as "this file cannot move a plan".
//
// The pass is pure, allocation-light and cannot fail — it has no error return
// because there is no malformed FROM clause it could reject that the parser
// would have accepted.

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
// Default ON since take2 P0-13: the positive-control gate (TPC-H changed=0,
// exactly TPC-DS {Q72,Q75} moved) cleared it; `=0` opts back out.
func pgShapedCollapseFromEnv(v string) bool { return v != "0" }

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
	// jointype is the JOIN node that PINNED this item's two members, and it is
	// meaningful only on the item `pinnedItem` builds — see `pinnedOuter` for
	// why the zero value (`parser.JoinInner`) is the safe default everywhere
	// else. M0127-P5.9-s.
	jointype parser.JoinType
	// sjinfo is the SpecialJoinInfo built during deconstruction for outer/
	// semi/anti joins. nil for leaves and flattened inner-join subproblems.
	// M0128-P1.1.
	sjinfo *SpecialJoinInfo
}

// joinlist is `make_rel_from_joinlist`'s input: items to be joined in an order
// the search chooses, where a sub-joinlist item is a subproblem planned
// separately and then treated as a single relation by the enclosing problem.
type joinlist []joinlistItem

func leafItem(rel int) joinlistItem     { return joinlistItem{rel: rel} }
func subItem(sub joinlist) joinlistItem { return joinlistItem{rel: -1, sub: sub} }
func (it joinlistItem) isLeaf() bool    { return it.sub == nil }

// pinnedItem is `combineJoinlists`' pinned arm as a value: the ONE item a pinned
// JOIN node contributes, carrying the type of the node that pinned it.
//
// M0127-P5.9-s added the type, and it is a correctness device rather than
// bookkeeping. Without it `makeRelFromJoinlist` sees only "a subproblem with two
// members" and searches it — which for an INNER pin is right (the two orders
// compute the same rows, so pinning restricts the PATHS and nothing else) and
// for an OUTER pin is a WRONG ANSWER: the search builds inner joins, so a
// `LEFT JOIN` would come back planned as an `INNER JOIN` with its unmatched left
// rows silently dropped. No corpus query reached that shape, but only because
// the seam declined every chain containing an outer link at all (09 §3.19) — an
// accident standing in for an invariant. With the type on the item, the consumer
// states the invariant itself.
func pinnedItem(t parser.JoinType, left, right joinlist) joinlistItem {
	return joinlistItem{rel: -1, jointype: t, sub: joinlist{subItem(left), subItem(right)}}
}

// pinnedOuter reports whether this item is a pinned join the search cannot
// rebuild: every type but INNER and CROSS, which is exactly the set `joinPinned`
// pins unconditionally.
//
// The polarity is deliberate. `parser.JoinInner` is `JoinType`'s zero value, so
// every item NOT built by `pinnedItem` — a leaf, a `from_collapse_limit`
// sub-list, a flattened INNER chain — answers false without its producer having
// to remember the field. The field exists to mark the shape that must be
// REFUSED, and the direction a forgotten tag fails in is therefore "search a
// subproblem whose rows are an inner join's", which is what the untagged item
// already is.
func (it joinlistItem) pinnedOuter() bool {
	if it.isLeaf() {
		return false
	}
	switch it.jointype {
	case parser.JoinInner, parser.JoinCross:
		return false
	default:
		return true
	}
}

// pinnedUnsearchable reports whether this pinned item names a join the SEARCH
// cannot rebuild — `pinnedOuter` minus the types C-03b/c taught it to build.
//
// The two questions were one function until C-04a and had to be separated,
// because they are asked of different consumers and now have different answers
// for LEFT:
//
//   - `pinnedOuter` is the SPINE walk's question ("is this item's order
//     forced?"), and a pinned LEFT item's order still is. It stays true, so a
//     LEFT link that pins anyway — the `GOOPG_PGSHAPED_COLLAPSE=0` regime, or
//     one sitting over a RIGHT pin (`pinnedOverAPinnedSide`) — is peeled
//     exactly as before rather than falling out of both mechanisms at once.
//   - this is `makeRelFromJoinlist`'s question ("would handing it to the
//     search emit an inner join where the statement wrote an outer one?").
//     For LEFT the answer is now NO: `join_is_legal` matches the link's
//     SpecialJoinInfo, `jointypeForDirection` (C-03b) builds paths in the one
//     legal orientation and `createPlanNode` (C-03c) emits a LEFT join. That
//     is C-04a §3.3.
func (it joinlistItem) pinnedUnsearchable() bool {
	return it.pinnedOuter() && it.jointype != parser.JoinLeft
}

// joinTypeName renders a `parser.JoinType` for the one message that has to name
// one: `makeRelFromJoinlist`'s refusal to plan a pinned outer join. Spelled here,
// beside `pinnedOuter`, so the two stay in step if a join type is added.
func joinTypeName(t parser.JoinType) string {
	switch t {
	case parser.JoinInner:
		return "INNER"
	case parser.JoinLeft:
		return "LEFT"
	case parser.JoinRight:
		return "RIGHT"
	case parser.JoinFull:
		return "FULL"
	case parser.JoinCross:
		return "CROSS"
	case parser.JoinSemi:
		return "SEMI"
	case parser.JoinAnti:
		return "ANTI"
	default:
		return "unknown"
	}
}

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

// innerPrefixBelowOuterSpine splits a statement's joinlist into the INNER
// PREFIX a search may plan and the pinned OUTER links stacked above it,
// outermost first. `spine` is empty — and `prefix` is `jl` itself — when the
// joinlist is not topped by a pinned outer join.
//
// M0127-P5.9-s. The shape it recognises is the one `deconstructFromItem` builds
// for the corpus's every explicit-JOIN query: a left-deep chain whose INNER
// links flatten into one subproblem and whose outer links each wrap that
// subproblem in a two-member pin, so the joinlist nests exactly as deep as the
// chain has outer links. Peeling them is what makes the prefix searchable while
// the outer links keep the order they were written in — the same division
// `runJoinSearchBelowPinned` (predp.go) already makes for the semi/anti spine
// pre-DP unnesting pins, and for the same reason: goopg cannot yet infer the
// `SpecialJoinInfo` ordering constraints that would let an outer join enter the
// search legally (03 §4.4), so the choice is "search what is below it" or
// "search nothing", and before this the answer was nothing.
//
// A pin whose sub is not `pinnedItem`'s two-member `[left, right]` shape is
// declined rather than interpreted: which member is the left side is the whole
// question here, and guessing it wrong swaps the join's sides.
func (jl joinlist) innerPrefixBelowOuterSpine() (prefix joinlist, spine []parser.JoinType) {
	cur := jl
	for len(cur) == 1 && cur[0].pinnedOuter() {
		sub := cur[0].sub
		if len(sub) != 2 || sub[0].isLeaf() {
			return jl, nil
		}
		spine = append(spine, cur[0].jointype)
		cur = sub[0].sub
	}
	if len(spine) == 0 {
		return jl, nil
	}
	return cur, spine
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
	return deconstructJointreeScoped(from, lim, collapseJoins, nil)
}

// deconstructJointreeScoped is deconstructJointree with C-01 P3-01's
// name → leaf scope for SpecialJoinInfo population. A nil scope keeps the
// legacy behaviour (min = syn). The lower slice accumulates already-built
// SpecialJoinInfos bottom-up across the whole FROM clause — PG's
// root->join_info_list, which make_outerjoininfo scans for ordering
// restrictions (initsplan.c:1823); disjoint comma items never overlap so they
// contribute nothing to each other's scans.
func deconstructJointreeScoped(from []parser.FromExpr, lim collapseLimits, collapseJoins bool, sc *sjiScope) joinlist {
	jl, _ := deconstructJointreeScopedSJI(from, lim, collapseJoins, sc)
	return jl
}

// deconstructJointreeScopedSJI is deconstructJointreeScoped returning
// `root->join_info_list` alongside the joinlist.
//
// C-04a. Until now the list was recovered AFTER the fact by walking the
// joinlist for items carrying an `sjinfo` field (`collectSpecialJoinInfos`),
// which silently made the ordering constraints a function of PINNING: an
// outer join that does not pin has no item to hang its SpecialJoinInfo on, so
// relaxing the pin (§3.2) would have deleted the very constraint
// `join_is_legal` needs to keep the search from reordering across the outer
// join — wrong answers, not lost plans. Upstream has no such coupling:
// `deconstruct_recurse` appends to `root->join_info_list` inside
// `make_outerjoininfo` (initsplan.c:1743) whether or not the `JoinExpr` forces
// its order, and the joinlist is a separate output. This is that division.
//
// The order is bottom-up across the whole FROM clause — `lower` is threaded
// through every item — which is the order `join_is_legal`'s commutativity scan
// depends on and the order `collectSpecialJoinInfos` produced.
func deconstructJointreeScopedSJI(from []parser.FromExpr, lim collapseLimits, collapseJoins bool, sc *sjiScope) (joinlist, []*SpecialJoinInfo) {
	var jl joinlist
	remaining := len(from)
	nextRel := 0
	var lower []*SpecialJoinInfo
	for i := range from {
		sub, made := deconstructFromItemScoped(from[i], nextRel, lim, collapseJoins, sc, i, lower)
		lower = append(lower, made...)
		nextRel += fromItemRels(from[i])
		subMembers := len(sub)
		remaining--
		if subMembers <= 1 || len(jl)+subMembers+remaining <= lim.fromCollapseLimit {
			jl = append(jl, sub...)
		} else {
			jl = append(jl, subItem(sub))
		}
	}
	return jl, lower
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
	jl, _ := deconstructFromItemScoped(item, firstRel, lim, collapseJoins, nil, 0, nil)
	return jl
}

// deconstructFromItemScoped is deconstructFromItem with the SJI scope (nil =
// legacy). It returns the item's joinlist plus the SpecialJoinInfos it built,
// in bottom-up order, so the caller can extend the lower list PG's
// commutativity scan reads. lower holds the SJIs built for earlier joins of
// this item and earlier comma items.
func deconstructFromItemScoped(item parser.FromExpr, firstRel int, lim collapseLimits, collapseJoins bool, sc *sjiScope, itemIdx int, lower []*SpecialJoinInfo) (joinlist, []*SpecialJoinInfo) {
	left := joinlist{leafItem(firstRel)}
	next := firstRel + 1
	var made []*SpecialJoinInfo
	for _, j := range item.Joins {
		right := joinlist{leafItem(next)}
		next++
		pinned := joinPinned(j.Type, collapseJoins) || pinnedOverAPinnedSide(j.Type, left)
		// The left joinlist as it stands BEFORE this link folds it in — the
		// SpecialJoinInfo's syntactic LHS. Captured rather than recovered from
		// the folded result (C-04a): when the link pins, `combineJoinlists`
		// buries it at `left[0].sub[0].sub`, and when it does not pin there is
		// nothing to recover it from at all.
		prevLeft := left
		left = combineJoinlists(j.Type, pinned, left, right, lim.joinCollapseLimit)
		// M0128-P1.1: build SpecialJoinInfo for every outer/semi/anti join.
		if j.Type == parser.JoinInner || j.Type == parser.JoinCross {
			continue
		}
		sj := makeSpecialJoinInfoScoped(j.Type, prevLeft, right, j.On, sc, itemIdx, lower)
		// When pinned, combineJoinlists returns exactly one pinnedItem and the
		// SpecialJoinInfo also lives ON it — `pinnedOuter`'s consumers read it
		// there. When NOT pinned (C-04a's LEFT relax) the join flattens and
		// there is no single item to attach to; the SJI reaches
		// `root->join_info_list` through `made` either way, which is the whole
		// point of the split (see deconstructJointreeScopedSJI).
		if pinned && len(left) == 1 && !left[0].isLeaf() {
			left[0].sjinfo = sj
		}
		lower = append(lower, sj)
		made = append(made, sj)
	}
	return left, made
}

// joinPinned reports whether a JOIN node must force its own order rather than
// offer its sides to the enclosing problem.
//
// RIGHT and FULL are still always pinned. Upstream pins only FULL and
// constrains the rest with `SpecialJoinInfo`; goopg now infers those (C-01),
// so C-04a relaxes LEFT to the same collapse-dependent rule INNER has, and
// C-04b will do the same for RIGHT. FULL stays pinned for good: its safety
// rests on the tree side (FULL link → opaque leaf → leaf-count decline) and on
// C-03b's path-generation decline, and a FULL that flattened into the search
// would lose the first of those.
//
// INNER and CROSS are the same case to upstream — `a CROSS JOIN b` parses as a
// `JoinExpr` with `jointype = JOIN_INNER` and no quals — and are pinned only
// while `GOOPG_PGSHAPED_COLLAPSE` is off.
//
// Relaxing a pin is only safe because the SpecialJoinInfo no longer rides on
// the pinned ITEM for the purposes of `root->join_info_list`
// (`deconstructJointreeScopedSJI`): the ordering constraint survives the
// flattening, and `join_is_legal` still refuses every pairing that would
// reorder across the outer join.
func joinPinned(t parser.JoinType, collapseJoins bool) bool {
	switch t {
	case parser.JoinInner, parser.JoinCross, parser.JoinLeft:
		return !collapseJoins
	default:
		return true
	}
}

// pinnedOverAPinnedSide keeps an otherwise-collapsible OUTER link pinned when
// its left side is itself ONE pinned outer item — C-04a, and it is a capability
// guard rather than a correctness one.
//
// The seam's two representations have to agree link for link (`splitOuterSpine`).
// A LEFT link over, say, a RIGHT pin flattens on the JOINLIST side (LEFT is
// collapse-dependent now) while the PLAN side still stops at the RIGHT link,
// which `extractSearchLeaves` returns as one opaque leaf — the leaf count then
// disagrees with the relation count and the whole statement falls back to the
// syntactic shape, losing the prefix search the peel used to give it. Keeping
// the link pinned instead puts it back on the spine, where the pair
// `[LEFT, RIGHT]` is peeled exactly as it was before C-04a.
//
// It fires only when the left side is a single pinned outer item, so a LEFT
// link over a flattened INNER chain — the Q72 shape C-04a exists for — is
// unaffected.
func pinnedOverAPinnedSide(t parser.JoinType, left joinlist) bool {
	switch t {
	case parser.JoinInner, parser.JoinCross:
		return false
	}
	return len(left) == 1 && left[0].pinnedOuter()
}

// combineJoinlists is the tail of `deconstruct_recurse`'s `JoinExpr` arm
// (initsplan.c:1410-1441): fold the two sides together unless the node forces
// its order or `join_collapse_limit` forbids it.
func combineJoinlists(t parser.JoinType, pinned bool, left, right joinlist, joinCollapseLimit int) joinlist {
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
		//
		// M0127-P5.9-s: the item carries `t`. Upstream needs no such tag —
		// its joinlist member is the `JoinExpr` itself, which HAS a
		// `jointype` — and goopg's did not because its only consumer
		// searched every subproblem alike. See `pinnedItem`.
		return joinlist{pinnedItem(t, left, right)}
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
