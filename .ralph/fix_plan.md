# goopg Fix Plan

The roadmap below is derived from `.ralph/specs/GOAL_AND_REQUIREMENTS.md`. The
"Definition of Done (Initial Milestone)" in §10 of the spec is the target;
items here decompose that target into agent-sized chunks. Pick the topmost
unchecked item unless a dependency forces a different order.

NOTE: past milestones are stored in `completed_milestones/` and should NOT be copied. If you need to reference a past milestone, you can see these files for the historical record, but they are not part of the active fix plan. Only items in this file are actionable.

### M0092 outcome — structural changes landed; TPS NOT improved

M0092 (`docs/milestones/0092-lazy-row-emission-in-scan-and-project.md`)
landed in 4 commits on 2026-05-11:

- `57312d5` — 3 design docs.
- `5211387` — NLI prerequisite: `nestedLoopIndexJoinOp`
  deep-copies outerRow into `currentOuter`.
- `dc52f60` — `projectOp.Next` drops per-row `cloneRow`;
  `MaterializedSlot.Materialize` now always deep-copies.
- `8f32c07` — `indexScanOp` lazy refactor (TID-list-eager +
  heap-fetch-lazy; arena field removed).

End-to-end pgbench select-only @ -c 10 -T 180 scale 100:

- post-M0091 (commit 460809c): **510.52 TPS** / 19.6 ms
- post-M0092 (commit 8f32c07): **437.62 TPS** / 22.8 ms
  (−14 %)

The structural changes are correct (all tests pass, data
integrity preserved) but did NOT deliver TPS improvement at
this workload. Per the post-fix alloc profile, the cloneRow
path moved into `slot.Materialize` (now always deep-copies)
rather than being eliminated; rowPool.New stayed at ~35 % of
allocs. The residual is broadly distributed across small
sites (SlotFromRow, ParseHeapTuple, PageGetHeapTuple,
protocol cells slice) + GC at 80 % of CPU.

The structural changes still matter for OTHER workloads
(wide TPC-H index scans get a memory-footprint reduction;
slot contract is tightened; NLI is defensive). They just
don't move pgbench-c10's TPS needle.

**M0092 follow-up landed 2026-05-11** (commits `55f6de0`,
`a0817bb`, `1d331a1`, `da7224d`, `1916109`): all four
broadly-distributed allocation cuts (SlotFromRow stack-
aliasing, protocol DataRow allocation reduction,
ParseHeapTupleNoCopy + RLock-held-across-decode,
`track_io_timing` GUC gating 14 I/O hooks) confirmed gone
from the steady-state top-23 alloc list. **TPS did NOT
move** past the noise floor: M0092 baseline re-measures at
317 TPS; M0092 followup runs at 283-342 TPS. CPU pprof
shows the goopg server at 0.17 % CPU — pgbench-S is NOT
CPU-bound. Full analysis:
`bench/pgbench-compare/results/20260511_goopg_select-only_m0092_followup_summary.md`.

The actual bottleneck identified during the M0092 follow-up
audit: **per-commit WAL fsync for read-only transactions**.
goopg currently emits an XactCommit WAL record + sync
fsync for every transaction including pure SELECT, which
differs from PostgreSQL's lazy-XID model where read-only
transactions skip `RecordTransactionCommit` entirely. 60-s
server log shows 19,684 `walwriter flush` lines matching
the transaction rate. Filed as M0093 below.

Results:
`bench/pgbench-compare/results/20260511_133003_goopg_select-only_c10_m0092.txt`
+ `20260511_goopg_select-only_m0092_summary.md`
+ `20260511_goopg_select-only_m0092_followup_summary.md`.

## M0093 — Read-only commit skip-WAL (PG-parity) (filed 2026-05-11)

**Background:** M0092 follow-up identified that goopg emits
a synchronous WAL `XactCommit` record + `FlushUpTo`
(fsync) for **every** transaction, including read-only
`SELECT`. This diverges from PostgreSQL's lazy-XID-allocation
design where read-only transactions never call
`RecordTransactionCommit` and emit zero WAL on commit. The
result: pgbench-S `-c 10` is bottlenecked on per-query
fsync at 282-342 TPS while the goopg server idles at
0.17 % CPU.

Milestone doc:
`docs/milestones/0093-read-only-commit-skip-wal-emission.md`.

Design docs:
- `docs/design/0093-0001-readonly-commit-skip-wal.md`
  (chosen design: **A — wroteWAL flag on transaction
  state**; every WAL-Append call site enumerated for the
  M0093-0002 audit boundary).
