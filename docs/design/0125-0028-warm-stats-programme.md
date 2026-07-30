# 0125-0028 — The warm-statistics programme: ANALYZE scope, restart persistence, bench warm-up, and the planning line they unlock

Status: **-0028 LANDED (2026-07-30, execution record in §-0028a); -0029 LANDED (2026-07-30, execution record in §-0029a); -0030 … -0031 filed**
Date: 2026-07-30 (filed by the user's interactive session, per user directive)
Milestone: M0125
Tasks: `M0125-0028` (ANALYZE per-DB scope) → `M0125-0029` (stats survive restart,
every DB, every connection) → `M0125-0030` (bench clusters ANALYZE + CHECKPOINT)
→ `M0125-0031` (warm-stats planning: timeout elimination + optimization/stabilization;
**gated on the first three**).

## The directive (2026-07-30, verbatim intent)

1. Statistics must be restored across a server restart. **As an explicit
   exception, this does NOT have to be PG-spec-compatible** — a goopg-private
   mechanism is authorized where PG has no faithful home for the data.
2. `ANALYZE <table>` must stop erroring (today it raises 42P01 in any
   non-default database).
3. The TPC-H and TPC-DS cluster **build scripts** must ANALYZE each benchmark
   table and issue a CHECKPOINT; the **already-built clusters** get the same
   treatment once. From then on, every benchmark measurement may assume
   **warm statistics** as its premise.
4. (Unlocked only when 1–3 are done) Under the warm-stats premise: eliminate
   the TPC-DS goopg-only timeout class, and drive TPC-H / TPC-DS runtime
   reduction and planning stabilization.

This replaces the standing S-cold premise. Everything measured so far —
M0124-0001's SF=1 board, every SF0.5 gate run, M0125-0003's arms, the
timeout class itself — ran with `TableStats.RowCount == 0` on every relation:
never a measured statistic. What the planner saw instead depends on the arm —
before the relation-size fallback (and in every flag-OFF arm) the DP seeded
every table at rows=1; the fallback arms (C2, §D8's flag-ON gate, and the
default since `d4071df4`) got block-derived size guesses (0.37–1.01× of
truth), still with no column statistics. M0125-0003/-0005 built that fallback
precisely to cope with the no-stats state; this programme makes the state
itself exceptional instead of default.

## Current state (verified against HEAD 2026-07-30 — the five load-bearing facts)

| # | fact | where |
|---|---|---|
| a | **`ANALYZE <table>` resolves against the shared catalog, not the connection's database.** `expandAnalyzeTargets` calls `cat.LookupTable(name)` directly; SELECT's per-connection DB-scoped resolution is never consulted, so in db `tpch`, `SELECT count(*) FROM lineitem` works while `ANALYZE lineitem` raises 42P01. Reproduced 2026-07-27; also why HammerDB's final "GATHERING SCHEMA STATISTICS" step fails. | `internal/executor/operators_analyze.go:145` (`expandAnalyzeTargets`); ledger row `bench-reorg ANALYZE-scope` (2026-07-27) has the resume point |
| b | **Bare `ANALYZE;` is a silent no-op.** `targets()` returns `nil` for an empty target list — the comment admits "we don't support the catalog-wide form yet" while the docstring claims upstream parity. Measured ~4 ms vs ~18 s for one named SF=1 table. | `internal/executor/operators_analyze.go:169-177` |
| c | **Persistence exists but writes to the wrong database and loses the size.** `persistStatsToPGStatistic` (M0112, landed) writes per-column stats to the pg_statistic heap — but with `DBOid: catalog.DefaultDBOid` hardcoded, so per-DB tables (`tpch`, `tpcds`, `tpcds05`) route to the default DB's relfile. | `internal/executor/operators_analyze.go:184-211` |
| d | **Startup restore exists but restores columns only — `RowCount`/`Pages` stay 0 by design** (ledger `pq-P6`: pg_statistic has no reltuples slot). The consumers that decide join order — the bushy-DP seed, `tableRows`, the NLI cost gate — read `RowCount`, so a restart still leaves the planner size-blind even where the column stats came back. `loadStatisticsFromHeap` also reads only `cat.DBOID()`'s relfile. | `internal/initdb/open.go:3479` (`loadStatisticsFromHeap`); `internal/catalog/catalog.go` `TableStats` |
| e | **`pg_class` is VIRTUAL** — `reltuples` is rendered live from `t.Stats.RowCount` (`catalog.go:6977`), so there is no pg_class heap row to persist the count into. This is exactly where the directive's PG-compat waiver bites: PG stores reltuples in pg_class; goopg has no faithful place for it today. | memory of the pg_class-virtual/pg_attribute-heap split; `internal/catalog/catalog.go:6977` |

One more measured symptom, cause not yet pinned: **cross-connection
invisibility** — ANALYZE run in one connection did not make
`tbl.Stats.RowCount` visible to a different connection's planning (verified
2026-07-23 on the TPC-H SF=1 bench server, port 65433 — before the 2026-07-27
cluster rebuild; the record does not name the database, so whether the
symptom is per-DB-only is itself part of the question). `SetTableStats` mutates the
shared `catalog.Table` pointer, which *should* be global — the suspected
mechanism is the per-connection materialization of per-DB catalog views
(the `DBName→Context` threading), i.e. connections to a non-default DB may
plan against per-connection `Table` copies. **-0029 must root-cause this**;
"persisted at restart" is worthless if a freshly connected session still
cannot see the stats.

