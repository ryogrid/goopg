# IMPLEMENTATION-TODO — parallel-query, phases P0–P5

| field | value |
| --- | --- |
| status | in progress |
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
| P5 | Partial / Finalize aggregation | `AggMode`, combine rules, whitelist refusals | [ ] |

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
- commit: _(this commit)_

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
- commit: _(this commit)_

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
- commit: _(this commit)_

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
