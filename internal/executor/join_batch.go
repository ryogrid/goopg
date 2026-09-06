package executor

// Hybrid hash join: work_mem-bounded builds via batch spill files.
//
// M0127-P3.2; design docs/design/leftdeep-joins/06-hash-spill-and-memory.md
// §2.2-2.4. PG oracle: nodeHash.c (ExecHashIncreaseNumBatches :1030),
// nodeHashjoin.c (ExecHashJoinSaveTuple :1414, ExecHashJoinNewBatch :1130,
// the replayed-tuple re-check rules :1172-1202).
//
// Before this file, `ctx.WorkMem` bounded nothing about a hash join: the build
// drained its whole side into one Go map, so a build larger than memory was an
// OOM, not a spill (the deferral ledger row for M0127-P0.2 records exactly
// that). The scheme here is PG's classic hybrid hash, adapted to goopg's
// representation:
//
//   - the geometry (nbuckets, nbatch) comes from the SHARED sizing rule,
//     internal/hashsize.Choose, so the planner's cost model and the executor
//     cannot disagree about whether a build fits (M0127-P3.1);
//   - batch 0 lives in the existing `map[string][]Row` / `map[int64][]Row`
//     table; every other batch lives in a pair of temp files, one for the
//     build (inner) side and one for the probe (outer) side;
//   - `batchno = (hash >> log2(nbuckets)) & (nbatch-1)`, PG's scheme, whose
//     load-bearing property is that DOUBLING nbatch can only move a row
//     FORWARD (batch k becomes k or k+nbatch). That is what makes it legal to
//     grow the batch count in the middle of a build whose earlier rows are
//     already on disk: nothing that has been processed can need to move back.
//
// Scope (P3.2 + P3.4): INNER / Semi / Anti / probe-filling LEFT, single-key,
// with a private build. The composite-key lane keeps its own maps and is not
// batched yet, and a LEFT join built on the left side needs the build-side
// sweep of P4.2. Every declined case behaves exactly as it did before this
// file existed — `o.batches` stays nil and not one line of the old path
// changes.
//
// E-09a (docs/design/executor-e09a-shared-spilling-build/DESIGN.md): a shared
// (parallel) build that spills is PUBLISHED, not declined. The leader's
// prebuild freezes its batch state into an immutable descriptor
// (freezeForSharing) and every Gather participant derives a PRIVATE
// hashBatchState from it (newParticipantBatchState): its own outer files,
// curBatch and replay, with `inner` pointing at the leader's read-only files.
// Three invariants hold on that path and each is pinned by a test: a
// participant never writes or unlinks a shared inner file (innerShared), growth
// is frozen (growEnabled=false, PG's "all changes to the number of batches
// happen during the build phase"), and every participant opens each inner file
// exactly once.

import (
	"fmt"
	"io"
	"log/slog"
	"math/bits"

	"github.com/goopg/goopg/internal/executor/hashsize"
	"github.com/goopg/goopg/internal/optimizer"
)

// maxJoinBatches caps nbatch growth. Two independent reasons to have a cap:
// the hash is 32 bits and `bucketBits + log2(nbatch)` bits of it are consumed
// by the batch/bucket split, and a query that keeps doubling is one whose
// estimate was wrong by orders of magnitude — at 4096 batches the per-batch
// file handles and buffers cost more than the memory the spill is saving. PG
// has no explicit ceiling here (it stops when the pointer arithmetic would
// overflow); goopg gives up earlier and loudly, per 06 §2.4's "capped
// give-up + WARNING".
const maxJoinBatches = 1 << 12

// minBatchSpaceAllowed floors the in-memory budget left for build rows after
// the bucket array is charged against it. goopg's map slot is 48 bytes where
// PG's bucket is an 8-byte pointer, so for a narrow build row the bucket array
// can eat most of work_mem (the clamp hashsize.Choose documents in place of
// PG's `Assert(bucket_bytes <= hash_table_bytes/2)`). Without a floor the
// residual budget can round to nothing and every single row would spill,
// doubling nbatch until the cap — an infinite-regress shape, not a bound.
const minBatchSpaceAllowed = 64 << 10

// joinBatchFile is one batch's spill file. Its life is strictly
// write-then-read: a batch is written while earlier batches are being
// processed, then read exactly once when it becomes current, and never
// written again afterwards (rows only ever move FORWARD to higher batch
// numbers). That one-way discipline is what lets `nextBatch` decide a batch is
// empty by looking at the row counter.
type joinBatchFile struct {
	// w is the writer while the file is being filled; nil once the file has
	// been frozen for sharing (freezeForSharing), after which only path is
	// consulted and only by readers.
	w    *spillWriter
	path string
	rows int64
	// createdNBatch is nbatch as it stood when the file was created. A file
	// created before a later doubling may hold rows the final geometry assigns
	// to a HIGHER batch (they were routed under the smaller nbatch), and that
	// is what freezeForSharing uses to decide which files need settling
	// before they can be read by participants that may not write.
	createdNBatch int
}

