# M0149 — Recovery/subscription TAP port with prerequisite implementation

**Filed:** 2026-11-15 (owner decision)
**Status:** planned — banner item 10, after the already-filed M0122 queue and
**immediately before M0148**. M0148 is paused while M0149 has a selectable
task; the M0148-0001c3a10 state recorded in the baton/fix_plan is resumed
when M0148 becomes selectable again. **M0149 tasks and all their descendants inherit item-10 rank — the S7
correctness-only carve-out does not demote them.**
**Coverage authority:** `docs/test-port/postgres-oracle-target-inventory.csv`
(suites `recovery-tap`, `subscription-tap`)
**Readable roll-up:** `docs/test-port/upstream-tap-coverage.md`
**Oracle:** PostgreSQL 18.3 — source under `./postgres/` (read-only), binaries
under `./postgres/local_install/bin`

## Goal

Port every upstream TAP test under `postgres/src/test/recovery/t/` and
`postgres/src/test/subscription/t/` that is not yet `port` in the inventory —
74 tests at filing (41 recovery, 33 subscription) — so that each runs as a
must-pass `TestPort_*` Go test against goopg **and against real PostgreSQL
18.3 peers**. When a test needs a feature goopg lacks, the test's task
implements that feature first, as an implementation child of the test task.
Discovery of a gap is never completion.

goopg's claim is drop-in PostgreSQL compatibility. For replication and
recovery that claim is only true if a goopg node can stand in for a PG 18.3
node in any position of a replication topology and if every file goopg writes
can be read by PG 18.3 and its tools. M0149 makes the upstream recovery and
subscription suites the proof of that claim.

## Compatibility contract (binding — read before every M0149 task)

**A port that only works goopg↔goopg, or that passes because goopg reads and
writes a goopg-private format, does NOT count as ported.** The inventory row
stays non-`port` and the task stays open until the contract below holds for
everything the upstream test exercises.

### C1. Wire protocol — byte-compatible with PG 18.3

- Frontend/backend protocol 3.0 including the `replication=true` /
  `replication=database` startup parameter, and the walsender command
  grammar of `src/backend/replication/repl_gram.y`: `IDENTIFY_SYSTEM`,
  `SHOW`, `TIMELINE_HISTORY`, `CREATE_REPLICATION_SLOT` (physical/logical,
  `TEMPORARY`, `RESERVE_WAL`, `SNAPSHOT`, `TWO_PHASE`, `FAILOVER`),
  `ALTER_REPLICATION_SLOT`, `READ_REPLICATION_SLOT`, `DROP_REPLICATION_SLOT
  [WAIT]`, `START_REPLICATION` (physical with `TIMELINE`, logical with
  plugin options), `BASE_BACKUP` (all PG 18 options, incl. `MANIFEST`,
  `TARGET`, `INCREMENTAL`) and `UPLOAD_MANIFEST` — same result-set shapes,
  column types, and error SQLSTATEs.
- CopyBoth streaming messages: XLogData (`w`), primary keepalive (`k`),
  standby status update (`r`), hot-standby feedback (`h`), and the
  end-of-timeline exchange (CopyDone + next-TLI result set).
- pgoutput, protocol versions 1–4 (`src/backend/replication/pgoutput/`,
  `src/backend/replication/logical/proto.c`): Begin, Commit, Origin,
  Relation, Type, Insert, Update, Delete, Truncate, Message, Stream
  Start/Stop/Commit/Abort (v4 abort LSN/timestamp), Begin Prepare, Prepare,
  Commit Prepared, Rollback Prepared, Stream Prepare; options
  `proto_version`, `publication_names`, `binary`, `messages`, `streaming`
  (`off`/`on`/`parallel`), `two_phase`, `origin`; tuple column kinds
  `n`/`u`/`t`/`b`.
- `test_decoding` output: byte-identical text to
  `contrib/test_decoding/test_decoding.c` for every option the tests use.
