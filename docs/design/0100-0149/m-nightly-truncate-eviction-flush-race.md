# M-NIGHTLY — TRUNCATE racing a dirty-page eviction flush (AI-20261031-002109-005)

**Landed 2026-10-31.** Nightly pgbench stage: `ERROR: flush victim: write …/base/5/16406 blk 2681: out of range (nblocks=0)` aborts a client on the `-S` workload; the pgbench `history` relation's file was caught at **0 bytes** mid-run.

## Symptom

`ci/batch/stages/stage-pgbench.sh` (s=50, c=100, 180 s × 3 workloads) failed twice in a row (2026-10-30/31 nightlies). One client aborts with

```
ERROR:  flush victim: write …/base/5/16406 blk 2681: out of range (nblocks=0)
```

`16406` is `pgbench_history`. A file watcher caught the history file at 0 bytes at the abort moment while `pgbench_accounts` was intact; the server's autovacuum log showed the history page count shrinking toward zero at each workload boundary.

## Root cause

pgbench runs `truncate pgbench_history` at the start of every workload (`postgres/src/bin/pgbench/pgbench.c:7375`, `tryExecuteStatement(con, "truncate pgbench_history")`) — legitimate. goopg's TRUNCATE (`internal/executor/operators_ddl.go` `truncateTableAndPartitions`) does, in statement order: `Pool.InvalidateRel(rel)` → `Manager.TruncateRelation(rel)` (truncate file to 0, relFile `nblocks=0`).

The eviction path raced it:

1. `claimVictim` (bufpool.go) samples a **dirty** history page from the previous workload and CASes it to `ioInflight` — which **clears valid+dirty**.
2. `InvalidateRel`'s CAS targets `valid+unpinned` → misses the claimed slot; the dirty page is not marked discarded.
3. `evictVictim` then flushes: `relFile.writeBlock(blk)` with `blk >= nblocks(0)` → `out of range (nblocks=0)` (smgr.go:1061) → surfaces as `flush victim: …` (bufpool.go pinNewXID/pinLoad, prefetch.go) and aborts the next client statement.

PostgreSQL cannot reach this: `TRUNCATE` (AccessExclusiveLock) calls `DropRelationBuffers` (`postgres/src/backend/storage/buffer/bufmgr.c:4533`), whose own comment states the motivation — invalidation must not leave buffers "that the background writer or checkpointer can lead to a PANIC error while flushing buffers corresponding to files that don't exist". It takes the buffer content lock (which waits out in-progress I/O) and **discards the dirty page without writing it** — the data is being truncated, so flushing is pointless and dangerous.

## goopg fix (port of DropRelationBuffers semantics)

PG's lever is the content lock (waits for I/O, then discards). goopg's invalidation runs with no lock that the flush honors, and introducing one inverts lock order; the flush's lifecycle is already stamped by the **slot generation** that `claimVictim` bumps. The fix signals through it:

- `Pool.InvalidateRel` / `Pool.InvalidateBlock` (bufpool.go): for a slot of the invalidated relation in `ioInflight` state, `bumpSlotGen` bumps the slot's generation (CAS). This is the "discard the dirty page" signal for the in-flight flusher.
- `Pool.evictVictim` (bufpool.go): takes the new `claimGen` from `claimVictim` and re-checks the slot generation
  1. **pre-write**, inside `contentMu` (after `OnFlushWait`, before `flushSlot`): on mismatch the flush is **skipped** and the slot retired like a clean eviction (bufmap delete, tombstone) — mirrors DropRelationBuffers discarding;
  2. **post-failure**: if `flushSlot` fails and the generation has since been bumped, the write raced a truncate that landed first — the page is discarded, the statement succeeds. A flush failure with an unchanged generation is a real defect and propagates unchanged.
  The post-failure check closes the residual window (bump after the pre-write check, truncate's `r.mu` before the write's).
- `Pool.flushBatch` (checkpoint): a batch assembled from a dirty scan now re-checks `valid+dirty` under `contentMu` and skips slots invalidated in the gap — PG's checkpoint never writes buffers `DropRelationBuffers` discarded.