// hashBatchState is the per-join batching state. nil on a joinOp means the
// build was projected to fit work_mem (or batching was declined), and the
// operator behaves exactly as it did before M0127-P3.2.
type hashBatchState struct {
	nbatch     int
	origNBatch int
	// nbatchOutstart is nbatch as it stood when the outer (probe) scan
	// began. PG keeps the same counter for the same reason: together with
	// origNBatch it tells nextBatch whether a one-sided batch is genuinely
	// empty or merely un-reassigned (nodeHashjoin.c rules 2 and 3).
	nbatchOutstart int
	curBatch       int
	// bucketBits is log2(nbuckets) — the low hash bits notionally reserved
	// for bucket selection, so the batch bits are disjoint from them. goopg's
	// buckets are Go map slots hashed by the runtime, so the reservation buys
	// no independence here the way it does in PG; it is kept because the
	// SHIFT is what makes doubling move rows forward by exactly nbatch.
	bucketBits uint

	spaceAllowed int64
	spaceUsed    int64
	peakSpace    int64
	growEnabled  bool

	inner []*joinBatchFile
	outer []*joinBatchFile

	// buildIsLeft is the build orientation, needed to re-evaluate the build
	// key of a row reloaded from an inner batch file. The two widths that
	// complete the merged key slot are read live off the operator
	// (o.lazyLW/o.lazyRW) rather than captured here, because a child with no
	// schema only reveals its width once the build's first row arrives.
	buildIsLeft bool

	// ctx is the statement's context: the owner of the spill-file registry
	// every batch file is registered with (M0127-P3.3). It is stored rather
	// than passed because a batch file is created deep inside the build and
	// probe hot paths, where threading a Context argument through would buy
	// nothing.
	ctx *Context

	// replaying is true while o.lazyProbe is a batch replay operator rather
	// than the real probe child; replayHash is the hash value stored with the
	// row that operator most recently returned.
	replaying  bool
	replayHash uint32
	replayOp   *batchReplayOp

	innerSpilled int64
	outerSpilled int64

	// nbuckets is the bucket count hashsize.Choose picked for this build —
	// the CHOSEN geometry, which is what PG's nbuckets reports too, not a
	// measurement of the Go map. Two divergences follow and both are
	// deliberate: goopg has no bucket ARRAY that could be resized, so there is
	// no ExecHashIncreaseNumBuckets and the reported original never differs;
	// and presizeLazyHash caps its allocation at maxPresizeBuckets, so on a
	// large build this number is bigger than the slots actually reserved (the
	// SF1 evidence run shows 2,097,152 against a 1<<20 cap). Retiring that gap
	// means making the presize revisable, which is the same deferral
	// maxPresizeBuckets already carries.
	nbuckets int

	// stats is the EXPLAIN (ANALYZE) sink for this join, owned by the
	// statement's Context and keyed by the plan node. nil when the operator
	// was built without one (unit fixtures, and any path that never went
	// through NewContext).
	stats *HashJoinStats

	// innerShared marks a participant state derived from a shared batch
	// descriptor (E-09a): `inner` aliases files the leader wrote and froze,
	// and other participants read the same files concurrently. While it is
	// set this state must never write to, truncate, or unlink an inner file
	// — writeInner refuses, openReader leaves the writer alone, discard and
	// close skip the inner slice, and loadInnerBatch closes its reader with
	// closeKeepFile. Growth is frozen too, so the reload path can never need
	// to re-route a row forward (every file was settled before publication).
	innerShared bool
}

// testHookInnerBatchOpened, when non-nil, is called by loadInnerBatch each
// time a batch state opens an inner batch file for reading. It exists for the
// E-09a exactly-once-open invariant test and is nil in production; it is set
// before a Gather fans out and cleared after it joins, so the goroutine
// start/join edges order every access.
var testHookInnerBatchOpened func(bs *hashBatchState, b int, path string)

// HashJoinStats is the per-plan-node hash-join instrumentation EXPLAIN
// (ANALYZE) reports, PG's HashInstrumentation (nodeHash.h) minus the fields
// goopg has no analogue for.
//
// PG attaches this to the HASH node, which is a separate plan node under the
// Hash Join. goopg has no Hash node — the build lives inside joinOp — so the
// line attaches to the Hash Join node itself. That is the one deliberate
// divergence in the output shape; the line's own text is PG's, verbatim from
// show_hash_info (explain.c).
//
// Values merge by MAXIMUM across re-Opens, which is PG's rule too
// (ExecHashAccumInstrumentation, nodeHash.c): a rescanned hash join reports the
// largest table it ever held rather than the last one.
type HashJoinStats struct {
	NBuckets     int
	OrigNBuckets int
	NBatch       int
	OrigNBatch   int
	SpacePeak    int64
	// BuildTimeNs is the wall-clock time the hash table build took.
	// Merged by MAXIMUM across re-Opens, same as the geometry fields.
	// PG's Hash node (a separate plan node under the Hash Join) carries
	// this implicitly as its (actual time=…) line; goopg has no Hash
	// node, so the measurement hangs here. M0128-P2.1.
	BuildTimeNs int64
}

// hashJoinStat returns the instrumentation sink for one hash-join plan node,
// creating it on first use. Mirrors Context.memoizeStat.
func (c *Context) hashJoinStat(j *optimizer.Join) *HashJoinStats {
	if c == nil || j == nil {
		return nil
	}
	if c.HashJoinStats == nil {
		c.HashJoinStats = make(map[*optimizer.Join]*HashJoinStats)
	}
	st, ok := c.HashJoinStats[j]
	if !ok {
		st = &HashJoinStats{}
		c.HashJoinStats[j] = st
	}
	return st
}