- SQL-level replication interface (`pg_logical_slot_*_changes`,
  `pg_replication_slot_advance`, `pg_create_*_replication_slot`,
  `pg_backup_start/stop`, `pg_promote`, `pg_wal_replay_*`, the
  `pg_stat_replication` / `pg_stat_wal_receiver` / `pg_replication_slots` /
  `pg_stat_subscription*` / `pg_stat_archiver` views): same columns,
  values, and SQLSTATEs.
- Server log lines and error texts that an upstream test matches
  (`wait_for_log`, `log_contains`, `like($stderr, …)`) are matched against
  the **PG 18.3 text**, not a goopg paraphrase.

### C2. Files — every file goopg creates is the PG 18.3 format

- `pg_wal/` segments: long/short page headers, `XLogRecord` + block
  references, rmgr ids and `xl_info` values, record payloads, full-page
  images, continuation records, `XLP_FIRST_IS_OVERWRITE_CONTRECORD` /
  `XLOG_OVERWRITE_CONTRECORD`, segment naming, `.partial` segments.
- `pg_wal/<TLI>.history` timeline history files.
- `pg_wal/archive_status/*.ready|*.done` lifecycle, and archived file
  content/naming as produced by `archive_command`.
- `backup_label`, `tablespace_map`, `backup_manifest` (verifiable by PG
  18.3 `pg_verifybackup`), `pg_wal/summaries/` WAL summaries.
- `global/pg_control` (incl. `minRecoveryPoint`, `backupEndRequired`,
  state) — readable by PG 18.3 `pg_controldata`.
- Signal files exactly as PG names and interprets them: `standby.signal`,
  `recovery.signal`, and the promote trigger `promote` (`PROMOTE_SIGNAL_FILE`
  in `src/include/access/xlog.h`). Example of a current deviation to
  verify: `internal/initdb/standby.go` defines `PromoteSignalFile =
  "promote.signal"`.
- `postgresql.auto.conf` / `primary_conninfo` as written by PG 18.3
  `pg_basebackup -R`.
- Replication-slot state `pg_replslot/<slot>/state`
  (`ReplicationSlotOnDisk`: magic, version, checksum), logical spill files
  `pg_replslot/<slot>/xid-*.spill`, `pg_logical/snapshots/*.snap`,
  `pg_logical/mappings/`, `pg_logical/replorigin_checkpoint`.
- `pg_twophase/` state files, SLRUs (`pg_xact`, `pg_subtrans`,
  `pg_multixact`), relation forks (`_init`, `_fsm`, `_vm`), `pg_tblspc/`
  links and in-place tablespace directories, `pgsql_tmp` temp-file naming,
  `pg_stat/pgstat.stat`, `postmaster.pid` line layout.

### C3. Mixed-topology verification against real PG 18.3

Every ported test runs its upstream topology all-goopg **and** at least one
mixed variant using PG 18.3 nodes from `postgres/local_install/bin`
(`internal/testutil/pgcluster`, `internal/testutil/pubsubcluster`; earlier
heterogeneous precedents: `e2e_failover_goopg_to_pg_test.go`,
`e2e_pg183_standby_full_cycle_test.go`,
`e2e_logical_message_pg_recvlogical_test.go`):

- Physical tests (≥2 nodes): goopg primary → PG 18.3 standby **and**
  PG 18.3 primary → goopg standby, including through promotion, archive
  restore, and timeline switches where the test has them; for cascades,
  each goopg position is covered at least once with a PG neighbour.
- Logical/subscription tests: goopg publisher → PG 18.3 subscriber **and**
  PG 18.3 publisher → goopg subscriber.
- Single-node tests: artifact cross-check — PG 18.3 tools
  (`pg_waldump`, `pg_controldata`, `pg_verifybackup`, `pg_archivecleanup`,
  `pg_receivewal`, `pg_recvlogical`, `pg_basebackup`) consume what goopg
  produced during the test, and where the test crash-recovers or restarts,
  a PG 18.3 server started on a copy of the goopg data directory reaches the
  same observable state (precedent: `e2e_pg_crashstart_on_goopgdata_test.go`).
  This overlaps M0131 (bidirectional cluster-directory cold start): a gap
  found here that M0131 already owns is referenced, not duplicated (see
  task model item 4).
