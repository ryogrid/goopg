# M0146-0123 — fold an immutable built-in over constant arguments at plan time

Status: done 2026-10-09 (157eaf6d8). Parent: M0146-0007h.

## Problem

M0146-0007h's ledger row (2) recorded that goopg does not fold immutable
calls over constants. A scratch probe against PG 18.3 showed the gap across
EXPLAIN:

| query fragment | PG 18.3 | goopg before |
|---|---|---|
| `a = abs(-1)` | `Filter: (a = 1)` | `Filter: (a = abs('-1'::integer))` |
| `b = length('abc')` | `Filter: (b = 3)` | `Filter: (b = length('abc'))` |
| `a::text = upper('x')` | `= 'X'::text` | `= upper('x')` |
| `a = 1 AND length('abc') = 4` | `Result  One-Time Filter: false` | a gate over `Seq Scan` |

`FoldConstants` (foldconst.go) folded operators, casts and CASE, but only
recursed into a `FuncCall`. So the estimator saw a non-constant, and a
constant-false call stayed a per-scan gate.

## PG behaviour

`eval_const_expressions` → `simplify_function` → `evaluate_function`
(clauses.c) folds a call into a Const when all of these hold:

- the function is `provolatile = 'i'`, or `'s'` in estimate mode only;
- it is not `proretset`;
- it is a plain function;
- every argument is a Const.

A strict function with a NULL Const argument folds to a NULL Const. An
error raised during evaluation surfaces at plan time.

## Change

- **Overload flags.** `cmd/gen-pg-proc-data` now parses `prokind` and emits
  `pgProcFoldableOIDs`: the overloads with `provolatile` 'i', no
  `proretset` and `prokind` 'f'. `catalog.ProcIsFoldable` reads it.
  - The decision is per overload. `length(text)` folds even though
    `length(bytea, name)` is stable.
  - Aggregates such as `max(1)` are immutable in pg_proc and are excluded
    by `prokind`.
- **Generated file.** The block was hand-merged into
  `pg_proc_names_generated.go` rather than regenerating the whole file. A
  full regeneration would drop two hand-added stats entries (OIDs 8101 and
  8102) that the generator does not know about.
- **`tryFoldFuncCall`** (foldconst.go) resolves the overload from the
  literal arguments' exact types with `LookupProcForNode`, as
  `ExprResultType` does. It declines:
  - star, variadic or DISTINCT calls;
  - UDF-stamped calls;
  - schema-qualified names;
  - non-literal or NULL arguments.
- **Evaluation hook.** The executor registers `optimizer.EvalConstFunc`
  (fold_func.go). The call runs through the `evalFuncCall` dispatch a row
  uses, with a bare Context, so the folded value is the run-time value.
- **Result types.** Only int2/int4/int8, numeric, bool and text/varchar
  results fold. Those are the Datums that round-trip exactly through a
  literal node (`IntegerConst`, `Wide` for int8, `NumericConst`,
  `BooleanConst`, `TypedStringLit`). An evaluation error, a panic or a NULL
  result declines.

## Verification

- **Test.** `TestFoldConstantsEvaluatesImmutableBuiltins` covers abs,
  length, upper, nested folding, and the six kept-as-call cases.
- **Scratch probe.** Every row of the table above now prints as PG. The
  folded `(b > 5) AND (a = 6)` qual order also matches PG.
- **TPC-DS fire set.** No plan changed at either scale.
- **TPC-H.** Plans byte-identical.
- **Regress A/B** over 32 cases. The only changes are row-order flips in
  `join` and `stats_ext`; no plan or value changed.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  (96/96) and ea-ratchet (7) all PASS.

## Not covered (ledgered)

- Float, date/time and other result types. goopg's float function bodies
  (e.g. `sqrt`) do not produce PG's float8 value, so a float literal would
  change results.
- Strict-call NULL folding and plan-time errors.
- Schema-qualified calls (`pg_catalog.abs`).
- The non-pg_proc constructs: COALESCE / NULLIF / GREATEST / LEAST.
- Stable functions in estimate mode (`estimate_expression_value`).
- A constant-FALSE LEFT JOIN ON clause. PG prints `Join Filter: false`
  over a dummy `Result` inner; goopg prints `Join Filter: (false)` over the
  scan.
