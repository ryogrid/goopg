# TPC-DS SF=1 — goopg Round-2 Fix Report

**Date:** 2026-07-27
**Branch:** `tpcds-fix2`
**Base:** `ee86594e` · **Commits:** `b3493a6e`, `21301982`, `9740fce9`
**Design doc:** `docs/design/tpcds-round2-fixes/README.md`
**Parent report:** `analysis/tpcds-sf1-goopg-20260726.md`

---

## §0 Result

Round 2 targeted the 24 TPC-DS SF=1 queries where **PostgreSQL 18.3 succeeds and
goopg does not** — 2 goopg-only errors, 6 row-count divergences, 16 timeouts.
Queries that also fail on PG (Q36, Q70, Q86, Q4) were excluded.

Three defects were root-caused and fixed, one was contained, and five were
root-caused but deliberately held back behind an unrunnable regression gate.

| outcome | queries |
| --- | --- |
| **Fixed, exact PG match** | Q76 (0 → 100), Q83 (0 → 22) |
| **Confirmed already correct** — measurement artefacts, not engine bugs | Q45 (14), Q46 (100) |
| **Contained** — server crash became a normal SQL error | Q8 |
| **Root-caused, fix designed, deliberately deferred** | Q47, Q50, Q72 |
| **Open, hypotheses narrowed** | Q39, Q49, Q35 |
| **Not attempted this round** | the 16 timeouts |

The first three rows list 27 queries against a 24-query scope: Q45, Q46 and Q35
are measurement artefacts carried over from the previous report rather than
members of the 2 + 6 + 16 decomposition. **Q34 is a fourth such carry-over and is
not addressed here** — `/tmp/tpcds-bench-v4.txt` has a PG line (374 rows) but no
goopg line at all, lost to the same mid-benchmark restart as Q46. It needs a
measurement, not a fix.

The headline finding is structural rather than per-query: **three of the four
correctness classes are one defect class.** goopg's planner carries fourteen
independent hand-written expression walkers, each a partial copy of the others,
each silently passing through the `Expr` node kinds it does not enumerate.
`internal/planner/plan.go` declares 32 concrete `Expr` types; the walker that
caused the observed wrong answers enumerated 11, and the worst pair enumerates 4.
Silence is the defect: a walker that skips `IsNullExpr` does not fail, it leaves a
stale `ColumnRef.Index` behind and the query quietly returns the wrong rows.

**One of those fourteen walkers was fixed.** The shared-traversal fix the design
doc specifies for the other thirteen was not landed — see §2.2.

---

## §1 What was fixed

### §1.1 RC-1a — expression walker skipped `IS NULL` after join reordering → Q76

`rewriteMultiWayChain` sorts `MultiHashJoin.Tables` by **table OID**, then
`remapByPosMap` (`internal/planner/bushy.go:2154`) rewrites every
`ColumnRef.Index` into that new coordinate space. Its type switch enumerated 11
kinds and had **no default arm**, so `IsNullExpr` — along with `IsBoolExpr`,
`IsDistinctFromExpr`, `CollateExpr`, `RowExpr`, the `MultiAssignSubq` forms,
`InExpr.List`/`.Args` and `Exists`/`Subquery` `.Args` — was a silent no-op.

For `store_sales, item, date_dim` the FROM order and the OID order disagree:

| relation | FROM-cumulative | post-sort MHJ |
| --- | --- | --- |
| `store_sales` (23 cols) | 0–22 | 50–72 |
| `item` (22 cols) | 23–44 | 28–49 |
| `date_dim` (28 cols) | 45–72 | 0–27 |

`ss_customer_sk` sits at FROM index 3 and should become 53. It stayed at 3 — a
`date_dim` column that is never NULL — so the predicate was never true.

**Measured, both engines:**

| query | before | after | PG |
| --- | ---: | ---: | ---: |
| `… where ss_customer_sk is null and ss_sold_date_sk=d_date_sk and ss_item_sk=i_item_sk` | 0 | **64858** | 64858 |
| same with `ss_addr_sk is null` | 0 | **65038** | 65038 |
| **TPC-DS Q76** | 0 | **100** | 100 |

The control that always worked — `ss_quantity = 1`, a bare `BinaryOp`, which the
switch did enumerate — was correct before and after (26786 on both).

### §1.2 RC-3 — IN-list literals were never coerced → Q83

