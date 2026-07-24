package executor

// parallel_hash_build.go — P8 of docs/design/parallel-query/, chapter 07.
//
// PostgreSQL offers two parallel hash joins: a non-shared one where every
// worker builds its own complete copy of the table, and `Parallel Hash`, where
// workers cooperatively build ONE table in dynamic shared memory behind a
// barrier protocol with explicit phases. Both exist because shared memory is
// expensive to manage, so neither option is free.
//
// goopg needs neither. Workers are goroutines in one address space, so the
// table can be built once and shared by pointer. The correctness argument is
// the ordinary Go one: a map is safe for unlimited concurrent reads provided
// no writer runs concurrently, and the goroutine-start edge supplies the
// happens-before that publishes it. That replaces PG's whole DSA + barrier
// apparatus with a struct and a map lookup.
//
// What goopg gives up in exchange is PG's parallel BUILD. Here the build stays
// serial in the leader. That is acceptable because the planner puts the small
// side on the build side by construction (IsSmallDimensionSide), so the build
// is not usually the bottleneck — but it is a genuine capability PG has and
// this does not, recorded rather than glossed (chapter 07 §3.1).

import (
	"github.com/goopg/goopg/internal/planner"
)

// sharedHashBuild is one hash join's build-side result, frozen and shared by
// every worker running that plan node.
//
// The map is the obvious part. The scalars are the part that is easy to miss:
// they are per-INSTANCE fields on joinOp, not entries in the map, so sharing
// the table does not carry them along. antiBuildHasNull in particular decides
// NOT IN's three-valued-NULL result, and a worker that defaulted it to false
// would silently return wrong rows for NOT IN over a NULL-containing subquery.
type sharedHashBuild struct {
	hash     map[string][]Row
	hashCTID map[string][]joinRowCTID

	// int64 fast-path (INNER only): when the build side's keys were all
	// int64-representable, buildLazyHashTable/lazyHashFinalize sets
	// lazyHashIsInt and frees lazyHash, leaving the table in lazyIntHash. These
	// must ride along, or a worker probes an empty string map and drops every
	// match (the symptom: parallel INNER joins return 0 rows).
	intHash   map[int64][]Row
	hashIsInt bool

	// probeIsLeft records which side the build consumed, so a worker opens the
	// other one without re-deriving the rule.
	probeIsLeft bool

	preserveBuildSide bool
	antiBuildRows     int
	antiBuildHasNull  bool
	leftWidth         int
	rightWidth        int
}

// captureSharedBuild snapshots the build-phase results of a joinOp whose
// buildLazyHashTable has just completed.
func (o *joinOp) captureSharedBuild(probeIsLeft bool) *sharedHashBuild {
	return &sharedHashBuild{
		hash:              o.lazyHash,
		hashCTID:          o.lazyHashCTID,
		intHash:           o.lazyIntHash,
		hashIsInt:         o.lazyHashIsInt,
		probeIsLeft:       probeIsLeft,
		preserveBuildSide: o.preserveBuildSide,
		antiBuildRows:     o.antiBuildRows,
		antiBuildHasNull:  o.antiBuildHasNull,
		leftWidth:         o.lazyLW,
		rightWidth:        o.lazyRW,
	}
}

// applySharedBuild adopts a published table instead of building one.
//
// Every field buildLazyHashTable would have set must be set here too. Leaving
// one out does not fail loudly — it produces a join that runs and returns the
// wrong rows.
func (o *joinOp) applySharedBuild(sb *sharedHashBuild) {
	o.lazyHash = sb.hash
	o.lazyHashCTID = sb.hashCTID
	o.lazyIntHash = sb.intHash
	o.lazyHashIsInt = sb.hashIsInt
	o.preserveBuildSide = sb.preserveBuildSide
	o.antiBuildRows = sb.antiBuildRows
	o.antiBuildHasNull = sb.antiBuildHasNull
	o.lazyLW = sb.leftWidth
	o.lazyRW = sb.rightWidth
}