## Work plan

### M0125-0028 — ANALYZE resolves in the connection's database

The ledger row's resume point stands: route `expandAnalyzeTargets` (and the
42P01 arm) through the same per-connection, DB-scoped resolution SELECT uses,
instead of raw `cat.LookupTable`. Acceptance: `CREATE DATABASE d; CREATE TABLE
… ; ANALYZE <table>` **in d** succeeds and populates stats (regression test);
`ANALYZE lineitem` in db `tpch` on the bench cluster stops erroring
(`scripts/tpch-relsize-arm.sh probe-analyze` is the ready-made probe).
While in the file, fix (b) or ledger it explicitly: PG's bare `ANALYZE`
analyzes every table in the **current database** — with per-DB resolution in
hand the "no public iterator" excuse in `targets()` is stale. This task also
un-blocks M0125-0003's owed W1/W2 control arms (ledger row 2026-07-30, which
names this exact fix as its resume point).

### §-0028a Execution record (2026-07-30)

Landed exactly along the resume point, plus the twin/sibling pins the code
walk surfaced:

- **`expandAnalyzeTargets`** resolves named targets via `ctxPlanCatalog` (the
  per-connection, DB-scoped catalog every SELECT plans against) instead of raw
  `ctx.Catalog.LookupTable`; `PartitionChildren` calls now thread
  `NamespaceDBOid(ctx.CurrentDatabaseOid)`.
- **Fact (b) is FIXED, not ledgered**: bare `ANALYZE;` implements PG's
  every-relation-in-the-current-database semantics (`get_all_vacuum_rels`)
  over a new live-handle iterator `catalog.UserTableHandles` — live pointers
  because `SetTableStats`'s contract is "the Table came from
  LookupTable/CreateTable", and `AllTables`' deep copies would take the scan
  and drop the result. Non-owned and other-sessions' temp relations are
  skipped silently, matching upstream; partitioned parents join the
  inheritance pass without child expansion (their leaves are their own
  namespace entries — expanding would analyze each leaf twice).
- **VACUUM's twins changed together** (Hard-won rule 2): named-target
  resolution in `expandVacuumTargets`/`vacuumTableTargets` uses the same
  ctxPlanCatalog path (`VACUUM lineitem` in db `tpch` silently skipped its
  target before), and **`relationStillExists` — which re-checks the target
  after the maintenance lock — now uses `LookupTableByOIDAllDBs`**: its
  DefaultDBOid-pinned lookup would otherwise have read every per-DB target as
  "concurrently dropped" and skipped it silently *right after* resolution was
  fixed.
- **Deferred with ledger rows (2026-07-30)**: database-wide `VACUUM` still
  enumerates DefaultDBOid via `AllTables` deep copies (its
  reltuples/relfrozenxid writes are silently lost — needs its own
  freeze-bookkeeping verification); bare ANALYZE cannot cover heap-backed
  system catalogs (none are registered in the executor namespace map);
  `VACUUM <missing>` silently succeeds where PG raises 42P01.

