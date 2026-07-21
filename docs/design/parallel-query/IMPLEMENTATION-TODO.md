# IMPLEMENTATION-TODO — parallel-query, phases P0–P5

| field | value |
| --- | --- |
| status | **P0–P6 complete and MEASURED.** Parallel scan is live by default and delivers ~2.8x on scan-bound TPC-H queries (Q1 2.75x, Q6 2.83x) at 3-way parallelism |
| started | 2026-07-21 |
| branch | `parallel-query` |
| start HEAD | `592f166a` (bundle commit) |
| scope | phases **P0–P5** of [10-roadmap.md](10-roadmap.md) — every phase that is scaffolding with **zero user-visible behaviour change**. P6 (enable Gather insertion) is reported and approved separately |
| tracking rule | a stage is DONE only when every gate line below it carries a measured result and a commit hash |

One commit + push per stage. Standing gates on every stage: units
(`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`),
`make race-gate`, `scripts/tpch-spotcheck.sh` (Q12=2 / Q13=33),
`make plan-gate`, and the pre-commit pgbench smoke — never `--no-verify`.

**Sequencing rule:** run the capped bench server and the pgbench smoke
*sequentially*, never concurrently. A concurrent bench server has been measured
degrading the smoke gate from 700 to 390 TPS and aborting a transaction.

## Stage table

| # | Stage | Scope | Status |
|---|---|---|---|
| P0 | GUC fidelity fixes | `UnitBlocks`, `min_parallel_*` units, `max_parallel_workers`, enum bool synonyms, cost ceilings | [x] (this commit) |
| P1 | Session GUC plumbing | `session*` readers + typed context fields, both protocol paths | [x] (this commit) |
| P2 | `HashAggregate` label correction | rename before `Partial `/`Finalize ` prefixes cement the misnomer | [x] (this commit) |
| P3 | Concurrency substrate | worker contexts, per-worker arenas, `Perm()` mutex, error/panic/cancel | [x] (this commit) |
| P4 | Parallel Seq Scan + Gather | shared block allocator, Gather operator; insertion still OFF | [x] (this commit) |
| P5 | Partial / Finalize aggregation | combine rules + whitelist refusals | [x] `726999e4` |
| P6 | Enable Gather insertion | post-pass, safety refusals, worker-count rule, identity gate | [x] `90ac9d98` |
| P7 | Gather Merge | per-worker Sort + leader-side merge; ordering gate | [x] `a98b5a64` |
| P8 | Parallel Hash Join | build once in the leader, share by pointer; partial probe side | [x] `37742171` |
| P9 | Partial / Finalize placement | split aggregates across the Gather; EXPLAIN prefixes | [x] `9917539d` |
| P10 | Split cost model | gate the split on the reduction ratio; chapter 11 | [x] (this commit) |

## Design amendments to fold in during these stages

Two survey findings postdate the bundle and must land in the docs alongside the
code:

- **Plan cache defeats per-session parallel planning.** `plancache.go` is
  process-wide and cross-session, keyed on `namespace-oid + normalized SQL`
  only (`planCacheKey`, `dispatch.go:1598`). A plan built under
  `max_parallel_workers_per_gather = 4` would be reused by a session that set
  it to `0`, so `SET … = 0` would silently fail to disable parallelism.
  **Resolution (user-confirmed): the Gather post-pass runs AFTER the cache
  lookup, per statement** — the cache stores serial plans only. This requires
  the post-pass to be **non-mutating**: it returns a new root wrapping shared
  children, never edits a cached node in place, or it is a data race under
  `race-gate`. Lands with P6; recorded here so it is not rediscovered.
- **The extended protocol plans before the executor context exists.**
  `dispatch_extended.go` calls `planner.Plan` at `:92`/`:103`; `ectx` is built
  at `:141`. Planning-time GUC reads therefore go through
  `sess *config.SessionRegistry`, not the context. Lands with P1.

## P0 — GUC fidelity fixes  [x]

Three of these are observable through `SHOW` today, independently of whether
parallel query is ever implemented.

- [x] **`UnitBlocks` added** (`internal/config/guc.go`), mirroring upstream's
      `GUC_UNIT_BLOCKS` with `blockSize = 8192`. Surface touched: the `Unit`
      const block, `memoryDisplayUnits`, and `bytesFamily` inside
      `convertUnit`. Deliberately **not** `unitFromSuffix` — upstream has no
      `block` input suffix either.
- [x] **The negative-multiplier branch in `FormatDisplayValue`.** PG's blocks
      row for `kB` is `-(BLCKSZ/1024)` = −8: a block is *larger* than the
      display unit, so `convert_int_from_base_unit` multiplies rather than
      divides. goopg's loop only ever divided and gated on
      `n%multiplier == 0`, so 64 blocks matched no row and would have printed
      a bare `64` instead of `512kB`. This was the one structural change; the
      rest of the unit work is table data.
- [x] **`min_parallel_table_scan_size`** → `UnitBlocks`, BootVal `8MB`
      (stored 1024 blocks). Was `UnitKB`/`8388608`, i.e. `SHOW` answered
      **8GB** where PG answers **8MB** — wrong by 1024× in both value and
      unit, because the boot value was a byte count mislabelled as kB.
- [x] **`min_parallel_index_scan_size`** → `UnitBlocks`, BootVal `512kB`
      (stored 64 blocks). Was `512MB` by the same error. This is the case that
      exercises the negative multiplier.
- [x] `MinVal`/`MaxVal` verified rather than assumed: PG uses `0 .. INT_MAX/3`
      for both, and `715827882` is correct in blocks as well — the old kB
      registration happened to carry the same number.
- [x] **`max_parallel_workers` registered** (int, boot **8** — PG's default,
      not the 2 used by `max_parallel_workers_per_gather`, which is a
      different knob). It was absent entirely.