// publish max-merges the live counters into the statement's instrumentation.
// Called at the two moments the reported numbers can change — the geometry is
// chosen, and nbatch grows — plus once at close, which is where peak memory is
// flushed (keeping it out of insertBuildRow's per-row path).
func (bs *hashBatchState) publish() {
	st := bs.stats
	if st == nil {
		return
	}
	if bs.nbuckets > st.NBuckets {
		st.NBuckets = bs.nbuckets
	}
	if bs.nbuckets > st.OrigNBuckets {
		st.OrigNBuckets = bs.nbuckets
	}
	if bs.nbatch > st.NBatch {
		st.NBatch = bs.nbatch
	}
	if bs.origNBatch > st.OrigNBatch {
		st.OrigNBatch = bs.origNBatch
	}
	// Report the bucket array alongside the rows, as PG does:
	// "Account for the buckets in spaceUsed (reported in EXPLAIN ANALYZE)"
	// — postgres/src/backend/executor/nodeHash.c, ExecHashTableCreate and
	// ExecHashIncreaseNumBuckets.
	//
	// Reporting ONLY, never the growth trigger. The trigger is already
	// correct and must not be touched: `spaceAllowed` is pre-deducted by
	// `nbuckets*MapSlotBytes` where it is computed, which makes
	// `peakSpace > spaceAllowed` algebraically identical to PG's
	// `spaceUsed + nbuckets*sizeof(HashJoinTuple) > spaceAllowed`. Adding
	// the buckets here as well would charge them twice and batch early.
	//
	// Why this matters beyond tidiness: `Memory Usage:` was reporting the
	// SMALLER of the join's two memory terms. On the TPC-H Q9 `orders`
	// build it printed 44,026 kB of rows while omitting 98,304 kB of
	// buckets — so the line under-reported peak memory by more than half,
	// and four successive measurements of this join's memory behaviour
	// (2026-09-05/06, `analysis/minimize-datum/`) were read against it.
	// Ledger `take3-D-05-spacepeak-reporting`.
	peak := bs.peakSpace + int64(bs.nbuckets)*hashsize.MapSlotBytes
	if peak > st.SpacePeak {
		st.SpacePeak = peak
	}
}

// joinBatchEligible reports whether this join may spill its build.
//
// M0127-P3.4 admits LEFT (probe-side fill), Semi and Anti. The argument that
// lets them in is one sentence from 06 §2.5: **a probe row belongs to exactly
// one batch, and so does every build row that could match it** — equal keys
// hash equal, so they route together. Every per-probe-row decision these join
// types make (emit-at-most-once for Semi, emit-iff-no-match for Anti, null-pad
// on miss for LEFT) is therefore decidable inside the row's own batch, with no
// cross-batch state. The two things that are NOT batch-local are handled
// elsewhere: `antiBuildHasNull`/`antiBuildRows` are maintained by the build
// loop before any row is routed, so they are batch-global by construction; and
// the batch-SKIP rule stops being "one side empty ⇒ no output" (see
// `batchSkippable`).
//
// Still excluded, each because its correctness argument is a separate slice:
//
//   - a LEFT join built on the LEFT side fills from the BUILD side, which needs
//     the post-replay unmatched sweep of 07 §3 — M0127-P4.2 (the executor has
//     no build-side fill at all today, batched or not);
//   - the composite-key lane keys its own maps (join_composite_key.go) and
//     would need its packed key hashed the same way on both sides (deferral
//     ledger 2026-08-03 M0127-P3.2);
//   - the FOR-UPDATE ctid build keeps `lazyHashCTID` in lockstep with
//     `lazyHash`, so spilling one without the other would lose the tid a
//     downstream LockRows needs;
//   - `noBatch` is the caller-side decline; nothing sets it (E-09a publishes
//     a spilling shared build instead of declining it), and it is kept as the
//     knob a future decline would otherwise have to re-invent.
func (o *joinOp) joinBatchEligible() bool {
	if o.noBatch || o.multiKey() || o.preserveBuildSide || o.preserveCTIDRel != nil {
		return false
	}
	switch o.plan.Type {
	case optimizer.JoinTypeInner, optimizer.JoinTypeSemi, optimizer.JoinTypeAnti:
		// NullAware (NOT IN) rides along: its two short-circuits read the
		// build-global counters above and fire before nextLazy probes
		// anything, so they are unaffected by how the build was partitioned.
		return true
	case optimizer.JoinTypeLeft, optimizer.JoinTypeRight, optimizer.JoinTypeFull:
		// M0127-P4.2 (07 §3): every outer-join orientation batches now. The
		// probe-fill half was always per-row; the build-fill half is per-batch
		// because the sweep runs while that batch's table is still resident
		// (nextLazy's probe-EOF arm), so no unmatched row outlives its batch.
		return true
	}
	return false
}

// probeFillsUnmatched reports whether a probe row that matches nothing still
// produces output. It is PG's `HJ_FILL_OUTER` in the shape goopg's executor
// expresses it (nodeHashjoin.c's HJ_FILL_OUTER_TUPLE macro), and the batch-skip
// rule of 06 §2.3 turns on it: for a filling join an outer-only batch is NOT
// skippable, because every probe row it holds emits — null-padded under LEFT,
// as-is under ANTI. Skipping it loses rows silently, which is exactly the
// failure mode 06 §2.3 warns "the SF0.5 gate would catch only late".
func (o *joinOp) probeFillsUnmatched() bool {
	if o.plan.Type == optimizer.JoinTypeAnti {
		return true
	}
	// M0127-P4.2: the outer-join half is now one shared decision (07 §3), so
	// the batch-skip rule and the emit path cannot disagree about which side
	// fills.
	return o.fillProbeSide()
}

// buildFillsUnmatched is the same rule for the other side, PG's
// `HJ_FILL_INNER`. It gives batchSkippable the arm 06 §2.3 left implicit: for a
// build-filling join an INNER-only batch is not skippable either, because every
// build row it holds is unmatched by construction and therefore emits.
func (o *joinOp) buildFillsUnmatched() bool {
	return o.fillBuildSide()
}