- `docs/design/0093-0002-pgbench-remeasurement-target.md`
  (re-measurement methodology; target TPS ≥ 1,000 =
  M0091's bar; secondary target: walwriter flush rate
  drops from ~19,600 / 60 s to < 100 / 60 s).

### Sub-milestones

- [ ] **M0093-0001** — Author/finalise design doc
      `0093-0001-readonly-commit-skip-wal.md`. Choose
      between Design A (`wroteWAL` flag — recommended) and
      Design B (lazy XID assignment, PG-parity). Enumerate
      every WAL-Append call site that needs to participate
      and the transaction-wiring approach. Status: drafted
      2026-05-11; review + accept.

- [ ] **M0093-0002** — Implementation. Add
      `mvcc.Manager.NoteWrote(xid)` (Design A) or
      lazy-allocate XID at first write (Design B). Gate
      `Manager.finish`'s `xactMarker` invocation on the
      "wrote WAL" condition. Wire every WAL-Append call
      site from a transactional context. Unit tests:
      - `TestReadOnlySelect_NoWALEmitted`
      - `TestReadWriteInsert_EmitsCommitRecord`
      - `TestMixedTxn_FirstWriteFlipsFlag`
      - `TestRollback_ReadOnlyNoAbortRecord`
      - `TestRollback_AfterWriteEmitsAbortRecord`
      - `TestOpportunisticPrune_FromSelectFlipsFlag`
      Crash-recovery integration test confirming no
      missing-WAL / torn-WAL errors after kill -9 mid-
      pgbench-S.

- [ ] **M0093-0003** — pgbench select-only re-measurement
      post-fix. Target: TPS ≥ 1,000 (M0091's acceptance
      bar). Secondary: walwriter flush rate during a
      60-s window < 100 (down from ~19,600). Capture
      pprof. Three back-to-back runs; report median.
      Method:
      `docs/design/0093-0002-pgbench-remeasurement-target.md`.

- [ ] **M0093-0004** — pgbench standard / simple-update
      re-measurement to confirm read-write commit emission
      is unchanged. No regression vs M0092 baseline.

### Note on prior `## pgbench select-only @ -c 10` section

The measurement immediately below this M0091 block is the
**reproducer that surfaced this milestone.** It establishes
the pre-fix baseline (350.89 TPS) against which the M0091
sub-milestones' improvements are measured.

## pgbench select-only @ -c 10 (post-M0090, 2026-05-11 12:13)

Spot measurement requested by the user: scale=100, `-c 10
-j 10 -T 180`, select-only workload, same goopg configuration
as the M0090 verification run (`shared_buffers=2560MB`,
`wal_buffers=100MB`, etc.). Run against the same scale-100
data dir from the M0090 verification.

Result file:
`bench/pgbench-compare/results/20260511_121306_goopg_select-only_c10.txt`.

| metric | value |
|---|---:|
| transactions | 63 169 |
| failed | 0 (0.000 %) |
| tps | **350.89** |
| latency avg | 28.50 ms |
| latency stddev | 11.85 ms |
| initial connection time | 6.09 ms |

Throughput drifted downward over the 180 s run (10 s sample:
383.8 TPS → 170 s sample: 313.2 TPS, final 180 s sample:
356.1 TPS — modest TPS decay observed). 0 failed transactions
throughout; pkey IndexScan + heap-fetch path is correctness-
clean under read-only contention.

Cross-reference: the M0090 verification's select-only at the
same scale but `-c 100 -j 100` yielded 386.50 TPS. At -c 10
the TPS is lower (350.89) because there are fewer concurrent
in-flight queries to saturate the CPU; latency per query is
~28 ms vs ~258 ms at -c 100 (10× less per-query queueing).
This is the expected concurrency / throughput trade-off
shape — no anomaly.

## M0094 — Replication E2E Completion & TAP Test Porting (D-003 / D-004)

Milestone doc: `docs/milestones/0094-replication-e2e-and-tap-test-porting.md`

Background: M0005 (streaming replication) and M0008 (logical replication) are
substantially complete but two E2E tests remain hard-skipped. M0094 closes the
remaining gaps and ports a prioritised subset of the D-003 recovery TAP suite
(6 tests) and D-004 subscription TAP suite (3 tests).

### Sub-milestones

- [x] **M0094-0001** — Design doc `0094-0001-streaming-replication-e2e-gap.md`
      status → `accepted` (2026-05-11). Added `PreCloneHook func(*cluster.Cluster) error`
      to `replcluster.Options`; wired in `Setup()` after primary start, before
      standby clone. WAL `ApplyRecord` audit: all record kinds already handled
      (BtreeInsert, BtreeSplit, HeapVacuum all have replay functions — no gaps).
      Un-skipped `TestE2E_PhysicalReplication` with a hook that creates `repl_t (id int)`
      before clone, inserts a row on primary, waits, queries standby.
      Key files: `internal/testutil/replcluster/replcluster.go`,
      `internal/testport/e2e_replication_test.go`.

