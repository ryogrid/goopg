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

## Slice 51 (2026-10-22): a streaming UNION ALL passes the borrow on — TPC-DS Q71, Q5

**Target, named up front:** TPC-DS Q71, 0.64–0.66 s warm against PG's 0.08 s (re-timed warm this loop; PG's
EXPLAIN ANALYZE inflates its own Q71 to 0.45 s with timing overhead, so plain runs were compared).

**Cause.** In the profile, `cloneRowOwnedPrefix` under `seqScanOp.Next` took 40 % and its allocator another 9 %.
Q71's worker tree is `Nested Loop → Nested Loop → Parallel Append → three Parallel Hash Joins`, each probing a
sales-table scan. The Gather worker root (slice 23) starts `markScanBorrowChain` at the top. The walk passes
through both nested loops, then stops at the Append — a `setOp`, which it did not know. So every web/catalog/store
sales probe row was cloned.

**Rule.** A streaming UNION ALL (`setOp.nextStreaming`) returns each child slot as it is. It keeps nothing
between calls: it drains one branch, then the other, closing each at EOF. So `markScanBorrowChain` now continues
into both branches of a `setOp` that is streaming and has no merge keys.

Two other `setOp` kinds keep rows, so the walk stops at them as before:

- a Merge Append (`nextMerge`) holds each side's front row across calls;
- the buffering set operations (INTERSECT / EXCEPT / the non-streaming variants) keep their input.

**Pin.** `TestScanBorrowThroughUnionAll` checks that both UNION ALL branches borrow under a borrowing
grouped aggregate and under a Sort, and that an `INTERSECT ALL` (HashSetOp) under an aggregate borrows nothing.
Every result must equal the cloning path's with tail poison armed (in order for the Sort). Admitting every
`setOp` to the walk fails the INTERSECT case. The executor package passes with deform poison armed globally.

**Measured.** Warm, two interleaved rounds:

| query | HEAD (s) | candidate (s) |
|---|---|---|
| TPC-DS Q71 | 0.64–0.66 | 0.40–0.42 |
| TPC-DS Q5 | 0.87–1.70 | 0.68–1.25 |
| TPC-DS Q2 | 0.40–0.44 | 0.36–0.37 |
| TPC-DS Q14 (UNION ALL `cross_items`) | 20.4–20.9 | 17.5–19.3 |

In the sf025 sweep, Q5 went from 1.53 to 0.73 s and Q71 from 0.41 to 0.30 s. The sweep sum rose (116.7 →
119.0 s) on its noisy rows; none of them repeated in an A/B:

- Q14 is faster on the candidate;
- Q64 and Q23 are equal;
- Q78, which has no UNION, spans 1.5–3.1 s on both builds;
- Q77 is equal over ten runs.

TPC-H values are identical (acceptance 24 MATCH), and the sweep's plan shapes are identical.

**Left.** Q71 is still ~5× PG. Its nested loops probe `item_pkey` and `time_dim_pkey` per outer row, and the
Gather queue copies every surviving row (`transferRowForQueue`).

## Slice 52 (2026-10-22): a skip-scan group key is formed from the entry's own images — TPC-DS Q16

**Measurement note first.** Profiling Q80 and Q58 this loop put 6–8 % in `storage.pageChecksumBlock` under
`relFile.readBlock`: warm runs were still reading pages from disk. The private bench clone (`tmp/q64-clone`), like
the shipped goopg corpora, runs at the default 128MB `shared_buffers`. The `:65438` PG reference runs at 2GB, and
the SF0.25 database is 761MB. Both clusters verify checksums on read (`data_checksums = on`), so goopg paid a
re-read and a checksum per page that PG, with everything cached, never pays.

The clone now runs at 2GB to match PG. It is a private `tmp/` copy, so this changes no shipped corpus or gate.
Re-timed at equal buffers:

| query | goopg (s) | PG (s) |
|---|---|---|
| Q72 | 1.1 | 0.6–1.4 |
| Q16 | 0.67 | 0.06 |
| Q97 | 0.63 | 0.12 |
| Q80 | 0.23 | 0.08 |
| Q78 | 1.33 | 0.46 |
| Q59 | ~1.9 | 0.74 |
| Q71 | 0.17 | 0.08 |

Q72 is at parity. Earlier slices' A/B ratios stand: both builds ran at the same 128MB.

**Target, named up front:** TPC-DS Q16, the largest ratio at equal buffers.

**Cause.** After slice 49, `groupBounds` was still 40 % of the query. Per prefix group it decoded the group's first
entry to Datums (`pgIndexTupleKeyDatums`, 22 %) and re-encoded them into the probe and pivot (`pgIndexTupleKey`,
15 %). PG's skip array never round-trips the value: `_bt_advance_array_keys` copies the skipped attribute's datum
straight from the index tuple into its scankey.

**Rule.** `groupBoundsRaw` forms both keys from images:

- it deforms only the entry's `s` prefix attributes (`nbtree.DeformPGIndexTuple`), and passes those images to
  `FormPGIndexTuple`;