// newHashBatchState installs batching for a build whose geometry came out
// multi-batch. buildIsLeft says which side the build drains, so a reloaded row
// can have its key re-evaluated exactly the way the original insert did.
func newHashBatchState(ctx *Context, plan *optimizer.Join, sizing hashsize.Sizing, buildIsLeft bool) *hashBatchState {
	nbatch := sizing.NBatch
	if nbatch > maxJoinBatches {
		nbatch = maxJoinBatches
	}
	// The bucket array is real memory and is charged against the budget the
	// rows get to use, exactly as PG charges `nbuckets * sizeof(HashJoinTuple*)`
	// to spaceUsed in ExecHashTableCreate.
	space := sizing.SpaceAllowed - int64(sizing.NBuckets)*hashsize.MapSlotBytes
	if space < minBatchSpaceAllowed {
		space = minBatchSpaceAllowed
	}
	bs := &hashBatchState{
		nbatch:         nbatch,
		origNBatch:     nbatch,
		nbatchOutstart: nbatch,
		bucketBits:     uint(bits.Len(uint(sizing.NBuckets)) - 1),
		spaceAllowed:   space,
		growEnabled:    true,
		buildIsLeft:    buildIsLeft,
		ctx:            ctx,
		nbuckets:       sizing.NBuckets,
		stats:          ctx.hashJoinStat(plan),
	}
	bs.inner = make([]*joinBatchFile, nbatch)
	bs.outer = make([]*joinBatchFile, nbatch)
	// Publish before a single row arrives: a build that produces no output at
	// all still chose a geometry, and PG reports it (nbatch > 0 is the only
	// gate show_hash_info applies).
	bs.publish()
	return bs
}

// batchOf maps a hash value onto a batch number, PG's
// `(hashvalue >> log2_nbuckets) & (nbatch - 1)`.
func (bs *hashBatchState) batchOf(h uint32) int {
	if bs.nbatch <= 1 {
		return 0
	}
	return int((h >> bs.bucketBits) & uint32(bs.nbatch-1))
}

func (bs *hashBatchState) write(files []*joinBatchFile, b int, h uint32, row Row) error {
	f := files[b]
	if f == nil {
		w, err := newSpillWriter(bs.ctx)
		if err != nil {
			return err
		}
		f = &joinBatchFile{w: w, path: w.Path(), createdNBatch: bs.nbatch}
		files[b] = f
	}
	if f.w == nil {
		// A frozen file (freezeForSharing) has no writer by construction.
		// Reaching it is a bug in the caller, never a data condition.
		return &ExecError{
			Code:    "XX000",
			Message: fmt.Sprintf("hash join attempted to write frozen batch file %d", b),
		}
	}
	if err := f.w.WriteRowHashed(h, row); err != nil {
		return err
	}
	f.rows++
	return nil
}

func (bs *hashBatchState) writeInner(b int, h uint32, row Row) error {
	if bs.innerShared {
		// E-09a invariant: a participant never writes a shared inner file.
		// Every legitimate writer (the build loop, growth, the forward
		// re-route on reload) is confined to the leader's prebuild, which
		// completes before this state exists; this arm is the poison that
		// turns any future violation into an error rather than a partition
		// silently read by the other participants.
		return &ExecError{
			Code:    "XX000",
			Message: fmt.Sprintf("hash join participant attempted to write shared inner batch file %d", b),
		}
	}
	bs.innerSpilled++
	return bs.write(bs.inner, b, h, row)
}

func (bs *hashBatchState) writeOuter(b int, h uint32, row Row) error {
	bs.outerSpilled++
	return bs.write(bs.outer, b, h, row)
}

// insertBuildRow is the batched replacement for lazyHashInsertDatum: a row
// whose key hashes into the current batch goes into the in-memory table, every
// other row goes to its batch file (PG's ExecHashTableInsert /
// ExecHashJoinSaveTuple split, nodeHash.c:1616).
//
// `row` must already be owned storage (ownedBuildRow) — it is either retained
// by the map or encoded into a file, and both outlive the child's slot.
func (bs *hashBatchState) insertBuildRow(o *joinOp, kd Datum, row Row) error {
	// The hash is only needed once there is more than one batch to choose
	// between. Skipping it keeps the single-batch case — every join that fits
	// work_mem, which is nearly all of them — at one add and one compare.
	if bs.nbatch > 1 {
		h := joinBatchHash(kd)
		if b := bs.batchOf(h); b != bs.curBatch {
			return bs.writeInner(b, h, row)
		}
	}
	o.lazyHashInsertDatum(kd, row)
	bs.spaceUsed += estimatedRowBytes(row) + hashsize.RowSliceBytes
	if bs.spaceUsed > bs.peakSpace {
		bs.peakSpace = bs.spaceUsed
	}
	if bs.spaceUsed > bs.spaceAllowed && bs.growEnabled {
		return bs.increaseNumBatches(o)
	}
	return nil
}

