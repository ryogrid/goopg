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
	"fmt"
	"io"
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
	// edge). When any participant's build spilled, `table.batches` is the
	// merged batch descriptor and the maps hold batch 0 only (M0146-0090).
	table sharedHashBuild
	// seeded records that `table` has taken its first participant's maps
	// (the first contribution's maps are adopted whole; later ones are merged
	// in).
	seeded bool
	// parts are the published contributions, merged when the last attached
	// participant arrives: only then is the batch count every one of them
	// must be routed under known.
	parts []parallelHashPart

	// builders / rowsPublished are witnesses, not control: how many
	// participants published a share and how many build rows they published
	// in total. The identity tests read them to prove the build was genuinely
	// partial (several builders, whose shares sum to the inner exactly once).
	builders      int
	rowsPublished int

	// The probe-phase half of PG's PHJ_BATCH_PROBE → PHJ_BATCH_SCAN, for a
	// join that fills its build side (RIGHT, FULL, RIGHT ANTI; M0146-0005dj).
	// No one participant sees every match, so each keeps a private matched
	// bitmap over the shared table — the same key and row position name the
	// same build row in every participant — and ORs it in here when its probe
	// side is exhausted. The participant whose detach brings `probers` to
	// zero runs the unmatched sweep alone, with the merged bits; it is PG's
	// "last participant to detach from the probe phase scans for unmatched
	// tuples" (ExecParallelPrepHashTableForUnmatched). A participant that
	// attaches after the sweep was claimed probes nothing: every participant
	// that reached its probe EOF saw the shared partial scan exhausted, so
	// there is no probe input left for it.
	probers      int
	sweepClaimed bool
	mergedS      map[string][]bool
	mergedI      map[int64][]bool
}

