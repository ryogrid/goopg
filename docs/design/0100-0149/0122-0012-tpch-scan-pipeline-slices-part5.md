# M0122-0012 — scan pipeline slices, part 5 (TPC-DS executor gaps)

Continues [0122-0012-tpch-scan-pipeline-slices-part4.md](0122-0012-tpch-scan-pipeline-slices-part4.md) (slices
34–45). Split out on 2026-10-22 under the 800-line design-doc limit; slice numbering is unchanged. Targets still
come from the per-query gap census of the TPC-DS SF0.25 corpus against the `:65438` reference (PG warm times,
goopg's latest sweep), and TPC-H is re-measured for every slice.

## Slice 46 (2026-10-22): integer variance and stddev are PG's numeric_stddev_internal — TPC-DS Q39

**Target, named up front:** TPC-DS Q39, 2.5 s in the sf025 sweep against PG's 0.59 s. It is two statements, and
each one's time is a CTE: a hash aggregate of 390 000 inventory rows into ~90 000 (warehouse, item, month)
groups, each computing `stddev_samp` and `avg` of an integer column. PG computes that CTE in ~0.39 s; goopg took
~2.05 s.

**Cause.** `exactIntVariance` was 28 % of the profile. It is the integer-input finisher for `var_pop`,
`var_samp`/`variance` and `stddev_pop`/`stddev_samp`/`stddev`, and it did two things:

- **The square root.** It ran a 15-step Newton-Raphson in 128-bit `big.Float`, allocating three Floats per step,
  for every group. Its `big.Float.Quo` alone was 15 % of the query.
- **Not PG's digits.** The variance was printed to 12 decimals with trailing zeros dropped, and the stddev to 18
  significant digits. PG prints both at `select_div_scale`'s rscale:

  | aggregate | goopg | PG |
  |---|---|---|
  | `var_samp(1,2,4)` | `2.333333333333` | `2.3333333333333333` |
  | `stddev_samp(1,2,4)` | `1.52752523165194667` | `1.5275252316519467` |
  | `var_pop` of five bigints | `21704` | `21704.000000000000` |

  The window-function test had pinned goopg's text as a known, ledgered gap: the 2026-07-27
  "tpcds-round2 stddev-precision" row.

PG's path for integer input is `int2/int4/int8_accum` → `numeric_poly_*` / `numeric_*` → `numeric_stddev_internal`
(`numeric.c`), with sumX and sumX2 at display scale 0.

**Rule.** `exactIntVariance` is now that function, entirely in exact `big.Int` arithmetic:

1. NULL for N = 0, or N ≤ 1 for the sample forms, checked before anything else.
2. `num = N·sumX2 − sumX²`. A non-positive numerator gives `0` at display scale 0, as PG's
   `make_result(&const_zero)` does.
3. `den = N·(N−1)`, or `N²` for the population forms.
4. `rscale = select_div_scale(num, den)` (`pgSelectDivScaleInt`):
   - take the NBASE (10⁴) weights `w1`, `w2` and leading NBASE digits `f1`, `f2`;
   - `qweight = w1 − w2`, minus 1 when `f1 ≤ f2`;
   - `rscale = 16 − 4·qweight`, clamped to [0, 1000].
5. **Variance:** `div_var(num, den, rscale, round)`, i.e. the quotient rounded half away from zero at rscale.
6. **Stddev:** `sqrt_var(variance, rscale)` (`pgSqrtScaled`).
   - PG's Karatsuba square root yields the exact truncated square root at F fractional NBASE digits:
     `F = res_ndigits − res_weight − 1`, which is `rscale/4 + 1`, or more when the clamp to one result digit
     applies.
   - `round_var` then rounds that to rscale.
   - Here that is `big.Int.Sqrt` (an exact floor square root) of the variance scaled by 10^(8F), followed by the
     same half-away rounding.

The result is a numeric Datum at scale rscale, as `avg(int)` already returns, instead of a string.

**Pins.**

- `TestExactIntVarianceMatchesPGDigits` holds eleven cases whose wants were read back from PG 18.3. They include
  negatives, a two-row stddev that keeps 16 decimals, an int8-extreme group whose root rounds to an integer, and a
  variance past int64. HEAD fails nine of them.
- `TestWindowAggregateFunctions`' variance/stddev cases now assert PG's `window.out` text instead of goopg's.
- `TestExactIntVarianceZeroAndDegenerate` keeps the Q39 zero-variance crash guard. It now reads the result through
  `Format()` and expects PG's `2.0000000000000000` for `var_samp(1,3)`.
- A live probe matched PG byte for byte on all 696 output lines: 11 variance/stddev forms over ~680 `store_sales`
  groups (int2, int4 and int8 inputs, negatives, products past int64) plus `date_dim` groups and edge rows.

**Measured.** TPC-DS Q39, warm, two interleaved rounds of three runs:

| build | Q39 (s) |
|---|---|
| HEAD | 3.01–3.89 |
| candidate | 2.10–2.40 |

In the sf025 sweep, Q39 went from 2.53 to 1.89 s, with its checksum equal to the PG oracle's as before (the
checksum normalises numeric text, which is why the old digits never tripped it). The sweep sum went from 125.2 to
120.8 s. TPC-H values are identical (acceptance 24 MATCH), and the sweep's plan shapes are identical.