// increaseNumBatches doubles nbatch and evicts every in-memory row that the
// new batch count no longer assigns to the current batch
// (ExecHashIncreaseNumBatches, nodeHash.c:1030).
//
// goopg's eviction is per-KEY where PG's is per-tuple, and that is not a
// shortcut: every row filed under one map key shares that key's hash value, so
// a bucket moves or stays as a unit. The map key itself carries enough to
// recompute the hash (an int64 key is canonicalised the same way datumKey
// would, a string key IS the canonical form), so eviction never touches a
// Datum or an expression.
//
// PG's freeze rule is reproduced exactly: if the doubling moved out either
// none of the rows or all of them, subdividing further cannot help — the rows
// share too few distinct hash values — so growth is disabled and the current
// batch is allowed to exceed the budget. 06 §2.4 asks for that degradation to
// be loud, so it is logged; it is deliberately NOT a client-visible WARNING,
// because PG emits nothing here (HJDEBUG only) and a NoticeResponse goopg
// invents would be a protocol-level difference in every spilling query.
func (bs *hashBatchState) increaseNumBatches(o *joinOp) error {
	if bs.nbatch >= maxJoinBatches || bs.bucketBits+uint(bits.Len(uint(bs.nbatch))) >= 32 {
		bs.freezeGrowth("batch cap reached")
		return nil
	}
	bs.nbatch *= 2
	if n := bs.nbatch - len(bs.inner); n > 0 {
		bs.inner = append(bs.inner, make([]*joinBatchFile, n)...)
		bs.outer = append(bs.outer, make([]*joinBatchFile, n)...)
	}

	var inMemory, freed int64
	if o.lazyIntHash != nil {
		for ik, rows := range o.lazyIntHash {
			inMemory += int64(len(rows))
			h := joinBatchHashInt64(ik)
			b := bs.batchOf(h)
			if b == bs.curBatch {
				continue
			}
			for _, r := range rows {
				if err := bs.writeInner(b, h, r); err != nil {
					return err
				}
				bs.spaceUsed -= estimatedRowBytes(r) + hashsize.RowSliceBytes
				freed++
			}
			delete(o.lazyIntHash, ik)
		}
	}
	if o.lazyHash != nil {
		for sk, rows := range o.lazyHash {
			inMemory += int64(len(rows))
			h := hashKeyString(sk)
			b := bs.batchOf(h)
			if b == bs.curBatch {
				continue
			}
			for _, r := range rows {
				if err := bs.writeInner(b, h, r); err != nil {
					return err
				}
				bs.spaceUsed -= estimatedRowBytes(r) + hashsize.RowSliceBytes
				freed++
			}
			delete(o.lazyHash, sk)
		}
	}
	if freed == 0 || freed == inMemory {
		bs.freezeGrowth("hash values too few to subdivide")
	}
	// The doubling is the only thing that makes EXPLAIN's "Batches: N
	// (originally M)" form appear, so publish it here rather than waiting for
	// close — a query cancelled mid-build has still already paid for it.
	bs.publish()
	return nil
}

func (bs *hashBatchState) freezeGrowth(why string) {
	if !bs.growEnabled {
		return
	}
	bs.growEnabled = false
	slog.Warn("hash join stopped increasing batches; current batch may exceed work_mem",
		"reason", why, "nbatch", bs.nbatch, "batch", bs.curBatch,
		"space_used", bs.spaceUsed, "space_allowed", bs.spaceAllowed)
}

// batchSkippable reports whether batch b can be dropped unread — PG's three
// skip rules from ExecHashJoinNewBatch (nodeHashjoin.c:1141-1160), which are
// far easier to state than to get right:
//
//	rule 1  a batch empty on one side produces no output — UNLESS the join
//	        fills from the side that does have rows. An outer-only batch under
//	        LEFT/ANTI emits every row it holds (M0127-P3.4); an inner-only
//	        batch under RIGHT/FULL would emit on the unmatched sweep, which
//	        goopg does not have yet (P4.2), so only the outer arm exists here.
//	rule 2  an inner file written before a doubling may hold rows that now
//	        belong to a LATER batch; it has to be read so they can be
//	        re-routed forward, even with nothing to probe it with.
//	rule 3  the same for an outer file written before a doubling that happened
//	        after the outer scan began.
//
// Rules 2 and 3 are what make "empty on one side" insufficient on its own:
// dropping such a file unread loses its rows silently.
func (bs *hashBatchState) batchSkippable(o *joinOp, b int) bool {
	hasInner, hasOuter := bs.inner[b] != nil, bs.outer[b] != nil
	if hasInner && hasOuter {
		return false
	}
	if hasInner && bs.nbatch != bs.origNBatch && !bs.innerShared {
		// rule 2 — except on a participant, whose inner files were settled
		// by freezeForSharing: no row in inner[b] belongs to a later batch,
		// so there is nothing to re-route and the file may be skipped.
		return false
	}
	if hasOuter && bs.nbatch != bs.nbatchOutstart {
		return false // rule 3
	}
	if hasOuter && o.probeFillsUnmatched() {
		return false // rule 1, fill arm (outer side)
	}
	if hasInner && o.buildFillsUnmatched() {
		return false // rule 1, fill arm (inner side) — M0127-P4.2
	}
	return true
}

// nextBatch advances to the next batch that has work: it clears the in-memory
// table, reloads that batch's inner file into it, and points o.lazyProbe at a
// replay of the batch's outer file. Returns false when every batch is done.
//
// This is PG's HJ_NEED_NEW_BATCH state (ExecHashJoinNewBatch,
// nodeHashjoin.c:1130); the skip decision is factored into batchSkippable.
func (bs *hashBatchState) nextBatch(o *joinOp) (bool, error) {
	bs.closeReplay()
	bs.curBatch++
	for bs.curBatch < bs.nbatch && bs.batchSkippable(o, bs.curBatch) {
		bs.discard(bs.curBatch)
		bs.curBatch++
	}
	if bs.curBatch >= bs.nbatch {
		return false, nil
	}
	if err := bs.loadInnerBatch(o); err != nil {
		return false, err
	}
	var r *spillReader
	if bs.outer[bs.curBatch] != nil {
		var err error
		r, err = bs.openReader(bs.outer, bs.curBatch)
		if err != nil {
			return false, err
		}
	}
	bs.replayOp = &batchReplayOp{r: r, bs: bs}
	bs.replaying = true
	o.lazyProbe = bs.replayOp
	return true, nil
}

