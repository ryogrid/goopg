package executor

// parallel_hash_shared.go — M0146-0002: PG's `Parallel Hash`
// (`parallel_hash = true`, try_partial_hashjoin_path joinpath.c:1290-1297;
// executor MultiExecParallelHash nodeHash.c, ExecParallelHashJoin
// nodeHashjoin.c).
//
// The build side of an optimizer.Join with ParallelHash is a PARTIAL path:
// every participant under the Gather runs its claimed share of the inner and
// publishes it into ONE shared table. A build barrier holds every attached
// participant until the whole inner is in the table, and only then does any
// of them probe. This is the opposite of the leader prebuild
// (parallel_hash_build.go), where the table is finished before fan-out. There
// is no leader election here: whoever attaches, builds.
//
// The one invariant everything below exists to keep: NO PROBE BEFORE THE
// BUILD IS COMPLETE, and a build that failed anywhere fails everywhere. A
// participant probing a partial table would silently drop matches — the
// failure class of the 2026-09-21 parallel SEMI defect, which only a
// parallel-vs-serial identity comparison could see.
//
// Design: docs/design/0100-0149/m0146-0002-parallel-hash-partial-inner.md.

import (
	"errors"
	"sync"

	"github.com/goopg/goopg/internal/optimizer"
)

// parallelHashBuild is one Parallel Hash join's shared build state: PG's
// ParallelHashJoinState plus its build barrier, reduced to what goroutines in
// one address space need.
type parallelHashBuild struct {
	mu sync.Mutex
	// attached / finished count participants that entered the build phase and
	// that left it. The build is complete when they are equal after a
	// finish: a participant finishes only after its inner scan returned EOF,
	// i.e. after every block of the inner was CLAIMED, and a participant
	// still holding a claimed block is attached and not finished — so
	// equality cannot be reached while any inner row is still unpublished.
	attached int
	finished int
	complete bool
	done     chan struct{}
	err      error
	// groupDone is the Gather group's cancellation. Workers' contexts derive
	// from the group, but the LEADER waits on its own statement context,
	// which a failing worker does not cancel — so the barrier watches the
	// group directly, or a leader would wait forever on a worker that died.
	groupDone <-chan struct{}

	// The merged table. Written only under mu, only before `complete`;
	// read unlocked after `done` is closed (the close is the publication
	// edge).
	table sharedHashBuild
	// seeded records that `table` has taken its first participant's maps
	// (the first finisher's maps are adopted whole; later ones are merged in).
	seeded bool

	// builders / rowsPublished are witnesses, not control: how many
	// participants published a share and how many build rows they published
	// in total. The identity tests read them to prove the build was genuinely
	// partial (several builders, whose shares sum to the inner exactly once).
	builders      int
	rowsPublished int
}

func newParallelHashBuild(groupDone <-chan struct{}) *parallelHashBuild {
	return &parallelHashBuild{done: make(chan struct{}), groupDone: groupDone}
}

// attach is BarrierAttach: it reports whether the caller must build. A
// participant arriving after the build completed builds nothing and adopts
// the finished table, exactly as PG's late participant skips PHJ_BUILD_*.
func (ph *parallelHashBuild) attach() (mustBuild bool) {
	ph.mu.Lock()
	defer ph.mu.Unlock()
	if ph.complete {
		return false
	}
	ph.attached++
	return true
}

// finish publishes one participant's private build (nil when it failed) and
// arrives at the barrier. The first error wins and is returned to every
// participant by wait.
func (ph *parallelHashBuild) finish(local *sharedHashBuild, err error) {
	ph.mu.Lock()
	defer ph.mu.Unlock()
	if err != nil {
		if ph.err == nil {
			ph.err = err
		}
	} else if local != nil {
		ph.builders++
		ph.rowsPublished += local.rowCount()
		ph.merge(local)
	}
	ph.finished++
	if ph.finished == ph.attached && !ph.complete {
		ph.complete = true
		close(ph.done)
	}
}

