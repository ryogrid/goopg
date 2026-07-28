(idle — nothing in flight)

Last loop (#18, 2026-07-29): **M0124-0003 CLOSED** — the round-2 §10
deferral-ledger completion. 13 rows appended to `.ralph/deferral_ledger.md`
(516 → 529 lines): the seven §10 rows (in-list-common-type, scaninput-reorder,
smalldim-gate, grouping-sets-operator, exists-under-or, setop-parallel,
plancache-analyze), the `aggregateOp work_mem` DROP row as `status = resolved`
(no new status value invented), and five audit rows (row-anchor
value-blindness, exprwalk-residual, posmap-assert, panic-to-xx000,
q47-q49-q51). Plus the `pq-P10` UPDATE naming M0125-0003 as consumer of its
option (b). Design doc flipped to `accepted` with an execution record.

Doc-only loop — no Go code touched.

**Reusable findings (do not re-derive):**
- The round-2 README's line cites are STALE by ~10 commits. Six drifted:
  `open.go:2911`→`:2924`; `planner.go:1020`→`:1012` (push) vs `:1024` (remap)
  vs `:1037` (`pushSingleSourceFiltersAfterRemap`); grouping sets `:3176`→
  `:650`; MHJ gate conditions at `local_filters.go:171`/`:175`; `*SetOp` at
  `parallel.go:313`. Re-resolve before citing.
- **M0125-0002 is NINE walkers, not seven** — `walkColumnRefsImpl`
  (`pushdown.go:362`) and the `shiftColumnRefs` closure
  (`mhj_input_rewrite.go:735`) also lack `default:`. The first is fail-open in
  the dangerous direction: no callback ⇒ no `onOuter()` veto ⇒ a conjunct
  wrapping an outer ref reads single-side and can be pushed below an outer join.
- ANALYZE **cannot** reach `planCache.Invalidate()` by any path: both call
  sites (`dispatch.go:2976`, `dispatch_extended.go:364`) are
  `*planner.DDL`-guarded and ANALYZE plans to `*planner.Utility`
  (`planner.go:212-218`).
- Ledger rendering: nine PRE-EXISTING rows carry unescaped `|` inside code
  spans and already render with 8–21 cells on GitHub (GFM splits cells before
  inline parsing). Never put a bare `|` in a cell; escape as `\|`.

NEXT LOOP — re-read the `## Current Priority` banner. Its "NEXT TASK TO SELECT"
pointer is STALE (still names M0124-0001, closed loop #17). M0124 remains
priority 2 with three open items, all needing multi-hour benchmark runs:
**M0124-0004** (Q35 row count — needs a small SF0.5 script change first: no
per-query mode, `TPCDS_RESULTS_DIR` not env-overridable, `restart_goopg`
hardcodes sf1; then a solo 900 s run on 65437), **M0124-0005** (SF0.5 oracle
checksum column), **M0124-0002** (retroactive TPC-H + plan-gate A/B, the
longest). M0124-0004 is the cheapest of the three and its `EXPLAIN ANALYZE`
also discharges RC-8's "measure first" criterion for Q10/Q35/Q69 at once.
M-NIGHTLY still PARKED: `ci/logs/action-items.md` unchanged since 2026-07-25,
all 26 filed as ID RANGES `-008..-026`, so a per-ID grep FALSE-NEGATIVES —
grep loosely (`grep 20260725 .ralph/fix_plan.md`).

Gates run: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
(all cached — correct, zero Go files changed); D6 render check via
`gh api --method POST /markdown` (1 table, 14 rows, 7 cells each);
`make ralph-state-guard`; pgbench smoke via the commit hook.
In-flight: none.