- the bound columns' images (`encodeValuePG`) are formed once per Rescan;
- the tail is `pgIndexTupleKey`'s: zero heap TID, the pivot stamped with its own attribute count, and the leaf
  item-size check, all with `indexProbeKey`'s error text.

A new `nbtree.PGIndexImageInline` admits only images `FormPGIndexTuple` re-forms into exactly the bytes of the
value encoded afresh: fixed width, a short varlena that is not a TOAST pointer, or a 4-byte uncompressed varlena
(re-headered short). Anything else takes the decoding path, which also reports the errors:

- an entry that is not a full-arity leaf tuple;
- a prefix value stored compressed or out of line;
- a bound value that is a TOAST pointer.

**Pins.**

- `TestSkipGroupBoundsMatchesIndexProbeKey` now asserts the raw path takes every entry.
- `TestSkipGroupBoundsRawImagesMatchDecodedTypes` checks that the raw probe and pivot are byte-equal to the
  decode-and-encode keys for an index on `(numeric, date, char(4), text, int)`. It skips four columns, with a NULL
  prefix, padded and blank `char` values, an empty text and a long text value.

Dropping the pivot's attribute-count stamp fails both. The nbtree package passes under `-race`.

**Measured.** Warm, 2GB clone, two interleaved rounds:

| query | HEAD (s) | candidate (s) |
|---|---|---|
| TPC-DS Q16 | 0.65–0.74 | 0.48–0.51 |
| TPC-DS Q95 | 3.48–3.87 | 3.20–3.53 |
| TPC-DS Q94 | 0.10–0.13 | 0.09–0.10 |

In the sf025 sweep (128MB), Q16 went from 0.62 to 0.50 s and the sum from 119.0 to 115.7 s. Its Q9/Q97/Q31 rises
did not repeat in an A/B (equal on both builds). TPC-H values are identical (acceptance 24 MATCH), and the sweep's
plan shapes are identical.

**Left.** Q16 is still ~8× PG. What remains is the walk's per-entry tuple comparisons
(`comparePGIndexTupleAttrs`, ~38 %): PG compares scankeys with each attribute's comparator, while goopg compares
two whole tuple images.

## Slice 53 (2026-10-22): nested Projects and non-correlated sublinks keep the deform bound — TPC-DS Q14

**Target, named up front:** TPC-DS Q14 at equal buffers, 13.7–16.3 s against PG's ~8 s: the largest absolute gap
left.

**First attempt, discarded (measured non-improvement).** Q78 and Q97 profiles showed two row copies in a row.
A Gather Merge worker's Sort keeps a materialised copy of each row, and the queue transfer copies it again.
Marking a Sort's emitted rows and a Gather's queue rows as already owned (`MaterializedSlot.owned`) removed the
copy. A/B showed Q78 inside its 1.5–3.1 s noise and Q97 slightly *slower* (0.42 → 0.46 s), so the change was
dropped uncommitted.

**Cause.** In Q14's profile, `indexScanOp.Next` took 43 %, and 26 % of that was full-width heap decoding
(`decodeRowIntoInfo`). A debug build counting decoded rows per index scan showed:

| probe | rows decoded | deform width |
|---|---|---|
| `store_sales` (Q14b) | 1.38 M | all 23 columns |
| `date_dim` | 1.31 M | all 28 columns |
| `item` | 1.55 M | all 22 columns |

The queries read a 9–13-column prefix of each. Two rules widened the bound to full:

- **A Project under a Project kept every target live.** Slice 35 gives a consumer-complete Project (under a plain
  aggregate, through sorts) a live set. Q14 stacks that re-expansion Project on a second one listing every column
  of its lateral joins. The inner Project was built with `live = nil`, so it folded the joins' last column and
  every probe below deformed in full.
- **A non-correlated sublink declined the deform walk.** Q14b's `date_dim` probe filters on
  `d_week_seq = (SELECT d_week_seq FROM date_dim WHERE …)`. `deformSublinkArgs` declined every unlowered sublink,
  so:
  - the probe deformed every `date_dim` column;
  - the lateral outer-ref fold (`deformLateralOuterRefs`) failed the outer side, `store_sales` and `item`, to
    full.

**Rule.**

- **Nested Projects.** `buildProjectNode` builds a child Project with
  `projectLiveSet(child, liveProjectTargets(p, live))`. A projection's only reader of its child's rows is itself,
  and it evaluates exactly its live targets (every target when `live` is nil), so the child is consumer-complete.
  This is `buildCompleteChild`'s rule under an aggregate, applied one level down. Unskippable dead targets stay
  live (`projectTargetSkippable`).
- **Non-correlated sublinks.** A sublink with `IsNonCorrelated` (`!planHasOuterRef(inner)`) and no lowered params
  reads nothing of the row: `evalSubquery` keys it by the sublink alone (`nonCorrelatedCacheKey`, InitPlan
  semantics), and the row it pushes on `OuterRows` is never read. `deformSublinkArgs` now reports it as reading no
  columns, and the lateral fold accepts it. A correlated or unlowered sublink still declines.

**Pins.**

