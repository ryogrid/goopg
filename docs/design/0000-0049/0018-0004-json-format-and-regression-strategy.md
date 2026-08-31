# 0018-0004 — TIMING/SUMMARY OFF Wiring + JSON Snapshot Strategy

**Status:** accepted (Stage B coverage)
**Milestone:** [0018 — EXPLAIN / EXPLAIN ANALYZE Support](../../milestones/0018-explain-and-explain-analyze.md)
**Spans seam:** explicit-set tracking on ExplainOptions, TIMING/SUMMARY
toggle wiring, deterministic JSON snapshot test strategy.
**Cross-links:**
[0018-0001](0018-0001-explain-parser-options-and-ast.md) (parser AST),
[0018-0002](0018-0002-static-plan-rendering-and-output-contract.md)
(static rendering),
[0018-0003](0018-0003-explain-analyze-instrumentation.md) (ANALYZE
instrumentation; this slice extends its TIMING/SUMMARY handling).

## Context

M0018-0003 hard-coded TIMING and SUMMARY to "always on" under
ANALYZE. Operators running `EXPLAIN (ANALYZE, TIMING off) ...` to
get reproducible output for tooling — or `SUMMARY off` to suppress
the timing footer — got no effect.

This slice closes the M0018 milestone by:

1. Tracking "explicitly set" on `ExplainOptions` so the executor
   can distinguish "user said off" from "user said nothing" — the
   latter defaults to true under ANALYZE matching upstream.
2. Wiring `Options.Timing` / `Options.Summary` through to the
   ANALYZE rendering path so the toggles actually affect output.
3. Documenting a deterministic JSON snapshot strategy and pinning
   it with one canonical regression-shape test.

BUFFERS / SETTINGS counter rendering needs Pool-level
instrumentation (per-statement page-pin / page-read counters) that
is its own substantial slice. That work is queued as a follow-up;
the milestone is "accepted" with M0018-0001..0004 covering parser
+ static + ANALYZE + JSON-snapshot strategy.

## ExplainOptions explicit-set tracking

Adding bool fields per-option for "was set" would double the
ExplainOptions surface. Instead, a single companion struct:

```go
type ExplainOptionsSet struct {
    Analyze, Verbose, Costs, Buffers, Settings, Timing, Summary, Format bool
}
```

`ExplainStmt` grows an unexported `setFlags ExplainOptionsSet`
field with a `Set()` accessor (the executor consumes it). The
parser flips the corresponding bit when an option name appears in
the statement (keyword or parenthesised form). Pre-M0018 callers
keep using the bare form which sets nothing.

## TIMING/SUMMARY toggle semantics

Under ANALYZE:

```
effectiveTiming  = !setFlags.Timing  || opts.Timing
effectiveSummary = !setFlags.Summary || opts.Summary
```

i.e. ON by default; user-set OFF wins.

Under non-ANALYZE: TIMING and SUMMARY are no-ops (matches upstream).

When `effectiveTiming` is false, the instrumentation wrapper still
counts rows / loops but skips `time.Now()` calls — `nodeStats.timing
== false` suppresses the `time=` bracket in TEXT mode and the
`Actual Total Time` / `Actual Startup Time` keys in JSON mode.

When `effectiveSummary` is false, the trailing
`Planning Time:` / `Execution Time:` rows (TEXT) and root-level
`Planning Time` / `Execution Time` keys (JSON) are suppressed.

## JSON snapshot regression strategy

The JSON shape is now stable enough to support snapshot tests.
Strategy: a single test parses `EXPLAIN (FORMAT JSON) SELECT 1`
into the expected stable shape (Node Type + nested Plans only,
no Plan Rows / Actual* / timing) and asserts the parsed JSON
matches. The shape contract is:

```json
[
  { "Node Type": "<label>", "Plans": [ ... ] }
]
```

Optional keys (`Plan Rows`, `Output`, `Actual Rows`, `Actual
Loops`, `Actual Total Time`, `Actual Startup Time`, `Planning
Time`, `Execution Time`) appear only when their gating option /
counter is active. The test asserts the **required** keys exist
and the **gated** keys are absent without their option.

Future BUFFERS / SETTINGS slices add their keys without
disturbing this shape — the snapshot is structural, not exact-byte
comparison.

## Tests

- `TestExplainAnalyzeTimingOffSuppressesTimeBracket`:
  `EXPLAIN (ANALYZE, TIMING off) SELECT 1` TEXT output contains
  `rows=N loops=N` but no `time=`.
- `TestExplainAnalyzeSummaryOffSuppressesFooter`:
  `EXPLAIN (ANALYZE, SUMMARY off) SELECT 1` text has no
  `Planning Time:` / `Execution Time:` rows.
- `TestExplainAnalyzeTimingOffJSONOmitsTimeKeys`:
  the JSON form omits `Actual Total Time` / `Actual Startup Time`
  keys when TIMING off.
- `TestExplainAnalyzeSummaryOffJSONOmitsTimingKeys`:
  the JSON form omits root-level `Planning Time` / `Execution
  Time` keys when SUMMARY off.
- `TestExplainAnalyzeTimingDefaultsOnUnderAnalyze`: pins that
  the default-true semantics under ANALYZE survive — without
  `TIMING off`, time= is still rendered.
- `TestExplainJSONShapeStable`: structural snapshot for a
  stable known query. Asserts shape contract (required keys
  present, gated keys absent) for `EXPLAIN (FORMAT JSON) SELECT 1`.

## Out of scope

- BUFFERS counter rendering — needs Pool-level per-statement
  page-pin / page-read counters; queued as a follow-up.
- SETTINGS rendering — needs GUC snapshot at statement start.
- Cross-version JSON byte-stable snapshots for VERBOSE +
  ANALYZE compositions — exact-byte snapshots against
  expected files are noisy across architectures (timing
  values drift); structural snapshot is the right contract.
