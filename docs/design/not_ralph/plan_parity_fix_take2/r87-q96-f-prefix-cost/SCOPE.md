# R87 SCOPE — Q96 F-branch L2 selectivity and partial-hash cost attribution

This is a measurement-only successor to R86. It identifies the first wrong
input, if any, in the same-relset partial-path election which makes Q96 retain
(store_sales ⋈ store) ⋈ household_demographics rather than PG 18.3's
(store_sales ⋈ household_demographics) ⋈ store. It changes no planner,
executor, renderer, gather policy, or costing behavior.

## 0. Precondition and narrowly selected question

R86 (6ab1f3a38) proved the following under the pinned Q96 environment:

| partial {ss,hd,store} path | L2 input total / rows | L3 total / rows | pathkeys / disabled |
| --- | ---: | ---: | --- |
| PG-shaped (ss ⋈ hd) ⋈ store | 16508.22 / 23222 | 16615.81 / 1935 | 0 / 0 |
| retained (ss ⋈ store) ⋈ hd | 16321.68 / 19352 | 16562.60 / 1935 | 0 / 0 |

Both L3 paths have equal final rows, pathkeys, and disabled nodes. The
ordinary partial-path comparator therefore removes the former in favour of the
latter (margin 53.21). cpgather partials=1 and the final NLI's
inputtotal=16562.60 prove that the non-PG prefix is the surviving input.
This is branch F under R86's binding order A -> B -> F -> D -> E -> G.

The selected question is deliberately only:

> Which measured input or faithfully-transcribed term makes the partial
> L2 ss⋈store candidate 186.54 cheaper than partial ss⋈hd, before their
> respective L3 joins reduce that lead to 53.21, and is that input inconsistent
> with PG 18.3's equivalent planning model?

R86 ruled out base-row/provenance and partial-row-unit explanations: the common
store_sales partial scan is 232218 rows from base 719876, and the private clone
has PG's table cardinality. The later parameterised-NLI Gather refusal is
consequential, not eligible work in this round.

Not selected:

- admitting a partial NLI beneath Gather or editing partialPathDrivingKind /
  executor walks;
- changing GOOPG_GATHER_PATHS, any enable_* GUC, a tie-break, or a magic
  constant to make a desired tree win;
- a row division, table re-load, ANALYZE, or stats rewrite;
- Q25/Q26/Q28/Q29/Q7, Q14/K92, Q1/R81, or Q91 Materialize.

## 1. Evidence protocol

Build the clean committed R87-scope HEAD into a fresh binary. Use a fresh
private cp -a copy of /tmp/pp2/clone-ds025-r82 on an unused 55xx port, run in
the foreground via scripts/goopg-test-run.sh, and prove listener executable and
database. Do not touch the retained R86 clone, peer :5533, or read-only PG
:65438/tpcds025.

Pin, capture, and SHOW in every plan session: work_mem=64MB,
max_parallel_workers_per_gather=4, and parallel_leader_participation=on.
Keep scan and join enable GUCs at defaults, recording each value on both
engines. Record Q96's relation-bit map and retain raw outputs at /tmp/pp2/r87/.

First capture natural goopg Q96 trace A/A with GOOPG_PGSHAPED_DP_TRACE=1 and
live PG's pinned Q96 plan. Then run a diagnostic GOOPG_GATHER_PATHS=top arm
only to expose the partial list; it cannot establish parity or authorize a
configuration change. The natural and diagnostic plans are observations, not
changes to the query.

Temporary trace code is permitted only when existing records cannot identify a
term. It must be default-off, log exactly the two named L2 partial hash paths,
not alter values/costs/ordering, and be removed before the R87 report commit.
The report must show trace-on/off EXPLAIN identity after removal. No persistent
source change is permitted in R87.

## 2. Required decomposition

For both L2 partial Hash Join candidates, record one table with each exact
cost-path input and resulting term:

1. outer kind, rows, width, startup/total, workers and divisor;
2. inner kind, rows, width, startup/total, local filters, join key, estimated
   filter selectivity, and base-stat provenance;
3. build-side choice, hash sizing/batches or no-spill verdict, and every
   startup, run, CPU tuple, operator/qual, page, width, parallel, and disabled
   component actually charged;
4. join selectivity, result rows/width, startup/total, and subsequent L3 cost;
5. comparator inputs: pathkeys, disabled count, insertion order, and the final
   PartialPathlist survivor set.

Read equivalent PG accounting from the checked-in PG 18.3 oracle, not a forced
query. PG cannot reveal DP losers, so live PG is evidence only for its selected
ss⋈hd prefix. Attribute a difference only if a goopg value demonstrably
mistranscribes the corresponding PG rule or consumes a different measured
input. If both candidates follow faithful rules from different physical or
statistical facts, classify G and choose another target; do not tune Q96 by
outcome.

| hypothesis | discriminating evidence |
| --- | --- |
| H1: ss⋈store's about-1/12 estimate versus ss⋈hd's 1/10 is causal | cardinality formula, NDV/null/filter inputs, PG-equivalent selectivity rule |
| H2: store (1 row, cost 1.15) versus filtered hd (720 rows, cost 140) changes build/probe terms faithfully | explicit inner and hash-build component table |
| H3: width (ss⋈store 1104, ss⋈hd 476) has a mispriced memory/page/CPU effect | build sizing and each width consumer, compared with PG source |
| H4: partial cost is correct but a pathlist/comparator mismatch loses PG prefix | equal-cost/equal-property comparator accounting |

A total-cost difference, a forced winner, or an unexplained PG shape is not an
answer.

## 3. Pre-declared outcomes

Select exactly one earliest result in this binding order:
**F1 -> F2 -> F3 -> G**.

- F1 — partial cardinality/selectivity transcription error. One L2 input
  disagrees with PG 18.3 while base statistics agree. Scope that estimator or
  metadata input precisely.
- F2 — partial hash-cost transcription error. Cardinality/selectivity
  accounting is faithful, but one cost term, including
  width/batch/build/probe/parallel accounting, disagrees with PG. Scope that
  term only.
- F3 — partial pathlist/comparator error. After F1 and F2 are false, handling
  of the measured cost/property tuples differs from PG's path model. Scope
  comparator pins; do not force a tree.
- G — no faithful local F lever. Inputs/terms match PG rules or depend on a
  proved physical/statistical difference. Preserve R86 D evidence as deferred.

Do not select D/E in this round. They are unreachable until PG's L3 prefix
remains applicable. Do not select F1 merely because the selectivities differ.

## 4. Predictions, gates, and deliverable

- P0: trace A/A is identical and reports the R86 two-path inputs and
  one-survivor L3 list.
- P1: one source-accounting table explains the 186.54 L2 gap and 53.21 L3
  margin without changing a plan.
- P2: Q96 values match PG and Q9/Q41/Q91/Q96 trace controls are identical
  after temporary tracing is removed.
- P3: repository code is unchanged at the end.

All program execution is foreground:

1. go test ./internal/optimizer ./internal/executor
   ./internal/testutil/estimateaudit, then the same package go vet;
2. Q96 natural trace A/A, then Q96 and Q9/Q41/Q91 trace-on/off controls;
3. diagnostic top trace only after natural capture;
4. Q96 values against PG after temporary code is removed;
5. standard SF0.25 sweep if temporary instrumentation could have affected
   execution before removal;
6. git diff --check, prove temporary code is gone, agent-review REPORT.md,
   then explicit-path commit -n and push.

The deliverable is r87-q96-f-prefix-cost/REPORT.md with the two L2 and two L3
tables, formula/source citations, one outcome above, artifact paths, and a
separately scoped successor. Success is attribution, not plan movement.