Verification: 3 new pins in `internal/executor/analyze_dbid_routing_test.go`
(named ANALYZE / bare ANALYZE / named VACUUM under a distinct dbOid), each
**proven to fail pre-fix** in the documented direction (42P01, silent no-op,
silent skip). Units precommit suite PASS; `tpch-spotcheck.sh` PASS (Q12=2,
Q13=35, 33.0 s query phase — unchanged vs the M0125-0005 baseline);
`probe-analyze` acceptance FLIPPED: `ANALYZE lineitem` in db `tpch` succeeds,
`pg_class.reltuples` = 5,997,241 (exact truth for this load).

**Observation for -0029 (gap 3):** the probe's SECOND session also read
reltuples = 5.997241e+06 — the 2026-07-23 cross-connection-invisibility
symptom did **not** reproduce once resolution was per-DB. Consistent with
`SetTableStats` mutating the shared live `catalog.Table`, which every
connection now resolves to. -0029 should re-verify (esp. across a restart and
for the SF=1/SF0.5 TPC-DS DBs) rather than assume, but the suspected
"per-connection Table copies" mechanism is now doubtful; the 2026-07-23
record may have been this same DefaultDBOid mis-routing seen from the other
side.

### M0125-0029 — statistics survive restart, for every database, visible to every connection

Three gaps close together, because acceptance is end-to-end:

1. **Per-DB routing** — `persistStatsToPGStatistic` and
   `loadStatisticsFromHeap` must use the table's owning database, not
   `DefaultDBOid`/`cat.DBOID()`.
2. **The size itself** — persist and restore `RowCount`/`Pages`
   (reltuples/relpages). PG's home for these is pg_class, which goopg renders
   virtually (fact e) — **the directive's waiver applies here**: a
   goopg-private mechanism is authorized. Candidates (decide in-task, cite
   which): extend the existing goopg-private catalog-DDL WAL record +
   startup-replay mechanism (the pattern the two-catalog-durability memory
   documents); or a goopg-private row/fork alongside the pg_statistic heap.
   Constraint either way: the on-disk pg_statistic rows a PG standby can scan
   must stay PG18-canonical — additive only, never a format change to the
   shared heap (M0112's end-state remains PG-faithful; this task is the
   authorized interim).
