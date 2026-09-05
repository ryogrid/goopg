# E-04 (EX4-01) — `filterOp` predicate compilation: measured, DROPPED

Date: 2026-09-05. Item: `docs/design/not_ralph/minimize_datum/TODO_ALL.md`
E-04 (take3 `13-executor-target-design.md` §6, EX4-01). Outcome: **dropped
under SKIP policy rule 1 (no measured performance gain)**, code reverted,
ledger row `take3-E-04-dropped`.

## Measurement protocol

Fresh capped server per arm (`scripts/tpch-acceptance-arm.sh`), TPC-H SF=1
on 65433, GOGC=100 / GOMEMLIMIT=12GiB, serial, S-cold, `work_mem` 64 MB,
statistics pinned (`GOOPG_ANALYZE_SEED=20260905`, autovacuum off — see
`docs/design/planner-gate-reproducibility/DESIGN.md`). Values checked with
`tpch-runner -digest` + `-diff` on every arm: **24/24 MATCH throughout**;
plan captures byte-identical; no plan moved.

Two baseline arms were run, before and after the change arms, so that
baseline drift is visible rather than assumed. **They agree to within 1%
on every query**, which is what makes the Q18 result readable.

## What was implemented

`filterOp.Open` compiled its predicate into the existing `exprTreeSlab`
(the same treatment `seqScanOp` already gives its prefilter) and `Next`
evaluated through `evalFastExpr`. Three successive variants were measured:

1. **compile per Open** — the naive form.
2. **+ slab cached across re-Opens**, keyed on the `Context` pointer (a
   `filterOp` on the inner side of a nested loop is re-Opened per outer row,
   so rebuilding per Open pays a build cost per row).
3. **+ decline an `ExprAdapter` root** — when `buildExprCtx` compiled
   nothing (the whole predicate is a SubPlan / IN-subquery / CASE / IS NULL),
   the compiled path is `evalExprSlot` plus one extra indirection per row.

## Result (seconds, serial)

| Q | base A | base B | v1 compile | v2 cached | v3 declined |
|---|---|---|---|---|---|
| Q18 | 31.93 | 31.87 | 34.62 | 34.48 | **34.82** |
| Q5 | 21.60 | 21.01 | 21.06 | 20.36 | 20.77 |
| Q7 | 15.72 | 15.43 | 15.57 | 14.86 | 15.49 |
| Q9 | 13.17 | 12.72 | 12.62 | 12.80 | 12.67 |
| Q21 | 12.80 | 12.79 | 12.65 | 12.79 | 12.55 |
| Q12 | 12.71 | 12.57 | 13.07 | 13.12 | 12.84 |
| Q1 | 7.15 | 7.18 | 7.68 | 7.12 | 7.33 |
| Q3 | 6.25 | 5.68 | 5.64 | 6.20 | 5.63 |
| Q13 | 5.16 | 5.05 | 5.11 | 5.03 | 5.04 |

(The remaining twelve queries all sit under 2.7 s and move by less than
0.05 s in every arm; full table in this directory's source data.)

## Reading

- **No query improved outside the noise band in a repeatable way.** Q5 and
  Q7 look better in v2 but not in v3; Q3 looks better in v1 and v3 but not
  v2. The two baselines bracket those swings, so they are drift.
- **Q18 regressed by 8.5%, consistently, in all three variants** (34.48 /
  34.62 / 34.82 against 31.93 / 31.87). Two baselines agreeing to 0.2%
  either side of three change arms agreeing to 1% is not noise.
- The regression **survived the adapter-root decline**, so it is not the
  wrapper indirection that variant 3 was built to remove. It was not chased
  further, because even eliminating it entirely would leave the item at
  "no gain".

## Why this was the expected outcome, in hindsight

EX4-01's own predicted effect was **~0.33 percentage points** of CPU
(take3 13 §6, citing the take7 prefilter measurement). The measurement
protocol's noise band is ±17% by ground rule 4, and observed run-to-run
drift on this host is ±3%. **The predicted win is an order of magnitude
below the measurement floor**, so the item could not have produced a
readable gain even if it worked perfectly. That is a property of the item,
not of this attempt.

The mechanism also overlaps work already done: `seqScanOp`'s prefilter
(`scan_prefilter.go`) already compiles and applies the same predicate
*before* deformation on scan-adjacent filters, which is where the hot rows
are. The `filterOp` above such a scan sees only survivors, so compiling it
a second time optimises the cold path.

## What was kept

Nothing in `internal/executor/`. The code is reverted; this report and the
ledger row are the artifacts. The twin-parity test harness written for the
attempt (both arms driven over the same rows, compared on survivors and on
error text, with a guard that fails if the compiled arm was never
exercised) is described here so a future attempt does not re-derive it —
it caught a real defect during development: `IsNullExpr` compiles to an
adapter root, so a naive "compile everything" guard would have silently
tested the interpreter against itself.

## If this is ever resumed

Resume only with a witness shape where a `filterOp` predicate is
demonstrably hot and NOT already covered by the scan prefilter — for
example a filter above a join or aggregate over millions of rows with a
multi-term arithmetic predicate. Measure that shape directly rather than
the suite total, since the suite cannot resolve a 0.3 pp effect. Also
re-check the Q18 regression's cause before re-landing: an unexplained 8.5%
on a semi-join shape is a finding in its own right, whatever happens to
EX4-01.