- `TestNestedProjectLiveSet` builds Aggregate → re-expansion Project → re-expansion Project → scan. It checks that
  the inner Project's live set is exactly the target the outer's live target reads, and that the scan deforms one
  column. Under a root (full) outer, the inner keeps every read target and the scan deforms in full. HEAD's build
  leaves the inner live set empty and fails.
- `TestDeformNonCorrelatedSublinkReadsNothing`: `col = (non-correlated sublink)` folds to that column alone and the
  lateral fold keeps the key column, while the correlated forms stay full. HEAD's `scan_deform.go` fails.
- `TestLateralProbeWithInitPlanFilterValues` runs a nested-loop index probe filtered by a non-correlated sublink,
  with the deform poison armed, and requires the hash join's result.
- The executor package passes poison-armed.

**Measured.** Warm, 2GB clone, each Q14 statement separately, two interleaved rounds:

| statement | HEAD (s) | candidate (s) |
|---|---|---|
| Q14a | 7.10–7.81 | 6.69–6.76 |
| Q14b | 8.01–8.84 | 6.82–7.36 |

The sf025 sweep sum went from 115.7 to 113.4 s. Its Q60/Q7/Q1 rises did not repeat in an A/B (equal on both
builds), and TPC-H's warm total is flat (15.1 vs 15.5 s, Q2 bimodal). TPC-H values are identical (acceptance
24 MATCH), and the sweep's plan shapes are identical.

**Left.**

- The slab builder's Project arm (`buildRec`) still evaluates every target; only `buildNode`'s path applies live
  sets.
- A CTE body is built with an unbounded `Build(p.Child)` (`newCteScanOp`), so its scans deform from a full
  incoming bound.
- Both are bound-threading gaps for the next slices.

## Slice 54 (2026-10-22): the descent compares without copying or returning walks — TPC-DS Q72, Q16, Q14b

**Target.** At HEAD `7e6b592d1` a CPU profile of TPC-DS Q14 on the 2GB clone put 30 % of samples in B-tree
descents: the per-probe descents of the nested-loop index joins. Line-level, two costs had nothing to do with
comparing keys:

- `comparePGIndexTupleAttrs` took 1.80 s flat out of 21 s. Of that, 1.06 s sat on its two `newPGIndexTupleWalk`
  call lines, while the constructor's own body took 0.30 s. The constructor returns a 48-byte walk plus an error
  by value, twice per comparison. This is the stack-returned-struct pattern that cost the Datum-returning
  deform path a store-forwarding stall.
- `keyExceedsHighKey`, the move-right test run at every tree level, took 1.33 s. 1.05 s of that went to
  `pageHighKey`, which copied the high key twice per call: `PGHighKeyRaw` copied the raw item
  (`PageGetItemRawAllowDead`), then `parse` copied its key. The test then compared the key and dropped it.

**Change.**

- `pgIndexTupleWalk.init` fills a caller-declared walk in place. `comparePGIndexTupleAttrs` and
  `deformPGIndexTupleInto` declare theirs, and `newPGIndexTupleWalk` is gone.
- `pgHighKeyRawNoCopy` and `indexFormat.pageHighKeyNoCopy` alias the page. The three compare-and-drop tests use
  them: `keyExceedsHighKey`, `keyAtOrPastHighKey` and `itemOvershootsHighKey`. Each runs on a page its caller holds
  pinned, the same contract the leaf and inner binary searches already rely on (`parseNoCopy`).
- `PGHighKeyRaw` and `pageHighKey` still return copies; `pageHighKey` now makes one copy instead of two.

After the change, the walk-init lines take 0.33 s and `keyExceedsHighKey` 0.60 s. `comparePGIndexTupleAttrs`
went from 3.04 s to 2.35 s cumulative.

**Pins.**

- `TestPageHighKeyNoCopyMatchesCopy` covers every page of a blob-format and a tuple-format tree. Both readers
  must return the same key and presence. After the page is cleared, `pageHighKey`'s key must be unchanged (a copy)
  and `pageHighKeyNoCopy`'s must not (an alias).
- The walk's comparison and deform semantics are pinned unchanged by the existing comparator and walk tests
  (`pgcompare_walk_test.go`, `pgcompare_test.go`). The nbtree package passes under `-race`.

**Measured.** Warm (second run of each), 2GB clone, two interleaved rounds:

| query | HEAD (s) | candidate (s) |
|---|---|---|
| Q72 | 1.10–1.12 | 0.98 |
| Q16 | 0.47 | 0.41–0.43 |
| Q14b | 6.32–6.44 | 5.83–5.94 |
| Q14a | 6.07–6.39 | 6.15–6.34 |

The sf025 sweep sum (128MB) went from 113.4 to 112.3 s. Values are identical (sf025 PASS=99, 61 ck; acceptance
24 MATCH) and plan shapes are identical.

**Tried and dropped.** A per-Context cache of `pgIndexKeyColumns` in `indexProbeKey`, the by-name key-column
resolution repeated on every probe: Q14a/Q14b flat within noise (≈2 % of samples, under the 3 % bar).

**Left.** Q14's per-probe descent remains the largest share, followed by `cloneRowOwned` of Gather-queue rows
and the string decode of unread prefix text columns.
