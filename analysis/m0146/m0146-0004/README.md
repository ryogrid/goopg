# M0146-0004 — per-worker Memoize + Gather-over-Memoize: floor measurement

Scoping/floor-measurement pass for M0146-0004 (the task's planner+executor
mechanism already landed under M0142-0005a, 2026-09-19). Design doc:
`docs/design/0100-0149/m0146-0004-memoize-partial-admission-closeout.md`.

## Method

The corpus captures under `analysis/m0146/m0146-0003d/` are the
measurement source — their candidate binary carries HEAD's planner code.
Re-measured against them, not re-run:

- Memoize node counts per engine per corpus (TPC-H, TPC-DS SF0.25, SF1).
- Every PG Memoize child's node kind (Index Scan 34 / Index Only Scan 3
  at SF1) and its parent join type (all plain `Nested Loop` — no
  LEFT/SEMI/ANTI consumers in the corpus).
- Whether any Memoize-under-parallel divergence sits at a
  first-divergence position (none — the three index-only memoize spots
  Q53/Q63/Q77 diverge earlier on window/subquery, incremental sort, CTE).
- M0146-0001's ranked residual list re-read: no memoize-owned category.

## Verdict

`parallelism`-adjacent mechanism discharged: Gather-over-Memoize is
produced, elected, and executed correctly post-cutover (Q34/Q73 render
`Gather Merge → Sort → NL → Memoize → Index Scan`; sweeps execute the
shape at oracle-verified counts; new `TestParallelNLIMemoizeIdentity`
pins serial-vs-parallel multiset identity at workers 1/2/4 under -race).
The task closes `Movement: none` — no production change; the mechanism's
movement was credited to M0142-0005a when it landed.
