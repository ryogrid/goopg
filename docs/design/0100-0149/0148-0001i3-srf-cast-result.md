# M0148\-0001i3 — a cast over an SRF prints as a Result above the ProjectSet

**Parent:** M0148\-0001i2 (split: part 2) · **Status:** landed 2026\-11\-04;
closed

## Divergence

aggregates.sql v\_pagg\_test (and any `unnest(a)::int` sorted above):

```
PG 18.3                                                  goopg (before)
Sort                                                     Sort
  Sort Key: pagg_test.y, (((unnest(...)))::integer)        Sort Key: pagg_test.y, t
  ->  Result                                               ->  ProjectSet
        ->  ProjectSet
```

## PostgreSQL

`split_pathtarget_at_srfs` (tlist.c) splits the target list into levels:

- the ProjectSet evaluates the bare set\-returning function;
- an expression over the SRF's output, here the cast, is evaluated in a
  Result above it.

A sort key on that column is an OUTER\_VAR into the Result's targetlist.
ruleutils parenthesises the non\-Var referent once for the inner
`unnest(...)` OUTER\_VAR, once inside the cast's `(…)::integer`, and once
for the key itself: `(((unnest(st.a)))::integer)`.

## Change

goopg's ProjectSet evaluates both levels: `UnnestCol.CastType` casts each
element, and `Wrappers` handles other enclosing expressions. Display only:

- **`UnnestCol.ExplicitCast`** (planner). It is set only when the SQL
  wrote a cast over the unnest that changes the element type. `CastType`
  alone is also used for the inferred element type, so a bare
  `unnest(integer[])` must not get a Result. A cast to the element's own
  type is a no\-op coercion the parser drops, so it doesn't count either.
- **`srfNeedsResultAbove`** (EXPLAIN text walker, plain and ANALYZE). A
  child printed as a ProjectSet with an explicit\-cast unnest, a wrapped SRF
  or wrappers gets a synthesised `Result` line above it, the way the Hash
  node is synthesised over a hash join's build side (`emitHashNodeLine`).
- **Key chase.** `resolveKeySourceDepth`'s ProjectSet arm maps a key on
  such a column to `(((unnest(ARR)))::type)`. ARR's own columns are chased
  into the ProjectSet's input (`(string_agg(...))` for v\_pagg\_test).

## Verification

- `TestExplainSRFCastResult` pins PG 18.3's plan for a cast unnest under
  a Sort, and the absence of a Result for a bare unnest.
- **Regress runner.**
  - aggregates: 31 → 20 lines. The only difference left in the suite is
    the `','::text` literal label (M0148\-0001f3).
  - tsrf, rangefuncs, arrays, subselect, union, json, jsonb and select are
    unchanged. A first cut keyed on `CastType` added false Results in
    tsrf and subselect; `ExplicitCast` fixed that.
- `TestPort_RegressSuite`, `TestPort_PgDump*` and `TestPort_Isolation*`
  pass. The TPC gates show no change.

## Not covered

- **JSON EXPLAIN** does not synthesise the Result.
- **VERBOSE** prints no Output for the synthesised Result.
- **Typed literal labels** in function and aggregate arguments (`','::text`)
  need a builtin signature table (M0148\-0001f3).