**Left.**

- **Numeric input.** `exactNumericVariance` keeps `big.Rat` accumulators without the inputs' display scale. PG
  starts from `sumX.dscale`, so that path cannot be ported exactly until its state carries it. It still prints
  12 decimals / 15 significant digits.
- **Float input.** The float path prints with `strconv 'g' 15`, not `float8out`.
- Both are recorded in the deferral ledger.

## Slice 47 (2026-10-22): CASE compiles — TPC-DS Q59, Q2

**Target, named up front:** TPC-DS Q59, 2.2–2.4 s warm against PG's 0.72 s, the largest absolute gap in a fresh
warm re-time of the ratio candidates. The others re-timed were Q78 1.65 vs 0.49 s, Q97 1.05 vs 0.11, Q16 0.97
vs 0.08, and Q8 1.0 vs 0.14.

**Cause.** EXPLAIN ANALYZE put 2.27 s of Q59 in its CTE `wss`, against PG's 0.55 s. The CTE is a hash aggregate
of 687 k `store_sales ⋈ date_dim` rows computing seven
`sum(CASE WHEN d_day_name = 'Sunday' THEN ss_sales_price ELSE NULL END)` columns.

`evalCaseExpr` took 46 % of the profile, and `runtime.convTslice` 11 %. CASE was not in the compiled evaluator:
`buildExpr` made it an ExprAdapter, so each of the 4.8 M CASE evaluations ran through
`evalExprSlot → evalCaseExpr(x, slotToRow(slot), ctx)`. That flattened the slot into a Row, then boxed the Row
into a fresh `rowSlotView` interface for every WHEN and THEN it evaluated.

**Rule.** A new compiled kind, `ExprCase`, carries an `exprCaseInfo` built at slab-build time:

- the slab indices of the operand (noExpr for the searched form), each WHEN/THEN pair and the ELSE;
- the declared-type facts the simple form's implicit `=` needs: the operand's and each WHEN's bpchar width
  (`declaredBpcharTypmod`) and bare-literal flag (`isBareStringLit`).

These are the facts `evalCaseExpr` reads from the expressions. `evalFastCase` is its twin, arm for arm:

- the operand is evaluated once;
- each WHEN, in order, is a boolean predicate (searched form), or `operand = WHEN` resolved through the same
  `comparisonOperandsAsBpchar` and `compareEq` (simple form);
- a NULL or non-bool result is no match;
- the first match's THEN is evaluated and returned, otherwise the ELSE, otherwise NULL.

Only the chosen arm is evaluated. A CASE with a nil arm keeps the adapter. Every compiled call site gains the
node: aggregate arguments, projections, quals and join filters. The batch evaluator reaches it through its
per-row fallback.

