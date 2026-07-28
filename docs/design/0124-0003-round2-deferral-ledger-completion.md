# 0124-0003 — Round-2 §10 deferral-ledger completion

Status: accepted (executed 2026-07-29 — see "Execution record")
Date: 2026-07-28
Milestone: M0124-0003 (`docs/design/tpcds-round2-fixes/README.md` §13.5 action 6)

## Problem

`.ralph/fix_plan.md`'s deferral rule is unconditional: never close a task silently with a
forward reference; append one row to `.ralph/deferral_ledger.md`, which "is the source of
truth for every 'DEFERRED' note below".

Round 2 broke it in the aggregate. §13.2: the seven `tpcds-round2` rows that exist are the
rows the **work** produced (RC-1b, Q8, Q49, Q39, timeouts, stddev-precision,
Q75-eval-order), not the rows §10 **planned**; the two lists overlap on one item. Every §7.3
deferral therefore exists only inside a design document — exactly the forward reference the
rule forbids. This is load-bearing rather than cosmetic: M0119 consumes the ledger as a work
queue, so a deferral that never reaches it is a deferral that is never scheduled.

## Design

### D1. Row shape

`.ralph/deferral_ledger.md`'s own header documents the 7-column form
`status | date | task-id | landed | deferred | resume point | why`. (`.ralph/fix_plan.md`
documents a 6-column form without `status`; the ledger file is authoritative and the
fix_plan text is stale.) All rows below are `status = -` (open) unless noted.

### D2. The seven §10 rows

| # | task-id | deferred scope | resume point / reopen criterion |
|---|---|---|---|
| 1 | `tpcds-round2 in-list-common-type` | parse-time IN-list `select_common_type` (§5.4). The landed fix (`b3493a6e`) is executor-side `compareEq` cross-kind delegation; PG resolves the common type at parse time | `internal/parser` IN-list analysis plus the two §5.4 divergences. Executor-side coercion is right for Q83 but diverges from PG on error text and on `IN` over mixed non-coercible types |
| 2 | `tpcds-round2 scaninput-reorder` | reordering `rewriteScanInputsWithSingleTablePredicates` after `remapWithBindings` (§3.5) — the IndexScan-promotion half. **Distinct from the existing `RC-1b` row**, which records the filter-push move that landed | `internal/planner/mhj_input_rewrite.go`; mirror the `pushSingleSourceFiltersAfterRemap` restructure. RC-1b fixed the two-coordinate-space bug for filter push-down only; the same hazard remains in index promotion |
| 3 | `tpcds-round2 smalldim-gate` | `shouldAttachBeforeMHJ`'s `len(bindings) >= 5` + `SmallDimension` gate (RC-5). `SmallDimension` is hardcoded to `region`/`nation` (`internal/initdb/open.go:2911`, `internal/executor/operators_ddl.go:3376`), so no TPC-DS relation qualifies | `internal/planner/local_filters.go:154`. The gate prevents a *measured* TPC-H Q8/Q21 PASS→CANCEL regression (its own comment) and masks two incomplete walkers. Reopen after **M0125-0002** and **M0125-0005** |
| 4 | `tpcds-round2 grouping-sets-operator` | shared-scan GROUPING SETS operator (RC-7). ROLLUP/CUBE become a UNION ALL chain of N independently planned SELECTs — Q5 3×, Q14 5×, Q67 9× re-execution | a real `GroupingSetsAggregate` modelled on PG's `AggStrategy`, `GroupingSetData` and `postgres/src/backend/executor/nodeAgg.c`. Ceiling is 3–9× vs 100×+ for a wrong star-join probe side. Reopen **if Q5/Q14/Q67 still time out after M0125-0003** |
| 5 | `tpcds-round2 exists-under-or` | EXISTS/IN under `OR` never decorrelates (RC-8, `internal/planner/unnest.go:147 subqueryANDReachable`) — Q10/Q69, **and Q35**, which has the same `exists(…) and (exists(…) or exists(…))` shape | **measure first** with the per-SubPlan `Calls/Rebuilds/Rescans/CacheHits/CacheMisses` counters (`internal/executor/operators_explain.go`). PG's `pull_up_sublinks` does not decorrelate under OR either — it uses a hashed SubPlan. If `CacheMisses ≈ Calls`, the fix is hashed-SubPlan caching, far smaller. M0124-0004's single `EXPLAIN ANALYZE` discharges this criterion for three queries at once |
| 6 | `tpcds-round2 setop-parallel` | parallelising `SetOp` (RC-9) — `terminatesPartial` (`internal/planner/parallel.go:311`) kills parallelism at `SetOp`, so every ROLLUP query is serial by construction | `internal/planner/parallel.go`. Downstream of row 4: fixing RC-7 **deletes** the `SetOp` |
| 7 | `tpcds-round2 plancache-analyze` | `plancache` invalidation on ANALYZE. `planCache.Invalidate()` (`internal/server/plancache.go:93`) fires only on a `*planner.DDL` node; ANALYZE plans to `*Utility` | `internal/server/plancache.go` plus the ANALYZE dispatch sites. Not only perf — it is a **measurement-protocol blocker**: §8 step 6 restricts S-warm to "queries not yet issued in this process" purely because of it |

