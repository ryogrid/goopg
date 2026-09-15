# M0142-0004a — EXPLAIN ANALYZE's `rows=` must be a per-loop average, not a cumulative total

Status: accepted (landed 2026-09-15)

## Task

`.ralph/fix_plan.md`'s M0142-0004a line. M0142-0004 filed this as a recon
task: 19 of its 140 `ea-ratchet` findings are `Index Scan using X_pkey`
nodes goopg estimates at `est=1` (a deliberate, PG-faithful unique-equality
special case in `indexScanRows`), several of which carry a PG-side estimate
that is *also* far from 1 for the same named index on the same table — a
signature inconsistent with both engines pricing the same single-probe
operation. The task's own leading hypothesis: the scan runs once per outer
row of a correlated context, and EXPLAIN ANALYZE's `rows=` is a per-loop
average that the capture/scoring tooling might be reading as a bare total.
Concrete next step named: read one witness's raw `EXPLAIN (ANALYZE, ...)`
output and check `loops=`.

## Method

Read the raw capture line for the task's named witness directly (not through
the JSON summary), from the artifact M0142-0004 already produced:
`analysis/planner-refactor-take3/c20a-estimator-census-20260915/ea-capture-20260915.txt:2520`
(`===== Q34 =====` block):

```
->  Index Scan using store_pkey on store  (cost=0.00..0.01 rows=1 width=676) (actual rows=9969.00 loops=10082)
```

`loops=10082 > 1`, confirming the hypothesis's premise. Two candidate causes
remained: the capture/scoring script (`scripts/estimate-parity/parity.py`),
or the planner's own `indexScanRows`. Read both before touching either:

- `scripts/estimate-parity/parity.py:48-52` (docstring) already states and
  implements PG's convention correctly — it reads whatever `arows=` the
  captured text carries and never multiplies/divides by `loops` itself,
  because upstream's own annotation is defined to already be the per-loop
  average. This ruled out the scoring script.
- `internal/executor/operators_explain.go` (former lines 2472-2475,
  2718, 2606): the text, JSON, and per-worker renderers all printed
  `float64(s.rowsOut)` directly, where `rowsOut` (`instrument.go`'s
  `nodeStats`) is a running total that accumulates across every Open/Next
  cycle and is **never reset on re-Open** — i.e. the *cumulative* total
  across all `loops` executions, not one execution's worth.
- `postgres/src/backend/commands/explain.c:1835` (`ExplainNode`, text/plan
  path) and `:1901` (structured-format path): `double rows =
  planstate->instrument->ntuples / nloops;` — confirms PG's own `rows=` is
  the cumulative `ntuples` divided by `nloops`, i.e. the average per loop.
  Notably, `operators_explain.go`'s own pre-existing comment at line 2444
  already stated this ("`rows` is `instrument->ntuples / nloops`
  (explain.c:1835)") — the renderer's documentation was correct and the
  implementation simply never carried it out.

This is a genuine ENGINE bug, not a capture/scoring artifact and not a
planner cardinality-estimator defect: `indexScanRows`'s `est=1` was never
wrong for the operation it estimates (a single index probe per loop); the
`actual=9969.00` printed alongside it was wrong, because it was really
`9969` total probes' worth of output summed across `10082` loops rather than
one loop's average (`9969/10082 ≈ 0.99`, which is what a correct renderer
would have printed — matching `est=1` almost exactly).

## Fix

Added a shared helper in `operators_explain.go`:

```go
func rowsPerLoop(rowsOut, loops int64) float64 {
	if loops <= 0 {
		return float64(rowsOut)
	}
	return float64(rowsOut) / float64(loops)
}
```

(`loops<=0` — an unexecuted node — returns the raw value rather than
dividing by zero; `rowsOut` is 0 in that case in practice, matching the
existing "loops=0 is NOT zero rows" convention documented elsewhere in the
same file for parallel worker build sides.)