// loadInnerBatch reads the current batch's inner file back into the cleared
// hash table. A row whose stored hash now belongs to a LATER batch (nbatch
// grew after it was written) is re-routed forward instead of inserted — PG's
// replayed-tuple rule 2 (nodeHashjoin.c:1172-1202) — and that decision is made
// from the stored hash alone, with no key expression evaluated.
//
// Growth can fire again here, which is the estimate-was-low case 06 §2.4
// names: the same insert path runs, so a batch that still does not fit is
// subdivided exactly as the build was.
func (bs *hashBatchState) loadInnerBatch(o *joinOp) error {
	o.resetHashTable()
	bs.spaceUsed = 0
	if bs.inner[bs.curBatch] == nil {
		return nil
	}
	r, err := bs.openReader(bs.inner, bs.curBatch)
	if err != nil {
		return err
	}
	if bs.innerShared {
		// The file is the leader's and other participants are reading it:
		// close the descriptor, never the path.
		defer r.closeKeepFile()
	} else {
		defer r.Close()
	}
	if testHookInnerBatchOpened != nil {
		testHookInnerBatchOpened(bs, bs.curBatch, r.path)
	}
	var buf Row
	for {
		h, row, err := r.ReadRowHashedInto(buf)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		buf = row
		if b := bs.batchOf(h); b != bs.curBatch {
			if bs.innerShared {
				// freezeForSharing settled every file before publication,
				// so a foreign row here is a broken invariant — report it
				// rather than skip it (a skipped row is a lost match).
				return &ExecError{
					Code: "XX000",
					Message: fmt.Sprintf("shared hash join batch file %d holds a row of batch %d",
						bs.curBatch, b),
				}
			}
			// Re-spilled straight from the read buffer: the encode completes
			// before the next ReadRowHashedInto can overwrite it.
			if err := bs.writeInner(b, h, row); err != nil {
				return err
			}
			continue
		}
		owned := cloneRow(row)
		kd, ok, err := o.buildKeyOfRow(owned)
		if err != nil {
			return err
		}
		if !ok {
			// A NULL key cannot match anything; it was only spilled because
			// the build loop files rows before it knows that. Dropping it
			// here matches the in-memory build, which never inserts it —
			// and, since M0127-P4.2, retains it for the fill sweep on the
			// two join types where "matches nothing" still emits.
			o.recordBuildNullKey(owned)
			continue
		}
		o.lazyHashInsertDatum(kd, owned)
		bs.spaceUsed += estimatedRowBytes(owned) + hashsize.RowSliceBytes
		if bs.spaceUsed > bs.peakSpace {
			bs.peakSpace = bs.spaceUsed
		}
		if bs.spaceUsed > bs.spaceAllowed && bs.growEnabled {
			if err := bs.increaseNumBatches(o); err != nil {
				return err
			}
		}
	}
}

// openReader closes the batch's writer and hands back a reader over it,
// clearing the slot so the file is owned by exactly one of the two.
//
// The slot is the CALLER's: on a participant state `inner` is a private copy
// of the descriptor's slice, so clearing it there says "this participant has
// consumed batch b" without touching the leader's descriptor or any other
// participant. A frozen file (w == nil) is simply opened by path.
func (bs *hashBatchState) openReader(files []*joinBatchFile, b int) (*spillReader, error) {
	f := files[b]
	files[b] = nil
	if f.w != nil {
		if err := f.w.Close(); err != nil {
			return nil, err
		}
	}
	return newSpillReader(f.path)
}

// discard drops both files of a batch that will produce no output.
//
// A participant's inner files are the leader's (innerShared): they are only
// forgotten here, never closed or unlinked — the descriptor's release, or the
// statement registry, retires them once every participant has joined.
func (bs *hashBatchState) discard(b int) {
	if f := bs.inner[b]; f != nil {
		if bs.innerShared {
			bs.inner[b] = nil
		} else {
			bs.dropFile(f)
			bs.inner[b] = nil
		}
	}
	if f := bs.outer[b]; f != nil {
		bs.dropFile(f)
		bs.outer[b] = nil
	}
}

// dropFile closes a private batch file's writer (if still open) and unlinks
// it. Eager unlink + deregister: a 1024-batch join must not hold 1024 files
// open-and-linked to statement end just because the registry would eventually
// reclaim them.
func (bs *hashBatchState) dropFile(f *joinBatchFile) {
	if f.w != nil {
		f.w.Close()
		f.w = nil
	}
	bs.ctx.removeSpillFile(f.path)
}

func (bs *hashBatchState) closeReplay() {
	if bs.replayOp != nil {
		bs.replayOp.Close()
		bs.replayOp = nil
	}
	bs.replaying = false
}

// close releases every file the join still owns. Safe to call twice.
func (bs *hashBatchState) close() {
	// Peak memory is flushed here instead of from insertBuildRow, which is the
	// per-build-row hot path. Close is reached on every path that produces
	// EXPLAIN output — EXPLAIN ANALYZE drains and Closes the inner plan before
	// it renders a single line (operators_explain.go).
	bs.publish()
	bs.closeReplay()
	for b := range bs.inner {
		bs.discard(b)
	}
}

// ── E-09a: shared spilling build ────────────────────────────────────────

// sharedBatchDesc is the immutable batch descriptor a spilling shared build
// carries beside its batch-0 maps (DESIGN.md §4 part 1). It is written once,
// by the leader's prebuild, before any worker exists, and read by every
// participant afterwards: the geometry so each can route its own probe rows
// identically, and the inner files 1..n-1, frozen (no writer, settled under
// the final nbatch) so each participant can reload batch k through a reader
// of its own.
type sharedBatchDesc struct {
	nbatch       int
	origNBatch   int
	nbuckets     int
	bucketBits   uint
	spaceAllowed int64
	buildIsLeft  bool
	// inner[0] is always nil (batch 0 is the in-memory table); every other
	// non-nil entry has w == nil and a path readable by any number of
	// spillReaders at once.
	inner []*joinBatchFile
}