### D3. The moot row — record the disposition, do not omit it

§10 also planned `aggregateOp` `work_mem` accounting and spill, **conditional on §6 showing
memory pressure was real**. §6 instead found Q39's failure was `exactIntVariance`'s
`big.Float.Quo(0,0)` on an all-equal group (fixed, `927472e0`), with `MemoryPeak` 13.2 G under
a 24 G cap and no `oom_kill` — the precondition never fired.

Silence is the wrong disposition: a future reader assumes it was forgotten, as the other seven
were. Append one row with `status = resolved`, **dropped as unfounded**, naming the
precondition and the measurement that falsified it. Appending it as an open item would be
worse than absent, because M0119 treats the ledger as a work queue.

Reopen criterion: any TPC-DS query that *dies* rather than times out, with a cgroup
`memory.events` `oom_kill` and no `backend goroutine panic` in the log (§8 step 5's
discriminator).

### D4. Cross-reference, do not duplicate, the `reltuples` row

§10's ninth item is already covered by `pq-P6` / `pq-P10`, as §7.1 intended. Do not append an
eighth row; append an `UPDATE` note to `pq-P10` naming M0125-0003 as the consumer of its
option (b), and recording that option (a) (persistence) stays deferred.

### D5. Five rows the audit itself produced

None are in §13.5's seven; all currently live only inside a design document, which is the
condition this task exists to end.

1. `tpcds-round2 oracle-value-blindness` — `ci/batch/tpcds-row-anchors.csv` is row-count only
   and structurally blind to the class Q75 exposed. (The SF0.5 oracle's half of this is being
   fixed by M0124-0005; this row tracks the CI fixture, which is separately pinned and forces
   its own re-capture.)
2. `tpcds-round2 exprwalk-residual` — two `Expr` type switches **not** in the §2.4 seven and
   therefore not converted by M0125-0002: `walkColumnRefsImpl`
   (`internal/planner/pushdown.go:362`) and the `shiftColumnRefs` closure
   (`internal/planner/mhj_input_rewrite.go`). Neither carries a `default:`.
3. `tpcds-round2 posmap-assert` — `GOOPG_POSMAP_ASSERT=1` (§4.4), 0 hits in the tree.
4. `tpcds-round2 panic-to-xx000` — §13.1 phase 0.2's unfinished half: a statement-level panic
   must become an `XX000` `ErrorResponse` + `ReadyForQuery` with the connection intact.
   `internal/server/` still holds exactly one `recover()` (`server.go:780`), which logs and
   closes the socket. Q8's bounds check removed the forcing function; Q39's `Quo(0,0)` panic
   then dropped a connection the same day. Reopen criterion: it can void a sweep — raise it
   if M0124-0001 loses a connection.
5. `tpcds-round2 q47-q49-q51` — §13.4 item 2's **three distinct** defects: Q47's downstream
   windowed self-join layer (its CTE body is now exactly correct at 661,185 = PG, yet the
   full query returns 0), Q49's one-row gap at SF0.5 (24 vs 25), and Q51, whose provisional
   RC-1b-family attribution RC-1b **disproved**. Resume: M0124-0001's sweep first.

### D6. Rendering hazard

A literal `<table>` / `<col>` / `<tr>` in a cell nests every following row on GitHub.
Entity-escape them and verify with
`gh api --method POST /markdown -f text="$(cat .ralph/deferral_ledger.md)"`.

## Non-goals

Implementing any deferred scope; re-triaging the pre-existing `tpcds-round2` rows (they are
accurate, just a different list).

## Precedent

`.ralph/fix_plan.md` exempts doc-only tasks from the per-task design-doc rule (M0119-0001,
M0122-0001). This doc is written anyway because each reopen criterion is a sequencing
decision, not a transcription.

## Gate

`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`, the pre-commit hook, and D6's
render check.

## Execution record (2026-07-29)

Executed as specified: **13 rows appended** to `.ralph/deferral_ledger.md` (516 → 529 lines) —
D2's seven, D3's drop row, D5's five — plus D4's `UPDATE` note on the existing `pq-P10` row.
No eighth `reltuples` row was created.