With these, every interleaving of `InvalidateRel`/truncate and eviction flush ends with the stale page discarded (pre-write check), discarded (post-failure check), written before the truncate (statement order: invalidate precedes smgr truncate, so the write hits a file that still has the block), or skipped by the batch re-check.

The bgwriter path already re-checks `valid+dirty+tag` under `contentMu.RLock` before flushing and ignores failures; combined with statement ordering it cannot produce the client-visible error.

## Evidence

- Deterministic repro (full nightly sequence, 2026-10-31): history file 0 bytes at the abort; autovacuum/analyze page-count timeline shrinking 1134→853→0 pages at workload boundaries.
- Stack instrumentation on `truncateToZero` (debug build, removed): the truncates of `base/5/16406` during workloads come from `execTruncate` ← pgbench's per-run `truncate pgbench_history`.
- Mutation check: removing the pre-write gen check reproduces `flush victim: write …: out of range (nblocks=0)` in `TestTruncateDuringEvictionFlushDiscardsStalePage`; the fix passes.

## Deviations (recorded)

- **Generation signal vs content lock.** PG serializes invalidation with I/O through the buffer content lock; goopg's eviction releases `pinMu` for the flush and no pool lock spans it, so invalidation signals via the generation stamp that `claimVictim` bumps. Outcome parity (stale page never lands after the truncate's new end; client never aborts), different mechanism.
- **Post-failure discard** has no direct PG analog (PG's write cannot fail this way) — it is the closure of the window between the pre-write check and `r.mu`.

## Tests

`internal/storage/truncate_invalidate_flush_test.go`:
- `TestTruncateDuringEvictionFlushDiscardsStalePage` — drives the exact interleaving via `OnFlushWait` (invalidate + truncate parked inside the victim flush); asserts the pin succeeds, the file stays 0 bytes (page not written), and the bufmap entries are gone.
- `TestInvalidateRelBumpsGenerationOfClaimedSlot` — the signaling half.
- `TestFlushBatchSkipsInvalidatedSlots` — checkpoint batch skips invalidated slots.

## Gates

`go test ./internal/storage/ ./internal/commands/vacuum/`; `go test -race ./internal/storage/`; units (precommit scope=units); TPC-H spot-check Q12=2 Q13=35; full `stage-pgbench.sh` repro; commit-hook pgbench smoke.

## Follow-up: 0-byte relfilenode files (filed 2026-10-31, same diagnosis run)

The same AI-20261031-002109-005 stack-instrumented pgbench diagnosis recorded
a second item: pgbench -i's opening
`drop table if exists pgbench_accounts, … cascade` executing via
`ddlOp.Next → execTruncate → truncateTableAndPartitions → Manager.TruncateRelation`
four times, synthesising relfilenodes 16406/16407/16409/16411, leaving four
0-byte files that the later CREATEs then reused.

### Verification (2026-10-31 loop)

The routing claim **does not reproduce at HEAD** (`6c62c820c`):
`execDropTable` never reaches the truncate executor — the missing-name arm
short-circuits with the PG-parity NOTICE (`table "m1" does not exist,
skipping`), consumes no OID (nextOID unchanged), and creates no file;
without IF EXISTS it raises 42P01. Verified at executor level (fixture)
and server level (fresh cluster, pgbench-style drop as the first user
statement: file inventory unchanged, first CREATE gets relfilenode
max+1 = 16406). The 0-byte files in the nightly data dir therefore come
from the two mechanisms the diagnosis *also* observed, which do reproduce:

### Landed fixes

- **smgr truncate never creates the file.** `Manager.relFile` opens with
  `O_RDWR|O_CREATE` + `MkdirAll` — correct for read/write paths, wrong for
  truncate: any truncate of a storage-less relation materialised a 0-byte
  file (and a database directory). PG's `mdtruncate`
  (`src/backend/storage/smgr/md.c:1291`) walks the relation's *already-open*
  segment descriptors: a relation with no open segments is a no-op.
  `TruncateRelation`/`TruncateRelationTo` now route through
  `relFileForTruncate`, which opens `O_RDWR` **without** `O_CREATE`, skips
  `MkdirAll`, and returns `opened=false` on `fs.ErrNotExist` → no-op.
- **TRUNCATE skips partitioned parents.** `truncateTableAndPartitions`
  now returns after the child recursion when `len(tbl.PartitionKey) > 0`,
  mirroring PG's ExecuteTruncateGuts "Skip partitioned tables as there is
  nothing to do" (`src/backend/commands/tablecmds.c:2148-2150`). goopg
  parents carry a synthesized relfilenode (PG: relfilenode 0, no file), so
  without the skip the parent-level undo snapshot, `InvalidateRel`,
  `TruncateRelation`, FSM/VM drop and index re-create all ran against the
  parent — creating/truncating a file PG never touches. `TRUNCATE ONLY p`
  on a partitioned parent already errors (operators_ddl.go, PG
  "cannot truncate only a partitioned table").

### Deviations (recorded)

- goopg partitioned parents keep a synthesized relfilenode and the
  0-byte file the CREATE path makes; PG has relfilenode 0 and no file at
  all. Full CREATE-time parity is ledger-deferred (see
  `.ralph/deferral_ledger.md`, 2026-10-31 rows), as are the
  VACUUM FULL/CLUSTER sibling truncate sites (operators_ddl.go ~21373,
  ~27745-27763, ~28067) and the read-path `relFile` O_CREATE policy.

### Tests

- `internal/storage/smgr_truncate_nocreate_test.go` — truncate of a
  missing file/missing db dir is a no-op (no file, no directory),
  truncate of an existing file still truncates (zero and N-block forms).
- `internal/executor/drop_truncate_nostorage_test.go` — DROP IF EXISTS
  arm: 4 notices, NextOID unmoved, file inventory unchanged, next CREATE
  next-in-sequence; 42P01 without IF EXISTS; DROP removes catalog row +
  file; TRUNCATE of a storage-less relation no-ops without creating the
  file; TRUNCATE on a partitioned parent leaves a materialised parent
  file untouched (1 block), truncates leaves to 0, parent file is not
  resurrected after removal.
- `internal/testport/drop_if_exists_no_storage_test.go` — server pin on
  a fresh cluster: pgbench-style DROP IF EXISTS changes no file
  (no user-range stubs), consumes no OIDs (next CREATE relfilenode =
  ref_before + 1).

### Gates

`go test ./internal/storage/ ./internal/executor/`; testport
`TestDropTableIfExistsMissingNoStorage`; units (precommit scope=units);
TPC-H spot-check; acceptance-arm; TPC-DS SF0.25 sweep; hook pgbench smoke.

## Follow-up 2: "relpages far above the block count" triage (filed 2026-10-31, recon loop #3)

The AI-20261031-002109-005 diagnosis also filed "autovacuum ANALYZE reports
relpages far above the relation's block count" (`analyze done
table=.pgbench_branches rows=50 pages=132` "for a 1-block file"). The recon
loop reproduced the nightly pgbench trio under instrumentation (launcher
tick gates untouched; analyzer print: nBlocks / manager-cached nblocks /
on-disk file size; vacuum print: scanned, visited, VM-skips, lastNonEmpty,
keep; smgr print: every TruncateRelation/To with before-count) and closed
the item as **not a defect**:

- Every ANALYZE's `pages` equalled the manager handle count AND the on-disk
  file block count at sample time (99/99/99 … 4003/4003/4003). A mid-workload
  `pages=132` is real dead-tuple bloat — PG's relpages reads the same.
- The "1-block file" side of the comparison is an observation-time artifact:
  pgbench's own lifecycle rebuilds the tables after those log lines
  (`-i` drop+create+COPY; `-S` init `TRUNCATE pgbench_history` — caught by
  the instrumentation as `RECON-TRUNC-ZERO rel=16406 cachedBefore=4003`),
  and pgbench's init `VACUUM ANALYZE` re-persists fresh stats (pg_class
  sampler showed `t0 p0 f0` after the -S init truncate: TRUNCATE zeroes
  `tbl.Stats` at operators_ddl.go:17179, PG parity per trunc.c:282-285
  vac_update_relstats(0,0)).
- The launcher's `vacuum done pages=` field counts VISITED pages (VM-skips
  uncounted: visits=1049 vs scanned=4003), which made the nightly vacuum
  lines look inconsistent with the analyze lines.
- Vacuum's tail truncation, VM-skip nonempty accounting (vacuumlazy.c
  heap_vac_scan_next_block port at vacuum.go:177-184), and the truncate
  decision were all measured PG-shaped; no live-data truncation observed
  (the first worry: it is correctly guarded).

Three real divergences surfaced by the same rigs and were filed as open
M-NIGHTLY tasks: COPY skips the pgstat row counters (probe: goopg
n_tup_ins=0/n_live_tup=0 vs PG 5000/5000); pgbench_branches reltuples=1
on an idle 10-row table (COPY/rewrite-then-update visibility undercount
family); post-TRUNCATE empty-relation cost (goopg 22.70/1270 vs PG
16.10/610, reltuples 0 vs -1). Repro rigs live in `tmp/mnightly-*.sh`.

### Gates

Repro rigs only (no code staged; instrumentation reverted before commit):
mini trio s=1 60s×3, trio s=50 120s×3, TRUNCATE cost probe vs PG temp
cluster, COPY probe vs PG temp cluster; census unchanged (REUSE).

## Follow-up 3: COPY feeds the pgstat row counters (filed 2026-10-31, loop #4)

The relpages triage above filed three divergences; the first worked was
"COPY skips the pgstat row counters": a 5000-row `\copy` left
`pg_stat_get_tuples_inserted`/`pg_stat_get_live_tuples` at 0 (PG: 5000), and
the COPY-loaded tables' autovacuum insert-growth triggers
(`needsVacuum` ins arm, `needsAnalyze` mod arm) never fired — pgbench `-i`
tables sat un-analyzed until update traffic fed the mod counter.

PG counts inserts inside the heap routines, not in copy.c:
`heap_insert` ends with `pgstat_count_heap_insert(relation, 1)`
(heapam.c:2251) and `heap_multi_insert` with
`pgstat_count_heap_insert(rel, ntuples)` (heapam.c:2702). goopg's COPY
executor writes through the same arms (`storeCopyRow` CIM_SINGLE,
`flushBatch` CIM_MULTI = the CopyMultiInsertBufferFlush twin) but never
called `recordRelInsert`, so the counters (staging/pending/shared tiers and
the direct-write autovacuum-trigger store) saw nothing.

The fix emits at exactly those two sites:

- `flushBatch`: one `recordRelInsert(ctx, tableOIDFromCatalog(plan.Table),
  len(batch))` per batch, after tuple placement succeeds — buffered rows
  count at flush like PG's CopyMultiInsertInfo buffers; a stream that dies
  mid-batch leaves the unflushed tail uncounted, as PG does;
- the CIM_SINGLE arm: one tuple per stored row.

Trigger-suppressed rows (BEFORE trigger `RETURN NULL`) never reach the
heap-write arm and are not counted (the M0146-0055 suppression rule; PG's
tuple never reaches `heap_insert`, so no count).

A second gap sat behind the emit (hard-won rule 2): the COPY wire handler
builds its `ectx` by hand and had never set `AdvisorySessionIdentity`, so
`sessionStatsID(ectx)` was 0 and COPY's folded counters sat in a bucket the
connection's own `pg_stat_force_next_flush()` (which flushes
`sessionStatsID(ctx)` of the observing statement) could not drain. Setting
`ectx.AdvisorySessionIdentity = sess` — the same `*misc.SessionRegistry`
dispatch wires for every other statement — lands COPY's counters where the
session's flush and the `pg_stat_get_*` getters read them (same class as
the M0119-0006 `GetSetting` wiring for the same hand-built context).

Semantics kept from the INSERT path (`recordRelInsert`): autocommit folds
staging into pending at record time; an explicit block stages until
COMMIT/ROLLBACK; the abort math counts attempted tuples, moves them to the
dead side, and emits no change events (`applyXactToPending` = PG's
AtEOXact_PgStat_Relations). COPY runs its own auto-commit transaction even
inside an open block (pre-existing, ledger'd), which matches PG reporting
COPY's counts at statement end.

Residuals (ledger 2026-10-31): the `pg_stat_*_tables` VIEW's
`n_tup_ins`/`n_live_tup` cells remain honest-0/reltuples renderers for
every DML class (M0122-0003 design; the getter layer the view is built on
is now exact for COPY); pending counters reach shared only via explicit
`pg_stat_force_next_flush()` (PG flushes at statement end); COPY into a
partitioned parent counts the parent, PG counts partitions.

Tests: `internal/executor/copy_pgstat_test.go` (multi/single/suppressed
arms; trigger + shared-tier deltas; commit/abort math twin) and
`internal/testport/copy_pgstat_server_test.go` (wire CopyIn over one
session; all four getters read 5000 after the flush). The testport pin
needs one connection for COPY + observing statements — `cluster.DSN()` was
added for that, and lib/pq's CopyIn lives on its driver.Stmt path
(`tx.Prepare(pq.CopyIn(...))` + row Execs + Close).

### Gates

units (`ralph-precommit-test.sh` units scope), `tpch-spotcheck.sh`
(Q12/Q13 canonical), acceptance arm vs `tmp/arm-off-20261030.txt`,
`tpcds-sf025-regression.sh` sweep, commit-hook pgbench smoke; pre-existing
postmaster package flakes (ledger 2026-10-30 M0122-0014 row) reproduced
identically at HEAD.

## Follow-up 4: reltuples=1 on pgbench_branches closed as not-a-defect (filed 2026-10-31, loop #5)

The second relpages-triage item claimed an analyzer/vacuum visibility
defect: "reltuples renders 1 on a 10-row pgbench_branches; launcher analyze
rows=1, vacuum live=1 of 10". The recon rig
(`tmp/mnightly-reltuples-rig.sh`, goopg :5617 vs a PG 18.3 temp cluster
:5618) showed the premise itself is false.

**pgbench scale semantics**: `pgbench -i -s 1` creates pgbench_branches
with exactly **1 row** (branches = scale, tellers = 10×scale,
accounts = 100000×scale, history = 0). `SELECT count(*)` = 1 on goopg
right after `-i`; the PG 18.3 oracle (stock `initdb` + stock `pgbench -i`)
renders the same branches=1 / tellers=10 / history=0 and reltuples=1 after
ANALYZE. Launcher `analyze rows=1`, `reltuples=1`, and vacuum `live=1,
dead=0` are honest numbers for a 1-row table; `dead=0` after ~14k tpcb
transactions matches HOT-chain pruning at insert (heap_prune keeps the
latest version of each HOT chain).

The bisection ladder (INSERT+ANALYZE / UPDATE 9 / COPY 10 / COPY+UPDATE 9
/ COPY+ALTER ADD PRIMARY KEY +UPDATE) agrees with PG 18.3 at every point
(count(*) and reltuples 10/10) — the COPY/rewrite/update
invisible-versions hypothesis is dead at SQL level; the loop #4 pin
(5000 visible after COPY) already covered the same ground from the
counters side. The filed "10 rows, 69 blocks" sampler line was run
conflation: `t=10` came from the SCALE=10 rig, `rows=1` logs from the
SCALE=1 rig (1 row — correct), and the probe's `branches t=1 p=4` line
was a default-SCALE=1 probe.

Rig-reuse note: pgbench 16+ DDL uses columns `bid`/`bbalance`/`filler`
(the old `branchid` spelling errors identically on PG 18.3 — not a goopg
divergence), so rig SQL must use the new names.

No code change; no ledger row (parity verified, nothing deferred). The
third relpages-triage item (post-TRUNCATE cost estimate) remains open.