- [x] **`debug_parallel_query` accepts PG's hidden boolean synonyms.**
      Upstream lists `true/false/yes/no/1/0` as `config_enum_entry` rows with
      `hidden = true` (`guc_tables.c:395-405`), so `SET debug_parallel_query =
      true` works there and failed here. No existing pattern to copy — the
      `TypeEnum` arm never consulted `parseBoolish`. Implemented as a fallback
      gated on the enum offering **both** `on` and `off` (`enumHasBoolPair`),
      so enums like `IntervalStyle` are unaffected, and the synonyms are NOT
      added to `EnumOptions` so `pg_settings.enumvals` and the error HINT stay
      PG-shaped. One deliberate superset, documented in code: `parseBoolish`
      also takes `t`/`f`, which upstream's hidden list omits for this GUC —
      accepting a strict superset rejects no valid PG input and avoids a
      second boolean parser.
      **This one is load-bearing**: `debug_parallel_query` is the lever the
      P4/P5 correctness gates are built on.
- [x] `parallel_setup_cost` / `parallel_tuple_cost` `MaxVal` → `DBL_MAX`
      (`math.MaxFloat64`); was `1e15`.
- [x] **`postgresql.conf.sample` updated in the same commit** —
      `TestSampleConfigCoversRegistry` (`sample_test.go:56`) asserts
      bidirectionally that every registered GUC has a sample entry and that
      each entry's literal equals the raw `BootVal`. Added
      `max_parallel_workers`, changed the two `min_parallel_*` lines and their
      unit hint comments.

Coverage: `internal/config/parallel_gucs_test.go` — display/parse round-trips
for both blocks GUCs (including the 512kB negative-multiplier case), the
`max_parallel_workers` registration and range, the full synonym table for
`debug_parallel_query` plus the assertion that synonyms do **not** leak into
`EnumOptions`, the guard that a non-on/off enum still rejects booleans, and
the cost ceilings.

- gates: units PASS; race-gate PASS (`internal/config`); spotcheck PASS
  (Q12=2 / Q13=33); **plan-gate 22/22 MATCH** — zero diffs as predicted, no
  planner code touched
- commit: `a98b5a64`

## P1 — Session GUC plumbing  [x]

- [x] Five readers in `internal/server/dispatch.go`, following the
      `sessionStatsTarget` shape: `sessionMaxParallelWorkersPerGather`,
      `sessionMaxParallelWorkers`, `sessionMinParallelTableScanSize`,
      `sessionParallelLeaderParticipation`, `sessionDebugParallelQuery`.
- [x] **Fallback direction chosen per reader, not copied.** Worker counts fall
      back to **0 (serial)** because zero is a legitimate user setting and the
      safe direction. `min_parallel_table_scan_size` falls back to PG's
      **1024 blocks**, NOT to zero — zero would mean "every relation qualifies
      for a parallel path", the unsafe direction. Leader participation falls
      back to **on**, PG's default. This matters because six other
      `NewContext()` sites (COPY, DDL, role DDL) populate nothing.
- [x] All reads go through `sess.Get`, never `GetDisplay`: `Get` returns the
      canonical bare integer (`"1024"`), `GetDisplay` the human form
      (`"8MB"`). Internal arithmetic must use the former
      (`internal/config/session.go:85-90`).
- [x] Five typed fields on `executor.Context` documented in the `StatsTarget`
      convention (name the GUC, name the consumer, state what the zero value
      means, note the wire path populates it).
- [x] Assigned on **both** protocol paths — `dispatch.go` and
      `dispatch_extended.go`. The existing precedent is applied
      inconsistently (`FreezeMinAge` / `EnableOpportunisticPrune` are
      simple-path only), so it was not copied mechanically.
- [x] PL/pgSQL child-context propagation **verified rather than assumed**:
      `plpgsql_runtime.go:386-388` derives a child with `*child = *ctx`, a
      whole-struct copy, so new fields propagate for free. Pinned by a test,
      because that is a property of the copy style and a future switch to
      field-by-field derivation would silently zero them.
- [x] Planning-time reads deliberately NOT routed through the context — the
      extended path plans at `dispatch_extended.go:92`/`:103`, before `ectx`
      exists at `:141`. The Gather post-pass (P6) reads from `sess` at the
      planning call sites; these context fields serve execution-time needs
      (fan-out size, leader participation). The two needs are distinct.

Coverage: `internal/server/parallel_session_gucs_test.go` — per-reader tables
including the nil-registry case and the fallback *direction* each protects;
`TestSessionDebugParallelQuery` also exercises P0's hidden synonyms end to end
(`SET debug_parallel_query = true` observed as canonical `"on"`);
`TestParallelGUCsReachExecutorContext` closes the seam nothing covered before —
the `ectx.X = sessionX(sess)` assignment lines themselves, which is exactly how
the pre-existing simple-path-only inconsistency survived.

- gates: units PASS; race-gate PASS (`internal/executor`, `internal/config`);
  spotcheck PASS (Q12=2 / Q13=33); **plan-gate 22/22 MATCH** — zero diffs as
  predicted, nothing reads the values yet
- commit: `37742171`

## P2 — `HashAggregate` label correction  [x]

- [x] `describePlan` emitted `GroupAggregate (%d keys)` for grouped
      aggregates. In PG, `GroupAggregate` means specifically the **sorted,
      streaming** strategy (`AGG_SORTED`) — a strategy goopg does not have.
      Verified before renaming: `aggregateOp.Open` builds
      `groups := map[string]*groupRuntime{}` for every case
      (`operators_join_agg.go:1312`), with no sorted/streaming variant and no
      hash-agg spill. The label was describing a strategy the engine lacks.
- [x] The ungrouped case keeps `Aggregate`, which is already faithful — PG
      labels `AGG_PLAIN` that way regardless of strategy.
- [x] Done now rather than later because P5 prefixes these labels with
      `Partial `/`Finalize `, which would otherwise cement
      `Partial GroupAggregate` onto a hash node.
- [x] Verified no test or code depends on the old string — the only
      references were design docs and archived evidence captures, which are
      historical records and correctly keep the label they were taken with.

