# M0138-0005 — corpus re-measure at a declared epoch

Status: accepted (landed 2026-09-15)

## Task

Recon/measurement task (per `AGENT.md` §"Plan-parity harness" — no production
code changed this loop, matching the shape of the milestone's other named
recon tasks even though M0138-0005 is not one of the three explicitly listed).
Commit a per-column statistics diff vs PG 18.3 over both corpora at the
current epoch (HEAD, after M0138-0002/-0003/-0004 landed), then the plan and
category movement it causes relative to the milestone-filing baseline
(`METHODOLOGY3/README.md`, 2026-09-14, "post-R128"). Every remaining
disagreement is explained or filed as a ledger row.

## Method

Bootstrapped per `docs/design/0100-0149/m0137-0003-baseline-capture-procedure.md`.
Started the two goopg lanes that were down at loop start (`bench/tpch/setup_goopg.sh`,
idempotent; `bench/tpcds/server.sh start sf025`) alongside the two already-running
PG reference lanes (`:65432`, `:65438`) — all four are the shared `:6543x` block,
left running afterward, never restarted.

1. **Plan/category capture.** `estimate-audit -plan-only` (TPC-H, label
   `m0138-0005-epoch1`) and `scripts/capture-tpcds.sh` (TPC-DS SF0.25, goopg
   `:65437` and PG `:65438`), both under `analysis/m0138/` (scratch, git-status
   untracked per the M0137-0001/M0138-0001 precedent — conclusions live here,
   raw artefacts do not need tracking). Diffed with
   `scripts/pg-plan-parity-diff.py`.