Wired all three sites that previously used the raw `rowsOut`/`RowsOut`
through it: the text renderer's `(actual ... rows=%.2f loops=%d)` line, the
JSON/XML/YAML `obj["Actual Rows"]` field, and the per-worker `Worker N:
...` text line.

The JSON path needed one more correction: PG's structured-format "Actual
Rows" is emitted via `ExplainPropertyFloat(qlabel, unit, value, 2, es)`
(`explain_format.c:250`, `psprintf("%.*f", ndigits, value)`) — rounded to 2
decimal digits before serialization. The text renderer gets this for free
from `fmt`'s own `%.2f` verb, but a `map[string]any` float64 handed to
`encoding/json` prints at full precision (e.g. `0.9887919063677841` instead
of `0.99`). Added `round2(v) = math.Round(v*100) / 100` and applied it only
at the JSON insertion site.

## Verification

- `TestRowsPerLoopIsPGsPerLoopAverage` and
  `TestRound2MatchesExplainPropertyFloatNdigits2`
  (`internal/executor/explain_analyze_test.go`) pin both helpers directly,
  including the exact q34 `store_pkey` witness (`9969, 10082 -> 9969/10082`).
- Full `internal/executor` suite green (`go test ./internal/executor/...`).
- `scripts/tpch-spotcheck.sh`: PASS (Q12=2, Q13=34) — this change touches
  only EXPLAIN ANALYZE's *reporting* of actual rows, not any executed
  query's row-producing logic, so no row-count regression is expected or
  was observed.

An end-to-end SQL-level regression test (build a real multi-loop node via
`EXPLAIN ANALYZE` over live SQL and assert on the rendered `rows=`) was
attempted but not landed:

- A correlated scalar SubPlan
  (`t1.b > (SELECT t2.b FROM t2 WHERE t2.a=t1.a LIMIT 1)`, 3 outer rows,
  1 exact match per outer row) does reach `loops=3` per its own
  `calls=3 rebuilds=1 rescans=2 hits=0 misses=3` summary line (a *different*
  stats mechanism, `SubPlanSiteStats`), but its inner plan nodes (`Limit`,
  `Seq Scan on t2`) render with **no** `(actual ...)` annotation at all under
  this harness — so the fixture could not exercise the fix either way. Filed
  as a new deferral-ledger row (unverified mechanism, not chased further per
  "one task per loop") rather than fixed here.
- A GUC-forced plain Nested Loop (`SET enable_hashjoin = off`) was tried as
  an alternative vehicle; `SET` is not supported at the raw-executor
  (`runDDL`) test-fixture layer used by this package's unit tests.

The two direct unit tests above were judged sufficient given (a) the exact
math is now centralized in one pure, trivially-testable function used by
all three call sites (eliminating the sibling-path-disagreement risk a
duplicated inline computation would carry), and (b) the live q34 witness
read during diagnosis independently confirms the fix's effect on real
captured output.

## Follow-up

**M0142-0004c** (filed in `.ralph/fix_plan.md`): re-run `make ea-ratchet`
to confirm the 19 C1 findings collapse below the `qerr>=10` flag threshold
post-fix, and check whether any of the other 121 qerr>=10 findings in the
2026-09-15 census also shared this `loops>1` shape without being named
(this task's own scope was the one C1 mechanism, not an exhaustive
`loops=` audit of the whole finding set).

## Gate-gap note

`scripts/pg-plan-parity-diff.py`'s nine categories carry no `rows=`
dimension, so this bug (and its fix) is invisible to the plan-parity gate —
it only ever reached `make ea-ratchet`, and only insofar as that gate reads
the buggy `actual` value as ground truth. Real PG-compatibility users
running `EXPLAIN (ANALYZE)` by hand over any correlated-subplan or
NL/NLI-inner-side query would have seen the inflated `rows=` directly; no
gate in this repository would have caught that independent of this
diagnosis.
