# M0142-0001 — entry recon: is the pricing blockage still the same one?

Status: accepted (landed 2026-09-15)

## Task

Per `.ralph/fix_plan.md`'s M0142-0001 line: M0138 changed every estimate
corpus-wide, so before scheduling any join-order-costing campaign the question
is whether the attribution that blocked four prior rounds (`R70` BLOCKED on
fragility, `R89` C2 missing inputs, `R98` UNOBSERVABLE, `R99` ORACLE INVALID —
`METHODOLOGY3/02-open-problems.md` B2/B5) still holds, or has moved.
Measurement-only recon per `AGENT.md` §"Plan-parity harness" — no production
code changed in this task.

## Method

Bootstrapped per `docs/design/0100-0149/m0137-0003-baseline-capture-procedure.md`.
All four bench clusters (`:65432`/`:65433` TPC-H, `:65437`/`:65438` TPC-DS
SF0.25) were already up at loop start — verified via `pg_isready`, none
restarted.

```
go build -o /tmp/estimate-audit-m0142-0001 ./cmd/estimate-audit
PGPASSWORD=tpch /tmp/estimate-audit-m0142-0001 -plan-only \
  -label m0142-0001-tpch-corpus -out analysis/m0142 \
  -port 65433 -db tpch -user tpch -password tpch \
  -ref-port 65432 -ref-db tpch -ref-user postgres -ref-password postgres

scripts/capture-tpcds.sh 65437 postgres postgres \
  analysis/m0142/m0142-0001-tpcds-goopg.txt "M0142-0001 goopg SF0.25" \
  bench/tpcds/runtime_goopg/data-sf025
scripts/capture-tpcds.sh 65438 tpcds025 ryo \
  analysis/m0142/m0142-0001-tpcds-pg.txt "M0142-0001 PG18.3 SF0.25 reference"

python3 scripts/pg-plan-parity-diff.py analysis/m0142/m0142-0001-tpch-corpus.plans.txt \
  analysis/m0142/m0142-0001-tpch-corpus.pg.plans.txt
python3 scripts/pg-plan-parity-diff.py analysis/m0142/m0142-0001-tpcds-goopg.txt \
  analysis/m0142/m0142-0001-tpcds-pg.txt
```