```sql
select count(*) from date_dim where d_date = '2001-07-13';          -- goopg 1  ✓
select count(*) from date_dim where d_date in (date '2001-07-13');  -- goopg 1  ✓
select count(*) from date_dim where d_date in ('2001-07-13');       -- goopg 0  ✗  PG 1
```

`compareEq` (`internal/executor/expr.go:7592`, pre-fix `:7568`), the equality oracle behind
`evalInExpr` and `CASE`, had no `KindTime`↔`KindString` arm and fell through to an
unconditional not-equal. The `=` form worked because `BinaryOp` routes through
`compareDatum` → `promoteCrossKind` → `tryParseStringAs`, which parses the
literal; `compareEq` bypassed all of it. A lone unknown-typed string operand now
delegates to `compareDatum`, exactly as the existing `KindNumeric` arm does.

PostgreSQL resolves this at parse time instead (`parse_expr.c transformAExprIn` →
`select_common_type` → `coerce_to_common_type`). goopg types a bare `StringConst`
as `unknown` and resolves coercion at runtime by design
(`docs/design/root-0019-unknown-literal-coercion.md`), so the fix belongs in the
executor. Two consequent divergences from PG are recorded in the ledger.

**Measured:** Q83 **0 → 22 rows**, exact PG match. The nested-IN probe
(`d_date in (select … where d_week_seq in (select … where d_date in (…)))`)
returns 21 on both engines, was 0.

### §1.3 Q8 — server crash contained into a normal SQL error

Q8 previously panicked with `index out of range [57] with length 1` at
`MaterializedSlot.Get`, reached through the hash-join build-side drain that
`gatherOp.Open` runs in the **leader** goroutine — outside `ParallelGroup.Go`'s
recover — so it escaped to `serveConn`, which logged and closed the socket. The
client saw "connection lost" and the harness restarted the server mid-benchmark.

`evalExprSlot` bounds-checked `rowSlotView` and `*VirtualSlot` but not
`*MaterializedSlot` (a bare `s.row[col]`) or `*Slot`. Both are now checked.

```
before: connection to server was lost          (server restart, benchmark disturbed)
after:  ERROR: column ref ca_zip/57 out of MaterializedSlot range 1
```

The server survives, the log records **zero** `backend goroutine panic` entries,
and the session stays usable. This matches PostgreSQL's contract that an ERROR
kills the statement, not the backend
(`postgres/src/backend/tcop/postgres.c`, `sigsetjmp` / `EmitErrorReport`).

**Q8 is contained, not fixed.** It still errors instead of returning 0 rows.

`buildBindingsPosMap` also gained the eight missing opaque-leaf arms (`SetOp`,
`RecursiveUnion`, `WorkTableScan`, `WindowAgg`, `ProjectSet`, `OrdinalityWrap`,
`RowsFrom`, `IndexOnlyScan`), five pass-through descends, and a **decline-on-
unknown** default. Declining is the safe direction — an unremapped tree is only
wrong when a reorder actually happened, whereas a mis-advanced offset is wrong
unconditionally — and all three callers already nil-check the result.

### §1.4 Harness — three defects that corrupted the previous report

`scripts/tpcds-bench-compare.sh` never sourced `bench/tpch/env_goopg.sh`, so
`psql` left `PATH` partway through the sweep: **every `*_explain.txt` from Q36
onward in the 2026-07-26 run is the stub** `timeout: failed to run command
'psql'`. It extracted row counts with `tail -1`, reporting only the last block of
a multi-statement template (Q14, Q23, Q24, Q39). And it ran goopg and PG
concurrently under `&` … `wait`, contaminating both timings.

All three are fixed; the script now also takes a query list/range and EXPLAINs
each statement separately.

**Consequence for the previous report — corrected on review.** The two `?` row
counts in `/tmp/tpcds-bench-v4.txt` were **Q35 and Q45**, not Q45 and Q46. Q46 had
a different cause: the previous report states it "was interrupted by a
mid-benchmark server restart" — i.e. the Q8/Q39 crash that §1.3 contains.

Neither Q45 nor Q46 is a multi-statement template (one `;` each), so the `tail -1`
defect cannot be their cause either; the PATH defect is the one that fits, and it
is independently corroborated — 63 of the 64 `goopg_q{36..99}_explain.txt` files
from the previous run are the `timeout: failed to run command 'psql'` stub.

Re-measured, both engines agree exactly — Q45 **14**, Q46 **100** — so neither is
an engine defect. But **Q35, the query the `?` actually hit, is still unmeasured**
(§2.3). Fixing the harness did not close that one.