// merge folds one participant's private table into the shared one. Caller
// holds mu. Every participant built the same key representation — the int64
// lane is chosen from the plan's key types, not from the data — so the maps
// merge key by key.
func (ph *parallelHashBuild) merge(local *sharedHashBuild) {
	t := &ph.table
	if !ph.seeded {
		*t = *local
		ph.seeded = true
		return
	}
	for k, rows := range local.hash {
		if t.hash == nil {
			t.hash = make(map[string][]Row, len(local.hash))
		}
		t.hash[k] = append(t.hash[k], rows...)
	}
	for k, ctids := range local.hashCTID {
		if t.hashCTID == nil {
			t.hashCTID = make(map[string][]joinRowCTID, len(local.hashCTID))
		}
		t.hashCTID[k] = append(t.hashCTID[k], ctids...)
	}
	for k, rows := range local.intHash {
		if t.intHash == nil {
			t.intHash = make(map[int64][]Row, len(local.intHash))
		}
		t.intHash[k] = append(t.intHash[k], rows...)
	}
	// NOT IN's three-valued result must see every participant's build rows
	// and every participant's NULL keys.
	t.antiBuildRows += local.antiBuildRows
	t.antiBuildHasNull = t.antiBuildHasNull || local.antiBuildHasNull
}

// rowCount is the number of build rows a table holds, across its maps.
func (sb *sharedHashBuild) rowCount() int {
	n := 0
	for _, rows := range sb.hash {
		n += len(rows)
	}
	for _, rows := range sb.intHash {
		n += len(rows)
	}
	return n
}

// wait is BarrierArriveAndWait's waiting half. It returns once the build is
// complete, or when the participant's context is cancelled (the Gather's
// Close, or another participant's error cancelling the group).
func (ph *parallelHashBuild) wait(ctx *Context) error {
	var cancelled <-chan struct{}
	if ctx != nil && ctx.Ctx != nil {
		cancelled = ctx.Ctx.Done()
	}
	select {
	case <-ph.done:
		return ph.err
	case <-cancelled:
		return ctx.Ctx.Err()
	case <-ph.groupDone:
		return errParallelHashAbandoned
	}
}

// errParallelHashAbandoned is returned to a participant whose Gather group
// was cancelled while it waited at the build barrier — another participant
// failed, or the Gather closed early. The group's own error is the one the
// client sees; this one only stops the waiter from probing.
var errParallelHashAbandoned = errors.New("parallel hash join: build abandoned (parallel group cancelled)")

// errParallelHashSpilled refuses a participant build that ended in batches.
// PG's parallel hash batches (ExecParallelHashIncreaseNumBatches) are not
// ported; keeping a spilled participant table would publish only its batch 0.
var errParallelHashSpilled = errors.New("parallel hash join: a participant's build exceeded hash_mem and spilled; parallel hash batching is not supported")

// lookupParallelHashBuild returns the shared build state for a Parallel Hash
// join, or nil when this execution is not under a Gather that registered one.
func lookupParallelHashBuild(ctx *Context, p *optimizer.Join) *parallelHashBuild {
	if ctx == nil || ctx.ParallelHashBuilds == nil || p == nil || !p.ParallelHash {
		return nil
	}
	return ctx.ParallelHashBuilds[p]
}

// registerParallelHashBuilds creates one shared state per Parallel Hash join
// on the partial plan's probe path and publishes them on ctx. Called by the
// Gather before any worker context exists (NewWorkerContext copies the
// reference). Returns whether anything was registered, so Close knows to
// retract it.
func registerParallelHashBuilds(ctx *Context, plan optimizer.Node, groupDone <-chan struct{}) bool {
	var joins []*optimizer.Join
	collectParallelHashJoins(plan, &joins)
	if len(joins) == 0 {
		return false
	}
	m := make(map[*optimizer.Join]*parallelHashBuild, len(joins))
	for _, j := range joins {
		m[j] = newParallelHashBuild(groupDone)
	}
	ctx.ParallelHashBuilds = m
	return true
}