- [x] **M0094-0002** — Design doc `0094-0002-logical-apply-delete-update.md`
      status → `accepted` (2026-05-11). Extended `RecordKindHeapDelete` WAL
      format to carry old-tuple bytes (optional); extended `LogHeapDeleteFunc`
      hook signature; executor DELETE/UPDATE paths capture pre-delete tuple.
      Classifier populates `Change.OldTuple`. `ReorderBuffer.Commit()` folds
      consecutive `(Delete, Insert)` pairs on same rel → `ChangeUpdate`. pgoutput
      encoder emits `'D'` with 'O' old-tuple body when OldTuple is non-empty;
      emits new `'U'` message for `ChangeUpdate`. Decoder added `'U'` parsing.
      `applyDelete()` and `applyUpdate()` implemented in `applyworker.go`
      via key-tuple heap scan + xmax stamp. `TestE2E_LogicalReplication` un-skipped
      and passes (INSERT + DELETE + UPDATE end-to-end). Unit tests:
      `TestReorderFoldDeleteInsertToUpdate`, `TestReorderFoldDoesNotFoldDifferentRels`,
      `TestPgoutputUpdateMessageEncoding`, `TestPgoutputDeleteWithOldTupleEmitsO`.

- [ ] **M0094-0003** — Design doc `0094-0003-recovery-tap-porting-strategy.md`
      status → `accepted`. Create `internal/testport/recovery_port_test.go`.
      Port 6 recovery TAP tests:
      - `TestPort_Recovery001StreamRep` (001_stream_rep.pl)
      - `TestPort_Recovery013CrashRestart` (013_crash_restart.pl)
      - `TestPort_Recovery019ReplslotLimit` (019_replslot_limit.pl)
      - `TestPort_Recovery038SaveLogicalSlots` (038_save_logical_slots_shutdown.pl)
      - `TestPort_Recovery039EndOfWal` (039_end_of_wal.pl)
      - `TestPort_Recovery047CheckpointPhysicalSlot` (047_checkpoint_physical_slot.pl)
      Add 6 rows to `docs/test-port/postgres-oracle-port-status.csv`
      (`status=port, pass_required=yes`). Regenerate `.md` via
      `go run ./cmd/gen-oracle-port-status`. All 6 tests pass.

- [ ] **M0094-0004** — Design doc `0094-0004-subscription-tap-porting-strategy.md`
      status → `accepted`. Create `internal/testport/subscription_port_test.go`.
      Port 3 subscription TAP tests:
      - `TestPort_Subscription001RepChanges` (001_rep_changes.pl)
      - `TestPort_Subscription004Sync` (004_sync.pl)
      - `TestPort_Subscription026Stats` (026_stats.pl)
      Add 3 rows to `docs/test-port/postgres-oracle-port-status.csv`.
      Regenerate `.md`. All 3 tests pass.

- [ ] **M0094-0005** — Verify M0005 and M0008 DoD checklists against current
      codebase. Update `docs/milestones/0005-streaming-replication-support.md`
      status → `complete`. Update `docs/milestones/0008-logical-replication-support.md`
      status → `complete`. `make ralph-state-guard` passes.

## Notes

- This file is the authoritative TODO list for Ralph. Update it after every
  meaningful change.
- Keep work to ONE item per loop. Decompose further if an item is larger
  than what fits in a single agent invocation.
- Every non-trivial subsystem must land alongside (or just before) a design
  doc under `docs/design/`. The spec treats this as a hard requirement.

## Maintenance Fixes

- [x] Fix `TestFoundationSeqScanFilterJoin` test 7 stale expectation (2026-05-04).
      rows[0][0] was expected to be "alpha" but alpha's t3.qty=100 is filtered
      by WHERE t3.qty>150; correct first row is [beta 200]. Stale from before
      M0039/M0041 fixed ColumnRef alignment for ≥3-table joins. Row-count check
      promoted from t.Logf to t.Fatalf. File: `internal/testutil/tpch/foundation_test.go`.

- [x] Silence `tmp/` build errors under `go test ./...` (2026-05-04).
      tmp/ utility scripts (find_wal_record.go, tuple_size.go, walprobe_main.go)
      all declared `package main`, causing "main redeclared" errors. Added
      `//go:build ignore` to each. (Note: tmp/ is in .gitignore; change is local.)

## Completed

- [x] Project initialization (Ralph harness wired up).
