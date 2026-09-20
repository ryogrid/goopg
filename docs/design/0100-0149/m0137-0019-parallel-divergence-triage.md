# M0137-0019 — per-query triage of the 16 parallel-mode `parallelism` divergences

Status: TRIAGE COMPLETE 2026-09-20
Kind: recon
Parent: none
Milestone: M0137 (plan-parity harness), headline work since the owner's
2026-09-20 decision that parallel mode is the canonical TPC-H parity corpus
Artefacts (committed, stats-epoch-pinned, NOT re-run per the task):
`analysis/m0137/m0137-0017-{serial,parallel}{,.pg}.plans.txt`

## 1. The instrument reproduces the filed claim exactly

`scripts/pg-plan-parity-diff.py` over both arms:

```
serial  : PLAN-PARITY queries=22 match=6 shapediff=14 missingnode=2
          CATEGORIES: … parallelism=0 …
parallel: PLAN-PARITY queries=22 match=2 shapediff=20 missingnode=0
          CATEGORIES: … parallelism=16 …
```

16 queries carry the `parallelism` category — Q1 Q3 Q4 Q5 Q7 Q8 Q9 Q10 Q12 Q14
Q16 Q18 Q19 Q21 Q22 Q15a-VIEWBODY — and exactly four are **parallel-only
regressions** (MATCH at `-serial=true`, SHAPE-DIFF at `-serial=false`): **Q1,
Q10, Q14, Q15a-VIEWBODY**, which is the set the task names.

## 2. The 16 collapse into four mechanism families

Derived by a structural feature scan of the plan pairs (`Parallel Hash` nodes,
`Gather` vs `Gather Merge`, finalize strategy, `Workers Planned`):

| family | queries | n |
|---|---|---|
| **A** — PG builds its hash side with `Parallel Hash`; goopg never emits one | Q3 Q9 Q10 Q14 Q16 Q18 Q21 | 7 |
| **B** — PG `Gather Merge`, goopg plain `Gather` (B2: 5 of them also flip PG's sorted finalize to goopg's hashed one — Q1 Q4 Q5 Q12 Q15a) | Q1 Q4 Q5 Q8 Q12 Q16 Q22 Q15a | 8 |
| **B′** — the mirror: goopg `Gather Merge` where PG uses a plain `Gather` | Q3 Q18 | 2 |
| **C** — `Workers Planned` differs | Q4 Q9 Q10 Q16 Q19 Q22 | 6 |
| **D** — goopg is fully SERIAL where PG parallelises | Q4 | 1 |

(Families overlap; Q7 is discussed separately in §3.4.)

## 3. Per-family verdict — the discrimination the task asks for

### 3.1 Family A (7 queries) — NOT a plan-selection defect. A stated capability refusal.

goopg emits **zero** `Parallel Hash` nodes anywhere in the corpus, and that is
by design, written down in the producer itself:

> `parallel_hash = true` is REFUSED: no goopg executor builds a hash table from
> a partial inner. The refusal is structural (this file never reads
> `inner.PartialPathlist`) and is stated rather than left as an absence.
> — `internal/optimizer/joinpathsparallel.go:59-61`

Upstream has two parallel hash joins (`try_partial_hashjoin_path`'s
`parallel_hash` argument, `joinpath.c:1290-1297`): partial outer over a
complete per-process inner, and `Parallel Hash`, a partial inner built
cooperatively in DSM behind a barrier. goopg implements **neither literally**
and something else instead — partial outer, COMPLETE inner, ONE shared build
performed once in the leader and adopted by pointer
(`prebuildSharedHashJoins`), which is coherent because goopg's workers are
goroutines in one address space.

So these seven records are a **known, designed divergence**. Closing them needs
an executor capability (build a hash table from a partial inner across
workers), not a planner or costing change. They belong to neither M0139/M0141/
M0142 nor to a Gather-placement bug; they are a capability item.

**Q14 is the cleanest witness** — its only category is `parallelism`, and the
plans are otherwise identical: `Finalize Aggregate → Gather → Partial
Aggregate → Parallel Hash Join`, where PG's build side is
`Parallel Hash → Parallel Seq Scan on part` and goopg's is a plain
`Seq Scan on part`.

### 3.2 Family B (8 queries, 5 with the coupled sorted finalize) — a COSTING gap on the parallel path.