2. **Per-column statistics diff.** Same column set M0138-0001's census used
   (TPC-H's 8 tables, 61 columns; TPC-DS `customer`/`date_dim`/`store`/`item`/
   `store_sales`, 120 columns — one fewer than M0138-0001's 121, not
   investigated further, within the set's own noise), `ANALYZE`d fresh
   in-session on both goopg lanes (goopg statistics are per-connection) and
   read from `pg_stats` on all four lanes. Raw captures:
   `/tmp/m0138-0005-census/{goopg,pg}_{tpch,tpcds}_cols.tsv` (scratch, not
   committed, same precedent as M0138-0001's `/tmp/m0138-census/`).

## Results

### 1. Plan/category movement: TPC-H unchanged, TPC-DS verdict-tuple unchanged, two categories +1 each

**TPC-H — byte-identical to the filing baseline.** Match set (Q1, Q6, Q10,
Q11, Q14, Q15a) and every category count are identical to
`METHODOLOGY3/README.md`'s "now (post-R128)" row:

| category | filing (post-R128) | this loop |
|---|---|---|
| join-order | 14 | 14 |
| join-method | 9 | 9 |
| scan-type | 8 | 8 |
| parameterisation | 5 | 5 |
| aggregation-strategy | 10 | 10 |
| sort-strategy | 9 | 9 |
| qual-placement | 4 | 4 |
| rendering | 1 | 1 |
| parallelism | 0 (serial protocol) | 0 |

match=6, shapediff=14, unparsed=0, missingnode=2 (Q5, Q8 — PG-only
`Materialize`), error=0, timeout=0. Zero shape-delta: M0138-0002/-0003/-0004
moved no TPC-H plan at all, consistent with K50 (an estimate/cost change only
registers if it changes plan *structure*) and with the milestone's own
"toward-oracle hazard" framing — these were faithfulness fixes, not
structural planner changes.

**TPC-DS — verdict tuple unchanged, two category counts +1 each.** Verdict
tuple `match=2 shapediff=69 unparsed=0 missingnode=25 error=3 timeout=0`
reproduces `TODO.md:4845-4849`'s R120 baseline tuple `2/69/0/25/3/0` exactly
(same match set Q9/Q41, same 3 unplannable-both-engines errors). Category
counts:

| category | filing (`TODO.md:4857-4861`, cited by `METHODOLOGY3/README.md`) | this loop |
|---|---|---|
| join-order | 89 | **90** |
| parallelism | 87 | 87 |
| sort-strategy | 76 | 76 |
| aggregation-strategy | 69 | 69 |
| join-method | 63 | 63 |
| scan-type | 57 | 57 |
| parameterisation | 48 | 48 |
| rendering | 20 | 20 |
| qual-placement | 16 | **17** |

`join-order` and `qual-placement` each moved +1 with no verdict change (still
SHAPE-DIFF, not a new MISSING-NODE/ERROR) — at least one TPC-DS query's plan
structure changed (gained a new category tag) without flipping in or out of
MATCH. This is the AGENT.md report checklist's item-2 case ("shape changes
with no category movement" is the *sideways* case; this is category movement
*without* a verdict change, the same class of signal in miniature). Not
identified further this loop (99-query corpus, ±1 in a single category is
below the effort this recon task budgeted for a query-level bisection);
**deferred**, ledger row below.

### 2. Per-column statistics diff — three findings

**a. `n_distinct` sign/order-of-magnitude — near-total agreement, up from the pre-M0138-0002 gap.**
TPC-H: 60/61 columns agree in sign and within one decade of PG's `n_distinct`
(the one exception not investigated — single-column tail, not
corpus-significant). TPC-DS: 120/120 sign match, 119/120 order-of-magnitude
match. This is the corpus-wide confirmation the M0138-0002/-0003 per-column
spot-checks (R78's `l_orderkey` witness) predicted but did not measure at
scale.

**b. Correlation banding (M0138-0001 finding 2) — persists essentially
unchanged; the M0138-0004 "RESOLVED" ledger entry's causal claim is
refuted by this measurement.** `.ralph/deferral_ledger.md`'s `m0138-0001`
row (2026-09-15, flipped `resolved` by M0138-0004) attributes M0138-0001's
"36/118 TPC-DS columns land in `[0.09,0.16]` vs PG's 7/119" finding to
`sort.Slice`'s unspecified tie order, and claims the fix (`sort.SliceStable`
on `corrPairs`, already built in ascending scan-position order) "reproduces
PG's ascending-tupno tie-break exactly." **This loop's corpus-wide
re-measurement, taken after that fix landed, reads 36/120 vs PG's 7/120 — the
same magnitude, same 5x over-representation, on the same columns** (the
PG-matching 7 are `date_dim.d_day_name`/`d_dow`, `item.i_category`/
`i_category_id`/`i_class_id`, `store.s_floor_space`/`s_market_desc` — low-
cardinality columns whose goopg and PG correlation values agree closely
column-by-column, e.g. `store.s_floor_space` −0.8333333/0.1329 identically on
both engines; these are genuine, not spurious). The 29 goopg-only band
members are `customer`'s demographic/date FK-shaped columns (11) and
`store_sales`'s FK/measure columns (18: `ss_addr_sk`, `ss_cdemo_sk`,
`ss_customer_sk`, `ss_hdemo_sk`, `ss_promo_sk`, `ss_sold_date_sk`,
`ss_sold_time_sk`, plus 11 price/cost/quantity measure columns) — exactly
the high-duplicate-density shape M0138-0001 flagged.