// parallelHashPart is one participant's published build share.
type parallelHashPart struct {
	local *sharedHashBuild
	// bs is the participant's batch state when its build grew past one batch:
	// detached from the operator, its maps hold the share's batch 0 and its
	// inner files the share's other batches, routed under bs.nbatch. nil for a
	// share that fit in memory.
	bs *hashBatchState
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
// arrives at the barrier. spilled is the share's batch state when its build
// grew past one batch, nil otherwise. The first error wins and is returned to
// every participant by wait.
//
// The participant whose arrival completes the build merges every share,
// still under mu: the others are parked on `done` and a late attacher on mu,
// so nothing can read the table while it is assembled.
func (ph *parallelHashBuild) finish(local *sharedHashBuild, spilled *hashBatchState, err error) {
	ph.mu.Lock()
	defer ph.mu.Unlock()
	if err != nil {
		if ph.err == nil {
			ph.err = err
		}
		if spilled != nil {
			spilled.close()
		}
	} else if local != nil {
		ph.builders++
		ph.rowsPublished += local.rowCount()
		if spilled != nil {
			ph.rowsPublished += int(spilled.innerSpilled)
		}
		ph.parts = append(ph.parts, parallelHashPart{local: local, bs: spilled})
	}
	ph.finished++
	if ph.finished == ph.attached && !ph.complete {
		if ph.err == nil {
			if merr := ph.mergeParts(); merr != nil {
				ph.err = merr
			}
		}
		if ph.err != nil {
			for _, p := range ph.parts {
				if p.bs != nil {
					p.bs.close()
				}
			}
			ph.table.release(nil)
		}
		ph.parts = nil
		ph.complete = true
		close(ph.done)
	}
}

// mergeParts assembles the shared table from every published share. Shares
// that all fit are folded map by map, as before. When any share spilled, the
// table becomes a batched one (mergeSpilledParts).
func (ph *parallelHashBuild) mergeParts() error {
	for _, p := range ph.parts {
		if p.bs != nil {
			return ph.mergeSpilledParts()
		}
	}
	for _, p := range ph.parts {
		ph.merge(p.local)
	}
	return nil
}

// mergeSpilledParts is PG's shared batch growth reduced to its outcome
// (ExecParallelHashIncreaseNumBatches, nodeHash.c): every participant ends up
// routing under ONE batch count, the largest any share grew to, and the
// table's other batches are files every participant can reload.
//
// PG grows the shared table's batch count while the build runs and
// repartitions cooperatively. goopg's shares build privately, each under its
// own growth, so the repartitioning happens here, once, after the last share
// arrived. It is legal for the same reason a serial doubling is (the
// join_batch.go header): batch numbers are the low bits of `hash >>
// bucketBits`, so under a larger power-of-two count a row of a share's batch
// k lands in a batch congruent to k. Batch-0 rows may move to any batch; a
// row of batch k != 0 can never move to batch 0.
//
// Every row that leaves memory or is re-filed is written with its hash and
// canonical key (the E-14 keyed frame), so no build key expression is
// evaluated here. The result is a settled, frozen descriptor — exactly what
// the leader prebuild publishes (freezeForSharing) — and the participants
// probe it through the same E-09 path: a private batch state per participant
// over the shared inner files.
func (ph *parallelHashBuild) mergeSpilledParts() error {
	var ref *hashBatchState
	nbatch := 1
	for _, p := range ph.parts {
		if p.bs == nil {
			continue
		}
		if ref == nil {
			ref = p.bs
		} else if p.bs.bucketBits != ref.bucketBits {
			return &ExecError{
				Code: "XX000",
				Message: fmt.Sprintf("parallel hash join: participants chose different bucket counts (2^%d vs 2^%d)",
					p.bs.bucketBits, ref.bucketBits),
			}
		}
		if p.bs.nbatch > nbatch {
			nbatch = p.bs.nbatch
		}
	}
	m := &hashBatchState{
		nbatch:         nbatch,
		origNBatch:     ref.origNBatch,
		nbatchOutstart: nbatch,
		bucketBits:     ref.bucketBits,
		spaceAllowed:   ref.spaceAllowed,
		buildIsLeft:    ref.buildIsLeft,
		ctx:            ref.ctx,
		nbuckets:       ref.nbuckets,
		inner:          make([]*joinBatchFile, nbatch),
	}
	dropMerged := func() {
		for _, f := range m.inner {
			if f != nil {
				m.dropFile(f)
			}
		}
	}
	for _, p := range ph.parts {
		if err := m.absorbParallelHashPart(p); err != nil {
			dropMerged()
			return err
		}
		ph.merge(p.local)
	}
	for b, f := range m.inner {
		if f == nil {
			continue
		}
		if err := f.w.Close(); err != nil {
			dropMerged()
			return err
		}
		f.w = nil
		if f.rows == 0 {
			m.ctx.removeSpillFile(f.path)
			m.inner[b] = nil
		}
	}
	ph.table.batches = &sharedBatchDesc{
		nbatch:       m.nbatch,
		origNBatch:   m.origNBatch,
		nbuckets:     m.nbuckets,
		bucketBits:   m.bucketBits,
		spaceAllowed: m.spaceAllowed,
		buildIsLeft:  m.buildIsLeft,
		inner:        m.inner,
		loads:        make([]*sharedBatchLoad, m.nbatch),
	}
	return nil
}

// absorbParallelHashPart moves one share into the merged batch state m: its
// in-memory rows that m routes past batch 0 are written to m's inner files
// and deleted from the share's maps (the maps are then folded into the shared
// batch 0 by merge), and every row of the share's own inner files is re-filed
// under m's batch count. The share's files are unlinked once copied.
func (m *hashBatchState) absorbParallelHashPart(p parallelHashPart) error {
	local := p.local
	for ik, rows := range local.intHash {
		h := joinBatchHashInt64(ik)
		b := m.batchOf(h)
		if b == 0 {
			continue
		}
		for _, r := range rows {
			if err := m.writeKeyed(m.inner, b, h, spillIntKey(ik), r); err != nil {
				return err
			}
		}
		delete(local.intHash, ik)
	}
	for sk, rows := range local.hash {
		h := hashKeyString(sk)
		b := m.batchOf(h)
		if b == 0 {
			continue
		}
		for _, r := range rows {
			if err := m.writeKeyed(m.inner, b, h, spillStrKey(sk), r); err != nil {
				return err
			}
		}
		delete(local.hash, sk)
	}
	bs := p.bs
	if bs == nil {
		return nil
	}
	for k, f := range bs.inner {
		if f == nil {
			continue
		}
		bs.inner[k] = nil
		if err := m.refileParallelHashBatch(f); err != nil {
			bs.dropFile(f)
			return err
		}
		bs.dropFile(f)
	}
	bs.close()
	return nil
}

// refileParallelHashBatch copies one share batch file's rows into m's files.
func (m *hashBatchState) refileParallelHashBatch(f *joinBatchFile) error {
	if f.w != nil {
		if err := f.w.Close(); err != nil {
			return err
		}
		f.w = nil
	}
	r, err := newSpillReader(f.path)
	if err != nil {
		return err
	}
	defer r.closeKeepFile()
	var buf Row
	for {
		h, k, row, err := r.ReadRowKeyedInto(buf)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		buf = row
		b := m.batchOf(h)
		if b == 0 {
			return &ExecError{
				Code:    "XX000",
				Message: fmt.Sprintf("parallel hash join: a spilled build row routes to batch 0 under %d batches", m.nbatch),
			}
		}
		if err := m.writeKeyed(m.inner, b, h, k, row); err != nil {
			return err
		}
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

// probeAttach registers a participant as probing. False means the sweep has
// already been claimed: the caller must not probe (see the probers field).
func (ph *parallelHashBuild) probeAttach() bool {
	ph.mu.Lock()
	defer ph.mu.Unlock()
	if ph.sweepClaimed {
		return false
	}
	ph.probers++
	return true
}

// probeDetach ORs one participant's matched bitmaps into the merged set and
// reports whether the caller is the last prober, which then owns the sweep
// and receives the merged bitmaps. The mutex is the publication edge: every
// other participant's marks were made before its own detach.
func (ph *parallelHashBuild) probeDetach(ms map[string][]bool, mi map[int64][]bool) (map[string][]bool, map[int64][]bool, bool) {
	ph.mu.Lock()
	defer ph.mu.Unlock()
	if ph.mergedS == nil {
		ph.mergedS = make(map[string][]bool, len(ms))
	}
	if ph.mergedI == nil {
		ph.mergedI = make(map[int64][]bool, len(mi))
	}
	for k, bits := range ms {
		orMatched(ph.mergedS, k, bits)
	}
	for k, bits := range mi {
		orMatched(ph.mergedI, k, bits)
	}
	ph.probers--
	if ph.probers > 0 || ph.sweepClaimed {
		return nil, nil, false
	}
	ph.sweepClaimed = true
	return ph.mergedS, ph.mergedI, true
}

// orMatched ORs a participant's bitmap for one bucket into the merged map. The
// first bitmap for a bucket is copied, not adopted: the participant may still
// hold it.
func orMatched[K comparable](merged map[K][]bool, k K, bits []bool) {
	m, ok := merged[k]
	if !ok || len(m) != len(bits) {
		merged[k] = append([]bool(nil), bits...)
		return
	}
	for i, b := range bits {
		if b {
			m[i] = true
		}
	}
}

// errParallelHashAbandoned is returned to a participant whose Gather group
// was cancelled while it waited at the build barrier — another participant
// failed, or the Gather closed early. The group's own error is the one the
// client sees; this one only stops the waiter from probing.
var errParallelHashAbandoned = errors.New("parallel hash join: build abandoned (parallel group cancelled)")

// errParallelHashUnregistered is returned by a Parallel Hash join that reaches
// execution without the Gather-registered shared build state.
var errParallelHashUnregistered = errors.New("parallel hash join: no shared build state is registered for this join (not under a Gather that registered it)")

// errParallelHashSpilled refuses a spilled share of a join that fills its
// build side (RIGHT, FULL, RIGHT SEMI/ANTI). Batches past 0 are probed by
// every participant separately, each with its own probe rows, so no one
// participant sees every match of a batch-k build row; PG's per-batch
// barrier that merges them (PHJ_BATCH_SCAN) is not ported (M0146-0090,
// ledgered). Other join types batch (mergeSpilledParts).
var errParallelHashSpilled = errors.New("parallel hash join: a participant's build exceeded hash_mem and spilled; parallel hash batching of a join that fills its build side is not supported")

// lookupParallelHashBuild returns the shared build state for a Parallel Hash
// join, or nil when this execution is not under a Gather that registered one.
func lookupParallelHashBuild(ctx *Context, p *optimizer.Join) *parallelHashBuild {
	if ctx == nil || ctx.ParallelHashBuilds == nil || p == nil || !p.ParallelHash {
		return nil
	}
	return ctx.ParallelHashBuilds[p]
}

// releaseParallelHashBuilds retracts a Gather's Parallel Hash states and
// unlinks the batch files a spilled build published. Called from Gather /
// GatherMerge Close after the fan-out has joined — no participant can still
// be reading.
func releaseParallelHashBuilds(ctx *Context) {
	if ctx == nil {
		return
	}
	for _, ph := range ctx.ParallelHashBuilds {
		ph.table.release(ctx)
	}
	ctx.ParallelHashBuilds = nil
}

// registerParallelHashBuilds creates one shared state per Parallel Hash join
// on the partial plan's probe path and publishes them on ctx. Called by the
// Gather before any worker context exists (NewWorkerContext copies the
// reference). Returns whether anything was registered, so Close knows to
// retract it.
func registerParallelHashBuilds(ctx *Context, plan optimizer.Node, groupDone <-chan struct{}) bool {
	joins := optimizer.ParallelHashJoinsIn(plan)
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
				ph.finish(nil, nil, errParallelHashAbandoned)
			}
		}()
		gotProbeLeft, err := o.buildLazyHashTable(ctx)
		// The batch state is installed even for a one-batch build (P3.2: the
		// memory bound is real only if growth can fire), so "spilled" is
		// nbatch > 1. A single batch is wholly in the maps and its state
		// carries nothing to publish. A spilled share is published with its
		// batch state detached from the operator: the merge owns its files
		// from here (M0146-0090).
		var spilled *hashBatchState
		if err == nil && o.batches != nil {
			switch {
			case o.batches.nbatch <= 1:
				o.releaseBatches()
			case o.fillBuildSide():
				o.releaseBatches()
				err = errParallelHashSpilled
			default:
				spilled = o.batches
				o.batches = nil
				// The share's own growth is reported now; the merged count
				// reaches EXPLAIN through the participant state below.
				spilled.publish()
			}
		}
		arrived = true
		if err != nil {
			ph.finish(nil, nil, err)
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
			ph.finish(local, spilled, nil)
		}
	}
	if err := ph.wait(ctx); err != nil {
		return err
	}
	o.adoptParallelHashTable(ctx, &ph.table)
	if ph.table.batches != nil {
		// A batched shared table: batch 0 is in the maps just adopted and
		// every other batch is a shared inner file. Each participant routes
		// its own probe rows and reloads batch k through the descriptor's
		// load slots — the leader prebuild's E-09 participant path.
		o.releaseBatches()
		o.batches = newParticipantBatchState(ctx, o.plan, ph.table.batches)
	}
	if o.fillBuildSide() {
		o.parallelFill = ph
		o.parallelProbing = ph.probeAttach()
		o.parallelSkipProbe = !o.parallelProbing
	}
	return o.openProbeSide(ctx, probeIsLeft)
}

// parallelFillDetach is the probe-EOF half of the shared sweep protocol: hand
// this participant's matched bits in and, if it is the last prober, take the
// merged ones for the sweep. It reports whether this participant sweeps the
// shared table. Outside a parallel fill-build join every participant sweeps
// its own table, as before.
func (o *joinOp) parallelFillDetach() bool {
	ph := o.parallelFill
	if ph == nil {
		return true
	}
	if !o.parallelProbing {
		return false
	}
	o.parallelProbing = false
	ms, mi, last := ph.probeDetach(o.lazyMatchedS, o.lazyMatchedI)
	if !last {
		return false
	}
	o.lazyMatchedS, o.lazyMatchedI = ms, mi
	return true
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
