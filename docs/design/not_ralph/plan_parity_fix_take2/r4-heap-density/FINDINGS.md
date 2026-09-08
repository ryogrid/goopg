# R4 — heap density, and a third falsified claim of mine

*Round 4 of `../TODO.md`. A **findings** round: it changes no code. It
began as "compute the worker count" (K11b) and ended by falsifying that
premise and locating the real cause, which is not in the planner at all.*

## 0. K11b was wrong

R2's adjudication recorded: *"worker count is not computed — goopg
always plans 4, PG derives it from relation size."*

**goopg already implements PG's rule.** `computeParallelWorker`
(`considerparallel.go:588`) is `compute_parallel_worker`
(`allpaths.c:4274`), with a post-planning twin in `parallel.go`, and its
comment names the oracle. Checked before designing a round on the
premise — which is the only reason this document is a finding and not a
wasted round.

That is the **third** claim of mine falsified in this workstream:
K11a (sorted aggregation "unreachable" — from a stale comment), the
root-causes rev-1 candidate-set claim (from an unexercised function),
and now K11b. All three share one shape: **a conclusion about behaviour
drawn from reading, without checking the behaviour.** The habit that
caught all three is the same — measure the thing itself before building
on the claim.

## 1. The real cause: goopg's heap is a different size

TPC-DS Q96, same query, same table, same node:

| | goopg | PG |
|---|---|---|
| `Parallel Seq Scan on store_sales` | Workers Planned: **4** | Workers Planned: **3** |
| `pg_class.reltuples` | 1,439,608 | 1,439,608 (**identical**) |
| `pg_class.relpages` | **29,761** | **25,928** |

PG's `compute_parallel_worker` steps the count at
`min_parallel_table_scan_size * 3^n` — with the default 1024 blocks,
3 workers spans [9,216, 27,648) and 4 spans [27,648, 82,944). PG's
25,928 pages lands in the 3 band; goopg's 29,761 lands in the 4 band.

**Both engines computed the worker count correctly from the page count
they were given.** The divergence is entirely that goopg stores the same
1,439,608 rows in 14.8% more pages.

## 2. It is systematic, bidirectional, and sorts by column type

Row counts are identical on every table. Page counts are not:

| table | PG pages | goopg pages | goopg/PG | column types |
|---|---|---|---|---|
| `inventory` | 25,504 | 25,460 | **0.998** | `integer` only |
| `store_returns` | 2,448 | 2,564 | 1.047 | int + `numeric` |
| `date_dim` | 1,424 | 1,522 | 1.069 | mixed |
| `catalog_sales` | 18,760 | 20,530 | 1.094 | int + `numeric` |
| `web_sales` | 9,416 | 10,269 | 1.091 | int + `numeric` |
| `store_sales` | 25,928 | 29,761 | **1.148** | `integer`, `numeric(7,2)` |
| `customer` | 2,872 | 2,046 | **0.712** | + `character(N)` |
| `item` | 1,284 | 736 | **0.573** | + `character(N)`, `varchar` |

The all-integer table matches to **0.2%**. That is a strong result: it
means goopg's page layout, per-tuple header and line-pointer overhead
are essentially PG-correct, and the deltas are **per-datatype storage
size**, isolated to two types:

- **`numeric` — goopg stores it LARGER.** Every table above 1.0 is
  numeric-bearing; `store_sales`, whose non-key columns are all
  `numeric(7,2)`, is the largest at 1.148.
- **`character(N)` — goopg stores it SMALLER.** The two tables below 1.0
  are exactly the two with blank-padded `char(N)` columns. PG's `bpchar`
  stores the padding to the declared length; goopg appears not to.

Both are stated as **hypotheses consistent with the data**, not as
verified mechanisms — the correlation is clean but neither has been
confirmed by reading a stored tuple. Confirming them is the first step
of any follow-up, and it should be done by measuring a single-column
table of each type rather than by inferring from these ratios.

## 3. Why this matters to the goal, more than any round so far

The goal is that goopg produce PG's plans by running PG's cost
computation on the same statistics. `relpages` **is** one of those
statistics, and it is a direct input to:

- every `seq_page_cost * pages` term (so every scan cost),
- `index_pages_fetched`'s Mackert-Lohman estimate,
- `compute_parallel_worker`'s thresholds (§1),
- `min_parallel_table_scan_size` admission.

A 15% page error is not noise at a threshold: it is the difference
between `Workers Planned: 3` and `Workers Planned: 4` on a plan that is
otherwise **identical**, and no planner change can correct it.

So there is a floor on plan parity that is not a planner property at
all. Some fraction of the remaining TPC-DS shape-diffs will not close
until goopg's heap stores a `numeric` and a `character(N)` in the same
number of bytes PG does. That is squarely inside the project's absolute
PG-compatibility rule — an on-disk representation difference — so it is
a compat defect in its own right and not merely a parity inconvenience.

**It also means the parity goal is not achievable by planner work
alone**, which is a conclusion this workstream should surface early
rather than discover at the end. It does not make the goal unreachable;
it relocates part of it.

## 4. What this round does NOT claim

- Not that the density gap explains most of the remaining shape-diffs.
  It explains the worker-count class and contributes to every
  page-priced comparison; its share is unmeasured.
- Not that `numeric`/`character` are confirmed as the mechanism (§2).
- Not that fixing density is cheap. It touches on-disk format.

## 5. Filed

- **R5 — confirm the per-type storage sizes** by measuring
  single-column tables (`integer`, `numeric(7,2)`, `character(10)`,
  `varchar`) against PG, then locate the divergence in goopg's tuple
  encoder. Cheap, isolating, and it converts §2's hypotheses into facts.
- K11b struck from the ledger; K14 recorded in its place.