Caveat: Q45 and Q46 were measured at 07:00–07:01, before the RC-1a binary was
built at 07:18. They have not been re-confirmed against the walker change that §3
itself names as the regression risk; the pending sweep will do that.

---

## §2 What was deliberately not landed, and why

### §2.1 RC-1b — MHJ filter push-down uses two coordinate spaces → Q47, Q50 (and probably Q72)

Root cause is confirmed **for Q47 and Q50** and the fix is designed, but it is
**held back**.

**Q72 is an attribution correction, and the hedge belongs in it.** Commit
`9740fce9` predicted Q72 would be fixed by RC-1a, through
`sum(case when p_promo_sk is null …)`. It was not — Q72 measured 0 rows again on
the fixed binary. That establishes only the negative: RC-1a is not Q72's cause.
It does **not** establish RC-1b as the cause, and no new positive evidence was
gathered. Q72 also reaches `promotion` through a `LEFT OUTER JOIN`, and it has not
been shown that an outer-joined leaf reaches `rewriteMultiWayChain` at all.

`pushSingleSourceFiltersIntoMHJTables` (`internal/planner/mhj_input_rewrite.go:624`)
computes per-table offsets from the **OID-sorted** `mh.Tables`, while the
conjuncts in `mh.Filters` still carry **FROM-cumulative** indices. The
reconciling remap runs later — the push happens at `planner.go:1020`, inside
`rewriteScanInputsWithSingleTablePredicates` (`:1012`), and `remapWithBindings`
only at `:1024`.

Observed directly:

```
Multi-Way Hash Join (3 tables)
      Filter: ((d_year = 2001) AND (d_moy = 8))
  ->  Seq Scan on public.date_dim d2
        Filter: ((ss_item_sk = sr_item_sk) AND (ss_customer_sk = sr_customer_sk))
```

A `store_sales`↔`store_returns` equijoin attached to `date_dim d2`, which has
neither column. In Q47's shape a `date_dim` OR-predicate lands on the **`store`**
scan. The arithmetic predicts exactly which conjuncts move and which straddles,
and matches the emitted plan.

The corruption is permanent: `applyJoinTreePosMap`'s `*MultiHashJoin` arm
(`bushy.go:2555-2570`) remaps `n.Filters` and then **returns without recursing
into `n.Tables[i]`**, so a conjunct already pushed into a table's `Filter` is
never revisited. This is why §1.1's fix does nothing for these three queries —
they are a genuinely separate bug, not the same one.

**Why it was not landed.** The fix moves a planner pass. An adversarial review of
the design found that the safety argument in the first draft was **false**: it
claimed FROM order and OID order coincide on TPC-H, when in fact 7 of the 8
join-heavy TPC-H queries differ (only Q3 coincides). So this is a real TPC-H
regression risk — and **the goopg TPC-H regression gate cannot currently run**:

```
$ scripts/tpch-spotcheck.sh
tpch-spotcheck: SKIPPED (no TPC-H data dir)
reason: cluster is up but lineitem is not loaded in any persistent database
```

`bench/tpch/runtime_goopg/data` was reloaded with TPC-DS during round 1, so the
goopg TPC-H dataset no longer exists. Landing a planner change of this risk class
without that gate would repeat the pattern already on record in
`analysis/tpch-evolution-round4-parallel-query-20260722.md`, where enabling
ANALYZE fixed TPC-H Q5 and simultaneously regressed Q22 128×, Q4 79×, Q8 53×.

**Unblocking step, with its real cost.** This is not a re-run: the repo contains
no TPC-H `*.tbl` data and no `dbgen` (`third-party/` holds only `tpcds-postgres`),
and `bench/tpch/runtime` is the *PostgreSQL* cluster, which cannot serve a goopg
gate. Unblocking means fetching or building `dbgen`, generating SF=1, and loading
it into a goopg data directory of its own — after which RC-1b can land behind a
full TPC-H power run in both flag states.

### §2.2 The structural fix from §0 was not landed — 13 of 14 walkers are untouched

§0 leads with the fourteen-walker finding. One walker was fixed. That needs saying
plainly here rather than being left as a discovery in the summary.

The design doc (§2.5) is explicit that patching `remapByPosMap` alone "would fix
Q76 and leave ten loaded guns", and specifies the real fix: a shared
`internal/planner/exprwalk.go` traversal primitive plus a `go/ast` exhaustiveness
test that turns an unenumerated `Expr` type into a **build failure**. That file
does not exist and no other walker was converted. Current arm counts:

| walker | arms |
| --- | ---: |
| `shiftColumnRefs`, `cloneExprForShift` | 4 each |
| `cloneExprShiftIdx` | 6 |
| `visitColumnRefsByName`, `conjunctIsLocalEligible`, `localizeExprToLeaf` | 7 each |
| `exprSide` | 8 |

`remapByPosMap` itself still has **no `default:` arm** — the very property §0 names
as the defect. That is defensible today (all 14 remaining unenumerated types are
genuine leaves with no child `Expr` slots, so the silence is currently harmless),
but the diagnosis was not applied even to the walker that was fixed. The next
`Expr` type added to `plan.go` reopens the hole.

### §2.3 The 16 timeouts

No timeout fix was attempted. The mechanisms were surveyed and documented with
file:line in the design doc §7; the primary hypothesis was confirmed live:

> The bench server plans with `pg_stats` = **0 rows** and `reltuples` = **0** for
> every table.

`loadStatisticsFromHeap` (`internal/initdb/open.go:3433`) restores per-column
statistics but leaves `TableStats.RowCount`/`Pages`/`AvgWidth` at zero. After
every restart `EstimateRows` returns 0 for every scan, the bushy DP seeds
`rowCounts[i] = 1` for every relation (`bushy.go:675`), and the MHJ probe side is
whichever scan the DFS reached first. **The 2026-07-26 benchmark measured a
planner with no usable size information at all.**

The designed fix — an `estimate_rel_size` fallback in `tableRows` from the live
block count already plumbed as `ParallelSettings.BlocksForTable` — is the
highest-regression-risk change in the bundle and is blocked on the same missing
TPC-H gate. It is specified in design doc §7.1 and ledger rows `pq-P6`/`pq-P10`.

### §2.4 Open with narrowed hypotheses

| query | ruled out | remaining |
| --- | --- | --- |
| **Q49** (30 vs 34) | `rank()` peer ties — `rank`/`dense_rank`/`row_number` are byte-identical to PG (`1,1,3,3,3,6`) | the `decimal(15,4)` ratio division reordering ties at rank 10; or the `LEFT OUTER JOIN … , date_dim` shape. 30 is exactly 3×10, one per branch (the three branches are joined by plain `union`, not `UNION ALL`) |
| **Q39** (connection lost) | **nothing — corrected on review.** An earlier draft listed "a Go panic" as ruled out because no `backend goroutine panic` line exists for Q39 in any log. That is an argument from silence over a window with **no log at all**: coverage is `goopg.tpcds.log` 07-24 16:58–17:40 and `goopg.csq-bench.log` 07-27 07:18 onward, while the v4 sweep ran 07-26 13:57–18:19. The panic-vs-SIGKILL question is **open**, not settled | a Go panic in an unlogged window; the cgroup cap (`MemoryMax=24G`) killing an arbitrary hash-join build side (`probeIdx` is meaningless at zero stats, and `inventory` is 11.7M rows); or the unbounded `aggregateOp`. Re-run Q39 alone with logging and RSS monitoring before choosing a fix |
| **Q35** | — | row count still unmeasured (525 s run) |

**Methodological caution learned here.** The Q6/Q7 investigation initially
concluded — and this report initially stated — that commit `9740fce9` had caused a
regression, on the strength of an A/B where the pre-fix binary was fast and the
post-fix binary slow. That A/B was confounded: the pre-fix arm ran on a fresh
server, the post-fix arm on one that had just executed two 600 s timeouts. Re-run
with **both** arms on fresh servers, the post-fix binary is fast and the code is
exonerated. Any future goopg timing comparison must control for server age and for
what ran before it in the same process.

---

## §3 Full 99-query sweep

A full sweep on both engines is running with the fixed harness at
**`TIMEOUT_SEC=600`**, writing to
`bench/tpch/runtime_goopg/tpcds-results/sweep-20260727.txt`. It is the only way to
detect a regression among the 75 queries that already passed, which matters here
because §1.1 makes `remapByPosMap` remap strictly *more* than before.

**Threshold note.** The baseline is not uniform: `/tmp/tpcds-bench-v4.txt` used
600 s for Q1–Q46 and 300 s for Q47–Q99. An initial 300 s sweep was started and
then discarded, because at 300 s several queries that legitimately completed in
the baseline — Q18 at 358 s, Q1 at 262 s, Q23 at 226 s — would report TIMEOUT and
read as regressions caused by §1.1. The re-run uses 600 s throughout, which is
≥ both baseline thresholds. Consequence when reading §5: for Q47–Q99 a query
taking 300–600 s will look like an improvement over the baseline when it is only a
longer clock.