**Follow-up recorded, not done:** the `(%d keys)` suffix is goopg's own
invention; PG emits a bare `HashAggregate` plus a separate
`Group Key: <exprs>` detail line, which goopg does not emit at all. Adding it
is a genuine fidelity improvement but a *different* change, and bundling it
here would have muddied the plan-gate review — two edits landing in one
recapture is exactly how an unintended diff gets waved through.

Plan-gate diff review (the point of this stage): 17/22 queries diverged,
**20 changed lines, every one of them the label**. Classified mechanically by
sorting the diff — no tree shape, key count, filter, or scan line moved. New
baseline captured as `plan_snapshots/pq-p2-hashagg.txt`; re-run against it
returns 22/22 MATCH.

- gates: units PASS; race-gate PASS (`internal/executor`); spotcheck PASS
  (Q12=2 / Q13=33); **plan-gate: intended 20-line label-only diff, reviewed
  and recaptured; 22/22 MATCH against the new baseline**
- commit: `9917539d`

## P3 — Concurrency substrate  [x]

Chapter 03's contracts, with **no parallel execution attached**. `race-gate` is
the point of the stage.

- [x] `NewWorkerContext` (`internal/executor/parallel_worker_ctx.go`) splits the
      ~130-field `*Context` by field rather than introducing a type hierarchy.
      The field list was **re-derived against `context.go`**, not copied from
      the design — the struct is actively edited and a field added since would
      have defaulted to *shared*, the dangerous direction.
- [x] `ParamExec` / `ParamSet` / `ParamDirty` / `OuterRows` are **copied by
      value**, not cloned empty. Empty clones are a correctness bug:
      `ExecParamRef` raises `XX000 "SubPlan parameter $N read before
      assignment"` on an unset slot, and the slots are bound by the *enclosing*
      sublink before the inner plan runs. Copied rather than aliased because
      `SetParamExec` grows them lazily and a shared backing array would race on
      append. Pinned by test.
- [x] Connection callbacks left nil, so a worker reaching one panics at the
      call site instead of mutating session state off-goroutine. Consequence
      recorded for P6: virtual catalog relations are backed by the `Pg*Rows`
      callbacks, so the post-pass must refuse a Gather over them.
- [x] `MaterializeForTransfer` / `AssertTransferable`
      (`internal/executor/parallel_runtime.go`). The assertion checks
      `ArenaID == 0 || ArenaID == PermContextID` on **every kind**, not just
      string/bytes — `cloneRowOwned` promotes only those two, but big-mantissa
      `KindNumeric` is arena-backed too and falls through with `ArenaID`
      intact. Pinned by a test asserting `cloneRow` does **not** satisfy the
      contract, since it is the obvious-looking helper that passes every
      single-threaded test.
- [x] **`mctx.Perm()` made safe for concurrent allocation.** Pre-existing
      defect: the permanent arena is process-global with an unsynchronised bump
      allocator, and `Bytes` reads the same slice `growChunk` appends to and
      memmoves — so two concurrent *sessions* doing big-mantissa numeric
      arithmetic already raced, independently of parallel query.
      The lock lives at **package level**, not on `Context`: exactly one
      context is ever shared, and `Context` is size-constrained to ≤96 B by
      `TestContextSizeof` — an embedded `RWMutex` would cost 24 B on every
      per-statement and per-expression context to protect one process-global
      one. `RWMutex` because a permanent payload is written once and read many
      times.
      **Verified the test actually catches the bug**: temporarily disabling
      the gate makes `-race` report DATA RACE immediately.
- [x] `ParallelGroup` — first-error-wins box, per-worker `recover()` converting
      a panic to `XX000` (a panic in a goroutine the server did not start would
      otherwise kill the *process*; `serveConn`'s recover only covers the
      connection goroutine), and child-context cancellation.

**Design bug found by the stage's own test.** The first `Wait()` cancelled
before joining. That let a worker blocked on `ctx.Done()` wake, report
`context.Canceled`, and win the first-error race against a sibling's genuine
failure — the caller would have seen "context canceled" instead of the error
that actually failed the query. Cancellation is a *consequence* of failure
here, not a peer of it. `Wait` now joins first and cancels after; callers
terminating early call `Cancel` then `Wait`, which is the documented contract
and is what the Gather operator will do in P4.

- gates: units PASS; **race-gate PASS (46 packages)**; spotcheck PASS
  (Q12=2 / Q13=33); plan-gate 22/22 MATCH
- note: one race-gate run reported `TestReserveEmittedAndPublishConcurrentChainAndStripePublishConsistent`
  (`internal/wal`) failing. Investigated rather than re-run away: the test
  passes 5/5 in isolation and 3/3 for the whole package under `-race` both
  with and without this change, and a second full race-gate run is green. It
  touches no code this stage modifies. Recorded as load-dependent flakiness,
  not attributed to this change and not dismissed.
- commit: _(this commit)_

## P4 — Parallel Seq Scan + Gather  [x]

First stage that actually executes in parallel — but does **not** plan it.
Nothing inserts a Gather; the tests construct the node directly.

- [x] `parallelScanState` (`internal/executor/parallel_scan.go`): one
      `atomic.Uint64`. PG's equivalent lives in DSM behind a spinlock and
      carries chunk size, ramp-down schedule and sync-scan position; goopg
      needs none of that, so the state reduces to a counter. One block per
      claim — PG's chunking exists mainly to amortise its spinlock, which has
      no analogue here.
- [x] `seqScanOp` gains `pscan`. The two `curBlock++` sites were routed through
      a single `advanceBlock()` helper rather than edited in place, so the
      serial and parallel disciplines cannot drift apart. Everything else stays
      per-worker: pin, page, decode buffer, emitted slot, per-page arena, ring,
      prefetch watermark.
- [x] Ring **disabled** for parallel scans (`ScanRing` has zero
      synchronisation). Both documented side effects are stated in the code:
      it removes pool-pollution protection for exactly the large relations the
      ring exists to protect, and — because the hint-bit path is gated on
      `o.pinned != nil`, which is nil under the ring — it turns hint-bit
      writing **on** for those scans.