// freezeForSharing turns the leader's just-completed build state into a
// sharedBatchDesc and detaches the files from this state (which is never used
// again: the prebuild operator does not probe).
//
// Settling: PG's own rule (nodeHashjoin.c, "completes all changes to the
// number of batches during the build phase") is what makes a per-participant
// reload possible without a cross-worker protocol — but goopg's serial reload
// also RE-ROUTES rows forward (loadInnerBatch, rule 2): a file written before
// a doubling may hold rows the final nbatch assigns to a later batch, and the
// serial path fixes that lazily by appending to the later batch's file when
// it reads the earlier one. A participant may not append to a shared file, so
// the leader does that work ONCE here, before publication: every file created
// under a smaller nbatch is rewritten with its own rows and its foreign rows
// appended to their final batch's file. Files created after the last doubling
// are already final and are left alone. After this, no inner file holds a row
// of another batch, which loadInnerBatch on a participant asserts.
func (bs *hashBatchState) freezeForSharing() (*sharedBatchDesc, error) {
	// The build's peak is reported through the leader's sink now; the
	// participants report only their reload peaks.
	bs.publish()
	// Growth is over: nothing after this point may double nbatch, and the
	// descriptor below is only correct if that holds.
	bs.growEnabled = false
	if err := bs.settleInnerFiles(); err != nil {
		return nil, err
	}
	for b, f := range bs.inner {
		if f == nil {
			continue
		}
		if err := f.w.Close(); err != nil {
			return nil, err
		}
		f.w = nil
		if f.rows == 0 {
			bs.ctx.removeSpillFile(f.path)
			bs.inner[b] = nil
		}
	}
	d := &sharedBatchDesc{
		nbatch:       bs.nbatch,
		origNBatch:   bs.origNBatch,
		nbuckets:     bs.nbuckets,
		bucketBits:   bs.bucketBits,
		spaceAllowed: bs.spaceAllowed,
		buildIsLeft:  bs.buildIsLeft,
		inner:        bs.inner,
	}
	// The descriptor owns the files from here: this state must not unlink
	// them if it is ever closed.
	bs.inner = make([]*joinBatchFile, bs.nbatch)
	return d, nil
}

// settleInnerFiles rewrites every inner file created before the last doubling
// so that it holds only rows of its own batch, appending each foreign row to
// the file of the batch the final geometry assigns it. Rows only ever move
// FORWARD (the doubling property this file's header explains), so walking
// batches in ascending order visits every appended row exactly once, in a
// file that is either final already or settled later in the same walk.
func (bs *hashBatchState) settleInnerFiles() error {
	for k := 1; k < bs.nbatch; k++ {
		f := bs.inner[k]
		if f == nil || f.createdNBatch == bs.nbatch {
			continue
		}
		if err := f.w.Close(); err != nil {
			return err
		}
		f.w = nil
		r, err := newSpillReader(f.path)
		if err != nil {
			return err
		}
		nw, err := newSpillWriter(bs.ctx)
		if err != nil {
			r.closeKeepFile()
			return err
		}
		nf := &joinBatchFile{w: nw, path: nw.Path(), createdNBatch: bs.nbatch}
		bs.inner[k] = nf
		var buf Row
		for {
			h, row, err := r.ReadRowHashedInto(buf)
			if err == io.EOF {
				break
			}
			if err != nil {
				r.closeKeepFile()
				return err
			}
			buf = row
			b := bs.batchOf(h)
			if b < k {
				r.closeKeepFile()
				return &ExecError{
					Code:    "XX000",
					Message: fmt.Sprintf("hash join batch file %d holds a row of earlier batch %d", k, b),
				}
			}
			if b == k {
				if err := nf.w.WriteRowHashed(h, row); err != nil {
					r.closeKeepFile()
					return err
				}
				nf.rows++
				continue
			}
			if err := bs.write(bs.inner, b, h, row); err != nil {
				r.closeKeepFile()
				return err
			}
		}
		r.closeKeepFile()
		bs.ctx.removeSpillFile(f.path)
	}
	return nil
}

// release unlinks the descriptor's files. Called by the Gather that published
// the build, after every participant has joined; the statement's temp-file
// registry is the backstop for the paths that never reach here.
//
// Idempotent (removeSpillFile tolerates a missing path), and the descriptor
// is left intact so a post-mortem — EXPLAIN, a test — can still read the
// geometry it published.
func (d *sharedBatchDesc) release(ctx *Context) {
	if d == nil {
		return
	}
	for _, f := range d.inner {
		if f != nil {
			ctx.removeSpillFile(f.path)
		}
	}
}

// newParticipantBatchState derives one participant's private batch state
// from a shared descriptor (DESIGN.md §4 part 3). Everything a probe mutates
// is this participant's own — the outer files, curBatch, the replay operator,
// spaceUsed and the stats sink (ctx is the participant's context, so a
// worker's EXPLAIN counters merge through MergeWorkerContext exactly as a
// private build's would). Only `inner` is shared, as a COPY of the slice whose
// entries alias the leader's frozen files: nextBatch clears a slot when the
// participant has consumed that batch, and that must not be visible to anyone
// else.
func newParticipantBatchState(ctx *Context, plan *optimizer.Join, d *sharedBatchDesc) *hashBatchState {
	bs := &hashBatchState{
		nbatch:         d.nbatch,
		origNBatch:     d.origNBatch,
		nbatchOutstart: d.nbatch,
		bucketBits:     d.bucketBits,
		spaceAllowed:   d.spaceAllowed,
		growEnabled:    false, // DESIGN.md §4 part 2: frozen after prebuild
		buildIsLeft:    d.buildIsLeft,
		ctx:            ctx,
		nbuckets:       d.nbuckets,
		stats:          ctx.hashJoinStat(plan),
		innerShared:    true,
	}
	bs.inner = append([]*joinBatchFile(nil), d.inner...)
	bs.outer = make([]*joinBatchFile, d.nbatch)
	return bs
}