**Pin.** `TestCaseCompiledMatchesInterpreted` compares `evalFastCase` with `evalCaseExpr` on every row of a
fixture. The fixture covers:

- the searched form, Q59's shape;
- several arms with no ELSE;
- a simple CASE over a `char(9)` operand, where padded values meet bare literals and a WHEN is NULL;
- simple CASEs over int and numeric operands with mixed-kind WHENs;
- a non-bool WHEN in the searched form;
- a CASE nested in a THEN.

It asserts at least eight distinct results. Passing a zero bpchar width in place of the compiled one fails it on
the padded `char(9)` rows.

**Measured.** Warm, two interleaved rounds of three runs:

| query | HEAD (s) | candidate (s) |
|---|---|---|
| TPC-DS Q59 | 2.32–2.37 | 1.80–1.89 |
| TPC-DS Q43 | 0.48–0.72 | 0.44–0.55 |

In the sf025 sweep, Q59 went from 2.30 to 1.82 s and Q2 (also day-name CASE sums) from 0.93 to 0.68 s. The
sweep's Q7/Q57/Q14 rises did not repeat in an A/B (equal or better on the candidate). TPC-H values are identical
(acceptance 24 MATCH), and the sweep's plan shapes are identical.

**Left.** In Q59's profile, the CASE WHENs' `d_day_name = 'Sunday'` comparisons are now the largest term:
`evalFastBinaryApply` takes 23 % on the bpchar arm, which normalises the operands' padding before comparing.
They are a next candidate.

## Slice 48 (2026-10-22): a Sort is a borrow root — TPC-DS Q97, Q87

**Target, named up front:** TPC-DS Q97, 0.93–1.11 s warm against PG's 0.11 s. EXPLAIN ANALYZE showed its Merge
Full Join returning its first row only at 1 225 ms. Both inputs are
`Group → Gather Merge → Sort → Parallel Hash Join (store_sales or catalog_sales probe) → Parallel Seq Scan`.

**Cause.** `cloneRowOwnedPrefix` under `seqScanOp.Next` took 40 % of the profile, with its allocator
(`heapSetTypeSmallHeader`) another 13 %. Every probe row of both Parallel Hash Joins was cloned by the scan,
because nothing above the scan made it a borrower.

A seq scan hands up its own reused row (`borrowRows`, M0145-0008u) only when `markScanBorrowChain` reaches it from
a root. A root is an operator that copies every row it keeps before pulling the next one:

- a borrowing aggregate;
- a hash join's build loop;
- a Gather worker's queue.

The walk descends through operators that keep nothing (Project, a hash join's probe side, the nested loops, a
Gather leader) and stops at anything else. In a Gather Merge worker the tree's top is a Sort, which stopped the
walk.

A Sort is a retention boundary, though. `sortOp.Open` calls `slot.Materialize()` on every child slot before it
pulls the next one: `cloneRowOwned` copies the row slice and promotes arena payloads. The scan's per-page arena
reset already names `sortOp.Open` as one of the boundaries it relies on. PG's Sort likewise copies each input slot
into its tuplesort (`tuplesort_puttupleslot`). The in-place `MaterializedSlot.Materialize` is also safe on a
borrowing scan: the scan decodes into its own `scanRow` and only re-points `o.slot.row` at it on each Next.

**Rule.** `finishSortNode` calls `markScanBorrowChain(child)`, and so does `finishIncrementalSortNode`, whose
`readGroup` materialises each slot the same way. A Filter between the Sort and the scan still stops the walk: its
batch mode reads child rows ahead, the known read-ahead hazard.

**Pins.**

- `TestScanBorrowUnderSort` runs four ORDER BY queries over a multi-page table whose text and big numerics live
  in the per-page arena:
  - Sort directly on the scan (`SELECT *`);
  - Sort over a column Project;
  - Sort over an expression Project;
  - Sort over a hash join probe.

  It asserts which scans borrow, and that the results equal the cloning path's in order, with tail poison armed.
  A Sort that keeps the child's row without copying fails the `SELECT *` case.
