# Milestone 0107 — Performance Optimization Refactor

**Status:** planned
**Filed:** 2026-05-20
**Depends on:** M0105 (heap page + tuple format parity, accepted), M0106 (PG relcache init file + pg_control parity, in-flight)
**Reference plan:** `.ralph/fix_plan.md` (M0107 section)

## Problem

The pgbench analysis report at `analysis/perf-optimize/` ran goopg side-by-side
with PostgreSQL 18.3 across three workloads (TPC-B-like / simple-update /
select-only) and three client counts (10 / 50 / 100). Three structural
findings (see `docs/design/perf-optimize/00-overview.md` §1):

1. **GC dominates CPU at 54–77 %** in every measured pattern.
   `runtime.gcBgMarkWorker` + `runtime.scanobject` consume 63 % cumulative CPU
   at c=10 select-only and rise to 77 % at c=10 simple-update. Driver: pointer
   density of the live heap (each `Datum` carries three GC-traced fields × ~50
   per row; 128 buffer-pool partitions each hold a pointer-rich
   `map[BufferTag]int`).
2. **Two single-mutex bottlenecks gate the write and read paths.**
   `internal/mvcc/manager.go:73`'s `Manager.mu` accounts for **92 % of write
   mutex delay** (gates Begin/SnapshotFor/Commit/OldestXmin/finish under one
   lock). `internal/activity/activity.go:123`'s `Registry.mu` accounts for
   **95 % of c=100 select-only mutex delay** (every protocol frame takes it
   for the WaitEventStart/End pair).
3. **A hot-page livelock at c=100 simple-update.** `pgbench_history` inserts
   deterministically target the relation's tail page; the 128
   `bufferPartition.mu` that covers that page gathers 19 goroutines; combined
   with GC STW windows the system stalls for ≥23 minutes (the c=100 standard
   and c=100 simple-update workloads SKIPPED with DEADLOCK).

The design series at `docs/design/perf-optimize/00-overview.md` … `09-migration-and-rollout.md` (10 docs, accepted) describes the architectural refactor. This milestone is the implementation track.

## Goal

Hit the post-refactor performance bands defined in
`docs/design/perf-optimize/09-migration-and-rollout.md` §5 while keeping every
PG-compatible artefact landed by M0105 + M0106 **byte-identical** to upstream
PostgreSQL 18.

Pre-refactor baseline (commit `ab1b955`) and post-refactor targets:

| Metric | Pre-refactor | Post-refactor target |
|---|---|---|
| c=10 select-only TPS | 2 307 | ≥ 8 000 |
| c=10 simple-update TPS | 410 | ≥ 1 500 |
| c=10 standard TPS | 349 | ≥ 1 200 |
| c=50 select-only TPS | 5 034 | ≥ 18 000 |
| c=50 simple-update TPS | 347 | ≥ 2 000 |
| c=50 standard TPS | 339 | ≥ 1 800 |
| c=100 select-only TPS | 6 400 | ≥ 12 000 |
| c=100 simple-update TPS | DEADLOCK / SKIPPED | ≥ 500 |
| c=100 standard TPS | DEADLOCK / SKIPPED | ≥ 500 |
| `gcBgMarkWorker` cum% (c=10 SO) | 63.3 % | < 15 % |
| `runtime.futex` cum% (c=100 SO) | 23.0 % | < 8 % |
| `mvcc.Manager.*` mutex top-20 | dominant | absent |
| `activity.Registry.*` mutex top-20 | dominant | absent |
| `bufferPartition.mu` mutex top-20 | dominant | absent |
| `Datum` sizeof | 64 B | 24 B (zero GC-traced fields) |

## Operational policy

- **PG18 byte compatibility is a hard invariant.** The companion design doc
  [`docs/design/0107-0001-m0106-pg-compat-invariants.md`](../design/0107-0001-m0106-pg-compat-invariants.md)
  catalogs every locked artefact (on-disk files, byte-equivalent Go structs,
  catalog row layouts, B-tree indexes, WAL record formats). Every M0107
  sub-milestone MUST verify that the artefacts in that catalog remain unchanged
  before being marked complete. **Deferral of this gate is not permitted**, in
  the same spirit as M0106's no-defer policy.
- **Internal-Go-API breakage is permitted.** The refactor changes `Datum`,
  `Slot`, `Operator`, `MVCC.Manager`, `activity.Registry`, `storage.Pool`, and
  `wal.Writer` shapes. Callers across `internal/` are updated in the same
  phase that lands the breaking change.
- **No experimental Go `arena` package** (`golang.org/x/exp/arena`). We build
  our own allocator under `internal/mctx` per
  `docs/design/perf-optimize/01-memory-context.md`.