// collectParallelHashJoins walks the partial path the way the claim walks do
// (probe side only) and collects every ParallelHash join. A Parallel Hash
// join's OWN build side is partial too, so its build subtree is walked as
// well: a Parallel Hash nested there is built by the same participants.
func collectParallelHashJoins(n optimizer.Node, out *[]*optimizer.Join) {
	switch x := n.(type) {
	case nil:
		return
	case *optimizer.Join:
		if x.Algo != optimizer.JoinAlgoHash {
			return
		}
		probe, build := x.Right, x.Left
		if probeSideIsLeft(x) {
			probe, build = x.Left, x.Right
		}
		if x.ParallelHash {
			*out = append(*out, x)
			collectParallelHashJoins(build, out)
		}
		collectParallelHashJoins(probe, out)
	case *optimizer.Filter:
		collectParallelHashJoins(x.Child, out)
	case *optimizer.Project:
		collectParallelHashJoins(x.Child, out)
	}
}

// openParallelHashJoin is openLazyHashJoin for a Parallel Hash join: build
// this participant's share, publish it, wait at the barrier, adopt the whole
// table, then open the probe side.
func (o *joinOp) openParallelHashJoin(ctx *Context, ph *parallelHashBuild) error {
	probeIsLeft := probeSideIsLeft(o.plan)
	if ctx.parallelHashObserver != nil {
		ctx.parallelHashObserver(ph)
	}
	if ph.attach() {
		// Every attached participant MUST arrive, or the others wait forever:
		// a panic inside the build still records a failure before it unwinds.
		arrived := false
		defer func() {
			if !arrived {
				ph.finish(nil, errParallelHashAbandoned)
			}
		}()
		gotProbeLeft, err := o.buildLazyHashTable(ctx)
		// The batch state is installed even for a one-batch build (P3.2: the
		// memory bound is real only if growth can fire), so "spilled" is
		// nbatch > 1. A single batch is wholly in the maps and its state
		// carries nothing to publish.
		if err == nil && o.batches != nil {
			spilled := o.batches.nbatch > 1
			o.releaseBatches()
			if spilled {
				err = errParallelHashSpilled
			}
		}
		arrived = true
		if err != nil {
			ph.finish(nil, err)
		} else {
			probeIsLeft = gotProbeLeft
			local := &sharedHashBuild{
				hash:             o.lazyHash,
				hashCTID:         o.lazyHashCTID,
				intHash:          o.lazyIntHash,
				hashIsInt:        o.lazyHashIsInt,
				probeIsLeft:      gotProbeLeft,
				antiBuildRows:    o.antiBuildRows,
				antiBuildHasNull: o.antiBuildHasNull,
				leftWidth:        o.lazyLW,
				rightWidth:       o.lazyRW,
			}
			// The rows just published live in THIS participant's build
			// arenas, and other participants will probe them after this
			// one has closed. Donate the arenas: from here Close only
			// dereferences them, and statement end reclaims them through the
			// parent chain (the Gather releases worker arenas only after the
			// fan-out has joined).
			o.buildBytesShared = true
			o.buildCellsShared = true
			ph.finish(local, nil)
		}
	}
	if err := ph.wait(ctx); err != nil {
		return err
	}
	o.adoptParallelHashTable(ctx, &ph.table)
	return o.openProbeSide(ctx, probeIsLeft)
}

// adoptParallelHashTable installs the completed shared table read-only — the
// applySharedBuild field set, minus the batch state (a parallel hash build
// never publishes batches) and minus the arenas (each participant keeps
// dereference-only ownership of its own donated ones).
func (o *joinOp) adoptParallelHashTable(ctx *Context, t *sharedHashBuild) {
	o.lazyHash = t.hash
	o.lazyHashCTID = t.hashCTID
	o.lazyIntHash = t.intHash
	o.lazyHashIsInt = t.hashIsInt
	o.antiBuildRows = t.antiBuildRows
	o.antiBuildHasNull = t.antiBuildHasNull
	if t.leftWidth != 0 || t.rightWidth != 0 {
		o.lazyLW = t.leftWidth
		o.lazyRW = t.rightWidth
	}
}