// batchReplayOp streams a saved outer batch file back as the probe input.
// It records each row's stored hash on the batch state so nextLazy can
// re-route a row that a later doubling pushed past the current batch without
// evaluating its key (PG's replayed-tuple rule 3).
type batchReplayOp struct {
	r   *spillReader
	bs  *hashBatchState
	out Row
}

func (op *batchReplayOp) Open(*Context) error    { return nil }
func (op *batchReplayOp) Schema() optimizer.Schema { return nil }

func (op *batchReplayOp) Next() (TupleSlot, error) {
	if op.r == nil {
		// A batch whose outer side is empty still has to be entered when a
		// doubling means its inner file needs reassigning (rule 2); there is
		// simply nothing to probe with.
		return nil, EOF
	}
	h, row, err := op.r.ReadRowHashedInto(op.out)
	if err == io.EOF {
		return nil, EOF
	}
	if err != nil {
		return nil, err
	}
	op.out = row
	op.bs.replayHash = h
	// The probe row stays bound while its whole match set drains, so it may
	// not alias the reader's reusable buffer.
	return asSlot(nil, cloneRow(row)), nil
}

func (op *batchReplayOp) Close() error {
	if op.r != nil {
		op.r.Close()
		op.r = nil
	}
	return nil
}

// resetHashTable empties the build table between batches, keeping whichever
// representation the build chose.
func (o *joinOp) resetHashTable() {
	// M0127-P4.2 (07 §3): the matched bitmaps are parallel to the table, so
	// they are per-batch too — the outgoing batch has already been swept by the
	// time nextBatch calls this.
	o.lazyMatchedS = nil
	o.lazyMatchedI = nil
	o.lazyMatchedCur = nil
	if o.lazyIntHash != nil {
		o.lazyIntHash = make(map[int64][]Row)
	}
	if o.lazyHash != nil {
		o.lazyHash = make(map[string][]Row)
	}
	if o.lazyHash == nil && o.lazyIntHash == nil {
		if o.lazyHashIsInt {
			o.lazyIntHash = make(map[int64][]Row)
		} else {
			o.lazyHash = make(map[string][]Row)
		}
	}
}

// buildKeyOfRow re-evaluates the build-side join key of a row reloaded from a
// batch file, through the same merged key slot the build loop used.
func (o *joinOp) buildKeyOfRow(row Row) (Datum, bool, error) {
	if o.batchKeySlot == nil {
		o.batchKeySlot = &MaterializedSlot{}
	}
	o.batchKeySlot.row = row
	realWidth, nullWidth := o.lazyRW, o.lazyLW
	if o.batches.buildIsLeft {
		realWidth, nullWidth = o.lazyLW, o.lazyRW
	}
	keySlot := o.lazyBuildKeySlot.rebind(o.batchKeySlot, realWidth, nullWidth, o.batches.buildIsLeft)
	return o.evalHashKeyDatumSlot(o.buildKeyNodes[0], keySlot)
}

// routeProbeRow decides whether a probe row belongs to the current batch. When
// it does not, the row is saved to its batch's outer file and the caller must
// move on to the next probe row (PG's ExecHashJoinOuterGetTuple,
// nodeHashjoin.c:979).
func (bs *hashBatchState) routeProbeRow(h uint32, row Row) (routed bool, err error) {
	b := bs.batchOf(h)
	if b == bs.curBatch {
		return false, nil
	}
	if b < bs.curBatch {
		// Unreachable by construction: doubling only moves rows forward, and
		// a batch is never revisited. Report it rather than silently drop the
		// row, because "silently drop" is the failure mode this whole file is
		// meant to make impossible.
		return false, &ExecError{
			Code:    "XX000",
			Message: fmt.Sprintf("hash join probe row routed backwards (batch %d < current %d)", b, bs.curBatch),
		}
	}
	// No copy: WriteRowHashed encodes the datums before returning, and the
	// producer cannot advance (or reset its arena) until the caller asks for
	// the next row.
	if err := bs.writeOuter(b, h, row); err != nil {
		return false, err
	}
	return true, nil
}

// joinBatchHash is the batch-routing hash of a join key.
//
// It hashes the key's CANONICAL form — the same bytes datumKey produces — and
// that is the whole correctness argument. The executor has two key lanes (a
// map[int64] fast lane and a map[string] general lane) and can move from the
// first to the second mid-build (demoteIntHash), while a value that is
// int64-representable in one datum kind may arrive in another kind that is
// not. Hashing the canonical bytes makes every representation of one value
// route to one batch; hashing "the int if it is an int, the string otherwise"
// would not, and the symptom would be a lost match, not an error.
//
// The int64 path formats into a stack buffer, so the fast lane pays no
// allocation for its routing.
func joinBatchHash(d Datum) uint32 {
	if ik, ok := datumToInt64Key(d); ok {
		return joinBatchHashInt64(ik)
	}
	return hashKeyString(datumKey(d))
}

func joinBatchHashInt64(v int64) uint32 {
	var buf [32]byte
	return hashKeyBytes(appendCanonicalNumericKey(buf[:0], v, 0))
}

// hashKeyString / hashKeyBytes are FNV-1a 32 plus an avalanche finaliser.
// The finaliser matters here in a way it would not for a bucket hash: batch
// selection reads the HIGH bits (`h >> bucketBits`), and raw FNV-1a's high
// bits are the least mixed part of its state.
func hashKeyString(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return mix32(h)
}

func hashKeyBytes(b []byte) uint32 {
	h := uint32(2166136261)
	for _, c := range b {
		h ^= uint32(c)
		h *= 16777619
	}
	return mix32(h)
}

func mix32(h uint32) uint32 {
	h ^= h >> 16
	h *= 0x7feb352d
	h ^= h >> 15
	h *= 0x846ca68b
	h ^= h >> 16
	return h
}
