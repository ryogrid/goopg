# E-18 / EX5-03 — `Parallel Hash`: split the hash BUILD across workers

> **CORRECTION 2026-09-07 (implementation session). Read this before §0.**
>
> **goopg already has a cooperative parallel hash build.**
> `parallelBuildLazyHashTable` (`internal/executor/parallel_hash_build.go`,
> M0129-S4.1) is a producer/consumer split: N goroutines scan+filter the
> build side, claiming disjoint blocks from a shared `ParallelScanState`,
> while ONE consumer goroutine owns the map and inserts. §6.1's source pass
> read `prebuildSharedHashJoins` (:203) and concluded the build cannot be
> split at all; the cooperative builder is in the same file, and it fires
> for 17 of the 38 TPC-H build sites (measured, SF=1, bench settings).
>
> Two consequences invalidate §4's phase plan:
>
> 1. **Phase 1 is not work.** "Sharded map + phase barrier" is one way to
>    reach a cooperative build. goopg took a better one — single writer, no
>    lock, no barrier. There is nothing to build.
> 2. **Phase 2's hard half — SHARED BATCH FILES — is not needed.** §3.4 and
>    §4 treat them as the prize's precondition. They are PG's precondition,
>    not goopg's: `MultiExecParallelHash` needs them because every backend
>    inserts, so N backends write one batch. goopg has exactly ONE writer,
>    so batch files stay per-operator and single-writer, and the parallel
>    build SCAN is obtained with none of that machinery.
>
> What was actually missing was **eligibility, not mechanism** —
> `parallelBuildEligible`'s rules. Decline census over TPC-H SF=1:
> 17 OK / 13 `notseqscan` / 4 `multibatch` / 3 `multikey` / 1 `jointype`.
> Slice 1 retired the `multibatch` rule (its premise, "spilling builds
> can't be shared", was retired by E-09a/E-09b); slice 2 widened the
> `notseqscan` walker by one node kind, a hash join's PROBE side, and
> prebuilt the nested joins once in the leader so producers do not redo
> them. Both landed. Measured, parallel mode, values md5-identical:
> Q7 −25.3%, Q20 −66%, Q5/Q21 −6..7%.
>
> **§0's verdict ("no witness for the cheap half; the prize needs the full
> port") is therefore wrong in both halves.** The cheap half had witnesses
> the census could not see because it counted BUILD SIZES rather than
> ELIGIBILITY DECLINES, and the prize did not need the port.
>
> **What §0 was right about is Q9**, and the reason is neither of the ones
> in this doc. Q9's serial critical path is the 6M-row `lineitem` scan,
> which sits on a nested BUILD side, and `collectShareableJoins` descends
> PROBE sides only by design. Reaching it needs a bottom-up cooperative
> build of the whole inner chain — an ordering problem in goopg's own
> prebuild walk, not PG's shared-batch-file problem. See TODO_ALL E-18.

*TODO_ALL row: `docs/design/not_ralph/minimize_datum/TODO_ALL.md` E-18.
Status: DESIGN + second-witness census (2026-09-07). Implementation NOT
started — this doc is the row's mandated first deliverable ("design doc +
agent review + commit the design first, then implement").*

## 0. Verdict first, because the row demands the witness before the machinery

**There is no second witness for a single-batch cooperative build, and the
row's own Q9 witness needs the FULL machinery, not the cheap half.**

Per-site retained build sizes, TPC-H suite-wide (E-14 census,
`analysis/minimize-datum/tracke-e07-e13-e14-e09c-20260907/`, Datum bytes
at 48 B/cell):

| site (rows × width) | retained | batches at 64 MB work_mem |
|---|---|---|
| 6,001,255 × 16 | 4,609 MB | multi |
| 1,500,000 × 9 | 648 MB | multi |
| 800,000 × 5 | 192 MB | multi |
| 730,895 × 9 | 316 MB | multi |
| 455,688 × 9 | 197 MB | multi |
| 150,000 × 8 | 57.6 MB | single (maybe) |
| 29,824 × 8 / 10,000 × 7 ×2 | ≤ 11 MB | single |

Every multi-hundred-MB build is multi-batch at bench settings (the
spilling labels Q9/Q16/Q18/Q21 say the same from the execution side).
The ONLY single-batch-but-large candidate in either corpus is the
150,000 × 8 site (≈ 57 MB, build time order 100–200 ms) — parallelising
it 4× saves < 150 ms on one query. That is not a witness; it is E-04
territory (implement–measure–drop), and Phase 1 below is NOT built on
its strength.

Consequence for scoping: a cooperative build that declines spilling
(the cheap form, §4 Phase 1) has nothing to pay for at bench scale. The
prize — Q9 included — lives behind SHARED BATCH FILES (the full port,
§4 Phase 2). This doc specifies both, recommends Phase 2 as the priced
work, and records Phase 1 as specified-but-not-built.

## 1. What is missing (evidenced, not inferred)

Plans captured in parallel mode on both engines
(`analysis/planner-refactor-take3/q9-parallel-plans-20260907/`,
`max_parallel_workers_per_gather = 4`):

| TPC-H Q9 | PG 18.3 | goopg (tip) |
|---|---|---|
| `Parallel Seq Scan` nodes | 3 | **1** |
| `Parallel Hash` nodes | **4** | **0** |
| top-level cost | 113,104 | 836,644 (7.4×) |

PG runs `Parallel Hash Join` with a `Parallel Hash` over a `Parallel Seq
Scan on partsupp`. goopg parallelises only the `orders` scan; its inner
chain runs single-threaded while four workers wait. **What is missing is
precisely the work-splitting, NOT the sharing** — goopg has NEITHER of
PG's two parallel hash joins (C-19f): not the parallel-oblivious variant
(N private copies) and not `parallel_hash=true` (cooperative build). It
is a third thing — **the leader builds once and shares by pointer**
(`prebuildSharedHashJoins`, `parallel_hash_build.go:203`), and since
E-09a/E-09b that holds for spilling builds too. On the MEMORY axis goopg
is already where `parallel_hash=true` is: one table, not N. Only dividing
the *work* of building it is absent.

Correcting the record (again — it keeps reappearing in summaries): the
claim "each worker rebuilds the whole inner" is **false**. The leader
builds once; workers adopt the frozen descriptor. Report §9.2c.

**Known interaction:** the base-rel scan costing fix (`bf6109210`) moved
Q9 onto PG's own Parallel Hash Join shape and it got **1.47× slower**
(12.6 → 18.6 s) precisely because goopg cannot serve that shape. An
executor gap a parity-correct plan exposes; expect more as plan parity
improves.

## 2. PG's mechanism (oracle, read-only `postgres/`)

`MultiExecParallelHash` (`nodeHash.c:211-...`): a barrier-phased
cooperative build. Phases (`BarrierPhase(build_barrier)`):
`PHJ_BUILD_ALLOCATE` (one worker creates the shared table, all arrive) →
GROW batches/buckets (sub-barriers `grow_batches_barrier`,
`grow_buckets_barrier`) → `PHJ_BUILD_HASH_INNER` (every backend scans its
inner partition and inserts concurrently, skew tuples to skew tables via
`ExecHashSkewTableInsert`) → `PHJ_BUILD_HASH_OUTER` → RUN. Spills go to
SHARED batch files. Stats via `SharedHashInfo` (`execNodes.h:2795`:
`num_workers` + flex `hinstrument`), copied back by the leader at DSM
shutdown.

What PG has that goopg does not need: skew tables (goopg has no
skew-resident optimisation — E-02 is out of scope — so there is nothing
to parallelise there); DSM (goroutines share the address space; the
"shared memory" is a pointer).

## 3. goopg deltas — why this is a design, not a transcription

1. **The tables are plain Go maps** (`lazyHash map[string][]Row`,
   `lazyIntHash map[int64][]Row`, `operators_join_agg.go:54-72`) —
   concurrent insert is a data race by construction. The port needs
   sharded maps (shard by hash, one lock per shard) or partition-local
   tables merged under barrier. Sharded insert is the recommendation:
   probe-side reads stay lock-free after the build barrier flips the
   table read-only.
2. **No Barrier primitive exists** (`ParallelGroup` has `WaitGroup` +
   errBox + cancel, `parallel_runtime.go:155`). A phase barrier (arrive-
   and-wait with generation count) is new infrastructure with its own
   tests — it must also compose with `runWorker`'s IGNORED attach
   return (a worker that arrives late must still see a complete table,
   never a half-built one).
3. **Growth is currently free-fire then frozen**
   (`parallel_hash_build.go` header: growth fires during the leader
   build, frozen at descriptor cut). Concurrent growth needs either
   phase-gated growth (PG's GROW phases) or pre-sizing with a serial
   fallback when exceeded. Pre-sizing + fallback is the recommendation:
   the estimates already exist (`presizeLazyHash`), and a wrong estimate
   degrades to today's behaviour rather than to a corrupt table.
4. **Batch files are per-operator** (`join_batch.go` batch state is
   operator-local; E-09a freezes it into a descriptor workers derive
   from). SHARED batch files (concurrent writes at computed offsets)
   are the hard half of Phase 2 — the design must route around
   `bufio` buffering (per-worker buffers + explicit offset allocation
   under mutex) and the `tempFiles` registry (statement-end unlink must
   survive a worker dying mid-build).
5. **Keyed frames already travel** (E-14: `[4B hash][1B tag][key]
   [payload]`) — the canonical key rides the frame, so a worker
   inserting another worker's partition-crossing row... no: partitions
   are disjoint by scan block, and every row's key is computed by the
   worker that scanned it. No key traffic is needed. Stated because the
   first draft assumed otherwise.
6. **Arena ownership** (`buildBytes *mmgr.Context`, adopted by workers):
   a cooperative build must parent allocations per-worker (or accept
   arena contention) and STILL free exactly once at statement end (Cut 3
   owns the teardown). Parent-per-worker + leader-owned release list.

## 4. Slices, in landing order

- **Phase 1 — single-batch cooperative build: SPECIFIED, NOT BUILT.**
  Sharded map + phase barrier + pre-sized growth + serial fallback on
  spill prediction. Gated on a witness §0 shows does not exist. It stays
  a specification so Phase 2 does not re-derive it, not a commit.
- **Phase 2 — shared batch files + the spilling path.** The actual
  prize (every paying build at bench scale spills). Slices: (a) barrier
  primitive + tests; (b) sharded insert + pre-size + fallback, behind a
  knob, single-batch shapes first with the fallback armed and COUNTED
  (a fallback that never fires is untested); (c) shared batch files +
  registry survival; (d) the Q9-class A/B.
- **Explicitly not this item:** `parallel_hash=true`'s cooperative
  PROBE-side sharing is already delivered better (pointer adoption);
  skew tables (no goopg skew optimisation exists to parallelise);
  `Parallel Hash` plan-shape production (C-19f landed the path; the
  planner half is done).

## 5. Gates (for Phase 2)

Values both suites (ordered hashes, not row counts); a parallel-mode A/B
on Q9-class witness shapes (serial captures are BLIND to this item —
`estimate-audit` defaults `-serial`); the N-copies identity test extended
to build-partitioned merges... to build-partitioned hash joins (a worker
that misses a partition drops matches — plausible row counts, no error);
`plan_snapshots/` re-pin only if plans move (they should not — executor-
only); the fallback-fired counter reported alongside every number (an
unfired fallback is an untested one).

## 6. Adversarial review

### 6.1 Source-falsification pass (`internal/`, at `eabb38e0b`)

- Plain maps confirmed (`operators_join_agg.go:54-72`); no sync anywhere
  on the build path (only `join_batch.go:896`'s batch mutex, which guards
  batch-file state, not the table).
- `ParallelGroup` confirmed barrier-less (`parallel_runtime.go:155-160`).
- Leader-builds-once confirmed (`prebuildSharedHashJoins`,
  `parallel_hash_build.go:203`); workers adopt frozen descriptor
  (`sharedHashBuild`, `:42-62`); growth fires-then-freezes (file header).
- Per-site retained sizes confirmed from the E-14 census README (§0
  table); spilling labels Q9/Q16/Q18/Q21 confirmed in E-14's gate notes.
- CORRECTION APPLIED: first draft scoped Phase 1 as the build ("cheap
  cooperative build first"). The census (§0) falsified its premise —
  exactly one ≤64 MB site exists — so Phase 1 is specified-not-built
  and Phase 2 carries the prize. The draft's "second witness" search is
  §0 itself.

### 6.2 PG 18.3 oracle pass (`postgres/`, read-only)

- Barrier phases confirmed (`nodeHash.c:251-358`: ALLOCATE → GROW
  sub-barriers → HASH_INNER → HASH_OUTER → RUN).
- Shared batch files confirmed (spills go through the shared table
  machinery, not per-backend files).
- `SharedHashInfo` confirmed (`execNodes.h:2795-2799`).
- Skew tables confirmed present upstream AND correctly excluded here
  (no goopg skew optimisation exists; E-02 out of scope).
- CORRECTION APPLIED: first draft cited `create_plain_partial_paths`
  as the planner half E-18 needs. It does not — C-19f already landed
  `addPartialHashJoinPath` and the `Parallel Hash` plan shape exists;
  the planner half is DONE and §1's capture proves it (the shape is
  planned; only the executor cannot serve it).