- `TestScanBorrowBuildSideAndGatherWorkers` had pinned "only the build side borrows under the Sort". It now
  expects the probe side too; its serial and gathered value checks are unchanged.
- The executor package passes with deform poison armed globally.

**Measured.** Warm, two interleaved rounds of three runs:

| query | HEAD (s) | candidate (s) |
|---|---|---|
| TPC-DS Q97 | 0.93–1.11 | 0.77–1.02 |
| TPC-DS Q87 | 1.31–2.27 | 1.27–1.36 |

Q51 first looked slower, but repeated interleaved runs showed it bimodal on both builds (HEAD 1.35–2.68 s,
candidate 1.72–2.32 s), with a lower median on the candidate. In the sf025 sweep the sum went from 121.9 to
116.7 s. TPC-H's warm total is flat (15.50 → 15.77 s); its Q2/Q3 rows were re-run four times interleaved and
overlap fully (Q2 0.33–0.70 s on both builds). TPC-H values are identical (acceptance 24 MATCH), and the sweep's
plan shapes are identical.

**Left.** Q97 remains ~7× PG. The probe rows are now borrowed, but the Gather Merge workers still ship every
joined row through the queue (`transferRowForQueue`), and Group / Merge Full Join run in the leader. A next
profile should split the time between the workers' sort and the leader's merge.

## Slice 49 (2026-10-22): a skip-scan group key is built without re-resolving the index — TPC-DS Q16

**Target, named up front:** TPC-DS Q16, 0.97–1.20 s warm against PG's 0.08 s.

**Cause.** EXPLAIN ANALYZE put Q16's time in its anti-join and semi-join probes. They are an Index Only Scan on
`catalog_returns_pkey (cr_item_sk, cr_order_number)` and an Index Scan on `catalog_sales_pkey`, each keyed only on
the second column: skip scans. Each probe took ~18 ms against PG's ~1.5 ms.

In the profile, `btreeSkipEnum.walkLeaf → groupBounds` took 58 %. For every prefix group (every distinct
`cr_item_sk`), `groupBounds` decoded the group's first entry and called `ctx.indexProbeKey` twice, once for the
group's probe and once for its pivot. Each `indexProbeKey` call re-resolved:

- the index descriptor (`pgIndexKeyDesc`);
- the key columns (`pgIndexKeyColumns`), by a case-insensitive by-name lookup (`findColumnByName`,
  `strings.EqualFold`) that also allocates.

Those two alone were 37 % of the query.

**Rule.** The enumeration already holds both, resolved once per Rescan in `init` (`e.desc`, `e.keyCols`).
`groupBounds` now forms both images directly, through `btreeSkipEnum.probeKey`: one `pgIndexTupleKey` over
`e.keyCols[:n]` with the zero heap TID, wrapped with `indexProbeKey`'s own error text. The values go in a reused
buffer (`e.vals`): the prefix datums, then the bound columns' values.

This is exactly `indexProbeKey`'s tuple-format arm. A part list built from `keyCols` in order always passes its
"leading prefix of the key" check, so the bytes are the same.

**Pin.** `TestSkipGroupBoundsMatchesIndexProbeKey` checks that `groupBounds`' probe and pivot are byte-equal to
the `indexProbeKey` composition it replaced, for entries of two indexes:

- `(a, b)`, skipping one int column;
- `(a, c, b)`, skipping an int and a varchar, which brings in alignment and a varlena header.

The entries include an empty string and a negative key. Dropping the last bound value from the probe fails it.

**Measured.** Warm, two interleaved rounds of three runs:

| query | HEAD (s) | candidate (s) |
|---|---|---|
| TPC-DS Q16 | 0.97–1.20 | 0.66–0.90 |
| TPC-DS Q95 | 3.69–4.21 | 3.37–3.96 |

In the sf025 sweep, Q16 went from 0.94 to 0.67 s and the sum from 116.7 to 115.6 s. Q26's sweep rise was the
first-run warm-up; it is equal on both builds after that. TPC-H values are identical (acceptance 24 MATCH), and
the sweep's plan shapes are identical.