- Client tools the upstream test invokes (`pg_basebackup`, `pg_recvlogical`,
  `pg_receivewal`, `pg_ctl`, `pg_resetwal`, …) are the PG 18.3 binaries,
  never goopg re-implementations, wherever they act on a goopg node.
- A step that cannot be mixed **by upstream construction** is listed with
  its reason in the task's design doc; all other steps still run mixed.
  - `injection_points`: the `local_install` PG build has no
    `USE_INJECTION_POINTS` (not configured with
    `--enable-injection-points`), so injection-point steps **cannot** run on
    the PG node — they run on goopg nodes only.
  - A PG-side contrib module that `local_install` lacks (e.g.
    `test_decoding`) is built out-of-tree into a private prefix
    (e.g. `tmp/pg183-extra/`) — **never into `./postgres/`** — and loaded by
    pointing the PG node's `dynamic_library_path` GUC at it (plus PG 18's
    `extension_control_path` when the test runs `CREATE EXTENSION`). If that
    is impossible, escalate to the owner instead of dropping the mixed
    variant.

### C4. Port fidelity

- The port is a Go transcription of the `.pl` (in `internal/testport/`,
  `TestPort_Recovery0NN<Name>` / `TestPort_Subscription0NN<Name>`), keeping
  **every** upstream assertion (`ok`/`is`/`like`/`cmp_ok`/`command_*`) with
  the same intent and expected value; a comment cites the `.pl` line of
  each. Upstream `skip_all` conditions (ICU, injection points, platform) are
  mirrored, never broadened. **A mirrored skip condition must evaluate false
  in the all-goopg run; a test that skips there is not ported.**
- Adaptations of the kind used by earlier ports — driving pgoutput
  in-process instead of over a subscription connection, substituting a
  whole-server SIGKILL for a backend crash-restart, dropping an injection
  point step — are **not** ports under M0149. They mark a missing feature,
  which becomes an implementation child.
- **Architecture escape hatch.** A step that requires a change to goopg's
  process model (per-backend OS processes, SysV shared memory) is escalated
  as `[!] blocked-on owner: architecture` on a child, with the design doc's
  analysis; the loop does not substitute and does not keep filing.
- **Never `kill()` a goopg "backend pid".** `pg_backend_pid()` on goopg is
  a synthetic per-connection number (`internal/executor/expr.go`), not an
  OS PID. Upstream steps that `pg_ctl kill` / `kill` a backend PID
  (`017_shm`, `022_crash_temp_files`, `041_checkpoint_at_promote`) fall
  under the escape hatch above; SQL-level uses of the value
  (`pg_cancel_backend`, `pg_stat_activity.pid` filters, as in
  `037_invalid_database` and `048_vacuum_horizon_floor`) are fine.
- The PG-`Cluster.pm`/`Utils.pm` surface the test needs (`init(allows_streaming,
  has_archiving)`, `backup`, `backup_fs_cold`, `init_from_backup(has_streaming,
  has_restoring)`, `enable_streaming`, `promote`, `wait_for_catchup`,
  `wait_for_replay_catchup`, `wait_for_slot_catchup`, `poll_query_until`,
  `wait_for_log`, `log_contains`, `background_psql`, `append_conf`, `kill9`,
  `stop('immediate')`, `lsn`, …) is added to the Go harness
  (`internal/testutil/replcluster`, `pgcluster`, `pubsubcluster`, or a shared
  successor) as an implementation child of the first test that needs each
  method, working identically for goopg and PG nodes.
- Servers run under the cgroup memory cap (`scripts/goopg-test-run.sh`
  conventions; `testing.Short()` skip as in existing ports).

## Task model

1. **One task per upstream test.** `M0149-0001` … `M0149-0074` in
   `.ralph/fix_plan.md`, in the canonical queue order below, each
   `Kind: impl`, `Parent: none`.
2. **Start of a task.** Read the `.pl` end to end and the PG 18.3 code it
   exercises; verify the entry's "Likely prerequisites" hint at HEAD (it is
   a filing-time hint, not a finding); run whatever of the test is already
   portable to get a baseline.
