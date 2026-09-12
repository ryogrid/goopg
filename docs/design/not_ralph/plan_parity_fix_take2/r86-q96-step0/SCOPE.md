# R86 SCOPE — TPC-DS Q96 parallel-row provenance and NLI tournament Step-0

This round is measurement only. It selects Q96 from R82/R85's nearest
three-category tier, identifies the first planner divergence, and names one
bounded follow-up. It changes no planner, executor, cost, or renderer behavior.

## 0. Why Q96, and what is not selected

Current R85 parity is `match=2 shapediff=67 missingnode=27 error=3`. Q96 is
`[join-order,join-method,scan-type]`:

- goopg: partial `Merge Join`, with
  `store_sales -> time_dim -> household_demographics`, then `store`;
- live PG 18.3: partial `Nested Loop`, with
  `store_sales -> household_demographics -> store`, then a parameterised
  `Index Scan using time_dim_pkey`.

The nearest TPC-H Q14 is not a label fix: K92 requires a real shared
partial-inner hash execution model. TPC-H Q1 remains blocked on the
selectivity/width inputs measured in R81. DS Q25/Q26/Q28/Q29/Q7 are the larger
parallel family. Q91's PG-only Materialize is not locally actionable: R67
proved Materialize cannot move parity until the surrounding NL shape already
matches. R85's ParamRef LIMIT allowlist has zero executable-corpus sites.

Q96 is selected because one visible number can distinguish provenance before
any new instrumentation. Under a 3-worker Gather, live PG prints
`Parallel Seq Scan on store_sales rows=232218`; goopg prints `rows=719876`.
Fresh PG `tpcds025` reports `pg_class.reltuples=719876`, and
`clamp_row_est(719876 / get_parallel_divisor(3,on)) = 232218`. Therefore
goopg's printed number is either a different base-stat input or a lost
per-worker row count.
It is not yet evidence of a cost bug: the plan carrier may display a different
quantity from the path tournament. This round proves which.

## 1. Questions and evidence protocol

Use a clean-HEAD binary and a fresh `cp -a` private copy of
`/tmp/pp2/clone-ds025-r82` on an unused 55xx port. The retained clone and peer
`:5533` remain untouched. Run the server in the foreground through
`scripts/goopg-test-run.sh`; prove the listener's executable and serving
database. PG `:65438/tpcds025` is read-only. Pin and capture `work_mem=64MB`,
`max_parallel_workers_per_gather=4`, `parallel_leader_participation=on`, and
the enable_* join/scan GUCs on both engines.

Run Q96 EXPLAIN twice with `GOOPG_PGSHAPED_DP_TRACE=1`; plans and relevant
DPTRACE/DPPATH records must repeat byte-for-byte. Record the relation-bit map.
The existing trace already exposes producer, serial/partial list, relset,
kind, required outer, rows, startup/total, verdict, partition, width, and
input total. Add temporary trace fields only if a question below remains
unanswerable; any such instrumentation must be default-off, inert under an
on/off plan comparison, and fully reverted before the Step-0 commit.

Answer in order:

1. **Base statistic.** On both engines record `count(*)`, catalog
   reltuples/relpages (or goopg's exact supported equivalents), table
   reloption workers, and the Q96 base-rel `Rel.Rows`/baseRows as exposed by
   the trace. Do not ANALYZE the source clone. If the private copy's stored
   statistic disagrees with its row count, a second throwaway copy may be
   ANALYZEd as a counterfactual; capture before/after and discard it.
2. **Row unit.** Follow `scan.seq.partial` for `store_sales` through each
   accepted partial hash/NL/merge path, the chosen partial aggregate, Gather,
   `createPlanNode -> stampPlanCost`, and EXPLAIN. Identify the first site
   where total rows appear where per-worker rows are required. If no site
   does, explain the 719876 line from measured inputs rather than inference.
3. **Join-order divergence and actual election.** Q96's PG tree first forms
   `{store_sales, household_demographics}`; goopg's rendered winner first
   forms `{store_sales, time_dim}`. That is the first visible tree difference,
   not an L2 election: the two L2 relation sets coexist and `setCheapest` runs
   independently for each. List all offered paths for both relsets, their
   rows/costs/verdicts, and any producer veto. Follow the PG-shaped L2 path
   through its three-table `{ss,hd,store}` prefix. Within that L3 RelOptInfo,
   name whether the path originating at `{ss,hd}` survives dominance and is
   the applicable `CheapestTotal`. If that prefix survives, follow it into
   the final `time_dim` edge, where NLI absence/veto is D and a priced NLI
   loss is E; neither full-relset outcome belongs to F.
4. **NLI feasibility/election.** For the PG-shaped final edge, prove whether
   a parameterised `time_dim_pkey` path is generated with
   `RequiredOuter={store_sales...}`, whether serial and partial NLI producers
   consume it, and whether it is accepted, dominated, or vetoed. Name the
   exact producer/veto/comparator and, when the path reaches pricing, the
   winning margin; an absent/vetoed path instead requires the exact
   pre-pricing veto. Do not infer admission from the absence of a rendered
   node.

PG does not expose its internal DP losers. Its live plan supplies the oracle
partition, row units, chosen index probe, and chosen costs. Do not manufacture
a PG tournament number by changing the query. Diagnostic GUC arms are allowed
only after the natural trace is classified, and cannot prove parity by forcing
a shape.

## 2. Pre-declared outcome branches

Step-0 ends with exactly one primary branch and a separately scoped next
round. Select the **earliest causal failure** in the total decision order
below. A downstream path absent only because its required prefix already lost
is consequential evidence for the earlier branch, not a second branch. A
secondary display defect may be recorded but cannot masquerade as the cause of
Q96's election.

- **A — base-stat/provenance mismatch.** goopg's stored base rows differ from
  the actual SF0.25 table/PG input. Stop optimizer work; scope the statistics
  creation/clone/ANALYZE correction and re-baseline before touching costs.
- **B — partial-row propagation defect.** The producer starts with the right
  base total but a path used by the tournament first substitutes total rows
  for `rows/divisor`. Scope that one propagation site plus synthetic
  per-worker pins and a corpus movement census.
- **C — display-only row loss.** All tournament paths use per-worker rows but
  plan stamping/EXPLAIN prints total rows. Scope the display carrier fix
  separately, record that it cannot explain the join election, and continue
  classification at the real L2/NLI divergence.
- **F — upstream partition sizing/election.** The NLI mechanism is available
  in an isolating control, but before the final `time_dim` edge can be formed,
  the PG-shaped path carried from the coexisting `{ss,hd}` L2 relset loses
  within the three-table `{ss,hd,store}` prefix RelOptInfo or is not selected
  as that prefix's applicable input. Classify that measured L3 same-relset
  comparison here. Any downstream NLI non-offer caused by that loss is
  consequential, not D. Name the first differing row/selectivity/cost input
  and scope only that input. A tie-break or constant chosen merely to obtain
  PG's tree is out.
- **D — parameterised path/NLI absent.** The required prefix survives/is
  the applicable path at the common-relset level where the PG-shaped edge can
  be formed, but the
  parameterised probe or applicable serial/partial NLI consumer is never
  offered or is vetoed. Scope the exact admission/parameterisation producer or
  veto; no cost change.
- **E — NLI offered and loses on price.** The required prefix survives and the
  applicable prefix, probe, and NLI consumer reach pricing, but the NLI loses.
  Decompose the measured margin into scan/index, rescan, qual, row, width, and
  parallel terms against the PG 18.3 oracle implementation. Scope a correction
  only for a proved mistranscription or wrong input.
- **G — no faithful local lever.** If every candidate is admitted and priced
  consistently from genuinely different physical/statistical inputs (for
  example the K14 heap-density gap), record the external dependency and
  choose another corpus target. Do not force Q96.

The total primary decision order is **A -> B -> F -> D -> E -> G**: stop at
the first true condition. C is orthogonal and may accompany exactly one
primary result. In particular, F owns only a measured loss inside the L3
`{ss,hd,store}` prefix before the final NLI is formable; the two distinct L2
relsets never compete. If that prefix remains applicable, D owns an
absent/vetoed final NLI and E owns a priced final NLI loss at the full relset.
The next scope must state why its chosen lever can change the natural
tournament; a renderer-only correction does not satisfy that bar.

## 3. Predictions and stop rules

- P0: the 719876/232218 difference is attributed to one exact source and row
  unit, with the divisor and both engines' base inputs recorded.
- P1: the first visible structural divergence is localized to the two
  coexisting L2 relsets, then the three-table PG prefix's same-relset
  dominance/`CheapestTotal` verdict and, if it survives, the final NLI
  election are named; or the live PG tree disproves the assumed partition and
  the round stops for re-scope.
- P2: the `time_dim_pkey` parameterised path and both NLI consumers receive an
  explicit generated/accepted/dominated/vetoed verdict, or `not reached
  because <measured same-relset upstream loss>` under F.
- P3: trace on/off and repeat runs produce identical plans; repository code is
  unchanged at the end.

Stop on a data/database mismatch, non-repeatable trace, unexplained serving
provenance, values mismatch, or a proposed fix that requires changing more
than one outcome branch. Do not implement a tempting row division until the
trace proves the tournament reads the wrong unit; `costParallelSeqscan` and
the partial join producers already claim the PG one-divisor rule and have
unit pins, so the burden is runtime evidence.

## 4. Deliverable and gates

Write `STEP0.md` containing the source/stat table, row-provenance chain, L2,
decisive L3-prefix, and full-rel tournament tables, NLI verdict, one branch
from section 2, and the next-round scope seed. Preserve raw evidence under
`/tmp/pp2/r86/`.

All runs are foreground:

1. clean `go test ./internal/optimizer ./internal/executor
   ./internal/testutil/estimateaudit` and `go vet ./internal/optimizer
   ./internal/executor ./internal/testutil/estimateaudit`;
2. Q96 trace A/A and trace-on/off plan identity, plus Q9 (current DS match),
   Q41 (R85 match), and Q91 (neighbor control) on/off identity;
3. Q96 value/result comparison against PG on the final clean binary after all
   temporary instrumentation is removed; run the standard SF0.25 sweep as
   well if any diagnostic code could have affected execution before removal.
   No implementation means the full corpus sweep is otherwise deferred to the
   selected fix round;
4. `git diff --check`; no temporary code remains;
5. agent review of `STEP0.md`, then explicit-path `commit -n` and push.

The Step-0 success criterion is attribution, not an increased match count.
