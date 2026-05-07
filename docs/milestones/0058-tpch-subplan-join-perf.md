# Milestone 0058 — TPC-H SubPlan & Join-Unnesting Performance Fixes

**Status:** planned
**Depends on:** Milestone 0054 (TPC-H perf), Milestone 0057 (measurement infra)
**Drives:** 22/22 TPC-H SF=1 query completion within 1-hour-per-query budget

## Context

The M0054-0007 emulate run (tpch-runner, SF=1, 2026-05-06) exposed five
classes of performance and correctness gaps that prevent a subset of
TPC-H queries from completing within the 1-hour budget:

### Gap 1 — Non-correlated SubPlan cache miss (SubqueryCache key bug)

`SubqueryCache` in `internal/executor/context.go` keys results on the
**full outer row**. For non-correlated subqueries (zero `OuterColumnRef`
references), the result is identical for every outer row, but the cache
misses on every row because the outer row changes.

| Query | Symptom | Estimated cost |
|-------|---------|---------------|
| Q11 | HAVING scalar SubPlan re-evaluated ~8 K times | ~54 min |
| Q16 | NOT IN SubPlan re-evaluated ~800 K times (joined rows) | ~106 min; cancelled at 1248s |
| Q18 | IN SubPlan re-evaluated ~6 M times + unbounded RSS | timeout + OOM |
| Q22 | scalar avg SubPlan re-evaluated ~150 K times | ~2.5 h; cancelled at 53.5s |

**Fix:** detect zero-`OuterColumnRef` subqueries at planning time; use a
constant cache key (e.g., `""` or the subquery node address) so every
evaluation after the first is a cache hit.

### Gap 2 — EXISTS/NOT EXISTS not unnested to semi-join/anti-join

The planner evaluates `EXISTS`/`NOT EXISTS` correlated subqueries as
SubPlans per outer row instead of converting them to semi-join /
anti-join operators. Even when the inner scan uses an index
(`idx_lineitem_orderkey`), the per-row operator Open/Build/Close
overhead dominates at scale.

| Query | Outer rows | Inner index | Actual |
|-------|-----------|-------------|--------|
| Q4 | 1.5 M (orders) | idx_lineitem_orderkey | > 3600 s |
| Q21 | 6 M (lineitem) | idx_lineitem_orderkey | > 3600 s |

**Fix:** extend the M0040 unnesting pass to convert `EXISTS(subq)` →
semi-join and `NOT EXISTS(subq)` → anti-join when the subquery's
correlation predicate is an equijoin on a base-table key.

### Gap 3 — NUMERIC decode allocates *big.Int per column per row

HammerDB's TPC-H schema declares all integer-like columns as `NUMERIC`.
goopg's `parseNumeric()` allocates a `*big.Int` per column per row.
Measurement: Q17 (6 M lineitem rows × 16 cols × 400 ns/decode × 2
passes) = ~77 s. Actual Q17 = 70.4 s; model error ±11 %.

All compute-bound queries run at roughly half the speed they would if
integer columns were decoded via a fast `int64` path.

**Fix:** add an int4/int8 fast path to `parseNumeric()` that avoids
`*big.Int` allocation when the NUMERIC value fits in 64-bit integer
precision (the common case for TPC-H integer columns).

### Gap 4 — Q19 CROSS JOIN due to OR-of-ANDs join condition

Q19 has the form `WHERE (p_partkey=l_partkey AND ...) OR
(p_partkey=l_partkey AND ...) OR (...)`. The planner does not extract
the common equijoin key `l_partkey=p_partkey` from inside the OR, so
it falls back to `Nested Loop (CROSS)` with 6 M × 200 K = 1.2 T
estimated rows, filtered afterward.

**Fix:** enhance join-condition extraction to recognise shared equijoin
predicates across OR branches, converting the CROSS join to a Hash Join
keyed on the common predicate; the per-row OR filter is applied above.

### Gap 5 — Cancel does not propagate through NL/MHJ probe phase; TCP disconnect not wired

In addition to TCP disconnect not calling `queryCtx.Cancel()`, the
Nested Loop join (`operators_join.go`) and MHJ probe phase
(`multi_hash_join.go`) do not check `ctx.Err()` during iteration.
This means CancelRequest is also ignored once the query enters the
probe phase, even if the cancel registry fires correctly.

| Query | Duration before forceful kill | Root cause |
|-------|-------------------------------|------------|
| Q5 | 62 min | MHJ probe phase — no ctx.Err() check |
| Q13 | 58+ min | Nested Loop probe phase — no ctx.Err() check |

### Gap 5 (original) — TCP disconnect does not propagate to queryCtx

After a client disconnects or sends CancelRequest, the goopg server
goroutine detects the broken pipe only at the next network write. Until
that write, the backend continues consuming CPU. Observed: 167–178 %
CPU for 10+ minutes after cancellation during Q11/Q21.

**Fix:** when the server reads EOF or a CancelRequest on the client
socket, call `queryCtx.Cancel()` immediately so SubPlan/join loops
exit at the next `ctx.Err()` check (already present in all hot paths
after M0054 commits `a216093` + `f0b1c2c`).

## Sub-tasks

- **M0058-0001** Non-correlated SubPlan constant-key cache (~50 lines,
  high-impact, unblocks Q11/Q18/Q22)
- **M0058-0002** EXISTS/NOT EXISTS → semi-join/anti-join unnesting
  (~300 lines, unblocks Q4/Q21)
- **M0058-0003** NUMERIC int64 fast path in parseNumeric() (~200 lines,
  all queries ~50 % faster)
- **M0058-0004** OR-of-ANDs join condition extraction for Q19 (~100 lines)
- **M0058-0005** TCP disconnect → immediate queryCtx.Cancel(); also add ctx.Err() to
  Nested Loop and MHJ probe phases (~100 lines; unblocks Q5, Q13)
- **M0058-0006** WaitEventEnd hooks for I/O paths in open.go (~20 lines,
  observability only)
- **M0058-0007** Verification re-run: Q4, Q11, Q18, Q19 with all fixes

## Required Design Docs

- `docs/design/0058-0001-subplan-and-join-optimisation.md` — covers
  Gaps 1–4: cache-key fix, EXISTS unnesting, NUMERIC fast path,
  OR-of-ANDs join extraction, plus TCP cancel propagation (Gap 5).

## Definition of Done

- [ ] M0058-0001 merged; Q11, Q18, Q22 no longer trigger repeated SubPlan eval.
- [ ] M0058-0002 merged; Q4 and Q21 complete within 60 s via semi/anti-join.
- [ ] M0058-0003 merged; Q17 elapsed time decreases ≥ 30 % vs baseline 70.4 s.
- [ ] M0058-0004 merged; Q19 EXPLAIN shows Hash Join on l_partkey=p_partkey.
- [ ] M0058-0005 merged; CPU drops to idle within 5 s of CancelRequest.
- [ ] M0058-0006 merged; `pg_stat_activity.wait_event` is non-null during I/O.
- [ ] M0058-0007: verification run documents Q4/Q11/Q18/Q19 with new results.
- [ ] `go test ./...` passes with no regressions.

## ⚠ NO-DEFERRAL POLICY

Per project convention: blocked sub-tasks must carry `BLOCKED: <reason>`
in fix_plan.md and name a concrete follow-up. Silent close is not allowed.