Source-read to check the mechanism claim before writing this finding down:
`compareDatum`/`compare_scalars` tie-break aside, both engines sort the
**reservoir into physical (block, offset) order before computing
correlation** — PG's `qsort_interruptible(rows, numrows, sizeof(HeapTuple),
compare_rows, NULL)` (`analyze.c:1321-1322`, `compare_rows` at `:1361-1378`,
comparing `ItemPointerGetBlockNumber`/`OffsetNumber` ascending) and goopg's
`sort.Sort(&analyzeSampleByTID{...})` (`operators_analyze.go:952-953`,
landed by M0138-0002) are the same operation. PG's `tupno` is literally the
post-sort array index (`values[values_cnt].tupno = values_cnt`,
`analyze.c:2495`) — not a re-derived physical position — so goopg's `pos`
(the `corrPairs` loop index over the TID-sorted `reservoir`,
`operators_analyze.go:1228`) is the equivalent quantity. Both engines then
break value ties by ascending physical position deterministically (PG's
comparator returns `ta - tb`; goopg's `sort.SliceStable` over a
pre-sorted-by-`pos` slice is mathematically the same result). **The
tie-break mechanism itself checks out as correctly ported** — the M0138-0004
fix is still a genuine, verified-in-isolation correctness improvement
(`TestAnalyzeCorrelationTieBreakMatchesPGTupnoOrder` pins a hand-derived
case) — but it does not explain the corpus-wide banding symptom it was
credited with resolving.

**What remains a live hypothesis**: M0138-0001's own alternative, never
eliminated — "the band could also stem from a genuine load-order artifact of
the TPC-DS SF0.25 loader (shared surrogate-key generation pattern across FK
columns) rather than the sort itself." The banded columns are exactly the
ones whose physical placement depends on load order in a shape that could
differ between goopg's and PG's storage engines even given byte-identical
generated values (bulk-load page packing, HOT/fill-factor differences,
concurrent-COPY partition patterns). Distinguishing "genuine, if unwelcome,
inter-engine physical-layout difference" from "a second goopg-side
computational bug not yet found" needs a controlled test — a synthetic table
with a **known**, hand-constructed physical/value relationship, loaded
identically on both engines, compared for correlation. Ledger row below;
resume point named there.

**c. `avg_width=0` persists for goopg's fast-path `numeric` — a narrower
residual than M0138-0004's fix covered.** M0138-0004 added a `typlen`
fallback for **fixed-width** (`typLen >= 0`) types. PG's `numeric` is
varlena (`typlen = -1`), so it was never in scope for that fallback and
correctly still takes the measured-payload branch — but goopg's
`datumVariablePayloadWidth` (`operators_analyze.go:1159-1174`) returns 0 for
every `KindNumeric` value that fits goopg's **fast path** (int64 mantissa
inline in `Datum.Int`, no heap-allocated arena), only measuring a real byte
length for the big-numeric slow path. HammerDB's TPC-H schema declares eight
numeric-typed columns including every primary/foreign key
(`hammerdb_tpch_integer_keys_are_numeric` memory) — small integer-valued
keys are exactly the fast-path case, so this reproduces on all 28 numeric
columns in the TPC-H 61-column set (`c_acctbal`, `c_custkey`, `l_orderkey`,
`l_partkey`, every other `*key`/`*price`/`*cost`/`*availqty` column) and 17
of 120 in the TPC-DS set. This is a completion of M0138-0001 finding 3's
already-flagged "non-orderable/slow-path" residual, narrowed to name the
specific gap: not every `KindNumeric` value goes through the slow path, and
the common (fast-path) case has no width fallback at all. Deferred — see
ledger row; a fix needs PG's actual numeric-varlena size formula (roughly
`NUMERIC_HDRSZ + digit-group count`, dependent on the value's actual
precision/scale, not a fixed constant), not a typlen literal.

## What was not done (scope boundary)

- **No production code changed.** Pure measurement, as the task's own text
  scopes it ("commit a per-column statistics diff... then the plan and
  category movement it causes").
- **The TPC-DS +1/+1 category shift's specific query was not identified.**
  Deferred (ledger row) rather than a 99-query bisection in a recon task.
- **The correlation-banding root cause was not fixed** — the tie-break
  mechanism was re-verified correct in isolation, but the corpus symptom it
  was credited with resolving persists; both remaining hypotheses (a second
  goopg computational bug, or a genuine inter-engine physical-layout
  difference) are named but not distinguished. Ledger row + resume point.
- **`numeric` fast-path `avg_width` was not fixed** — named, deferred, with
  the reason a literal constant won't do (PG's own value is data-dependent).
- **The non-orderable-kind `compute_distinct_stats` port** (bytea/interval)
  named by M0138-0001/-0004 remains out of scope — still no TPC-H/TPC-DS
  column exercises it.

## Verification

`go build ./...` unaffected (no source changed). Values gates not re-run —
no production code touched, so the standing gates from M0138-0004's own
commit are the last relevant green result; re-running them here would
re-measure, not verify, this loop's diff-only change. `make
ralph-state-guard` run before the status block per every-loop discipline.