## Scope

### In Scope (in-memory layouts only)

1. New `internal/mctx` hierarchical memory-context allocator.
2. Pointer-free `Datum` (24 B; three GC-traced fields removed).
3. Concrete-type Volcano executor (`OpNode` / `Slot` / `PlanNode` / `ExprNode`
   sum-types; `Operator` and `TupleSlot` interfaces deleted from hot paths).
4. MVCC ProcArray + atomic XidGen + bank-locked CLOG (replaces `Manager.mu`).
5. Per-backend `wait_event_info` slot array (replaces `Registry.mu` +
   `map[string]*Backend`).
6. Lock-free buffer pool (replaces 128-partition `sync.Mutex` bufmapping).
7. WAL insert striping (8 stripes) + FSM-driven page distribution (replaces
   single `appendMu` and tail-page-targeting insert logic).
8. Bounded `//go:linkname` shims for `nanotime`, per-P xid cache, and slot
   semaphores under `internal/runtimeshim`.

### Out of Scope (locked by M0105/M0106 — see invariants doc)

- Any on-disk file format (`global/pg_control`, `pg_wal/...`,
  `pg_internal.init`, `pg_xact/`, `pg_subtrans/`, `pg_multixact/`,
  per-relation VM / FSM, heap pages, btree pages, `PG_VERSION`).
- Wire protocol bytes.
- Catalog heap-tuple row layouts (pg_class, pg_attribute, pg_proc, pg_type,
  pg_index, pg_opclass, pg_am, pg_amop, pg_amproc, pg_rewrite, pg_trigger,
  shared catalogs).
- WAL record byte format (`XLOG_CHECKPOINT_*`, `XLOG_HEAP_*`,
  `XLOG_HEAP2_*`, `XLOG_XACT_*`, `XLOG_BTREE_*`).
- Byte-equivalent Go structs (`ControlFileData`, `Form_pg_class`,
  `Form_pg_attribute`, `RelationData`, `CheckPoint`, `HeapTupleHeaderData`,
  `ItemIdData`, `PageHeaderData`, `XLogPageHeaderData`, `BTMetaPageData`,
  `BTPageOpaqueData`).

### Permitted PG interactions

Per `.ralph/AGENT.md` §"Vanilla PG Compatibility (ABSOLUTE)":

- Adding `elog(DEBUG1, ...)` calls under `./postgres/` for diagnostic purposes
  during investigation (must be reverted at task close).
- Reading PG source to confirm expected byte layout.
- `make install` to rebuild PG after adding/removing debug logging.

**Forbidden** (per the same section):

- Changing PG function signatures, struct layouts, or logic.
- Adding `if (goopg_compat) {...}` branches or similar PG-side workarounds.
- Any change that would make PG behave differently from upstream release.

## Sub-milestones

Mapped to the four-phase rollout in
`docs/design/perf-optimize/09-migration-and-rollout.md` §2–§5. Each
sub-milestone is independently shippable and revertible.

| ID | Phase | Title | Primary design doc |
|---|---|---|---|
| M0107-0001 | A | `mctx` memory-context substrate | `perf-optimize/01-memory-context.md` |
| M0107-0002 | B | Pointer-free `Datum` (24 B) | `perf-optimize/02-datum-pointer-free.md` |
| M0107-0003 | C | Concrete-type Volcano executor | `perf-optimize/03-executor-concrete.md` |
| M0107-0004 | D1 | MVCC ProcArray + XidGen + CLOG bank locks | `perf-optimize/04-mvcc-procarray.md` |
| M0107-0005 | D2 | Per-backend `wait_event_info` | `perf-optimize/05-activity-perbackend.md` |
| M0107-0006 | D3 | Lock-free buffer pool | `perf-optimize/06-bufpool-lockfree.md` |
| M0107-0007 | D4 | WAL insert striping + FSM page distribution | `perf-optimize/07-wal-fsm-insert.md` |
| M0107-0008 | D5 | Runtime internals (`//go:linkname` shims) | `perf-optimize/08-runtime-internals.md` |

Phase ordering and dependencies are stated in
`perf-optimize/09-migration-and-rollout.md` §2–§5. Concretely:

- D1 (M0107-0004) and D2 (M0107-0005) share `procNum`; land together or close.
- D4 (M0107-0007) depends on D3 (M0107-0006) — D4's FSM consults D3's `bufmap`.
- D5 (M0107-0008) is independent but the slot semaphore is consumed by D3.

## Definition of Done

For each M0107-NNNN sub-milestone:

1. The sub-milestone's primary design doc is `accepted` (not `draft`).
2. Implementation merged on `master`.
3. The sub-milestone's smoke-test band from
   `perf-optimize/09-migration-and-rollout.md` is met (TPS targets, GC%
   targets, mutex top-20 targets — phase-specific).
4. `TestE2E_FailoverGoopgToPG/async` PASS (PG18 standby attaches to a goopg
   primary, replays WAL, serves reads on replicated data).
5. The relevant byte-layout regression tests in `internal/initdb/...`,
   `internal/control/...`, `internal/wal/...`, `internal/access/heap/...`,
   `internal/access/btree/...` PASS — i.e., no artefact in the
   [invariants catalog](../design/0107-0001-m0106-pg-compat-invariants.md)
   has been silently modified.
6. `gofmt -l .` empty; `go vet ./...` clean; `make ralph-state-guard` PASS.

For the milestone overall:

1. All 8 sub-milestones have DoD satisfied.
2. The integrated acceptance band in
   `perf-optimize/09-migration-and-rollout.md` §5 is met
   (c=10 SO ≥ 8 000 TPS; c=50 SU ≥ 2 000 TPS; c=100 SU ≥ 500 TPS with no
   DEADLOCK; `gcBgMarkWorker` < 15 %; `runtime.futex` < 8 %).
3. The legacy design docs listed in
   `perf-optimize/09-migration-and-rollout.md` §6 are marked SUPERSEDED with
   forward-pointer headers (M0068-0003, M0073-0001, M0074-0003, M0098-0003,
   M0099-0002, M0091-0001).

## Required Design Docs

Already in the repo (do not re-draft):

- `docs/design/perf-optimize/00-overview.md`
- `docs/design/perf-optimize/01-memory-context.md`
- `docs/design/perf-optimize/02-datum-pointer-free.md`
- `docs/design/perf-optimize/03-executor-concrete.md`
- `docs/design/perf-optimize/04-mvcc-procarray.md`
- `docs/design/perf-optimize/05-activity-perbackend.md`
- `docs/design/perf-optimize/06-bufpool-lockfree.md`
- `docs/design/perf-optimize/07-wal-fsm-insert.md`
- `docs/design/perf-optimize/08-runtime-internals.md`
- `docs/design/perf-optimize/09-migration-and-rollout.md`

New (filed with this milestone):

- [`docs/design/0107-0001-m0106-pg-compat-invariants.md`](../design/0107-0001-m0106-pg-compat-invariants.md) — DO-NOT-BREAK catalog enumerating every PG-compatible artefact locked by M0105/M0106. Cited by every M0107 sub-milestone's DoD.

Per-sub-milestone design docs (added as each phase opens, if a
sub-milestone's work warrants more detail than the chapter doc carries):

- `docs/design/0107-0002-...` and onward — created in the implementing
  loop, indexed in `docs/design/README.md` in the same commit.

## Verification commands

The same commands `analysis/perf-optimize/` uses; reused unchanged so
post-refactor numbers are directly comparable to the `ab1b955` baseline:

```bash
# Focused per-phase (per-sub-milestone) verification:
go test ./internal/<package>/...
go test ./internal/<package>/... -race
make ralph-state-guard

# Cross-phase byte-compat regression gate:
go test -v -run 'TestE2E_FailoverGoopgToPG/async' ./internal/...

# Full pgbench performance suite (~60 min wall-clock; used per-phase
# smoke and at milestone close):
bash analysis/perf-optimize/scripts/run_perf_suite.sh
bash analysis/perf-optimize/scripts/analyze.sh "$(ls -t analysis/perf-optimize/runs/ | head -1)"

# Compare new run against pre-refactor baseline:
diff -u analysis/perf-optimize/runs/20260518_115032/results_summary.tsv \
       analysis/perf-optimize/runs/<NEW_RUN_ID>/results_summary.tsv
```

A `make perf-suite` wrapper for the three pgbench commands is described in
`perf-optimize/09-migration-and-rollout.md` §7.

## Rollback rules

Inherited from `perf-optimize/09-migration-and-rollout.md` §9:

1. If any unit test regresses, do not merge the phase.
2. If TPC-H regresses > 10 % on any query, do not merge.
3. If pgbench TPS regresses on a previously-working suite, do not merge.
4. If the acceptance band is not hit but no regression, merge is permitted
   at user discretion; the miss is noted in the milestone retrospective and
   addressed in a follow-up sub-milestone.
5. If a phase merges and a regression is discovered later, revert that
   phase's PR; re-attempt as a separate sub-milestone after root cause is
   understood.