// lookupSharedHashBuild returns the published table for a plan node, or nil
// when this execution is not under a Gather that pre-built one.
func lookupSharedHashBuild(ctx *Context, p *planner.Join) *sharedHashBuild {
	if ctx == nil || ctx.SharedHashBuilds == nil || p == nil {
		return nil
	}
	return ctx.SharedHashBuilds[p]
}

// probeSideIsLeft reports which side of a hash join is probed.
//
// This rule is duplicated in three places by necessity — the build loop, the
// parallel-scan attach walk, and the planner's partial-subtree search — and
// they must agree, because a disagreement puts the parallel scan on the BUILD
// side, where each worker would build a partition of the table and the join
// would silently lose rows. Hence one exported-ish helper rather than three
// copies of `if Semi/Anti { false }`.
func probeSideIsLeft(p *planner.Join) bool {
	buildLeft := p.BuildLeft
	if p.Type == planner.JoinTypeSemi || p.Type == planner.JoinTypeAnti {
		buildLeft = false
	}
	// The build consumed the left side ⇒ the probe is the right side.
	return !buildLeft
}

// prebuildSharedHashJoins runs the build phase of every shareable hash join in
// a partial subtree, in the LEADER, before any worker starts.
//
// It builds one throwaway operator tree for the sole purpose of running those
// build phases. That is cheaper than it sounds: the build child is opened,
// drained and closed inside the build phase, and nothing else in the tree is
// ever opened. What the tree costs is construction; what it buys is that the
// build runs through the SAME code path a serial execution uses, rather than a
// reimplementation that could drift from it.
//
// Returns nil when the subtree has no shareable hash join, which is the common
// case and costs nothing — the check is on the plan, not on a built tree.
func prebuildSharedHashJoins(ctx *Context, plan planner.Node, buildChild func() (Operator, error)) (map[*planner.Join]*sharedHashBuild, error) {
	// Decide from the PLAN, before building anything. An earlier cut built the
	// tree unconditionally and then looked for joins in it, which called the
	// Gather's child-builder one extra time — harmless for the production
	// builder (a pure Build(p.Child)) but not for any builder that counts its
	// invocations, as several tests legitimately do.
	if !planner.HasShareableHashJoin(plan) {
		return nil, nil
	}
	tree, err := buildChild()
	if err != nil {
		return nil, err
	}
	var joins []*joinOp
	collectShareableJoins(tree, &joins)
	if len(joins) == 0 {
		return nil, nil
	}

	out := make(map[*planner.Join]*sharedHashBuild, len(joins))
	for _, j := range joins {
		j.ctx = ctx
		probeIsLeft, err := j.buildLazyHashTable(ctx)
		if err != nil {
			return nil, err
		}
		out[j.plan] = j.captureSharedBuild(probeIsLeft)
	}
	return out, nil
}

// collectShareableJoins finds the hash joins in a tree whose build side can be
// shared.
//
// The walk descends the PROBE side only. A hash join nested on another join's
// build side is built as part of that build, serially, and must not be
// pre-built separately — doing so would run its build twice.
func collectShareableJoins(op Operator, out *[]*joinOp) {
	switch x := op.(type) {
	case *joinOp:
		if x.plan == nil || x.plan.Algo != planner.JoinAlgoHash || x.plan.Lateral {
			return
		}
		*out = append(*out, x)
		if probeSideIsLeft(x.plan) {
			collectShareableJoins(x.left, out)
		} else {
			collectShareableJoins(x.right, out)
		}
	case *filterOp:
		collectShareableJoins(x.child, out)
	case *projectOp:
		collectShareableJoins(x.child, out)
	case *sortOp:
		collectShareableJoins(x.child, out)
	case *aggregateOp:
		collectShareableJoins(x.child, out)
	case *instrumentedOp:
		collectShareableJoins(x.inner, out)
	}
}
