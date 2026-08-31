# M0134-0022 — window.sql: unify the window-function aggregate gates

**Status:** implemented (partial — one bucket of `window.sql`; the case remains
`failed`). **Date:** 2026-08-20. **Milestone:** M0134-0022.

## Problem

`scripts/pg-regress-runner.sh --verbose window` FAILS at HEAD: 4575 diff lines,
90 `^+ERROR`, 23 `^-ERROR` (`tmp/regress-diffs/window.diff`). The single largest
*contained* root cause is that goopg rejects most ordinary aggregates when they
are used as window functions — `var_pop`, `var_samp`, `variance`, `stddev`,
`stddev_pop`, `stddev_samp`, `bool_and`, `bool_or`, `array_agg` and user-defined
aggregates — even though every one of them is already implemented as an ordinary
aggregate in `internal/executor/operators_join_agg.go`.

PostgreSQL has no allow-list here. Window-capability is derived from the general
aggregate machinery: `postgres/src/backend/parser/parse_func.c:ParseFuncOrColumn`
resolves the name through `pg_proc`/`pg_aggregate` and
`postgres/src/backend/parser/parse_agg.c:transformWindowFuncCall` accepts *any*
aggregate over a window, rejecting only non-aggregate, non-window functions
("OVER specified, but %s is not a window function nor an aggregate function").

## Why the naive fix reduces zero diff lines

The obvious fix — widen the `default:` case of
`analyzeWindowFuncCall` (`internal/parser/analyzer/analyzer.go:1733`) — changes
nothing measurable. There are **three** independent gates in series, and the
second and third re-reject with a near-identical message:

| # | site | message on reject |
|---|---|---|
| 1 | `internal/parser/analyzer/analyzer.go:1614-1733` `analyzeWindowFuncCall` | `window function "X" is not supported in v0 analyzer` |
| 2 | `internal/executor/operators_window.go:325` `evalWindowFuncs` switch → `:427` `default:` | `window function is not supported in v0 executor` |
| 3 | `internal/executor/operators_window.go:440-446` `isFrameAggWindowFunc` | (silently excludes from the explicit-frame aggregate path) |
| 4 | `internal/optimizer/planner.go:6154` `buildWindowFunc` `default:` | planner-side twin of gate #1 |

Gate #4 was **not** in the original design — it was found during implementation,
because the new guard tests still FAILED after all three designed gates were
widened. It is the same failure mode the design warns about, one hop further
down: the planner has its own copy of the analyzer's window-function switch.
The lesson generalises past this slice: "N gates in series" is only ever a lower
bound until the guard test actually passes.

Widening #1 alone rewrites `+ERROR: ... in v0 analyzer` into
`+ERROR: ... in v0 executor` — same line count, still a diff, **net reduction 0**.
This is the loop's central finding and the reason the design is a *three-site*
change (Hard-won Rule #2, sibling paths).

## Name lists are duplicated in six places

There is no aggregate registry. The standard-aggregate name set is written out
independently at:

1. `internal/parser/select.go:455` `isParserAggregateName` — complete
2. `internal/optimizer/planner.go:7883` `isAggregateFunc` — complete
3. `internal/parser/analyzer/analyzer.go:545` `isAnalyzerAggregateName` — complete
   (its docstring already acknowledges the duplication)
4. `internal/parser/analyzer/analyzer.go:1614-1733` — **restricted window subset**
5. `internal/executor/operators_ddl_partition.go:900-911` `isKnownAggregate` — complete
6. `internal/executor/operators_window.go:440-446`/`:325` — **restricted window subset**

Sites 1/2/3/5 already agree with each other. Only the two window-specific gates
(4 and 6) are stale subsets. The fix therefore does **not** introduce a new
registry: gate #4 delegates to the existing `isAnalyzerAggregateName` (same
package, same file) and gate #6 delegates to a shared executor-side helper with
the same content as `isKnownAggregate`. Collapsing all six into one registry is a
larger refactor and is ledgered, not done here.

## Design

1. `analyzeWindowFuncCall` keeps its explicit `switch` for the true *window*
   functions (`row_number`, `rank`, `dense_rank`, `lag`, `lead`, `first_value`,
   `last_value`, `nth_value`, `ntile`, `cume_dist`, `percent_rank`) because those
   carry window-specific arity/argument validation. Its `default:` case no longer
   rejects outright: it accepts the name when `isAnalyzerAggregateName` says it is
   an aggregate, and only otherwise raises. The rejection wording for a genuine
   non-aggregate (e.g. `generate_series`) is unchanged in this slice — matching
   PG's wording is a separate, wording-only bucket.
2. `isFrameAggWindowFunc` accepts any aggregate name, so aggregate window calls
   with an explicit frame reach `evalExplicitFrameAggFuncs`.
3. `evalWindowFuncs`'s per-row switch no-ops for any `isFrameAggWindowFunc` name —
   the value has already been produced by `evalFrameAggFuncs` /
   `evalExplicitFrameAggFuncs` through the name-agnostic bridge
   `windowFuncToAggregateCall` (`internal/executor/operators_window.go:651`),
   which forwards to the shared `applyAgg`/`finishAgg`.
4. `buildWindowFunc` (`internal/optimizer/planner.go:6154`) accepts and resolves the
   same set. `isAggregateFunc` (`:7883`) is refactored into its unchanged
   `fc.Over != nil` guard plus a new pure-extraction helper `isAggregateFuncName`,
   which `buildWindowFunc` calls; `isAggregateFunc`'s own behaviour is unchanged, so
   its nine GROUP BY/HAVING call sites are untouched (audited).

## Known limits (recorded, not fixed here)

- **No moving-aggregate inverse transitions.** PG evaluates a moving frame with
  `msfunc`/`minvfunc` (`postgres/src/backend/executor/nodeWindowAgg.c:eval_windowaggregates`);
  `evalExplicitFrameAggFuncs` (`internal/executor/operators_window.go:601`)
  recomputes from scratch. `window.sql`'s `logging_agg_strict` /
  `logging_agg_nonstrict` / `sum_int_randomrestart` cases assert the literal
  inverse-transition call trace, so those ~131 diff lines change divergence class
  rather than vanishing.
- `LANGUAGE internal` user-defined aggregates (`nth_value_def`) stay unreachable —
  blocked upstream by a `CREATE FUNCTION` default-argument syntax gap.
- The `pg_temp.f()` inlined-SQL-function window query may land on the separate
  `column ref b/1 out of Slot range 1` bug (`internal/executor/expr.go:390`).

## Result

`window` still FAILS and the case stays `failed` in the CSV (no status change ⇒ no
`make regen-testport`). Measured: **4575 → 4604 diff lines, 90 → 64 `^+ERROR`,
23 → 23 `^-ERROR`**. The line count rose ~29 while the error count fell 26 — the
correct reading is that single-line `ERROR:` rejections became multi-row *value*
comparisons now that the queries execute. `bool_and`/`bool_or` and `array_agg`'s
frame case are byte-identical to the oracle; the variance/stddev family now runs
but diverges on numeric display scale (a pre-existing bug, reproducible without
`OVER`).

Two of the design's three "known limits" were confirmed by execution; the third
was refuted — `pg_temp.f()` does **not** hit the `expr.go:390` slot-range bug.
All are ledgered in `.ralph/deferral_ledger.md` (2026-08-20, M0134-0022).