Output: `analysis/m0142/m0142-0001-*` (scratch, untracked — same precedent as
M0138-0001/-0005/-0006's `analysis/m01NN/` artefacts). Each artefact carries
its own engine-id/stats-epoch stamp for provenance.

## Results — corpus-level category movement

| corpus | join-order (baseline, 2026-09-14) | join-order (HEAD, this task) | match floor |
|---|---|---|---|
| TPC-H | 14 | **14 — unchanged** | 6 (actual 6, unchanged) |
| TPC-DS SF0.25 | 89 | **90 — within one query, effectively unchanged** | 2 (actual 2 — Q9 excluded from TPC-DS since it's a TPC-H query id; floor held) |

Read alone, this says "no corpus-level movement" and would justify closing the
recon with "blockage unchanged, proceed with the old campaign." **That
reading is wrong for the two specific queries the four blocked rounds were
about** — R53/R68/R96's attribution was never corpus-wide, it was Q9 and Q96
specifically, and the corpus roll-up is exactly the kind of aggregate K50
warns hides per-query movement. The two queries need to be re-examined
individually, which is what M0138-0006 already started for Q9 and this task
completes.

## Results — Q9 (B2, R53/R68's subject): the blockage has moved

Today's fresh capture reproduces M0138-0006's numbers exactly (byte-identical
costs, same `# stats-epoch`): goopg `cost=124545.31..124545.68 rows=146`,
PG `cost=164770.46..164920.77 rows=60125`. New in this task is the
**category isolation**: `pg-plan-parity-diff.py` tags Q9 `SHAPE-DIFF
[join-order]` — a **single** category, no `join-method`/`aggregation-strategy`/
other confound. That is itself informative: the tool derives `join-method`
from matched-pairing comparisons, and Q9 has none — every join level after
the first diverges in relset membership, not just in operator choice.

The join spines (from this task's `estimate-audit` d-clause output) confirm
and sharpen M0138-0006's plan-shape description:

- **Both** engines join `partsupp ⋈ part` first (the one matched pairing, at
  the deepest level d16).
- **goopg** then walks `+lineitem -> +supplier -> +orders -> +nation`
  (left-deep, all Index-Scan-driven Nested Loops down the FK path, per
  M0138-0006).
- **PG** instead walks `+supplier -> +nation -> +lineitem -> +orders`
  (left-deep, all Hash Joins), i.e. **orders is PG's outermost/last join and
  goopg's innermost/first** after the shared `partsupp ⋈ part` root.

This is a genuine **topology** divergence (every join after the first picks a
different relset to add next), not "the same shape priced differently" — the
premise R53/R68's method assumed. Their method (`METHODOLOGY3/01-what-we-learned.md`
§"R53 (Q9)": "sizing OUT ... admission OUT ... 2.5% question") instrumented a
narrow cost margin **between two candidate relsets goopg's own search already
produced side by side**. That method has nothing to attach to here: the
question is no longer "which of two already-generated candidates is
mispriced by 2.5%" but **"does goopg's join search generate PG's topology as
a candidate at all, and if so, at what price relative to the chosen one?"** —
a shape-generation question, not a margin-instrumentation one. Answering it
needs join-search candidate tracing (does not exist at HEAD — see Deferral),
not a cost-formula audit.

M0138-0006 already flagged this as a hypothesis ("this was not verified by
forcing goopg onto that shape... a hypothesis for M0142-0001/-0002 to test,
not a conclusion"); this task's contribution is confirming the topology
divergence precisely (matched-root-then-diverge, not "unrelated shapes") and
naming the concrete missing instrument.

## Results — Q96 (B5, R96's subject): the blockage is structurally unchanged

Today's live TPC-DS capture: Q96 is `SHAPE-DIFF
[join-order,scan-type,parallelism,qual-placement]`, `goopg
cost=16367.52..16367.54` vs `pg cost=17673.05..17673.05` (both `rows=1`, top-level
totals — not directly comparable to R96's L3 **intermediate**-node margin of
157.50/0.68%, which named one join level inside the plan, not the query
total; re-deriving that exact intermediate figure was not attempted here, it
would re-run R96's own method on an already-adjudicated finding).

What matters for "is the blockage the same": **B5 is not a statistics
blocker.** R98/R99's finding was that PG's hash join's final-cost inputs
(`inner_unique`, `outer_match_frac`, `match_count`, PG's virtual-bucket/MCV
values, `QualCost` splits) are unobservable through EXPLAIN and that running
real PG over a copy of goopg's own data directory — the only route that could
expose them — crashes PG's parallel workers (`nbtsearch.c:707`, signal 6).
Nothing in M0138 (ANALYZE statistics) or M0139/M0140 changes PG's EXPLAIN
verbosity or repairs that crash; the blocker is orthogonal to everything the
plan-parity group has landed since R99. Live re-check today confirms Q96 is
still `SHAPE-DIFF` with `join-order` among its tags (not resolved, not
newly-passing) — consistent with "still blocked," not "no longer applicable."

**Verdict: B5/Q96 is unchanged. Do not re-attempt it without a new
observability mechanism; none exists at HEAD or is proposed by this task.**

## Overall verdict

The pricing blockage is **not** a single thing that either holds or has
lifted uniformly. Split by the two queries the prior rounds actually
converged on:

- **Q9 (B2)** — the blockage **has moved**. R53/R68's margin-instrumentation
  method no longer fits the mismatch it would be applied to; the open
  question is now join-search shape generation, which needs an instrument
  that does not exist yet (a slice list follows).
- **Q96 (B5)** — the blockage **has not moved**. It is a structural
  PG-side-observability gap untouched by anything M0137-M0140 has landed;
  restarting a pricing campaign against it without a new observability route
  would repeat R98/R99's outcome.

Per the task's own deliverable shape ("a verdict plus, if the blockage has
moved, a slice list"): the slice list below is filed for the Q9/B2 half only.
M0142-0002 ("re-measure Q9's join order after M0138") is satisfied by this
task's Results section above (M0138-0006 did the estimate half; this task
adds the topology/category-isolation half) — checked off in `.ralph/fix_plan.md`
in the same commit.

## Slices filed into M0142-0003

1. **M0142-0003a — join-search candidate trace (measurement-only
   instrumentation, env-gated, no default-path behavior change).** Add a
   temporary trace point in `internal/optimizer/joinsearchseam.go` /
   `joinsearch.go`'s DP loop that, under an env var (e.g.
   `GOOPG_JOINSEARCH_TRACE=1`), logs every relset pairing considered for a
   named query with its computed cost, then instrument it live against Q9.
   Deliverable: does goopg's DP ever construct PG's topology
   (`partsupp⋈part -> ⋈supplier -> ⋈nation -> ⋈lineitem -> ⋈orders`) as a
   candidate, and if so, at what cost relative to the chosen NLI chain's
   124,545? This is the concrete missing instrument named above; K61/K64's
   rule applies directly ("instrument the term, never infer it from the
   sum" — `AGENT.md`'s "Margins here are fractions of a percent" paragraph
   makes the same point for the sibling costing question).
2. **M0142-0003b — branch on 0003a's finding (not yet scoped, depends on
   0003a landing first).** If PG's topology **is** generated but priced
   worse, this becomes a costing-term audit (which term overprices the
   Hash-Join chain or underprices the NLI chain — B8's
   `indexProbeCostMultiplier` and B10's index-correlation defaults are the
   named adjacent suspects, both already flagged under M0142 as
   re-measure-first items). If PG's topology is **never generated**, this is
   a join-search completeness gap, not a costing gap — F6's "opening
   candidates moves join-order by zero" would need re-reading as a
   corpus-wide statement that does not preclude a per-query candidate gap,
   and M0142's premise (this milestone is costing-only, candidates are
   "already open at HEAD") would need to be re-scoped for Q9 specifically
   before implementation work starts.

`M0142-0003`'s placeholder line in `.ralph/fix_plan.md` is updated with these
two slices in the same commit, per its own "check this line off in the same
commit that files the real slices" instruction.

## Deferral

One ledger row: Q9's join-search shape-generation question (M0142-0003a/b),
resume point as above. B5/Q96 is not re-deferred with a new row — R98/R99
already carry that deferral and nothing here changes their resume point.

## Gates

`go build ./...` unaffected (no source file changed — pure recon, four
`analysis/m0142/*` artefacts plus this doc and the two tracker files).
No values-gate re-run needed (no production code touched). Pre-commit hook's
pgbench smoke: runs as part of `git commit` per the mandatory-on-every-commit
policy.