The candidate is not missing and is not un-offered. `partialaggupper.go`'s R56
worker-sort-under-`GatherMerge` arm
(`partialAggGatherMergeProducer = "upper.groupagg.gathermerge"`) fires, and a
`GOOPG_PGSHAPED_DP_TRACE=1` plan-only probe over Q1/Q5/Q12/Q15a at HEAD shows
it **generated and ACCEPTED every time — 9 of 9 `verdict=accepted`**.

It loses the final election on cost, and not narrowly. Q1's upper rel:

```
upper.groupagg.split       total=    67840.37  pathkeys=0  accepted   <- wins
upper.groupagg.gathermerge total=  1510695.91  pathkeys=2  accepted
upper.groupagg.gathered    total=   771272.06  pathkeys=0  dominated
```

goopg prices the GatherMerge shape at **1.51 M — about 7.5x PG's price for
Q1's entire plan (200 900.77)**. This is a cost-model divergence in the
`cost_gather_merge` + worker-sort arm, not an election-policy or
candidate-generation problem.

Territory: **M0140's parallel-path costing**, not M0139/M0141/M0142 plan
selection. Family **B′** (Q3, Q18) is the same arm with the opposite sign —
goopg takes the GatherMerge where PG does not — which is what a mispriced arm
looks like from the other side and is evidence that the term is wrong rather
than uniformly too high.

### 3.3 Family C (6 queries) — mostly downstream, not its own cause.

Worker counts differ in both directions (goopg 4 vs PG 2 on Q10 and Q19; goopg
3 vs PG 4 on Q9 and Q16). Four of the six (Q9, Q10, Q16, Q22) also carry family
A or B, so a different subtree is being parallelised and the count follows the
shape rather than causing it. Q19 is the one with a worker-count divergence and
nothing else in this feature set, and is the only member worth probing on its
own (`computeParallelWorkerForRel`'s inputs).

### 3.4 The two singletons

- **Q4 (family D)** — goopg plans Q4 **fully serially** in parallel mode:
  `HashAggregate → Nested Loop Semi Join → Seq Scan on orders`. PG parallelises
  the semi-join's outer (`Parallel Seq Scan on orders`) under
  `Finalize GroupAggregate → Gather Merge → Partial GroupAggregate → Sort`. So
  goopg files no partial path beneath a `Nested Loop Semi Join`. That is a
  distinct, nameable producer gap and the only query in the corpus that loses
  parallelism entirely.
- **Q7** — carries the `parallelism` category with no structural delta in the
  feature scan. Both plans are `Gather Merge → Sort → Hash Join`; the
  difference is WHICH node is parallel-aware — goopg labels the join
  `Parallel Hash Join`, PG labels only the scan `Parallel Seq Scan on
  customer`. That is family A's design difference surfacing as a label, not a
  fifth mechanism.

## 4. What this changes about the headline number

`parallelism=16/22` is not 16 plan-selection defects. Read by cause:

- **7 are a designed capability divergence** (family A) that no planner change
  can close;
- **8-10 are one mispriced arm** (families B and B′) — the GatherMerge /
  worker-sort cost term;
- **1 is a missing partial-path producer** (Q4, semi-join outer);
- the worker-count differences are mostly consequences of the above.

So the corpus's parallel-mode divergence is dominated by **two** causes, and
only one of them is a costing bug a planner task can fix.

## 5. Follow-ups filed

- **M0137-0019a** — reprice the `GatherMerge` + worker-sort arm against
  `cost_gather_merge`. Expected movement: the `parallelism` category on
  families B and B′ (8-10 of 22 TPC-H queries).
- **M0137-0019b** — a partial path beneath `Nested Loop Semi Join` (Q4).

Family A gets a deferral-ledger row rather than a task: it is an executor
capability, and filing it as a planner task would misattribute it.

## 6. Method notes

- The capture was NOT re-run (the task forbids it); the committed
  stats-epoch-pinned artefacts were used, which is what makes the
  serial-vs-parallel A/B in §1 valid.
- The one thing that needed a live instrument — is the GatherMerge candidate
  generated or not — was answered with a `DP_TRACE=1` **plan-only** probe at
  HEAD, which is a different instrument, not a re-capture, and produces no
  timing.
- `Movement: none`: this task changes no production code and states so.