D3 was followed literally, so **no new `status` value was invented**: the moot `work_mem` row
is `status = resolved` with the drop stated in its task-id
(`tpcds-round2 aggregate-work-mem (DROPPED — precondition never fired)`) and the falsifying
measurement in the row body. The ledger's existing two-value legend is unchanged.

### Every claim was re-verified against HEAD before it was written down, and six cites had drifted

The round-2 README's line numbers predate ten engine commits. Writing a resume point from a
stale cite is how a ledger row becomes unusable, so each was re-resolved:

| this doc / README said | HEAD | note |
|---|---|---|
| `internal/initdb/open.go:2911` (D2 row 3) | **`:2924`** | `SmallDimension` is still `region`/`nation` only, at both writers (`:2924`, `internal/executor/operators_ddl.go:3376`) |
| §3.5's `planner.go:1020` push vs `:1024` remap | **`:1012`** vs `:1024`, with the compensating `pushSingleSourceFiltersAfterRemap` at **`:1037`** | the row now cites all three, because the deferral is precisely the gap between them |
| D2 row 2 resume point `mhj_input_rewrite.go` | **`internal/planner/planner.go`** | the reorder is a *call-order* change in `planner.go`; `mhj_input_rewrite.go:51` is only the entry point being moved |
| §7.3's `planner.go:3176` (grouping sets) | **`:650`** (`rewriteGroupingSets`) | |
| §7.3's `local_filters.go:154` gate detail | `:154` correct; the two gate conditions are at **`:171`** (`len(bindings) < 5`) and **`:175`** (`SmallDimension`) | |
| §7.3's `parallel.go:311` | `:311` correct; `*SetOp` is at **`:313`** | |

Re-confirmed zero-hit / absence claims: `GOOPG_POSMAP_ASSERT` 0 hits, `GOOPG_RELSIZE_FALLBACK`
0 hits, `internal/planner/exprwalk.go` absent, `internal/server/` still holding exactly one
`recover()` (`server.go:780`), `tableRows` (`internal/planner/cardinality.go:89`) still
returning `tbl.Stats.RowCount` with no fallback.

### Three findings the verification pass added to the rows

1. **D2 row 7 is stronger than "fires only on DDL".** `planCache.Invalidate()` has exactly two
   call sites (`internal/server/dispatch.go:2976`, `internal/server/dispatch_extended.go:364`),
   both guarded by `node.(*planner.DDL)`, and ANALYZE plans to `*planner.Utility`
   (`internal/planner/planner.go:212-218`) — so ANALYZE cannot reach either site by any path,
   not merely "is not currently matched". Separately, `planCacheIsCacheable` (`:107`) has the
   comment "DDL, Transaction, and **utility** nodes … must never be cached" while its switch
   lists `*planner.DDL`, `*planner.Transaction`, `*planner.Copy` — `*planner.Utility` is
   absent. Comment and code disagree about a third class; folded into the same row to be fixed
   in the same commit.
2. **D5 row 2's `walkColumnRefsImpl` is a wrong-answer path, not just an unenumerated switch.**
   Its missing `default:` is *fail-open in the dangerous direction*: an unenumerated type
   produces **no callback at all**, and `onOuter()` is the veto that marks a conjunct
   out-of-scope — so a conjunct wrapping an outer ref in an unknown node reads as single-side
   and can be pushed below an outer join. The function's own `CastExpr` comment records that
   exact bug having been found and fixed once already. Consequence for sequencing: **M0125-0002
   converts nine walkers, not seven.**
3. **D5 row 1 covers both anchor fixtures, not just TPC-DS.** `ci/batch/tpch-row-anchors.csv`
   (`query,rows,kind,dataset,source`) is as value-blind as
   `ci/batch/tpcds-row-anchors.csv` (`query,expected_rows,kind`); neither has a value column.

### D6 render check

`gh api --method POST /markdown` over the header plus the fourteen touched rows returns
**one** `<table>`, **14** body rows, **7** `<td>` in every one — no nesting, no cell drift.

Noted but deliberately **not** fixed (Non-goals: no re-triage of pre-existing rows): nine
older rows contain unescaped `|` inside code spans (e.g. `q|status|rows|ck|secs`), and GFM
splits cells before inline parsing, so they already render with 8–21 cells. This is a second,
distinct instance of the hazard `deferral_ledger_raw_html_tag_nesting` recorded for raw tags —
the same escape discipline (`\|`) applies. Every row appended here avoids `|` inside cells
entirely.