- [x] Prefetch disabled for parallel scans: with a shared allocator a worker's
      next block is not `curBlock+1`, so a per-worker lookahead window
      prefetches blocks it will never read. N workers supply the I/O
      concurrency directly.
- [x] `planner.Gather` + `gatherOp` (`internal/executor/operators_gather.go`):
      batching (256 rows), buffered channel as the entire flow-control
      mechanism, per-worker arenas allocated **by the leader**, and
      `Close` = cancel → **drain** → join → merge notices → release arenas.
- [x] Implemented as a legacy `Operator` — verified `buildRec`'s `default` arm
      wraps it in an `OpAdapter`, so the live `BuildFastIterator` path reaches
      it with zero slab changes. Each worker builds its own tree from the
      shared read-only plan (`Build` is a pure function of the plan node).
- [x] EXPLAIN: label, `planChildren` (a new node is invisible without it), and
      `Workers Planned:` in `emitNodeDetailLines` so it renders in plain
      EXPLAIN as PG does.

**Three bugs found by this stage's own tests**, all in the first cut:

1. **`max_parallel_workers = 0` meant "no cap" instead of "no parallelism".**
   The clamp was `if cap > 0 && n > cap`, so setting the GUC to zero — the
   documented way to disable parallel query — would have been silently
   ineffective. It now clamps unconditionally, matching the P1 readers whose
   fallback is 0 for exactly this reason.
2. **A Gather with zero workers returned zero rows.** That is a wrong-results
   bug wearing the costume of a degraded plan: a Gather is a transport, not a
   filter. Fixed by implementing leader participation now rather than deferring
   it — the leader runs the child itself whenever nothing else will, and
   honours `parallel_leader_participation` otherwise.
3. **Early `Close` reported SQLSTATE 57014.** Abandoning a scan (satisfied
   `LIMIT`, error above the Gather) cancels the workers, who then correctly
   report cancellation — and returning that turned a normal early exit into a
   query error. `Close` now distinguishes a self-inflicted cancellation from a
   genuine one.

Coverage: allocator partitioning under 8 goroutines (every block exactly once —
a duplicate double-counts rows, a gap loses them); union-of-workers row
identity; the transfer contract at the boundary; worker error; worker panic
(process must survive); **early-close deadlock** with workers deliberately
blocked mid-send; cancellation; goroutine-leak across five open/close cycles;
and the zero-worker case.

- gates: units PASS; **race-gate PASS (46 packages)**; spotcheck PASS
  (Q12=2 / Q13=33); plan-gate 22/22 MATCH — zero diffs as predicted, nothing
  inserts a Gather yet
- commit: _(this commit)_

## P5 — Partial / Finalize aggregation  [x]

The combine layer: merging the per-group partial states workers produce.

- [x] **No serialisation needed at all.** PG's `aggserialfn`/`aggdeserialfn`
      exist solely because an `internal`-typed transition state cannot cross a
      process boundary. Workers here hand the `aggRuntime` across a channel
      directly, which removes an entire feature surface — and with it a
      classic round-trip mismatch bug class. What it costs instead: a DEEP
      MERGE with an explicit rule per pointer field, not a struct add.
- [x] `aggregateIsDecomposable` is a **whitelist**. `applyAgg` ends in a
      `default:` arm that silently does `count++; sum += arg.Int` for any
      unrecognised name, so a blacklist would let an aggregate added later
      split through it and return garbage. Refusals: `DISTINCT`, aggregate
      `ORDER BY`, `array_agg`/`string_agg` (order-dependent), `WITHIN GROUP`,
      user aggregates without `COMBINEFUNC`, and anything unknown.
- [x] `avg` needed no new state: `sum` and `avg` share one transition arm
      accumulating `(sum, count)` and diverge only in `finishAgg`, so the
      composite transition state PG must synthesise already exists.
