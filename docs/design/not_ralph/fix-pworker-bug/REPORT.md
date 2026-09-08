# EXPLAIN ANALYZE pre-filter row counts — effect verification report

Branch `fix-parallel-worker-bug`. Change under test: `85f0c5cd0`
(`executor(explain): collapsed nodes reported PRE-filter rows`).
Baseline: its parent. Date: 2026-09-08.

## 1. The headline result is that nothing moved — as designed

`DESIGN.md` §5 stated: *"No performance change is expected or claimed. This
fixes what EXPLAIN says, not what the executor does."*

TPC-H SF=1, alternating arms, one warm pass discarded, two measured per arm:

| arm | passes (s) | min |
|---|---|---:|
| before | 82.58, 85.48, 83.04, 86.15 | 82.58 |
| after | 85.44, 82.30, 87.87, 86.18 | 82.30 |

**Ranges overlap completely** — before [82.58, 86.15], after [82.30, 87.87].
Minima differ by **+0.3%**, far inside the spread. **Values 24/24 MATCH** on
ordered digests.

This is the intended outcome, and it is the *whole* correctness argument for a
reporting change: an EXPLAIN fix that moved the suite would mean it had
touched execution.

## 2. What the fix corrects — verified against PG 18.3

The original witness, same predicate on both engines:

| | `rows=` | `Rows Removed by Filter` |
|---|---:|---:|
| goopg **before** | **6,001,255** | 5,143,567 |
| goopg **after** | **857,688** | 5,143,567 |
| PG 18.3 | 856,819 | 5,142,016 |

6,001,255 − 5,143,567 = 857,688. goopg now reports post-filter rows, matching
PG's shape. (The residual difference between engines is the two clusters'
separate HammerDB loads, not the fix.)

The defect was never scan-specific. A `HashAggregate` with `HAVING`:

| | `rows=` | `Rows Removed` | true answer |
|---|---:|---:|---:|
| before | 3 | 2 | 1 |
| after | **1** | **2** | 1 |

## 3. Correctness

- **Units gate: 44 packages, 0 failures.** `go build ./...`, `go vet` clean.
- **`TestExplainAnalyzeCollapsedFilterRowsArePostFilter`** pins the invariant:
  a node printing `Rows Removed by Filter: R` must report the rows it
  *produced*. Uses the HashAggregate/HAVING shape deliberately — it needs no
  plan-forcing GUC, and `SET enable_indexscan = off` was found **not** to
  change the plan on this build despite `planner.go:3929-3947` documenting
  otherwise.
  **Mutation-checked**: reverting to the pre-fix source fails it with
  `rows=3 / removed=2`.
- **`TestExplainAnalyzeSeqScanFilterStillReports`** guards the fallback (§4.1).
- Non-ANALYZE `EXPLAIN` unaffected — the change is confined to the ANALYZE
  render path.

## 4. Two errors made while implementing, both now pinned

Recorded because each would have shipped a regression, and because the second
passed a green test.

### 4.1 The first fix silently deleted output

Substituting the Filter's stats **unconditionally** dropped the entire
`(actual …)` suffix *and* the `Rows Removed by Filter` line for **every seq
scan**.

Cause: E-17 cut 2 absorbed the seq-scan qual into `seqScanOp` and deleted the
`filterOp` while **keeping** the `*optimizer.Filter` plan node — the renderer
keys on the plan node. So a seq scan has a non-nil `attachedFilterNode` with no
stats entry, and its own `rowsOut` is already post-filter and correct.

The fix therefore **prefers the Filter's stats but falls back to the child's**.
The fallback is load-bearing, not defensive. An existing test caught this.

### 4.2 A double-count that a green test did not catch

`Rows Removed by Filter` printed **4** where **2** was correct.

Cause: `walkPlanAnalyzeFiltered` already accumulates the collapsed Filter's
rejects on the way down (`fr += fs.filterRejected`). Making the node-level
accumulator *also* read the Filter added them twice. The accumulator must stay
on the child — that is `seqScanOp`, which owns its counter.

**The invariant test asserted `rows` only, so it passed while the output was
wrong.** It was caught by reading a number that looked implausible, not by the
suite. The test now pins both `rows` and `Rows Removed`.

## 5. Scope: what this fix covers, and what it does not

**Covered.** Every collapsible parent — Aggregate/HAVING, Sort, Materialize,
SubqueryScan, bitmap and index-only scans — plus the `workerStats` path, without
which nodes below a `Gather` (which have no `stats[n]` entry at all) would have
printed byte-identical pre-filter numbers.

**Not covered**, and recorded in `DESIGN.md` §4 rather than silently left:

- `rows=` is not divided by `loops` while PG divides (`explain.c:1835`), yet
  `Rows Removed by Filter` **is** divided — two numbers on one line in
  different units when `loops > 1`.
- Parameterised inner index scans print no `(actual …)` at all.
- goopg labels the leader `Worker 4` beside `Workers Launched: 4`; PG folds the
  leader into the parent line.
- goopg duplicates the index cond into the `Filter` where PG strips it —
  wasted per-row work as well as a plan-text divergence.

## 6. The item this work actually settled

The task was to fix what `GAP-ANALYSIS.md` §5.2 called *"a concrete, isolated
bug … every worker scanning the full table in Q12"*.

**That bug does not exist**, and the claim was mine:

- **PG does the same thing.** A partial join pairs a partial outer with a
  **complete** inner (`joinpath.c:1437-1443`,
  `get_cheapest_parallel_safe_total_inner`). Every worker running the whole
  inner is merge-join semantics. goopg implements exactly that, with the
  citation, at `joinpathsparallel.go:250-254`.
- **The measurement under the wrong label was right.** Q12's 6,001,255 rows per
  worker are **real work** — ~94% of that query's runtime is inside that node.
  An adversarial review caught rev 1 of the design claiming the opposite while
  its own §1.1 established the truth.
- **The reporting bug fixed here is what made the mislabel easy.** A reader
  cannot distinguish "scanned 6 M rows" from "scanned 6 M, returned 857 K", and
  the alarming reading is the natural one.

Q12's real 15.1× gap versus PG is **plan choice**: goopg picks a merge join
whose complete inner is re-read per worker, where PG picks a filtered scan
feeding 31,354 index probes. That is the plan-quality programme of
`GAP-ANALYSIS.md` §5.1, untouched by this item.

## 7. Method

Same host, `shared_buffers = 2GB`, `work_mem = 64MB`, 4 workers, goopg on
65433, `tpch-runner -digest`, 600 s per-query timeout. Fresh memory-capped
server per arm (`GOGC=100`, `GOMEMLIMIT=12GiB`, own cgroup unit), one warm pass
discarded, two measured. **Arms alternated**, not blocked. Raw data:
`measurements/`.