A second harness defect was found during this review and fixed: the classifier ran
`grep -qi "ERROR"` over the entire psql output *before* the row-count branch, so a
result row whose text merely contained the word "error" silently became
`ERROR, rows=0`. It now matches psql's own `ERROR:`/`FATAL:`/`PANIC:` prefix.

**A third harness defect, found by the sweep itself — sweep-tail collapse.** The
first 600 s run reported Q6 (70 s in the baseline) and Q7 (67 s) as TIMEOUTs.
Both are 5-table star joins and neither is on the known-timeout list, so they
looked like regressions caused by §1.1. They were not. On a **freshly started**
server the same binary returns:

| | baseline 07-26 | during sweep | fresh server |
| --- | ---: | ---: | ---: |
| Q6 | OK 70 s / 44 | TIMEOUT >600 s | **OK 62 s / 44** ✓ |
| Q7 | OK 67 s / 100 | TIMEOUT >600 s | **OK 64 s / 100** ✓ |

Cause: the bench server runs with `GOGC=off` and `GOMEMLIMIT=12GiB`
(`bench/tpch/env_goopg.sh`), so the collector only fires as the heap nears the
limit. Q4 and Q5 immediately precede Q6 and **both time out at 600 s**, each
building an enormous intermediate. After them the heap sits at the limit and every
subsequent query in that process thrashes GC. This is the failure mode already on
record in the project's operational notes as *"sweep-tail collapse mimics a code
regression"* — and it did exactly that.

`scripts/tpcds-bench-compare.sh` now restarts the goopg server after any goopg
TIMEOUT (`RESTART_AFTER_TIMEOUT=1`, the default) so a heap bomb cannot poison the
rest of the run.

**This also casts doubt on the 07-26 baseline's own timings**, which were produced
by a single long-lived process with 16 timeouts in it. Any query that ran late in
that sweep may be recorded slower than it truly is.

Results are appended in §5 when it completes. Until then the claims in this report
are limited to the per-query measurements shown above, each run individually
against both engines.

---

## §4 Verification performed

| gate | result |
| --- | --- |
| `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` | **PASS** (both commits) |
| pgbench smoke (pre-commit hook) | **PASS** — 13.6–13.7k TPS |
| `go test ./internal/planner ./internal/executor` | **PASS** |
| `scripts/tpch-spotcheck.sh` | **SKIPPED — no TPC-H data.** Not a pass. See §2.1 |
| `make plan-gate` | not run — the baseline is TPC-H plans |
| per-query TPC-DS, both engines | as tabulated above |

**Net position, stated plainly: no planner regression gate ran against these two
planner commits.** `tpch-spotcheck` skipped for want of data and `plan-gate` was
not run because its baseline is TPC-H plans. What stands behind a change that
makes `remapByPosMap` remap strictly *more* than before is the package unit tests
plus a pgbench smoke — neither of which exercises a multi-table analytic plan. The
pending sweep (§3) is the first real regression signal, and it is not a substitute
for the TPC-H gate.

New regression test: `internal/executor/compare_eq_crosskind_test.go` pins the
IN-list cross-kind coercion and that NULL still short-circuits to NULL.

---

## §5 Sweep results

*(pending — appended when the run in §3 completes)*

---

## §6 Provenance

- goopg: branch `tpcds-fix2`, base `ee86594e`; go1.26.3; server
  `tmp/goopg-bench-bin` started via `scripts/csq-bench-server.sh` under the cgroup
  cap (scope `goopg-csq-bench`)
- goopg endpoint `127.0.0.1:65433`, db `postgres`, role `postgres`, data
  `bench/tpch/runtime_goopg/data`
- PostgreSQL 18.3 endpoint `127.0.0.1:65432`, db `tpcds`, role `ryo`, data
  `bench/tpch/runtime_goopg/pgdata`
- queries `bench/tpch/runtime_goopg/tpcds-data/queries/query{N}.sql`
- table row counts compared by `select count(*)` per table on both engines (not
  from `pg_class.reltuples`, which is 0 on goopg — see §2.3)
- deferral ledger: five rows appended 2026-07-27 under task-id `tpcds-round2`