3. **Cross-connection visibility** — root-cause the 2026-07-23 symptom and fix
   it so a NEW connection (and a restarted server's first connection) plans
   with the restored stats.

Acceptance (all three at once): ANALYZE the 8 TPC-H tables by name in db
`tpch` → restart the server → **new** connection → `pg_class.reltuples` > 0
for all 8 AND an `EXPLAIN` join-order/row-estimate change proves the planner
consumed them — with **zero** re-ANALYZE after the restart. Then run
`W_ARM_OK=1 scripts/tpch-relsize-arm.sh w1`/`w2`: §D3's "flag-on == flag-off
when ANALYZEd" invariant finally gets its measurement. Note the harness gap
is in scope: `ARM_ANALYZE` performs no ANALYZE today (it only gates on
`W_ARM_OK`) and the guard text demands a `cmd/tpch-runner -analyze` flag —
after this task a documented one-time per-table psql ANALYZE before the run
suffices, so add that step (or the documented pre-step) and retire the stale
guard text as part of -0029.

### §-0029a Execution record (2026-07-30)

Three gaps closed end-to-end, each verified by the E2E restart-durability pins:

**Gap 1 — Per-DB routing.** `persistStatsToPGStatistic` now routes both the
pg_statistic heap and the new goopg-private relstats sidecar to the connection's
database (`tableCatalogHeapDBOid(ctx)`), not `catalog.DefaultDBOid`. On the
startup side, `loadStatisticsFromHeap` iterates every database (default → each
`cat.ListDatabases()` entry) and reloads the heaps into that database's
namespace — the same per-DB sweep as `loadUserTablesFromHeapForDB`. A distinct-
dbOid ANALYZE now round-trips a restart without a single column-stat row
evaporating.

**Gap 2 — The size itself (reltuples/relpages).** A goopg-private sidecar heap
(`GoopgRelStatsRelationId` = 9410, beside pg_statistic's 2619 in each database's
base/<dbOid> directory) persists `(starelid, rowcount, pages)` per ANALYZE.
Write side: `persistStatsToPGStatistic` appends one size row alongside the
per-column pg_statistic rows; `GoopgRelStatsColumns()` defines the three-column
layout. Read side: `loadStatisticsFromHeapForDB` scans the sidecar with
`scanCatalogHeapRows` + `DecodeRowIntoMctxPGTuple`, fills `relSize{rowCount,
pages}` into a map, and attaches the counts to the restored `TableStats`. Both
heaps use the same append-only, last-live-tuple-wins convention. A sidecar size
row without pg_statistic rows (every column at SET STATISTICS 0) still restores
— the sidecar's relids join the `byRelid` key set so the apply loop reaches
them. AvgWidth is deliberately not persisted (nothing reads it today).

**Per-column resilience.** A wide-text column's histogram (e.g. TPC-H
partsupp.ps_comment, varchar(199) × up to 101 bounds) produces a pg_statistic
tuple larger than a heap page, and goopg's catalog heap writer has no TOAST
support. Before this change, the first such oversized tuple aborted the entire
`persistStatsToPGStatistic` call with a hard error — so `orders`, `customer`,
and `partsupp` (whose comment histograms exceed 8192 bytes) had zero
pg_statistic rows AND no size sidecar row. Now each column's write failure is
recorded (the first error is kept for the caller's non-fatal bookkeeping) but
does not prevent the remaining columns or the sidecar size row from being
written. The TOAST gap itself is a ledger row (M0125-0029, see ledger).

**Gap 3 — Cross-connection visibility.** The 2026-07-23 "per-connection stats"
symptom did NOT reproduce once per-DB resolution was in place, consistent with
`SetTableStats` mutating the shared live `catalog.Table` pointer. The restart-
durability test (`TestAnalyzeStatsSurviveRestartPerDatabase`) additionally
verifies a second, separately dialed connection reads the restored stats,
closing the concern from both angles.

**`tpch-relsize-arm.sh` w-arm warm-up.** The script now performs its own one-time
durable ANALYZE of all 8 TPC-H tables when `ARM_ANALYZE=1`, eliminating the
retired need for a `cmd/tpch-runner -analyze` flag. The warm-up step verifies
reltuples > 0 in a fresh post-warm-up session, then stops — the stats persist
across the per-query restarts that isolate each measurement. The stale guard
text that claimed the w-arms were "unconstructible" is retired.

**Verification.** Two E2E restart-durability pins in
`internal/server/stats_dbid_restart_test.go`:
`TestAnalyzeStatsSurviveRestartPerDatabase` (CREATE DATABASE → ANALYZE →
restart → NEW connection reads reltuples/relpages > 0 AND plans identically to
the pre-restart session, then a SECOND connection sees the same reltuples) and
`TestAnalyzeStatsSurviveRestartDefaultDatabase` (same round-trip for the default
`postgres` database). Both PASS; units precommit suite PASS. TPC-H spotcheck
PASS (Q12=2/Q13=35).

**Deferred (ledger 2026-07-30, one new row):** 

- **TOAST gap for pg_statistic.** Wide histogram tuples (varchar(199) × 101
  bounds) exceed a heap page; goopg's catalog heap writer has no TOAST, so they
  are lost silently (first error is captured). Real PG has
  `pg_statistic.h` (toast relation for 2619). Until goopg implements TOAST for
  catalog heaps, `partsupp`, `orders`, and `customer` on the TPC-H bench cluster
  have column statistics for their early (narrow) columns only — the comment
  histograms that don't fit are absent, and the non-fatal skip means no error
  reaches the operator.

### M0125-0030 — bench clusters get warm statistics + CHECKPOINT, at build time and once now

Script changes (each: per-table `ANALYZE <name>` over the benchmark tables +
`CHECKPOINT` after load):

- `bench/tpch/build_schema_goopg.sh` — HammerDB's own final ANALYZE step fails
  today (fact a); after -0028 it should succeed — verify rather than add a
  duplicate pass, and add the explicit CHECKPOINT.
- `scripts/tpcds-load.sh` (SF=1) — already claims "Schema + COPY + ANALYZE";
  verify the ANALYZE actually populates under -0028/-0029 and add CHECKPOINT.
- `scripts/tpcds-sf05-regression.sh load-goopg` (SF0.5) — same.

One-shot warm-up of the three standing clusters (after -0028/-0029): 65433
(`tpch@tpch`, 8 tables), 65436 (`tpcds`, 25 tables), 65437 (SF0.5, 25 tables)
— ANALYZE each table by name, CHECKPOINT, **restart, and verify the stats
survived** (that restart is the acceptance probe for -0029 in situ). All
server starts through the cgroup cap / lifecycle scripts as always.

Consequences to record in the same commit (this is where the premise flips):

- **Row-count gates must NOT move.** Q12/Q13 spotcheck canonical counts and
  the SF0.5 oracle are statistics-independent; any row change after warm-up is
  a real defect, not an expected re-pin.
- **Plan baselines DO move.** Capture a new plan-diff label (e.g.
  `warm-stats-base`) immediately after the warm-up; every prior label is a
  different stats regime.
- **Timed baselines reset.** Every `analysis/` number taken S-cold is no
  longer comparable; from this commit on, analysis docs state the stats
  regime explicitly.

### M0125-0031 — the warm-stats planning line (gated on -0028 … -0030)

The directive's goal state, filed as the successor umbrella:

- **(a) TPC-DS: zero goopg-only timeouts** at the SF0.5 gate. Baseline: **13**
  under the flipped default — `99d83714` §D8 measured `GOOPG_RELSIZE_FALLBACK=2`
  rescuing Q10/Q69/Q67/Q47 and Q72 joining the class, and `d4071df4`
  (M0125-0005) made that flag the default; the `TIMEOUT=16` at `e29faca9` is
  the superseded flag-OFF figure. Q4 stays excluded — the oracle itself times
  out — and Q36/Q70/Q86 stay dsqgen-artifact SKIPs.
- **(b) TPC-H + TPC-DS: runtime reduction and plan stabilization** under the
  warm premise.

The first motion is a **re-measurement, not a fix**: round-4 §2/§5 measured
that turning statistics ON fixed TPC-H Q5 22.8× and regressed Q22 128×,
Q4 79×, Q8 53×, Q2 26×, Q12 4.4× — the serial stream got 12 % *slower*. Under
the warm premise that trade-off is no longer optional; it is the default
behavior. The planner has changed substantially since round 4 (int64 keys,
§2 veto, composite-NLI keep, relsize line), so re-run the warm TPC-H power
sweep at HEAD first and fix what still regresses. Expect the TPC-DS timeout
class to **split**: members whose plans were starved of sizes may complete
outright; shape-class members (RC-8 rescan-per-outer-row, CTE×N
re-execution, missing TopN pushdown — M0125-0026's suspects a/d/e) will not,
because no cardinality input fixes a rescan-per-outer-row shape. Concrete fix
tasks are filed from evidence (M0125-0032+, shared runway with -0026's
per-class filings — coordinate the numbering).

## Relation to the standing M0125 lines

- **M0125-0026 (plan capture)**: unaffected and still owed — its OFF/relsize2
  arms answer "which members move on sizes alone" cheaply and its
  classification is the mechanism evidence -0031 files fixes from. If -0029/
  -0030 land first, add a fourth **warm** arm to the capture for free.
- **M0125-0003 / -0005 (relation-size fallback)**: becomes the **S-cold
  safety net** (fresh clusters, never-analyzed relations) rather than the
  primary line. Its `RowCount > 0` early-return makes it inert on warm
  clusters by construction (unit-tested). The owed timed four-arm study keeps
  its meaning for the S-cold regime; -0029 finally makes its W-control arms
  constructible.
- **M0112 (pg_statistic, PG-faithful)**: partially landed (facts c/d are its
  code). This programme repairs its routing and fills its authorized-private
  gap; M0112 stays open as the PG-faithful end-state (standby-readable
  reltuples story, `anyarray` completeness) and must not be marked complete
  by -0029.

## Constraints

- Any goopg server start goes through the cgroup cap; never `pkill -f goopg`;
  stop via the lifecycle scripts.
- `EXPLAIN ANALYZE` stays banned on goopg for the timeout set.
- -0031's fixes are plan-shape commits and inherit the full bar: plan-diff
  against the **new warm label**, timed 22-query TPC-H on a quiet host, full
  SF0.5 gate. -0028/-0029 are engine changes: units suite, tpch-spotcheck,
  and the restart-durability acceptance above (durable-path testing rules
  apply — no `--no-sync`/fsync-off cluster for the restart probe).
- The tree carries a concurrent Ralph loop's WIP; stage by explicit pathspec.