- [x] **The float variance lane — the trap.** `floatSx` is Σx, *not* the mean
      (read the field comment, not the algorithm's name), so `Sx` adds plainly
      and only `Sxx` needs a correction term. Implemented as PG's
      `float8_combine`, including its `N == 0` cases handled **before** the
      general formula — that formula divides by both counts, and a worker
      producing an empty partial for a group is routine.
- [x] The `regr_*`/`covar_*`/`corr` family adds plainly, and for a reason
      worth recording: goopg stores **uncentered** raw sums, unlike PG's
      centered Youngs-Cramer regression state. It is the opposite of the
      variance case, so the two must not be reasoned about together.
- [x] Exact `big.Int` and `big.Rat` lanes add, with nil treated as zero — a
      worker that saw no rows for a group leaves them nil.
- [x] Variance NaN convention (`floatM2 = NaN`) combined separately from
      `floatSpecial`, which covers only sum/avg.

**Verified the tests catch the design's own error.** Substituting the design's
original Chan-Golub-LeVeque-over-means formula makes
`TestCombineVarianceMatchesSerial` report `var_pop` as **7.75 and 16.0 where
serial says 4** — the silent wrong-results failure chapter 06 warns about,
committed by chapter 06's first draft and caught here.

Coverage: the whitelist and every refusal; exact aggregates asserted
**bit-identical** to serial across 2/3/4-way splits; the variance family within
a ULP tolerance (float lanes cannot be bit-identical — chapter 09 §1's stated
carve-out); empty partials in both directions; the full NaN/±Inf precedence
table; the exact-integer lane including a nil partial; and the loud failure for
an aggregate with no rule.

- gates: units PASS; **race-gate PASS**; spotcheck PASS (Q12=2 / Q13=33);
  plan-gate 22/22 MATCH — zero diffs, nothing plans a split yet
- commit: _(this commit)_

---

## Status after P5

The substrate is complete and every stage is independently committed. What
exists: correct GUCs, per-session plumbing, worker contexts with a proven
ownership contract, a parallel scan allocator, a working Gather with leader
participation, and combine rules for every decomposable aggregate.

What does **not** exist: any plan containing a Gather. Nothing in the planner
inserts one, so behaviour is byte-identical to before this work — which is why
plan-gate is 22/22 at every stage.

**P6 is the phase that changes that**, and it is the first that affects every
user. Its prerequisites are recorded above under "Design amendments": the
post-pass must run AFTER the plan-cache lookup and must be non-mutating, and
planning-time GUC reads go through the session registry rather than the
executor context. It also needs the refusals chapter 08 §1.1 lists — DML,
SERIALIZABLE, row marks, temp tables, virtual catalog relations, and
parallel-unsafe functions.

## P6 — Enable Gather insertion  [x]

The first stage whose behaviour a user can observe.

- [x] `planner.MaybeAddGather` (`internal/planner/parallel.go`) — a post-pass
      over the finished plan, mirroring the NLI/Memoize rewrite shape rather
      than adding partial paths to a join search that has no path abstraction.
- [x] **Runs AFTER the plan-cache lookup, on both protocol paths.**
      `plancache.go` is process-wide and cross-session, keyed on
      namespace-oid + normalised SQL only, so caching a plan that already
      contained a Gather would let one session's worker count leak into
      another's execution — `SET max_parallel_workers_per_gather = 0` would
      silently fail to disable parallelism. The cache holds SERIAL plans; the
      wrap is per statement.
- [x] **Non-mutating**, and pinned by test: the pass copies only the spine and
      shares every untouched subtree by pointer. The cached node is read
      concurrently by every other session running the same SQL, so an in-place
      edit would be a race that race-gate catches only under load.
- [x] Wired at **three** sites, not two — the simple path's cache block, the
      extended path, and `executeOneSimpleStmt`'s own fallback, which plans for
      statements the cache block skips (multi-statement queries, NOTIFY/2PC,
      pending DDL). A Gather appearing on one entry point but not another is
      how a feature comes to look intermittent.
- [x] Safety refusals, each enforced and tested: DML, SERIALIZABLE, row marks
      (`SELECT … FOR UPDATE` is not DML but still stamps xmax), temp tables,
      virtual catalog relations (backed by the `Pg*Rows` callbacks that worker
      contexts deliberately nil), and the `GOOPG_PARALLEL=off` kill switch.
- [x] Worker count reproduces upstream's `compute_parallel_worker()` ×3 ladder
      including the `parallel_workers` reloption precedence — a reloption goopg
      has parsed and stored since M0110-0001 and never read.
- [x] **No stats → no Gather**, the opposite default from the semi/anti NLI
      gate. Accepting without evidence risks workers for a tiny relation;
      declining keeps today's behaviour.
- [x] `EXPLAIN` descends into `Explain.Child`, so `EXPLAIN <query>` renders the
      same plan the query executes. Without it EXPLAIN would systematically
      under-report parallelism — worse than useless, since EXPLAIN is the tool
      people use to check whether parallelism happened. (Found because the
      first live check showed no Gather.)

**The bug this stage's identity gate caught.** Connecting the Gather to the
allocator revealed that nothing wired the allocator INTO the child trees: each
worker built an ordinary serial scan and read the whole relation. On live
TPC-H data the parallel query returned **240 298 rows where serial returned
120 149** — exactly double. Nothing else would have noticed: the plan looked
right, no assertion failed, no race fired. `attachParallelScan` fixes it, and
the walk deliberately fails toward serial for any node it does not model,
because duplicating rows is a wrong-results bug while declining to parallelise
is a missed optimisation.

### P6 follow-up: the size gate, and two measurement lessons

The first cut of P6 shipped a feature that never fired, and reported a speedup
that was not real. Both were corrected in the follow-up commit; both are worth
recording because the failure modes were invisible to every gate.

**1. The size gate was keyed on the wrong input.** It used
`Stats.RowCount / 60` — an invented divisor over a field that is *never
restored at startup* (see the `pq-P6` deferral-ledger row: goopg rebuilds
column statistics from `pg_statistic` but leaves `RowCount` zero). So the gate
refused every query on any server that had not been ANALYZEd since boot, while
presenting as deliberate policy.

The fix is what PG actually does: `compute_parallel_worker()` takes
`rel->pages`, which `estimate_rel_size()` fills from a live
`RelationGetNumberOfBlocks()` call (`plancat.c:1097-1100`);
`pg_class.relpages` is consulted only afterwards, to scale the *tuple*
estimate. So the gate now reads `smgr.NBlocks` — an O(1) in-memory counter
(`internal/storage/smgr.go:933`), not a scan and not a statistics lookup. PG
chooses a worker count without ANALYZE, and so does goopg. Parallelism is now
live by default: Q1, Q6 and Q15a-VIEWBODY gain a Gather.

**2. The first speedup number was cache warming, not parallelism.** A
cold-then-warm comparison showed 23.4 s → 13.9 s and looked like 1.68x.
Re-measured warm with alternating order it was 13.0 / 12.6 / 12.6 / 12.6 —
**no difference at all.**

Chasing that zero found a real design bug: `Next()` ran the leader's own child
to *exhaustion* before ever reading the channel. Workers filled their buffer,
blocked on send, and contributed nothing while the leader claimed every
remaining block. The feature was structurally inert. PG's `gather_getnext`
interleaves — try the readers without blocking, else take ONE tuple locally
(`nodeGather.c`) — and goopg now does the same.

**Measured after the fix** (SF1, warm, alternating order, same server):

| query | serial | 2 workers + leader | speedup |
|---|---:|---:|---:|
| ad-hoc `count/sum` over `lineitem` | 12.40 / 12.50 s | 4.62 / 4.57 s | **2.71x** |
| TPC-H Q1 | 19.42 s | 7.15 / 7.05 s | **2.75x** |
| TPC-H Q6 | 14.62 s | 5.23 / 5.17 s | **2.83x** |

Against a 3.0x ceiling for leader + 2 workers, that is 90–94 % efficiency.

- gates: units PASS; **race-gate PASS**; spotcheck PASS (Q12=2 / Q13=33);
  plan-gate — 3 queries gained a Gather (Q1, Q6, Q15a-VIEWBODY), diff reviewed
  and classified as Gather-insertion only, recaptured as
  `plan_snapshots/pq-p6-gather.txt`, 22/22 MATCH against it
- commits: `2e0caa46` (insertion) + _(this commit)_ (size gate + interleaving)

## P7 — Gather Merge  [x]

Before P7 a `Sort` terminated partial-ness: the Gather went BELOW it, so the
workers scanned and the leader did the entire sort. P7 lifts the Sort into the
workers and merges the already-ordered streams in the leader — PG's

    Gather Merge -> Sort -> Parallel Seq Scan

which moves the expensive part off the leader.

### What the ordering requirement changed

Plain Gather has one correctness property: the SET of rows must match serial.
Gather Merge has a strictly stronger one — the SEQUENCE must match. Three
consequences shaped the implementation:

- **Per-worker channels, not one shared channel.** The merge has to know which
  stream a row came from, because after popping a row it must take the next row
  from *that* stream. Gather's single shared channel interleaves the streams,
  which is exactly what a merge cannot tolerate.
- **Row-granular interleaving.** The leader cannot drain a batch at a time; it
  must hold one row per source at all times (the heap front).
- **`attachParallelScan` had to learn `sortOp`.** The worker's tree is now
  `Sort -> Seq Scan`, and the walk previously stopped at the Sort. Had it not
  been extended, every worker would have sorted the WHOLE relation — the same
  duplicate-rows failure P6 hit, reached by a different route.
  `TestGatherMergeNoDuplicates` exists specifically for that route.

The load-bearing invariant is that **a per-worker Sort is only ever produced
together with a GatherMerge above it**. A plain Gather over per-worker Sorts
compiles, runs, and returns every correct row in the wrong order, with nothing
to report it. `TestNoPlainGatherOverWorkerSort` asserts the invariant directly
over a set of plan shapes rather than checking one expected plan.

### Divergence from the design

Chapter 05 §4 said to reuse `sortOp`'s `sortHeap`. The ALGORITHM is reused, the
TYPE is not: `sortHeap` is typed on `*sortSource`, the external sort's
spill-file cursor, so reuse would have meant adding a parallel-query field to a
struct that has nothing to do with parallelism. Fifteen lines of heap
boilerplate (`gmHeap`) was the cheaper trade.

### Measured effect

SF1, warm, alternating order, same server, `ORDER BY l_extendedprice DESC
LIMIT 20` over `lineitem` (`Workers Planned: 4`, so leader + 4 = 5 lanes):

| run | serial | 4 workers + leader |
|---|---:|---:|
| 1 | 230.4 s | 64.6 s |
| 2 | 219.7 s | 65.0 s |
| 3 | 222.6 s | 64.2 s |
| mean | **224.2 s** | **64.6 s** |

**3.47x**, or 69 % of the 5.0x ceiling. Sorting scales less than a scan does —
the merge itself stays serial on the leader — so the gap from the ceiling is
expected rather than a defect to chase.

### Two observations recorded, not fixed

- **No TPC-H query gained a Gather Merge** (plan-gate 22/22 MATCH, zero diffs).
  Every TPC-H `ORDER BY` sits over an aggregate or a join, so
  `findPartialSubtree` correctly declines. The feature is reachable — verified
  by EXPLAIN on an ad-hoc `ORDER BY` over a bare scan — but TPC-H does not
  exercise it. Lifting `Aggregate` is P5's combine machinery meeting the
  planner, which is a later increment.
- **`make plan-gate` silently SKIPs.** Its `pg_isready` probe does not run
  under the Makefile's `ENV_PREFIX`, so `pg_isready` is not on PATH and the
  gate reports "not reachable" and exits 0 even with a healthy server. It also
  defaults to `PLAN_DB=tpch`/`PLAN_USER=tpch`, which do not survive a restart
  (roles and DBs are in-memory only). Both were worked around by hand
  (`PATH=... make plan-gate PLAN_DB=postgres PLAN_USER=postgres
  PLAN_PASS=postgres`); a gate that passes by skipping is worth fixing
  separately.
- **EXPLAIN child indentation diverges from PG** by 4 columns per level, for
  every node kind, not just Gather. Pre-existing and repo-wide (visible
  throughout `plan_snapshots/`), so it is out of P7's scope.

- gates: units PASS; race-gate PASS; spotcheck PASS (Q12=2 / Q13=33);
  plan-gate 22/22 MATCH against `plan_snapshots/pq-p6-gather.txt` — zero
  diffs, as analysed above
- commit: _(this commit)_

## P8 — Parallel Hash Join  [x]

Build once in the leader, publish, share by pointer; every worker probes the
same frozen table and opens only its own partial probe side.

PG needs a DSA allocator, a barrier protocol with explicit phases
(`PHJ_BUILD_*`) and shared-memory batch spilling to get here, and offers a
second non-shared variant besides. goopg needs a struct and a map lookup,
because a map is safe for unlimited concurrent reads with no writer and the
goroutine-start edge publishes it. That is the largest structural
simplification in the bundle, and it held up exactly as chapter 07 predicted.

### What the implementation actually required

- **A two-phase `Open`.** `openLazyHashJoin` drained the build child, closed
  it, and opened the probe child in one indivisible step. Under parallelism the
  halves must separate: the leader runs the build before fan-out, each worker
  opens only its probe. Split into `buildLazyHashTable` + `openProbeSide`.
- **The build-computed SCALARS, not just the map.** `antiBuildRows`,
  `antiBuildHasNull`, `preserveBuildSide` and the width fields are per-INSTANCE
  fields on `joinOp`, so sharing the table does not carry them.
  `antiBuildHasNull` is the dangerous one: it decides `NOT IN`'s
  three-valued-NULL result, and a worker defaulting it to `false` returns
  hundreds of rows where SQL says the answer is empty.
  `TestParallelHashJoinNotInNullSemantics` exists for that single flag.
- **Eligibility is narrower than "hash join".** The rule is "hash join whose
  per-probe-row verdict is worker-local": INNER, SEMI and ANTI qualify; LEFT
  qualifies only with the outer on the probe side (`!BuildLeft`), which is the
  only LEFT shape the lazy-hash runtime implements anyway; FULL and RIGHT are
  refused, because they need to know which BUILD rows went unmatched across ALL
  workers and no such cross-worker reduction exists.
- **The probe-side rule is stated once and consulted three times** — build
  loop, `attachParallelScan`, planner. They must agree: a disagreement puts the
  parallel scan on the BUILD side, where every worker hashes a partition of the
  build input and the join silently drops matches.

### One correction found by the existing tests

The first cut built a throwaway operator tree unconditionally and then looked
for hash joins in it. That called the Gather's child-builder one extra time —
harmless for the production builder (a pure `Build(p.Child)`) but not for the
P4 Gather tests, whose builders arm a failure with `sync.Once` or partition
rows by call index. Three of them failed immediately. The fix is better than
the workaround would have been: decide from the PLAN
(`planner.HasShareableHashJoin`) before constructing anything, so the common
case costs nothing at all.

### Measured effect: essentially none on TPC-H, and the reason is worth reading

plan-gate: **1 of 22 queries changed** — Q13's LEFT hash join gained a Gather.
Diff reviewed as Gather-insertion-only (identical join tree, plus the node and
`Workers Planned`), recaptured as `plan_snapshots/pq-p8-hashjoin.txt`, 22/22
MATCH against it.

Q13 timing, warm and alternating:

| | serial | 4 workers + leader |
|---|---:|---:|
| run 1 | 105.3 s | 102.4 s |
| run 2 | 101.2 s | 100.3 s |

**~1.5 %, which is noise.** The cause is structural, not a defect in the
implementation:

- `customer` (the probe side) has **150,000** rows; `orders` (the build side)
  has **1,500,000**. Q13 is a LEFT join with `customer` as the outer, so the
  runtime requires outer = probe, so the build side is necessarily the 10×
  larger table. P8 parallelises the small side and leaves 90 % of the input to
  the serial leader.
- The other 21 queries are unaffected because TPC-H's multi-table joins collapse
  into goopg's `MultiHashJoin`, which chapter 07 §6 puts out of v1 scope
  (no PG counterpart, hence no oracle plan).

So P8 is correct, exercised, and currently worth almost nothing on this
workload. That is the honest result and it should not be dressed up.

### What it tells us to do next

This is precisely the reopen condition recorded for **cooperative parallel
hash build** in [10](10-roadmap.md) — "a measured plan where build time
dominates". Q13 is now that measured plan. Two candidate follow-ups, in
increasing order of cost:

1. **Parallelise the build's scan+filter, keep insertion serial.** Q13's build
   side carries `o_comment NOT LIKE '%special%requests%'` over 1.5 M rows; a
   producer/consumer split would parallelise the predicate while one goroutine
   owns the map. Much cheaper than PG's design and probably most of the win.
2. **A genuinely concurrent build** (sharded or per-worker-partial-then-merge),
   which is what PG's barrier machinery buys.

Neither is P8. Both are now motivated by a measurement rather than by
speculation, which they were not before.

- gates: units PASS; race-gate PASS (including a probe-heavy `-count=2` run
  over the parallel suite); spotcheck PASS (Q12=2 / **Q13=33**, the query whose
  plan changed); plan-gate 1 reviewed diff, recaptured, then 22/22 MATCH
- commit: _(this commit)_

## P9 — Partial / Finalize placement  [x]

P5 built the combine rules and proved them in isolation; nothing placed a
split. P9 places it. This phase was **chosen by measurement, not by roadmap
order** — the roadmap ends at P8.

### Why this one, and how that was established

After P8, Q1 was measured across worker counts rather than at a single one:

| lanes (leader + workers) | Q1 |
|---|---:|
| 1 (serial) | 21.27 s |
| 3 | 7.84 s |
| 5 | 7.15 s |
| 9 | 7.35 s |

Adding workers past two bought nothing. Solving `T(n) = S + P/n` across those
points puts **~6.1 s of the 7.1 s floor in the serial tail**: the leader was
receiving ~5.9 M rows through the Gather and aggregating all of them down to
four groups by itself. No amount of parallel scanning could touch it.

That is the whole case for P9, and it is worth noting that the intuition
before measuring was the opposite — P6's "2.75x of a 3.0x ceiling, 92 %
efficient" reading suggested there was nothing left, because at two workers
the serial tail is still hidden.

### The transport question, and why the states do not travel

The obvious design puts transition states in the rows, which needs either a
pointer-bearing `Datum` kind (against the pointer-free-Datum work) or a
side-channel threaded through `rowBatch`, `TupleSlot` and the Gather — making
a node whose whole job is "move rows" learn about aggregation.

Instead the Partial node publishes into a shared accumulator keyed by its own
plan node, exactly as P8 publishes hash tables, and the Finalize node reads it
after draining the Gather to EOF. **The Gather needs no knowledge of
aggregation at all**, and the schema is unchanged at every level.

The synchronisation is free: a Partial node does all of its work in `Open`
(drain, group, merge) before it returns, so when the Gather reports EOF every
worker has already merged. Combining happens on insert under the accumulator's
own mutex, so no worker's group map outlives its own `Open`.

The Partial node emits **zero rows**. That is the one thing in this phase that
looks like a bug and is not, so both sides refuse loudly if the pairing is
missing: a Partial without an accumulator and a Finalize without a
`PartialSource` each raise XX000 rather than quietly returning nothing.

### A pre-existing wrong-results bug the P9 tests found

`TestPartialAggregateRefusals` failed on `string_agg` — and not because of the
split, which the whitelist correctly refuses. **Plain Gather, shipped in P6,
already broke order-dependent aggregates.** The Gather concatenates worker
batches in arrival order, so a leader-side `string_agg` over gathered rows
returned its elements shuffled, differently on every run, for any query large
enough to parallelise.

PostgreSQL tolerates exactly this — it marks these aggregates parallel-safe and
documents the unordered result as implementation-defined. goopg now refuses
instead (`AggregateIsOrderSensitive`, consulted by `subtreeHasUnsafeNode`),
for two reasons: the stated contract in chapter 09 is that a parallel plan
returns what the serial plan returns, and these aggregates are not decomposable
anyway, so refusing costs only a parallel scan while copying PG's laxity would
cost determinism on a query that was stable before parallelism existed. An
explicit `ORDER BY` inside the aggregate makes it deterministic again and is
not refused.

### Measured effect

Q1, warm, alternating, same server:

| lanes | before P9 | after P9 | |
|---|---:|---:|---|
| 1 (serial) | 21.27 s | 21.05 s | unchanged, as it must be |
| 3 | 7.84 s | 7.51 s | 2.80x |
| 5 | 7.15 s | **4.72 s** | 4.46x — 89 % of ceiling |
| 9 | 7.35 s | **4.01 s** | 5.25x |

**1.51x further at four workers, 1.83x at eight.** The serial floor drops from
~6.1 s to ~3.1 s; scaling past two workers exists now where it did not before.

Q13 is unchanged (109.6 s parallel vs 109.8 s serial). Its serial time drifted
from 105.3 s to 115.6 s between sessions on the same unchanged code path, so
the machine moved under the measurement — the comparison that matters is the
same-session pair, and it shows no regression. Q13 is still dominated by P8's
finding: the serial hash build over the 1.5 M-row side.

### Recorded, not fixed

> **CORRECTION (chapter [11](11-partial-aggregation-cost-model.md) §0).** The
> paragraph below names the wrong query and is left in place with this note
> rather than silently rewritten, because it was the stated motivation for the
> cost-model work that followed.
>
> Q13's aggregate does not read `customer`. It reads the output of
> `customer LEFT JOIN orders` — ~1.5 M rows at SF1 — and reduces that to 150 k
> groups per worker, a **4× reduction the split should perform**. 150,000 is
> the probe side's row count, not the aggregate's input. Q13 was never the
> hazard.
>
> The real case is **Q18's inner aggregate**,
> `select l_orderkey from lineitem group by l_orderkey`: 1.5 M distinct keys
> over 6 M rows, so every input row becomes a state and nothing is reduced.

**The split has no cost model.** Q13 groups by `c_custkey` — **150,000
groups**, one per probe row — so its Partial nodes merge 150 k entries per
worker through the accumulator mutex for no reduction at all. It does not show
as a regression there because the query is build-bound, but the shape is real:
when the group count approaches the input row count, the split is pure
overhead. PG guards this with cost estimates; goopg has no absolute node costs
(chapter 01 §4), and inventing a row-count heuristic would repeat the P6 error
of gating on statistics that are never restored (ledger `pq-P6`). Left as a
measured, documented hazard.

- gates: units PASS; race-gate PASS; spotcheck PASS (Q12=2 / **Q13=33**);
  plan-gate — 4 queries gained the split (Q1, Q6, Q13, Q15a-VIEWBODY), every
  diff the same shape (aggregate moved below the Gather, `Finalize `/`Partial `
  prefixes added), reviewed and recaptured as
  `plan_snapshots/pq-p9-partialagg.txt`, 22/22 MATCH against it
- commit: _(this commit)_

## P10 — the split cost model  [x]

Chapter [11](11-partial-aggregation-cost-model.md), implemented **without** the
pg_class persistence that chapter assumed it needed (ledger row `pq-P10`).

The insight that removed the prerequisite: the gate needs the reduction ratio
`rho = Gw*d/R`, and with the clamp `Gw = min(ndistinct, R/d)` that collapses to
`rho = min(1, (ndistinct/R)*d)` — a RATIO, never either absolute quantity. The
ratio is exactly PG's negative `stadistinct`, and goopg's on-disk pg_statistic
already carried it (`codec.go:1424` documents the sign convention); only the
sign handling was missing. Both write paths clamped negatives away and the
restore discarded them into 0. So ANALYZE now stores the distinct-to-rows
fraction and it round-trips through a column that already existed — no
Haas-Stokes, no reltuples writeback, no new persistence machinery at all.

The sampling bias runs the safe way. A low-cardinality column over-states its
fraction (6 distinct in 30,000 sampled rows reads as 2e-4 where the truth over
6M rows is 5e-7), which over-states `rho` and makes the gate MORE likely to
refuse. A near-unique column reads 1.0, which is exact and is the case the gate
exists for.

Verified on SF1 with ANALYZE run:

| shape | `n_distinct` | verdict |
|---|---:|---|
| Q1 — `GROUP BY l_returnflag, l_linestatus` | -1e-4 x -6.7e-5 | **splits** (Finalize / Gather / Partial) |
| Q18 inner — `GROUP BY l_orderkey` | -0.99 | **refused**, falls back to Gather-below-aggregate |

Five splits survive across the reference set; Q18 has none.

### Scope limit, deliberately

The gate applies only where the aggregate reads a base relation directly. The
fraction is relative to the BASE relation's rows, but a join-fed aggregate
reads the join's output — Q13's `c_custkey` is unique within `customer` (f=1)
yet accounts for a tenth of the 1.5M-row join it aggregates, so applying the
fraction there would refuse a 10x reduction. Those shapes keep today's ungated
behaviour, which also sidesteps the large regression the design review flagged.
It also does not descend a Project, because `columnStatsForChild` passes the
output ordinal through unremapped — a wrong n_distinct is worse than none.

### The plan-gate recapture conflates two causes, and that is stated

18 of 22 queries diverged, but **the split gate is not the main cause**:
running ANALYZE on the bench database to satisfy this feature's premise changed
the statistics, and with them join orders. Q9's diff is a Multi-Way Hash Join
going from 4 tables to 3 with `(stats)` annotations and real row counts — no
aggregate involved. The gate's own effect was verified per-query by EXPLAIN
instead, which is the only way to attribute it. Recaptured as
`plan_snapshots/pq-p10-splitgate.txt`, 22/22 MATCH against it.

- gates: units PASS; race-gate PASS; plan-gate 22/22 against the recapture
- commit: _(this commit)_
