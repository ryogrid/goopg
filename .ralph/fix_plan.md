# goopg Fix Plan

Roadmap derived from `.ralph/specs/GOAL_AND_REQUIREMENTS.md` (§10 "Definition of
Done (Initial Milestone)"). Pick the topmost unchecked item **unless the Current
Priority banner below or a dependency forces another order**.

## Notes / rules

- This is the authoritative TODO list for Ralph. Update it after every meaningful
  change (tick boxes, add newly-discovered follow-ups). ONE item per loop;
  decompose any item larger than a single agent invocation.
- Every non-trivial subsystem must land with (or just before) a design doc under
  `docs/design/<id>-NNNN-*.md` **and** a `docs/design/README.md` index entry —
  hard requirement, same loop.
- Deferrals: never close a task silently with a forward reference. Append one row
  to `.ralph/deferral_ledger.md` (`date | task-id | landed | deferred | resume
  point | why`) and leave the fix_plan item unchecked. **The ledger is the source
  of truth for every "DEFERRED" note below** — consult it for full context/resume
  points.
- Completed milestones are archived under `completed_milestones/` (latest:
  `completed_fix_plan_008.md`); they are reference-only, NOT actionable, and must
  not be copied back here.

## Current Priority (per 2026-06-20 directive)

Work order: **M0117 → M0118** (both complete + archived), then resume **M0110**
(its **M0119-0004/0005/0006/0007** spinoffs are the active, in-progress form of
that work), with **M0095** parked (blocked on logical decoding). **M0120 / M0121
are CLOSED** (2026-07-04) and archived. Policy: fix blockers in place; do NOT
defer unless genuinely compelling (then record a ledger row); commit + push at
every clean, green (build + pre-commit) checkpoint.

**Next up:** M0122-0003 (EXPLAIN/pg_stat instrumentation) is mostly done
(2026-07-05) — FORMAT XML/YAML, per-CTE ANALYZE stats, SETTINGS rendering,
BUFFERS TEXT+JSON/XML/YAML rendering, `pg_stat_io` row shape + real
reads/read_bytes/read_time/writes/write_bytes/write_time/extend_time/
hits/evictions/extends/extend_bytes, and `track_io_timing` runtime SET
have all landed; see the M0122-0003 line item for detail. Remaining
sub-items: `EXPLAIN (BUFFERS)` without ANALYZE (planning-time buffers),
local/temp-buffer terms, the 3 remaining `pg_stat_io` op counters
(reuses/writebacks/fsyncs), EXPLAIN's `I/O Timings` line, a `CTEDMLPrefix`
nested-node instrumentation residual. Pick up one of those next, or
continue the M0119-0004 pg_dump catalog-view
parity battery / next
unresolved DU-002 slice from `.ralph/deferral_ledger.md`.

## Archived — complete (see `completed_milestones/completed_fix_plan_009.md`)

M0117 (CLOG ↔ PostgreSQL subsystem alignment), M0118 (Upstream Isolation Spec
Suite Pass-Through), M0120 (WordPress WP-CLI verification execution + evidence),
M0121 (WordPress WP-CLI verification remediation).

## Archived — complete (see `completed_milestones/completed_fix_plan_008.md`)

M0096 (RC isolation feature impl + spec pass), M0100 (RC isolation runtime
closure / 21-spec pass), M0102 (heterogeneous streaming-replication +
SIGKILL-failover E2E), and the two completed Maintenance fixes
(MAINT-STATEGUARD-RECONCILE, MAINT-TPCH-RELOAD). Earlier milestones:
`completed_fix_plan_001.md` .. `completed_fix_plan_007.md`.

---

## M0095 — Client-Tools TAP Test Porting (filed 2026-05-12)

Design: `docs/design/0095-0003-*`. Goal: port the client-tools-tap suite and the
engine features its `t.Skip`'d scripts need. (`pg_ctl` 001–004 already PASS.)

- [x] **M0095-0002** — `pg_walsummary/002` ported (added `pg_stat_io` virtual view,
      `pg_available_wal_summaries()`; `TestPort_PgWalsummary002Blocks` PASS).
- [ ] **M0095-0003** — `pg_basebackup` 010/011/020 PASS (backup execution,
      `-X stream`/`-X fetch`, manifest + SHA-family checksums, in-place tablespace,
      `READ_REPLICATION_SLOT`). **Remaining:** `030 recvlogical` — blocked on logical
      decoding (not implemented; tracks with the logical-replication milestone / D-004).
      Deferred: on-disk `pg_tablespace` heap visibility (independent shared-catalog
      runtime write — see ledger). **Not actionable until logical decoding lands.**

## M0110 — Additional TAP Test Porting (beyond M0094/M0095) (filed 2026-05-22)

Scope = `docs/test-port/upstream-tap-coverage.md` tests not covered by M0094
(recovery/subscription) or M0095. Tags: SHOULD_PASS / BUG_FIX / UNIMPLEMENTED.
Already complete within M0110 (detail in git history): **M0110-0004** pg_resetwal
(RW-001..004 PASS), **M0110-0007 / M0110-0010** B-tree split & vacuum sibling
prev-link fixes.

- [ ] **M0110-0001 — pg_dump TAP** — `001_basic` ported (DU-001, CLI-only).
      `002–010` (schema dump, dump/restore round-trip, parallel, filter-file,
      connstr) DEFERRED on broad catalog-view parity + round-trip; being advanced
      one catalog gap at a time via the self-promoting
      `TestPort_PgDumpConnectionSetup` guard (CSV row DU-002, slice-by-slice).
      Design `0110-0001-pg-dump-tap-port.md`. Resume = next gap in pg_dump's
      getter battery (latest blocker tracked in `.ralph/working_set.md` / ledger).
- [ ] **M0110-0002 — pg_waldump TAP** — `001_basic` CLI tier ported (WD-001);
      WAL-format readability guarded by W-001 (`TestPort_WALPgWaldumpCompat`).
      **Remaining (WD-002, deferred):** `002_save_fullpage` — needs goopg to emit
      PG-decodable FPI/heap WAL with backup blocks (+ hash/gin/gist/spgist/brin AMs
      for the server tier). Design `0110-0002-*`.
- [ ] **M0110-0003 — pg_amcheck TAP** — `001_basic` (AC-001) + `002_nonesuch`
      (AC-002) ported; CREATE SCHEMA + user-schema table restart-durability enablers
      landed. **Remaining (AC-003, deferred):** `003_check`, `004_verify_heapam`,
      `005_opclass_damage` — need `verify_heapam()` SRF + opclass catalog parity +
      index AMs. (One 002 sub-section deferred: `datconnlimit=-2` invalid-DB filter —
      runtime shared-catalog write.) Design `0110-0003-*`.

## M0119 — Deferral-Ledger Backlog Consumption (filed 2026-06-29)

Milestone: `docs/milestones/0119-deferral-ledger-backlog-consumption.md`
(**living milestone** — tasks are appended over time). Source of truth:
`.ralph/deferral_ledger.md`. Goal: drive every open (`status = -`) ledger row to
closure — implement the deferred scope, or verify it already landed and mark the
row `resolved`.

**Per-task rule (applies to every M0119 implementation task):** before
implementation begins, the picking agent MUST (1) create a design doc at
`docs/design/<source-id>-NNNN-*.md` and index it in `docs/design/README.md`, and
(2) have that design doc pass an agent review. Implementation starts only after
the reviewed design doc exists. (The triage task M0119-0001 was doc-only, exempt.)

**Already landed (see git history / deferral ledger):** M0119-0001 triage
(2026-06-29: 224 open rows → 178 resolved, 46 remain), M0119-0002 (CLOG tail),
M0119-0003 (initdb options — empty backlog), M0119-0008 (isolation residual —
only the infeasible `deadlock-parallel` spec remains), M0119-0009 (UPDATE/DELETE
conflict-wait), plus the landed sub-slices of -0004 (NULLS NOT DISTINCT
enforcement + upsert arbiter) and -0005 (pg_waldump WD-003/WD-004 canonical
prune-WAL round-trip). The four open items below carry the remaining unbuilt scope.

- [ ] **M0119-0004 — pg_dump 002–010 TAP** (source: M0110-0001). Schema dump,
      dump/restore round-trip, parallel, filter-file, connstr — advance the
      catalog-view parity battery slice-by-slice (guard
      `TestPort_PgDumpConnectionSetup`; resume = next catalog getter gap tracked in
      `.ralph/working_set.md` / ledger). Two general SQL-engine gaps surfaced here
      remain: deferred-constraint *checking at COMMIT* (goopg checks immediately)
      and any residual dump-fidelity items.
- [ ] **M0119-0005 — pg_waldump server tier** (source: M0110-0002). `002_save_fullpage`
      (WD-003) + live `pg_waldump --rmgr=Heap2` round-trip DONE. **Still open:** only
      `001_basic.pl`'s server-dependent tier (per-rmgr/relation/block filtering) —
      needs hash/gin/gist/spgist/brin index AMs.
- [ ] **M0119-0006 — pg_amcheck server tier** (source: M0110-0003). `002_nonesuch`
      … `005_opclass_damage`; `CREATE EXTENSION amcheck` + `verify_heapam()` SRF on
      top of `internal/amcheck` + opclass catalog parity. Largest open cluster
      (~29 ledger rows): index AMs, `box`/`int4range`/`int4[]` types, STORAGE
      EXTERNAL TOAST corruption, the heapallindexed heap-scan producer, and the
      `datconnlimit = -2` invalid-DB filter (runtime shared-catalog write).
- [ ] **M0119-0007 — pg_basebackup recvlogical** (source: M0095-0003). `030 recvlogical`
      — blocked on logical decoding (tracks the logical-replication milestone / D-004).

> This task list is **seeded, not exhaustive.** M0119-0001 triage plus every future
> deferral-ledger entry (any new `status = -` row) feed additional M0119 tasks over
> time; the milestone's living nature means it need not be complete at filing.

## M0122 — Unimplemented-Feature Backlog Consumption (filed 2026-07-04)

Milestone: `docs/milestones/0122-unimplemented-feature-backlog-consumption.md`
(**living milestone** — tasks are appended over time). Source of truth:
`unimplemented_feat.json` (repo root; 181 entries generated 2026-07-02 from the
commit log). Goal: drive every `open` feature entry to closure — implement the
deferred scope, or verify it already landed and mark the entry `resolved`.

**⚠️ Verify-before-implement (READ FIRST):** `unimplemented_feat.json` is a
2026-07-02 snapshot and **may list features that are already implemented** — 24
entries have an `unclear`/absent `code_audit` and 61 have an open matching ledger
row (7 overlap both). When you pick up ANY M0122 task, FIRST re-verify each
candidate against current HEAD (grep/read code, probe a live goopg, check
ledger/fix_plan/git log). If it already exists, set the entry's `status` to
`resolved` (cite the proof) and DO NOT re-implement. Only build genuinely-missing
scope.

**Per-task rule (applies to every M0122 implementation task):** before
implementation begins, the picking agent MUST (1) create a design doc at
`docs/design/<id>-NNNN-*.md` and index it in `docs/design/README.md`, and (2) have
that design doc pass an agent review. Implementation starts only after the
reviewed design doc exists. (The triage task M0122-0001 is doc-only, exempt.)
Tracking field = a per-entry `status` (`open`/`resolved`) added by M0122-0001,
mirroring M0119's ledger `status` column.

- [ ] **M0122-0001 — Backlog triage / re-verification pass** (doc-only, exempt).
      Re-audit all 181 entries vs current HEAD; add the `status` field (init
      `open`/`resolved`); resolve the already-done ones — start with the 24
      `unclear`/no-audit + 61 `resolution_check.ledger=open` entries (7 overlap).
      Dedupe against M0119 + `.ralph/deferral_ledger.md` so nothing is worked
      twice. This task discharges the "may already be implemented" risk.
- [x] **M0122-0002 — Catalog system functions & pg_* view stubs** (~9). Quick wins:
      `pg_relation_size`/`pg_total_relation_size` (`f0b2bdb3`), `regexp_matches`
      (2026-07-04 loop #7, scalar/first-match only — SRF `'g'`-flag multi-row
      deferred, ledger row), `pg_get_expr`, `isfinite`, `justify_*`,
      `pg_get_serial_sequence`, `pg_get_indexdef` (already implemented, verified
      2026-07-04). Design: `docs/design/0122-0002-pg-relation-size-real-sizes.md`.
      **Follow-up (2026-07-04, later loop):** the deferred `'g'`-flag SRF
      multi-row case now lands for the SELECT-list/target-list position —
      `RegexpMatchesCol` in `internal/planner/plan.go`'s `ProjectSet`, detected
      in `buildSelectSrfProjectSet` (`internal/planner/planner.go`) alongside
      `generate_series`/`unnest`, expanded by `projectSetOp.openSelectSrfMode`
      (`internal/executor/operators_project_set.go`) via a new
      `evalRegexpMatchesSRF`/`regexpAllMatchesArrays` pair
      (`internal/executor/expr.go`) — verified byte-for-byte against a real
      PostgreSQL 18.3 cluster (`'g'` flag → one row per match, no flag → at
      most the first match, no match → **zero** rows, unlike the scalar
      fallback's NULL). Tests: `internal/executor/regexp_matches_srf_test.go`.
      **Follow-up (2026-07-04, later loop):** the FROM-clause form
      (`FROM regexp_matches(...)`) now lands too — `FromRegexpMatches` plan
      node + `planFromRegexpMatches` (`internal/planner/planner.go`,
      dispatched from `planTableFuncRangeVar` alongside `unnest`), executed
      by `fromRegexpMatchesOp`
      (`internal/executor/operators_from_regexp_matches.go`) reusing
      `evalRegexpMatchesSRF`. Single `text[]` column, default name
      `regexp_matches`, supports `AS alias(col)` and `WITH ORDINALITY`.
      Tests: `internal/executor/from_regexp_matches_test.go`. Discovered
      (not fixed — separate ledger row) two generic, unnest-shared gaps:
      `WITH ORDINALITY AS t(m, n)` fails when both columns are named
      explicitly in the outer SELECT list (`*` works), and a same-level
      comma/LATERAL join correlating a FROM-clause SRF arg to a preceding
      sibling FROM item's column fails (`ctx.OuterRows` unwired for that
      execution path) — M0122-0002 is now fully closed; those two are
      independent cross-cutting gaps, see ledger.
      **`WITH ORDINALITY` named-column gap FIXED (2026-07-04, later loop):**
      root cause was never in the planner (`wrapOrdinality`/`planFromUnnest`
      were always correct) — it was `internal/analyzer/analyzer.go`'s
      `tableFuncColumns`, which never threaded `rv.TableFunc.WithOrdinality`
      through and had no `unnest`/`regexp_matches` cases at all (silently
      fell to the generic single-`int8`-column default), so the analyzer's
      synthetic FROM-item table never had the ordinality/element columns
      naming an explicit outer-SELECT column against them hit `42703`
      even though the planner and executor already produced the row
      correctly (`*` worked because `analyzeStar` skips column-existence
      checking entirely). Fix: `tableFuncColumns` now takes `*parser.TableFuncRef`,
      strips/re-appends the trailing ordinality alias the same way
      `wrapOrdinality` does, and gained real `unnest` (N-column zip,
      `text`-typed pending real element-type inference — the analyzer
      runs before the FROM scope exists so it cannot resolve the array
      arg's type yet) and `regexp_matches` (`text[]`) cases. Tests:
      `internal/analyzer/analyzer_test.go`'s
      `TestAnalyzeWithOrdinalityNamedColumn`. Verified end-to-end against
      a live server + real `psql` (`unnest`/`generate_series`/
      `regexp_matches`, single- and multi-arg `unnest`). The comma/LATERAL
      `ctx.OuterRows` gap (ledger row 480) is separate and still open.
- [ ] **M0122-0003 — EXPLAIN output & pg_stat instrumentation** (~7, partial).
      FORMAT XML/YAML **done** (2026-07-04, loop #8) — design:
      `docs/design/0122-0003-explain-format-xml-yaml.md`. Per-CTE ANALYZE
      stats **done** (2026-07-04, loop #8, correcting an earlier note in
      this same banner): `Build()`'s `CTEScan`/`CTEDMLPrefix`/
      `MaterializedCTEScan` cases skipped `maybeInstrument`, so the CTE
      node's *own* EXPLAIN line (e.g. `CTE Scan on a`, `CTE DML`) reported
      cost-only estimates under ANALYZE — only its inlined child showed
      actual rows/time, which is what made the gap look closed under a
      surface read of existing tests (they only grep for "actual time="
      anywhere in the output, not on the CTE node's own line). Fixed by
      wrapping all 3 constructor call sites in `maybeInstrument`
      (`internal/executor/executor.go`); new regression tests
      `TestExplainCTEScanAnalyzeReportsActualRows` /
      `TestExplainCTEDMLPrefixAnalyzeReportsActualRows` assert the node's
      own line, not just the output as a whole. One residual gap deferred
      (ledger row): DML-CTE inner nodes (the INSERT/UPDATE/DELETE plan +
      outer body) are Built lazily inside `cteDMLPrefixOp.Open()`, outside
      the `withInstrumentation` scope, so they still don't show actual
      stats. SETTINGS rendering **done** (2026-07-04, later loop):
      `internal/config/guc.go` gains `FlagExplain` (mirrors `GUC_EXPLAIN`),
      tagged on the 45 goopg-registered GUCs upstream flags;
      `SessionRegistry.ExplainVariables()` + `Context.ExplainSettings`
      (wired in both dispatch.go and dispatch_extended.go) render
      `Settings: k = 'v', ...` (TEXT) / `"Settings": {...}` (JSON/XML/YAML).
      BUFFERS rendering **partial** (2026-07-04, later loop): `storage.Pool`
      gains `sharedHitCount`/`sharedReadCount` atomic counters (incremented
      at `Pin()`'s hit/miss decision points); the existing per-node
      `instrumentedOp` ANALYZE wrapper (`internal/executor/instrument.go`)
      now also diffs `Pool.BufferCounters()` snapshots per node; TEXT
      rendering only (`Buffers: shared hit=N read=N`), under ANALYZE only.
      BUFFERS rendering FORMAT JSON/XML/YAML **done** (2026-07-04, later
      loop): `planToJSONWithStats` sets flat `"Shared Hit Blocks"`/`"Shared
      Read Blocks"` properties whenever BUFFERS is requested (matches
      upstream's non-TEXT `show_buffer_usage` branch). `pg_stat_io` row
      shape **done** (2026-07-04, later loop): `internal/executor/
      pgstat_io.go` ports upstream's `pgstat_tracks_io_bktype`/`_object`/
      `_op` predicates (verified against a real PostgreSQL 18.3 cluster —
      79 rows), wired via `valuesOp.Open`; the one cell goopg instruments
      (client backend/relation/normal reads/read_bytes/hits) is real, every
      other tracked cell is an honest 0, untracked cells are NULL. Also fixed
      a wrong test assumption in `TestPort_PgWalsummary002Blocks` (upstream
      does report 2 walsummarizer rows even with summarize_wal=off).
      `dirtied=`/`written=` counters **done** (2026-07-04, later loop):
      `storage.Pool` gains `sharedDirtiedCount`/`sharedWrittenCount`
      (incremented at all 8 clean→dirty `MarkDirty*` CAS-success sites, and
      at `evictVictim`'s post-flush point only, deliberately excluding
      bgwriter/checkpointer flushes); rendered in TEXT and JSON/XML/YAML
      alongside hit/read. Verified against a real running server: an
      `UPDATE` immediately after `INSERT` withholds `dirtied=` (page
      already dirty), then reports it correctly after an intervening
      `CHECKPOINT`.
      `track_io_timing` runtime SET **done** (2026-07-04, later loop):
      `internal/activity/registry.go` gains a per-backend `TrackIOTimingOn`
      flag + a process-wide latching fast-path flag + `LookupTrackedGoroutine`
      (drop-in for `LookupCurrentGoroutine` that also requires the calling
      backend's flag on); `internal/initdb/open.go`'s buffer-pin and
      AIO/data-file I/O wait-event hooks are now wired unconditionally
      (previously skipped entirely unless the boot-time GUC was on) and
      consult the new helper; `internal/server/server.go`'s `New()` gains a
      `track_io_timing` `OnChange` hook (mirrors the pre-existing
      `application_name` one) plus a per-connection seed from the session's
      boot value. `SET track_io_timing` now takes effect live, no restart.
      Still open: `EXPLAIN (BUFFERS)` without ANALYZE (PG 17+ planning-time
      buffers — no planning-phase buffer counters exist), local/temp-buffer
      terms, `pg_stat_io`'s other 7 I/O counters (writes/extends/evictions/
      reuses/writebacks/fsyncs + their bytes/time columns), and actual
      per-wait-event timing *collection* to back `track_io_timing`/
      `I/O Timings` (the GUC now reaches the hooks live, but the hooks still
      only track wait *occurrence*, not wall-clock duration).
      Real read-timing collection + `writes` count **done** (2026-07-05,
      loop #30): `internal/activity/registry.go`'s `WaitEventEnd` now
      returns the real wall-clock `time.Duration` elapsed since the
      matching `WaitEventStart` (read from the mono-clock `stateChange`
      stamp before overwriting it) instead of a discarded value.
      `internal/storage/bufpool.go`'s `Pool` gains a `sharedReadTimeNanos`
      accumulator + `AddReadTimeNanos`/`ReadTimeNanos` methods.
      `internal/initdb/open.go`'s pre-existing `OnPinDone` closure (already
      gated on the pinning backend's `track_io_timing` flag via
      `LookupTrackedGoroutine`) now passes `WaitEventEnd`'s returned
      duration into `pool.AddReadTimeNanos`, so real per-read wall-clock
      time only ever accumulates when `track_io_timing` is genuinely on —
      no new gate needed, reusing the existing one. `internal/executor/
      pgstat_io.go`'s `fetchIOStatRows` renders this as the `read_time`
      column (milliseconds, reusing `operators_explain.go`'s `nsToMs`) and
      also fixed a pre-existing dead-wiring bug: `BufferCounters()`'s
      `written` return value was already being collected (dirtied/written
      counters landed 2026-07-04) but silently discarded by `fetchIOStatRows`
      — now wired into the `writes`/`write_bytes` columns. Tests:
      `internal/activity/registry_test.go` (`TestWaitEventEndReturnsElapsedDuration`,
      `TestWaitEventEndOutOfRangeProcNumReturnsZero`), `internal/storage/
      bufpool_counters_test.go` (`TestPoolReadTimeNanosAccumulates`),
      `internal/executor/pgstat_io_test.go`
      (`TestPgStatIOReadTimeAndWritesRendered`). Still open (at that point):
      `write_time` (evictVictim's flush has no wait-event hook to time yet)
      and the remaining 5 op counters (extends/evictions/reuses/writebacks/
      fsyncs + their bytes/time columns) — see ledger row 2026-07-05.
      `evictions`/`extends` op counters **done** (2026-07-05, later loop):
      `storage.Pool` gains `sharedEvictionCount`/`sharedExtendCount` +
      `EvictionCount()`/`ExtendCount()` accessors; eviction increments once
      per real victim eviction in `evictVictim` (any tag actually displaced,
      dirty or not), extend increments once per successful `PinNew` relation
      extension (the pool's sole `mgr.Extend` call site). `fetchIOStatRows`
      renders both plus `extend_bytes` for the one row goopg instruments.
      Tests: `internal/storage/bufpool_counters_test.go`
      (`TestBufferCountersEvictionAndExtend`), `internal/executor/
      pgstat_io_test.go` (`TestPgStatIOEvictionsAndExtendsRendered`).
      Design: `docs/design/0122-0003-explain-format-xml-yaml.md` new
      "`evictions`/`extends` counters" section. `write_time` **done**
      (2026-07-05, later loop): `storage.Pool` gains `sharedWriteTimeNanos`
      + `AddWriteTimeNanos`/`WriteTimeNanos` (exact mirror of
      `sharedReadTimeNanos`'s trio) plus a new `OnFlushWait`/`OnFlushDone`
      hook pair bracketing `evictVictim`'s dirty-victim `flushSlot` call
      (same `contentMu`-held span `OnPinWait`/`OnPinDone` brackets on the
      read side); `internal/initdb/open.go` wires it the same
      `WaitEventStart(..., WaitDataFileWrite)`/`WaitEventEnd` way, a
      deliberately new `Pool`-level pair distinct from `storage.Manager`'s
      existing `OnWriteWait`/`OnWriteDone` (those fire for every
      `WriteBlock` call including background bgwriter/checkpointer
      flushes). `fetchIOStatRows` renders it as `write_time` (col 8).
      Tests: `TestPoolWriteTimeNanosAccumulates`,
      `TestPoolOnFlushHooksFireOnDirtyVictimEviction`,
      `TestPgStatIOWriteTimeRendered`. Design: new "`write_time` counter"
      section. `extend_time` **done** (2026-07-05, later loop): same
      pattern applied to `PinNew`'s `p.mgr.Extend` call — `storage.Pool`
      gains `sharedExtendTimeNanos` + `AddExtendTimeNanos`/`ExtendTimeNanos`,
      a new `OnExtendWait`/`OnExtendDone` pair (distinct from
      `storage.Manager`'s own `OnExtendWait`/`OnExtendDone`, same
      per-backend-attribution reasoning as `write_time` vs. `mgr`'s write
      hooks), wired in `internal/initdb/open.go` via
      `WaitEventStart(..., WaitDataFileExtend)`; `fetchIOStatRows` renders
      it as `extend_time` (col 13). Tests:
      `TestPoolExtendTimeNanosAccumulates`,
      `TestPoolOnExtendHooksFireOnPinNewExtend`,
      `TestPgStatIOExtendTimeRendered`. Design: new "`extend_time` counter"
      section. Still open: the remaining 3 op counters (reuses/writebacks/
      fsyncs + their bytes/time columns) — each needs a genuinely new
      counting mechanism (strategy-ring reuse, bgwriter/checkpointer-scoped
      writeback attribution, fsync call-site instrumentation respectively),
      not a mechanical extension of the eviction/extend pattern; also
      EXPLAIN's `I/O Timings` line (now renderable since both `write_time`
      and `extend_time` exist) and `EXPLAIN (BUFFERS)` without ANALYZE.
      Ledger rows: `.ralph/deferral_ledger.md` (2026-07-04/2026-07-05, M0122-0003).
- [ ] **M0122-0004 — SQL language / executor features** (~21). Window frame
      ROWS/RANGE/GROUPS, GROUPING SETS/ROLLUP/CUBE, DEFAULT-clause
      parsing, intervals. **WITH CHECK
      OPTION removed from this bucket (2026-07-04, loop #14):** verify-before-
      implement caught that it was already fully resolved by the root-0025
      loop (enforcement, `44000`) plus prior `security_barrier`/
      `security_invoker` reloption-form parsing — only the `WITH
      (check_option=...)` reloption-form spelling itself was still an open
      gap, now closed (`internal/parser/ddl.go`; design
      `docs/design/root-0025-updatable-views.md`'s new Follow-up section;
      ledger). `unimplemented_feat.json`'s matching entry was stale, updated
      in place. Only remaining sub-item (restart persistence of
      check_option/security_barrier/security_invoker) tracks under
      M0119-0004, not here — a concurrent loop was mid-flight on exactly that
      gap; check `git log`/the ledger before re-picking it up.
      **BETWEEN SYMMETRIC removed from this bucket (2026-07-04, this loop):**
      implemented — `SYMMETRIC`/`ASYMMETRIC` reserved keywords
      (`internal/parser/token.go`/`keywords.go`), `p.acceptBetweenOrdering()`/
      `parseBetweenTail` (`internal/parser/select.go`) desugar
      `expr BETWEEN SYMMETRIC low AND high` to
      `(expr>=low AND expr<=high) OR (expr>=high AND expr<=low)` at parse
      time — no analyzer/planner/executor change (same strategy as plain
      BETWEEN). Tests: `internal/parser/between_test.go`. Design:
      `docs/design/0003-0013-between-operator.md` new Follow-up section;
      `unimplemented_feat.json` entry updated in place.
      **CTE-without-alias removed from this bucket (2026-07-04, this loop,
      verify-before-implement):** stale entry — already resolved by commit
      `8d281a1b` (FROM-subquery without alias gets synthetic `__sq_<pos>`
      alias, `internal/parser/select.go:1211-1220`); confirmed via a
      throwaway probe reproducing the uuid.sql shape the entry cited.
      `unimplemented_feat.json` entry updated in place, no code change
      needed.
      **ANY/SOME/ALL removed from this bucket (2026-07-05, this loop):**
      implemented the remaining operator × quantifier combinations and the
      subquery operand form. Previously only `=`/`!=`/`<>`/regex operators
      supported ANY against an array/scalar, `ALL` only for `=`, `SOME`
      wasn't a keyword, and no operator accepted a `(SELECT ...)` operand.
      New `KwSome` keyword (`internal/parser/token.go`/`keywords.go`,
      unreserved like `KwAny`); `parser.InExpr`/`planner.InExpr` gained
      `AllOp bool` alongside the existing `AnyOp`; `parseAnyTail`
      (`internal/parser/select.go`) now also accepts a `SELECT` operand
      mirroring `parseInTail`; a new dispatch block in `parseExprPrec`
      covers `<`/`>`/`<=`/`>=` × ANY/SOME/ALL and `!=`/`<>` ALL (the
      pre-existing `=`/`!=`/`<>`/regex ANY blocks were extended in place
      for SOME/ALL rather than rewritten). `internal/executor/expr.go`'s
      `evalInExpr` gained an AND-semantics ALL branch; the subquery operand
      needed zero new executor plumbing (`collectInValues` already drains
      an arbitrary single-column subquery for `IN (subquery)`). Tests:
      `internal/parser/any_all_test.go`, `internal/executor/any_all_test.go`.
      Design: `docs/design/0003-0008-subqueries.md` new Follow-up section
      (also removed the stale "ANY / SOME / ALL... deferred" out-of-scope
      line); `docs/design/README.md` row updated in place. Known limitation
      (not fixed, matches a pre-existing ANY simplification kept
      consistent): NULL elements are skipped rather than fully
      three-valued (see design doc).
      **Named windows removed from this bucket (2026-07-05, this loop):**
      implemented `WINDOW name AS (PARTITION BY ... ORDER BY ...)` clauses
      and the bare `OVER name` reference form (previously only the
      anonymous `OVER (...)` form parsed). `parser.SelectStmt` gained
      `WindowClause []NamedWindowDef`; `WindowDef` gained `RefName string`
      for the bare-name form. `parseWindowDef`
      (`internal/parser/select.go`) now branches on `(` vs. a bare
      identifier after `OVER`; the shared partition/order body was
      factored into `parseWindowSpecBody` so the anonymous and named forms
      can't drift apart. `WINDOW` is parsed via `acceptIdentKeyword`
      (mirrors the existing WITHIN/FILTER unreserved-keyword precedent,
      no new reserved keyword). `isAliasStart` gained a `"window"`
      exclusion alongside the pre-existing `"fetch"` one (otherwise
      `sum(x) OVER w WINDOW w AS (...)` would swallow `window` as an
      implicit column alias). All resolution happens in a new
      `analyzer.resolveNamedWindowRefs`, which walks the same expression
      positions `exprHasWindowFunc` already checks and copies a named
      definition's PartitionBy/OrderBy into the referencing WindowDef
      in-place before `analyzeTargets` runs — the planner and executor
      needed **zero** changes since they only ever see the resolved AST.
      Raises `42P20` for an undefined window name. Tests:
      `internal/parser/window_test.go`
      (`TestParseWindowClauseNamedWindow`,
      `TestParseWindowClauseMultipleNamedWindows`),
      `internal/analyzer/analyzer_test.go`
      (`TestAnalyzeNamedWindowClauseAccepted`,
      `TestAnalyzeNamedWindowUndefinedRejected`),
      `internal/executor/window_compat_test.go`'s
      `TestCompatWindowNamedWindowClause` (byte-identical output vs. the
      same spec written inline twice). Design:
      `docs/design/0020-0001-window-parser-and-ast.md` new Follow-up
      section; `docs/design/README.md` row extended; `unimplemented_feat.json`
      entry updated in place.
      **Frame-consuming aggregate window functions removed from this
      bucket (2026-07-05, this loop):** implemented `sum`/`count`/`avg`/
      `min`/`max` as window functions (`sum(x) OVER (...)`,
      `count(*) OVER (...)`, with `FILTER (WHERE ...)` support) —
      the prerequisite the previous loop's note called out (frame
      execution had no consumer since row_number/rank/lag/lead never
      consult a frame). Deliberately implements PostgreSQL's *default*
      frame (no explicit ROWS/RANGE/GROUPS clause needed: RANGE
      UNBOUNDED PRECEDING, cumulative + peer-inclusive, when ORDER BY
      is present; the whole partition otherwise) rather than general
      frame-clause parsing — verified against upstream PostgreSQL 18.3
      directly. `planner.WindowFunc` (`internal/planner/plan.go`) gained
      `Star`/`Filter`/`InputType`; `buildWindowFunc`
      (`internal/planner/planner.go`) gained a `sum/count/avg/min/max`
      case reusing `buildAggregateCall`'s output-type rules; DISTINCT and
      aggregate-internal ORDER BY are rejected with `0A000` (a genuine
      PG restriction on aggregate window functions, not a v0 gap —
      matches `parse_func.c`'s `transformAggregateCall` wording exactly).
      `windowCallKey` gained a `filter:` component (latent bug fix: two
      `sum(x) FILTER (WHERE a) OVER (w)` / `... FILTER (WHERE b) OVER
      (w)` calls previously collided onto the same output column).
      `analyzer.analyzeWindowFuncCall` mirrors the same validation.
      Executor (`internal/executor/operators_window.go`) reuses the
      *existing* GROUP BY aggregate accumulator
      (`aggregateOp.applyAgg`/`finishAgg`) via a new
      `windowFuncToAggregateCall` adapter and a bare `&aggregateOp{ctx:
      o.ctx}` helper — no second aggregation implementation — so
      numeric-exact sums and float4/float8 formatting come for free.
      `evalFrameAggFuncs`/`peerGroupBounds` compute peer-group
      boundaries per partition (reusing the same `samePeer` check
      rank() already used) and walk groups in cumulative order; with no
      ORDER BY, `samePeer` always returns true so this collapses to one
      group spanning the whole partition — the "no ORDER BY" default
      falls out with no special-casing. Tests:
      `internal/analyzer/analyzer_test.go`
      (`TestAnalyzeWindowAggregateFunctionsAccepted`,
      `TestAnalyzeWindowAggregateFunctionsRejected`;
      `TestAnalyzeWindowFunctionUnsupportedRejected` repointed at
      `first_value()` since `count(*) OVER ()` is no longer a valid
      rejection case), `internal/executor/window_compat_test.go`
      (`TestCompatWindowAggregatesDefaultFrame`,
      `TestCompatWindowAggregateNoOrderByWholePartition`,
      `TestCompatWindowAggregateFilterClause` — all cross-checked
      against a scratch upstream PostgreSQL 18.3 instance). Design:
      `docs/design/0020-0001-window-parser-and-ast.md` new Follow-up
      section; `docs/design/README.md` row extended;
      `unimplemented_feat.json`'s frame-clause entry annotated in
      place (frame clauses themselves remain confirmed-open — this
      slice only gives them a real consumer for a future loop).
      **first_value/last_value/nth_value removed from this bucket
      (2026-07-05, this loop):** implemented all three as window
      functions on the same default-frame infra the previous slice
      built. `buildWindowFunc`/`analyzeWindowFuncCall` gained
      `first_value`/`last_value` (1 arg) and `nth_value` (2 args)
      cases mirroring `lag`/`lead`'s arg-shape checks. Executor
      (`operators_window.go`) adds a per-partition `frameEnd[]` array
      (gated by `hasFrameValueWindowFunc`) derived from the existing
      `peerGroupBounds` — no new frame-bounds computation needed:
      `first_value` reads the partition head (`o.rows[pStart]`),
      `last_value` reads the current row's peer-group tail
      (`o.rows[frameEnd[localIdx]-1]`), `nth_value` evaluates its `n`
      argument per row (like `lag`/`lead`'s offset), rejects `n <= 0`
      with `22016` (matches `window_nth_value` in
      `postgres/src/backend/utils/adt/windowfuncs.c` exactly, error
      text included), and returns `NULL` once `pStart+n-1` reaches or
      passes the frame end. Tests:
      `internal/analyzer/analyzer_test.go`
      (`TestAnalyzeWindowValueFunctionsAccepted`,
      `TestAnalyzeWindowValueFunctionsRejected`;
      `TestAnalyzeWindowFunctionUnsupportedRejected` repointed at
      `ntile()` since `first_value()` is no longer a valid rejection
      case), `internal/executor/window_compat_test.go`
      (`TestCompatWindowValueFunctionsDefaultFrame`,
      `TestCompatWindowNthValueOutOfFrameAndInvalidN`) — cross-checked
      row-for-row (incl. the `nth_value(val,0)` error text) against a
      scratch upstream PostgreSQL 18.3 instance. Design:
      `docs/design/0020-0001-window-parser-and-ast.md` new Follow-up
      section; `docs/design/README.md` row extended;
      `unimplemented_feat.json` entry updated in place.
      **ntile/cume_dist/percent_rank removed from this bucket
      (2026-07-05, this loop):** implemented the three remaining ranking
      window functions — none were a mechanical drop-in the way
      first_value/last_value/nth_value were. `ntile(n)` reproduces
      `window_ntile` (`postgres/src/backend/utils/adt/windowfuncs.c`)
      exactly (n evaluated once per partition, `22014` for `n<=0`,
      remainder rows go to the first buckets not the last) via new
      `evalNtileFuncs`/`evalNtileFunc`; `percent_rank()` =
      `(rank-1)/(total-1)`; `cume_dist()` = `NP/total` reusing the
      existing `frameEnd[]` peer-group boundary (`hasFrameValueWindowFunc`
      extended to also gate it for `cume_dist`). Tests:
      `internal/analyzer/analyzer_test.go`
      (`TestAnalyzeWindowRankingFunctionsAccepted`,
      `TestAnalyzeWindowRankingFunctionsRejected`;
      `TestAnalyzeWindowFunctionUnsupportedRejected` repointed at
      `dense_rank()`), `internal/executor/window_compat_test.go`
      (`TestCompatWindowNtileBuckets`,
      `TestCompatWindowNtileMoreBucketsThanRows`,
      `TestCompatWindowNtileInvalidArgument`,
      `TestCompatWindowPercentRankAndCumeDist`). Design:
      `docs/design/0020-0001-window-parser-and-ast.md` new Follow-up
      section; `docs/design/README.md` row extended. Ledger:
      `.ralph/deferral_ledger.md` (2026-07-05, M0122-0004).
      **dense_rank() removed from this bucket (2026-07-05, this loop):**
      implemented as a window function — the last of the 11 standard
      PostgreSQL window functions to land (its `WITHIN GROUP`
      ordered-set-aggregate form, `pg_proc` OIDs 3992/3993, already
      existed separately and is unaffected). Joins the `row_number`/
      `rank` case in both `buildWindowFunc` (`internal/planner/
      planner.go`) and `analyzeWindowFuncCall` (`internal/analyzer/
      analyzer.go`) — same zero-arg/no-DISTINCT/no-star shape check,
      `int8` return type. No catalog change needed (`pg_proc` OID 3102
      `window_dense_rank` was already seeded, just never dispatched).
      Executor (`internal/executor/operators_window.go`) gains a
      `denseRank` counter alongside the existing `rank`/`rowNum` locals:
      `rank` jumps to the current row's 1-based position on a peer-group
      change; `denseRank` just increments by 1 at the same point, so it
      never skips a value after a tie (matches `window_dense_rank` in
      `postgres/src/backend/utils/adt/windowfuncs.c`). Tests:
      `internal/analyzer/analyzer_test.go`
      (`TestAnalyzeWindowRankingFunctionsAccepted` gains a `dense_rank()`
      case, `TestAnalyzeWindowRankingFunctionsRejected` gains a
      `dense_rank(1)` case; `TestAnalyzeWindowFunctionUnsupportedRejected`
      repointed at `array_agg() OVER ()` since `dense_rank()` is no
      longer a valid rejection case), `internal/executor/
      window_compat_test.go`'s `TestCompatWindowDenseRankPeerGroups`
      (same tie-then-gap fixture as `TestCompatWindowRankPeerGroups`,
      cross-checked against upstream PostgreSQL 18.3: `rank` 1,1,3 vs.
      `dense_rank` 1,1,2 on the same rows). Design:
      `docs/design/0020-0001-window-parser-and-ast.md` new Follow-up
      section; `docs/design/README.md` row extended;
      `unimplemented_feat.json` entry annotated in place. Gates:
      `go build ./...` clean; `go test ./internal/analyzer/...
      ./internal/planner/... ./internal/executor/...` PASS;
      `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33).
      **Still open in this bucket:** frame clause parsing/execution
      itself (ROWS/RANGE/GROUPS — now has three real consumers:
      `evalFrameAggFuncs`/`frameEnd`/`evalNtileFuncs` could generalize
      into an arbitrary frame-bounds function) is now the only open
      window-function item — every window function implemented across
      the M0122-0004 series still assumes PostgreSQL's default frame.
      Combining forms (`OVER (win ORDER BY ...)`, a named window based on
      another named window) are also out of scope (real upstream syntax,
      deferred — see design doc). Intervals also remain.
      **GROUPING SETS/ROLLUP/CUBE removed from this bucket (2026-07-05,
      this loop):** implemented real SQL:1999 §7.9 semantics — previously
      the parser discarded the construct into a plain GROUP BY (an
      `IntegerConst(0)` sentinel silently skipped in `buildAggregateStage`),
      so no subtotal/grand-total rows were ever produced.
      `internal/parser/select.go`'s rewritten `parseGroupByElems` (+
      `parseGroupingUnitList`/`parseGroupingSetsList`/`rollupAlternatives`/
      `cubeAlternatives`/`cartesianProductGroupingSets`) expands
      ROLLUP/CUBE/explicit GROUPING SETS into `SelectStmt.GroupingSets
      *GroupingSetsSpec` (`internal/parser/ast.go`), a fully materialized
      `[][]Expr` set list (cross-multiplied against any plain GROUP BY
      columns in the same clause, per upstream's cross-product rule).
      `rewriteGroupingSets` (`internal/planner/planner.go`, hooked into
      `planSelect` right after the indirection-star rewrite, before the
      CTE preplan and the `s.SetOp != nil` check) expands this into a
      synthetic UNION ALL chain of plain-GROUP-BY branches — falls
      straight through into the pre-existing N-ary set-op planning code
      (segment flattening, per-branch casts via `wrapSetOpBranchWithCasts`,
      `wrapSetOpSortLimit`), completely unmodified. `substituteGroupingExpr`
      replaces excluded-dimension references in each branch's target
      list/HAVING with `NULL` (recursing through `BinaryOp`/`UnaryOp`/
      `IsNullExpr`/`IsBoolExpr`/`IsDistinctFromExpr`/`CollateExpr`/
      `CastExpr`/`RowExpr`/`CaseExpr`/non-aggregate `FuncCall`) and
      resolves the new `GROUPING(...)` pseudo-function (dedicated
      `*parser.GroupingCall` AST node, analyzer-typed `int4` in
      `internal/analyzer/analyzer.go`) to a literal bitmask per branch —
      its value depends only on which generated set produced the row, so
      there's no runtime cost. No executor change was needed at all. Also
      removed the now-dead `IntegerConst{Value:0}` sentinel-skip branch in
      `buildAggregateStage` (a literal `GROUP BY 0` now correctly falls to
      the generic "position not in select list" 42P10 error instead of
      being silently ignored). Tests:
      `internal/parser/select_test.go` (`TestParseGroupByRollupExpandsToPrefixSets`,
      `TestParseGroupByCubeExpandsToAllSubsets`,
      `TestParseGroupByMixedPlainAndRollupCrossMultiplies`,
      `TestParseGroupingSetsExplicitList`, `TestParseGroupingFuncCall`),
      `internal/executor/grouping_sets_compat_test.go`
      (`TestCompatGroupByRollupGeneratesSubtotalsAndGrandTotal`,
      `TestCompatGroupByCubeGeneratesAllSubsetTotals`,
      `TestCompatGroupByExplicitGroupingSets`,
      `TestCompatGroupingFuncReportsRolledUpColumns`). Design:
      `docs/design/0122-0004-grouping-sets-rollup-cube.md`;
      `docs/design/README.md` new row; `unimplemented_feat.json` entry
      updated in place (`status: resolved`). Deferred (ledger row,
      2026-07-05): the substitution walker doesn't cover every
      `parser.Expr` variant (`InExpr`/`ExistsExpr`/array exprs) or
      window-function `.Over.PartitionBy`/`.OrderBy`. Gates: `go build
      ./...` clean; `go test ./internal/parser/... ./internal/analyzer/...
      ./internal/planner/... ./internal/executor/...` PASS (no
      regressions); `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33).
      **DEFAULT-clause parsing removed from this bucket
      (2026-07-05, this loop, verify-before-implement):** stale entry —
      the `unimplemented_feat.json` item ("DEFAULT clause in column
      definitions is skipped during parsing") predates 2026-05-12 and no
      longer matches the code: `internal/parser/ddl.go:4208-4214` stores
      `ColumnDef.DefaultExpr`, `internal/planner/planner.go`'s
      `defaultMarkerReplacement`/`rewriteInsertDefaultMarkers` substitute
      it for omitted INSERT columns and explicit `DEFAULT` markers
      (falling back to a synthesized `nextval(...)` for SERIAL/IDENTITY
      columns, else NULL), and `internal/executor/operators_ddl.go`
      persists/validates it across CREATE TABLE, `LIKE ... INCLUDING
      DEFAULTS`, ALTER TABLE ADD/ALTER COLUMN, and pg_dump's attrdef
      rendering. No code change needed. Verified at current HEAD:
      `TestInsertFillsMissingColumnDefault`,
      `TestInsertDoesNotOverrideExplicitColumnDefault`,
      `TestInsertFillsMissingColumnDefaultCurrentTimestamp`,
      `TestInsertFillsMissingColumnDefaultCurrentDate`,
      `TestInsertFillsMissingColumnDefaultNextval`,
      `TestInsertFillsMissingColumnDefaultNextvalAutoCreates`
      (`internal/executor/storage_test.go`) all PASS.
      `unimplemented_feat.json` entry updated in place (`status:
      resolved`).
- [ ] **M0122-0005 — Types / opclasses / casts / collation / domains** (~11).
      1-byte `char`(OID 18) disambiguation, `pg_collation_for`, function-based cast
      dumping, ALTER TYPE RENAME/OWNER, domain CHECK renderer, `pg_ts_config` OIDs.
      **ALTER TYPE RENAME/OWNER landed (2026-07-05, this loop, m0097-0017):**
      `OWNER TO` now works for enum + composite types; also fixed a separate bug
      where composite `RENAME TO` raised a spurious 42710 (unconditionally called
      the enum-only rename). Design `docs/design/0122-0005-alter-type-owner-rename.md`.
      Deferred: restart persistence of the new owner field, range/domain typowner
      (ledger row, same date). **Function-based cast dumping removed from this
      bucket (2026-07-05, this loop, verify-before-implement):** stale entry —
      already closed by commit `e12e573b` (2026-07-01, DU-002 slice 397),
      predating the M0122 backlog's 2026-07-02 snapshot. `dumpCast`'s
      `COERCION_METHOD_FUNCTION` arm already renders `WITH FUNCTION
      <ns>.<signature>` for a user-defined `CREATE CAST ... WITH FUNCTION`; no
      code change needed. Verified at current HEAD:
      `TestParseCreateCastWithFunction`, `TestValidateCreateCast`, and
      `TestPort_PgDumpConnectionSetup`'s slice-397/404 assertions (real
      `pg_dump` 18.3 round-trip) all PASS. `unimplemented_feat.json` entry
      updated in place (`status: resolved`). **Domain CHECK renderer also
      removed from this bucket (2026-07-05, this loop, verify-before-implement):**
      another stale entry — already closed by DU-002 slice 363 (2026-06-30),
      predating the 2026-07-02 snapshot. `renderDomainCheckPredicate`
      (internal/executor/operators_ddl.go) re-parses a domain's raw CHECK
      text and deparses it via the same fully-parenthesizing
      `defaultExprToSQL` renderer the table-CHECK path uses (slice 362),
      wired into the pg_constraint dump path (internal/executor/expr.go
      `AllDomains` branch) for generic (non-IN) domain CHECKs; `CHECK (VALUE
      IN (...))` keeps the pre-synthesized legacy wrap by design. No code
      change needed. Verified at current HEAD: `TestRenderDomainCheckPredicate`,
      `TestRenderCheckPredicate{,Fallback}`, and
      `TestPort_PgDumpConnectionSetup`'s slice-362/363 assertions all PASS.
      `unimplemented_feat.json` entry updated in place (`status: resolved`).
      Residual (already ledgered, unaffected): negative-literal `Const` casts
      inside a domain CHECK (`VALUE < -5`) still diverge from PG's typed
      `'-5'::integer` rendering (type-blind `defaultExprToSQL`). **`pg_ts_config`
      OIDs also removed from this bucket (2026-07-05, this loop,
      verify-before-implement):** another stale entry — the audit misread
      `mappedLocalCatalogPlaceholderOIDs`' deliberately-retained legacy
      3764/3765 placeholder-file entries (internal/initdb/initdb.go:1301,
      explicitly commented "stale") as a missing OID mapping. The actual
      seeded OIDs already match PG18 verbatim: `pg_ts_config`=3602
      (`pg_ts_config.h:30`), `pg_ts_config_map`=3603 (`pg_ts_config_map.h:30`),
      both asserted in `internal/initdb/pg_ts_config_nailed_test.go` and
      `pg_ts_config_map_nailed_test.go`. The legacy 3764/3765 entries only
      make `bootstrapMappedLocalCatalogHeaps` stub an extra (unused, harmless)
      relfilenode file — idempotent, no functional gap. No code change
      needed. Verified at current HEAD:
      `TestNailedLocalRelsContainsPgTsConfig{,Map}{,Indexes}`,
      `TestPgTsConfig{,Map}IndexInitialEntries`, and
      `TestPgTsConfig{,Map}AttrsTypeOIDsMatchPG18` all PASS.
      `unimplemented_feat.json` entry updated in place (`status: resolved`).
      **Still open in this bucket:** 1-byte `char` disambiguation,
      `pg_collation_for`.
- [ ] **M0122-0006 — On-disk catalog persistence & shared catalogs** (~8).
      Persistent `pg_index` heap, index column order (ASC/DESC/NULLS) across
      restart, `pg_tablespace` visibility, `pg_database.datconnlimit` write.
- [ ] **M0122-0007 — DDL / admin commands / ctl / GUC config** (~14). CREATE/DROP
      DATABASE full DDL, `goopg ctl restart`, REINDEX, SIGHUP config reload,
      tablespaces, ALTER FUNCTION/COLUMN, planner/jit GUC stubs.
- [ ] **M0122-0008 — Auth / roles / multi-DB isolation / encoding** (~6). SASLprep
      / channel binding / `scram_iterations`, RBAC + `SET SESSION AUTHORIZATION`,
      encoding constraints during bootstrap/runtime.
      **RBAC for INSERT/UPDATE/DELETE landed (2026-07-05, this loop,
      M0097-0040):** `dmlPrivilegePermitted` (`internal/executor/
      operators_storage.go`) checks the existing `tableACLs`/
      `HasTablePrivilege` store (TRUNCATE/MAINTAIN already consulted it;
      plain DML never did) pre-lock in `insertOp`/`updateOp`/
      `deleteOp.Open`, raising `42501` for a non-superuser, non-owner role
      missing the matching GRANT. FK-cascade deletes and the logical-
      replication apply worker write heap pages directly and are
      unaffected. Tests: `internal/executor/storage_dml_test.go`'s
      `TestDMLRequiresTablePrivilege`. Design:
      `docs/design/0118-0039-truncate-conflict-privilege-model.md` Follow-up
      section; `unimplemented_feat.json` M0097-0040 updated in place.
      **`SELECT` enforcement landed (2026-07-05, same day):**
      `seqScanOp.Open`/`indexScanOp.openPrep`/`indexOnlyScanOp.Open` now call
      `dmlPrivilegePermitted(ctx, tbl, "SELECT")`, with a
      `catalog.IsSystemRelation(tbl.OID)` carve-out that always permits
      SELECT on pg_catalog/information_schema (no pg_init_privs-equivalent
      default-ACL seeding exists). Tests:
      `TestSeqScanRequiresSelectPrivilege`,
      `TestIndexScansRequireSelectPrivilege`,
      `TestSystemCatalogSelectAlwaysPermitted`. Design doc Follow-up section
      extended; `unimplemented_feat.json` updated in place. **Still open in
      this bucket:** views inline into the querying role's own scan with no
      view-owner identity, so `GRANT SELECT ON view` alone (no base-table
      grant) is now denied (ledger, scope boundary — untested by any
      existing suite). SASLprep/channel binding/`scram_iterations`, encoding
      constraints.
- [ ] **M0122-0009 — WAL / recovery / crash-consistency infra** (~16). WAL segment
      recycling, `WALInsertLock` array (parallel inserts), MultiXact WAL,
      `pg_subtrans` truncation. Gate: `-race` + recovery E2E (WAL practice card).
- [ ] **M0122-0010 — Concurrency: buffer pool & btree locking** (~17, LARGE).
      Lehman/Yao crab-walk, `splitMu` removal, storage-pool pin-count race,
      re-enable the `-race` gate. Gate: race detector mandatory.
- [ ] **M0122-0011 — Query optimizer & TPC-H/HammerDB correctness** (~17). Anti/
      semi-join unnesting (NOT IN), Q8/Q9/Q21 row-count fixes; several blocked on
      the slot/TupleSlot pipeline (see M0122-0012). Gate: TPC-H spot-check.
- [ ] **M0122-0012 — Perf infra: vectorization / slot-pipeline / harness** (~19,
      ARCHITECTURAL). Borrow-semantics allocation rewrite, plannode migration,
      vectorized FilterOp/SeqScanOp, plan cache, HammerDB SF1 validation.
- [ ] **M0122-0013 — Physical/streaming replication & standby** (~10, EPIC/blocked).
      Streaming-replication epic (~25 sub-items), cascading replication,
      `STANDBY_SNAPSHOT_READY` transition.
- [ ] **M0122-0014 — Logical replication / decoding / subscription** (~11, EPIC).
      pgoutput DELETE identity, subscriber apply worker, DDL replication. Blocked
      on logical decoding (tracks D-004; overlaps M0119-0007 — dedupe).
- [ ] **M0122-0015 — Test-suite porting: amcheck / verify_heapam / pg_dump** (~8).
      `verify_heapam()` SRF + opclass parity, AC-002..005, pg_dump 002-010.
      **Overlaps M0119-0004/0006 — the triage assigns each item to ONE milestone;
      do not double-work.**

> This task list is **seeded, not exhaustive.** The M0122-0001 triage plus every
> future feature deferral appended to `unimplemented_feat.json` (any new `open`
> entry) feed additional M0122 tasks over time; the milestone's living nature
> means it need not be complete at filing. Small/residual entries (TOAST
> compression, autovacuum, FDW/HANDLER stub, GIST, LANGUAGE C) fold into the
> nearest cluster by the triage.