**Left.** In what remains of the skip walk, each group still decodes its first entry's prefix to datums and
re-encodes the probe and pivot (`pgIndexTupleKeyDatums` + `pgIndexTupleKey`, ~37 %), and the per-entry comparisons
are another 37 %. PG's skip arrays never form tuples: `_bt_advance_array_keys` copies the entry's prefix
attribute into the array's scankey and compares scankeys against index tuples. Building the pivot by truncating
the entry (`_bt_truncate`'s shape), and comparing on scankeys, is the next step for Q16, Q94 and Q95.

## Slice 50 (2026-10-22): a literal IN list of nine or more items is hashed — TPC-DS Q8

**Target, named up front:** TPC-DS Q8, 0.90–1.04 s warm against PG's 0.14 s.

**Cause.** EXPLAIN ANALYZE put 591 ms of Q8 in one scan, against PG's 20 ms: `customer_address` filtered by
`substr(ca_zip, 1, 5) = ANY ('{…400 zip codes…}')`. In the profile, `evalInExprScalarRow` + `compareEq` were 44 %.
goopg compared each of the 50 000 rows with each of the 400 cached items in turn.

PG never does. `convert_saop_to_hashed_saop` (`optimizer/util/clauses.c`) turns a constant `= ANY` array of at
least `MIN_ARRAY_SIZE_FOR_HASHED_SAOP` (9) elements into a hashed ScalarArrayOp, and `ExecEvalHashedScalarArrayOp`
probes a hash table built from the array once.

**Rule.** A literal IN list is already cached per statement (`inListConst`). With nine or more items it also
builds an `inListHash` once (`inListConst.hashed`), and the plain-equality loop of `evalInExprScalarRow` first
asks `inListHash.probe`. The probe returns:

- true on a hit;
- else NULL if the list holds a NULL;
- else false (IN) or true (NOT IN);

which is exactly what the loop returns. It serves only the pairings where the loop decides a match by exact key
equality — `compareEq` compares two strings with `==` and two ints with `==`, with no error path:

- **string operand / string items.** Both sides get the per-pair bpchar normalisation the loop applies
  (`comparisonOperandsAsBpchar`):
  - with a bpchar operand, the operand is always trimmed, and each item by its own declared type or
    bare-literal flag;
  - with no bpchar on either side, nothing is trimmed.

  A list mixing bpchar items under a non-bpchar operand trims the operand per item, which one probe key cannot
  model, so it keeps the loop.
- **int operand / int items.**

Every other pairing keeps the loop:

- a numeric, date or interval operand (slice 43's coerced items);
- a string operand against int items, or the reverse — `compareEq` compares those as text;
- a list mixing kinds;
- a list shorter than nine.

**Pin.** `TestInListHashMatchesLinear` checks that a cached, hashed list (with a Context) and per-row linear
evaluation (without one) agree on every row, for IN and NOT IN. It covers:

- text, `bpchar(5)` and int operands, with operand values that are NULL, padded, of the other kind, or absent
  from the list;
- string, int, padded, bpchar-cast, NULL-holding, mixed-kind and eight-item lists.

It asserts at least ten lists actually hash. Each of three mutations fails it: never trimming the operand,
ignoring the list's NULL, and admitting a mixed-kind list.

**Measured.** Warm, two interleaved rounds of three runs:

| query | HEAD (s) | candidate (s) |
|---|---|---|
| TPC-DS Q8 | 0.90–1.04 | 0.34–0.45 |

In the sf025 sweep, Q8 went from 0.96 to 0.36 s. The sweep's Q74/Q7/Q14 rises did not repeat in an A/B: Q7 0.43
s on both builds, Q74 within its 18.9–20.3 s spread. TPC-H values are identical (acceptance 24 MATCH), and the
sweep's plan shapes are identical.

**Left.** PG hashes any element type with a hash opclass, and also hashes `<> ALL` (NOT IN through the
negator). goopg's hash serves only string and int keys through the IN-list path. A numeric, date or interval
operand, and the explicit `op ANY(array)` / `<> ANY` loops, stay linear. Recorded in the deferral ledger.