3. **Prerequisites are children.** Each missing or non-PG-compatible feature
   is filed immediately as `M0149-00NNa`, `M0149-00NNb`, … (`Kind: impl`,
   `Parent: M0149-00NN`), naming the PG 18.3 oracle files/functions and the
   focused + TAP verification that proves it. Grandchildren
   (`M0149-00NNa1`, …) follow the same rule. **File each child immediately
   below its parent entry.** A test task with an open descendant is not
   selected itself: take its first open descendant in file order
   (depth-first). The parent is re-selected for its close-out run once all
   descendants are `[x]`.
4. **No duplicates across milestones.** If a prerequisite is already an
   open task elsewhere, the child references it (`Prerequisite: <id>`) and
   adopts or waits on it; do not file a duplicate. Known overlaps at filing:
   M0122-0014 (logical replication / decoding / subscription — the
   subscription tiers), M0131 (PG cold start on a goopg data directory —
   C3's single-node cross-check), and the M0122-0009a/b/c findings
   (REINDEX CONCURRENTLY / SET TABLESPACE writing relation files without
   WAL; VACUUM tail truncation emitting no `XLOG_SMGR_TRUNCATE`). The
   deferral ledger (2026-10-13 row) says M0122-0009a/b/c were filed, but
   **no such entries exist in `.ralph/fix_plan.md`** at M0149 filing — if
   still absent when a task needs them, file them as that task's children.
5. **No ledger-only completion.** A deferral-ledger row, sizing result,
   `PARKED` label, or "subsystem missing" statement never completes or parks
   a test task or a child (same rule as M0148). A genuine owner/external
   blocker is recorded with an explicit `[!] blocked-on <reason>` on an open
   child; selection resumes it when the blocker clears.
6. **Completion.** A test task closes only when its `TestPort_*` func passes
   in the all-goopg topology and in every mixed variant required by C3, with
   all upstream assertions present, stable over 3 consecutive runs, and the
   normal gates for the code touched (units pre-commit bar; planner/executor
   gates when those packages change).
7. **Loop granularity.** A test task normally spans many loops; each loop
   works exactly one task (the parent or one descendant).
8. **Movement instrument: `tap`.** Every M0149 task (parent or descendant)
   reports Movement as
   `Movement: yes — tap <test> assertions <before>/<total>→<after>/<total>`
   — upstream assertions passing across the all-goopg and mixed runs, e.g.
   `Movement: yes — tap 034_create_database 3/12→7/12`; `Movement: none`
   when the count is unchanged.

## Inventory update rule (binding)

In the **same commit** that lands a passing port:

1. In `docs/test-port/postgres-oracle-target-inventory.csv`, edit the test's
   row: `status` → `port`, `pass_required` → `yes`, `rationale` →
   `Ported as TestPort_<Func> in internal/testport/<file>.go` (plus the mixed
   variants covered), `deferred_to` = `-`. Give the row its id: `R-<NNN>`
   (recovery) / `S-<NNN>` (subscription), where `<NNN>` is the upstream
   file's 3-digit number (e.g. `R-034`, `S-100`).
2. Run `make regen-testport` (regenerates
   `docs/test-port/upstream-tap-coverage.md` and the other derived docs).
3. Run `make check-testport-inventory`.
4. If the test appears in `ci/batch/expected-failures.csv`, delete its row.

A port whose row is not promoted is not complete; a promoted row whose test
violates C1–C3 is a defect.

## Design-doc rule

The design doc for a test task is created **when the task is started** —
in the first loop that selects it — not up front:
`docs/design/0100-0149/0149-00NN-<slug>.md` (children:
`0149-00NNa-<slug>.md` when non-trivial), indexed in `docs/design/README.md`
in the same commit. It records the upstream test's assertion map, the
prerequisite children with PG 18.3 source citations, the mixed-topology
variants, and any C3 non-mixable step with its reason. It is updated as
children land. No Required-Design-Docs list is kept for M0149.

## Canonical queue

Order is dependency-driven: single-node recovery and file formats first,
then physical streaming, archiving/PITR, timelines, hot-standby conflicts,
then logical decoding on the recovery side, then subscriptions (basic apply
→ filtering/permissions/origin → streaming → two-phase), and finally the
cross-cutting tests. Earlier tiers build the substrate (harness methods,
archiver, timelines, `test_decoding`, `injection_points`, wire subscription
apply) that later tiers reuse. IDs are assigned in this order and never
renumbered.

**Tier 1 — single-node crash recovery and on-disk formats**

1. `M0149-0001` — `recovery/t/034_create_database.pl`
2. `M0149-0002` — `recovery/t/036_truncated_dropped.pl`
3. `M0149-0003` — `recovery/t/014_unlogged_reinit.pl`
4. `M0149-0004` — `recovery/t/022_crash_temp_files.pl`
5. `M0149-0005` — `recovery/t/037_invalid_database.pl`
6. `M0149-0006` — `recovery/t/018_wal_optimize.pl`
7. `M0149-0007` — `recovery/t/029_stats_restart.pl`
8. `M0149-0008` — `recovery/t/017_shm.pl`

**Tier 2 — physical streaming standby, promotion, replay correctness**

9. `M0149-0009` — `recovery/t/008_fsm_truncation.pl`
10. `M0149-0010` — `recovery/t/015_promotion_pages.pl`
11. `M0149-0011` — `recovery/t/016_min_consistency.pl`
12. `M0149-0012` — `recovery/t/021_row_visibility.pl`
13. `M0149-0013` — `recovery/t/005_replay_delay.pl`
14. `M0149-0014` — `recovery/t/007_sync_rep.pl`
15. `M0149-0015` — `recovery/t/012_subtransactions.pl`
16. `M0149-0016` — `recovery/t/009_twophase.pl`
17. `M0149-0017` — `recovery/t/026_overwrite_contrecord.pl`

**Tier 3 — WAL archiving, archive recovery, PITR**

18. `M0149-0018` — `recovery/t/002_archiving.pl`
19. `M0149-0019` — `recovery/t/020_archive_status.pl`
20. `M0149-0020` — `recovery/t/024_archive_recovery.pl`
21. `M0149-0021` — `recovery/t/042_low_level_backup.pl`
22. `M0149-0022` — `recovery/t/045_archive_restartpoint.pl`
23. `M0149-0023` — `recovery/t/003_recovery_targets.pl`
24. `M0149-0024` — `recovery/t/023_pitr_prepared_xact.pl`

**Tier 4 — timelines**

25. `M0149-0025` — `recovery/t/004_timeline_switch.pl`
26. `M0149-0026` — `recovery/t/025_stuck_on_old_timeline.pl`
27. `M0149-0027` — `recovery/t/028_pitr_timelines.pl`
28. `M0149-0028` — `recovery/t/043_no_contrecord_switch.pl`

**Tier 5 — hot-standby conflicts, storage/catalog replay, standby stats,
injection points**

29. `M0149-0029` — `recovery/t/032_relfilenode_reuse.pl`
30. `M0149-0030` — `recovery/t/033_replay_tsp_drops.pl`
31. `M0149-0031` — `recovery/t/031_recovery_conflict.pl`
32. `M0149-0032` — `recovery/t/030_stats_cleanup_replica.pl`
33. `M0149-0033` — `recovery/t/048_vacuum_horizon_floor.pl`
34. `M0149-0034` — `recovery/t/041_checkpoint_at_promote.pl`

**Tier 6 — logical decoding (SQL interface, `test_decoding`,
`pg_recvlogical`, slots on primary and standby)**

35. `M0149-0035` — `recovery/t/006_logical_decoding.pl`
36. `M0149-0036` — `recovery/t/010_logical_decoding_timelines.pl`
37. `M0149-0037` — `recovery/t/044_invalidate_inactive_slots.pl`
38. `M0149-0038` — `recovery/t/046_checkpoint_logical_slot.pl`
39. `M0149-0039` — `recovery/t/035_standby_logical_decoding.pl`

**Tier 7 — subscription apply over the wire (basics)**

40. `M0149-0040` — `subscription/t/003_constraints.pl`
41. `M0149-0041` — `subscription/t/005_encoding.pl`
42. `M0149-0042` — `subscription/t/006_rewrite.pl`
43. `M0149-0043` — `subscription/t/007_ddl.pl`
44. `M0149-0044` — `subscription/t/008_diff_schema.pl`
45. `M0149-0045` — `subscription/t/009_matviews.pl`
46. `M0149-0046` — `subscription/t/002_types.pl`
47. `M0149-0047` — `subscription/t/010_truncate.pl`
48. `M0149-0048` — `subscription/t/011_generated.pl`
49. `M0149-0049` — `subscription/t/012_collation.pl`
50. `M0149-0050` — `subscription/t/014_binary.pl`
51. `M0149-0051` — `subscription/t/020_messages.pl`
52. `M0149-0052` — `subscription/t/024_add_drop_pub.pl`
53. `M0149-0053` — `subscription/t/025_rep_changes_for_schema.pl`
54. `M0149-0054` — `subscription/t/032_subscribe_use_index.pl`
55. `M0149-0055` — `subscription/t/034_temporal.pl`
56. `M0149-0056` — `subscription/t/035_conflicts.pl`

**Tier 8 — permissions, row filters, column lists, partitions, origins**

57. `M0149-0057` — `subscription/t/027_nosuperuser.pl`
58. `M0149-0058` — `subscription/t/033_run_as_table_owner.pl`
59. `M0149-0059` — `subscription/t/028_row_filter.pl`
60. `M0149-0060` — `subscription/t/031_column_list.pl`
61. `M0149-0061` — `subscription/t/013_partition.pl`
62. `M0149-0062` — `subscription/t/030_origin.pl`

**Tier 9 — streaming of in-progress transactions (incl. parallel apply)**

63. `M0149-0063` — `subscription/t/015_stream.pl`
64. `M0149-0064` — `subscription/t/016_stream_subxact.pl`
65. `M0149-0065` — `subscription/t/017_stream_ddl.pl`
66. `M0149-0066` — `subscription/t/018_stream_subxact_abort.pl`
67. `M0149-0067` — `subscription/t/019_stream_subxact_ddl_abort.pl`

**Tier 10 — two-phase logical replication and apply-error handling**

68. `M0149-0068` — `subscription/t/021_twophase.pl`
69. `M0149-0069` — `subscription/t/022_twophase_cascade.pl`
70. `M0149-0070` — `subscription/t/023_twophase_stream.pl`
71. `M0149-0071` — `subscription/t/029_on_error.pl`

**Tier 11 — cross-cutting finale**

72. `M0149-0072` — `recovery/t/040_standby_failover_slots_sync.pl`
    (needs physical standby + logical slots + subscriptions + two-phase)
73. `M0149-0073` — `subscription/t/100_bugs.pl` (grab-bag of historical
    regressions across all of the above)
74. `M0149-0074` — `recovery/t/027_stream_regress.pl` (runs the whole
    regress schedule on a streaming primary and compares `pg_dumpall` of
    primary and standby; its pass condition depends on M0148's regress
    parity — if reached before M0148 is accepted, the replication-side
    children are still implemented, then the task is marked
    `[!] blocked-on M0148 regress parity` as an explicit cross-milestone
    blocker, not a ledger-only park)

All paths above are relative to `postgres/src/test/`.

## Out of scope / known gap

The nine rows already `port` at filing (recovery 001, 013, 019, 038, 039,
047; subscription 001, 004, 026) are not M0149 tasks. Several of them are
adaptations that would not satisfy C3/C4 (the subscription ports drive
pgoutput in-process; `013` substitutes a whole-server SIGKILL for a backend
crash-restart; `047` omits its injection point). M0149 does not reopen them
without an owner decision; when an M0149 child lands the missing feature
(e.g. `injection_points`, wire-level subscription apply), the child's design
doc notes which of these ports could now be made faithful.

## Acceptance

M0149 is complete when all 74 tests above are `port` / `pass_required=yes`
in the inventory, each satisfying the compatibility contract (all-goopg and
mixed PG 18.3 variants passing, all upstream assertions present),
`make check-testport-inventory` succeeds, and the regenerated
`docs/test-port/upstream-tap-coverage.md` shows no `not-tried` row under
`postgres/src/test/recovery/t/` or `postgres/src/test/subscription/t/`.
