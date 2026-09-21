# Cutover readiness: the arm-vs-arm timing A/B (M0145-0008)

Status: measurement landed 2026-09-21; the cutover is BLOCKED on what it found.
Task: `.ralph/fix_plan.md` M0145-0008. Parent: M0145-0007 (whose NLI census
raised the question). Kind: recon.

## Why this measurement exists

M0145-0007's census established that the jointree pipeline hands pulled-up
semijoins to the search, that the search files an NLI path for them
(`gate=filed`), and that `add_path` then out-costs it. The remaining question
was whether that cost preference is RIGHT — and the ledger recorded that
settling it needs runtime, not another plan census.

Every gate this milestone runs compares VALUES, and the two arms are
value-identical (acceptance arm 24/24 on both). A cost misjudgement is
therefore invisible to every gate in the harness. Runtime is the only channel
that can see it.

## Method

Two `scripts/tpch-acceptance-arm.sh` runs, TPC-H SF1, serial, per-query cap
600s, each on its own fresh memory-capped server — the arm's own design, which
holds server age at zero for both (the "sweep-tail collapse" hazard in
CLAUDE.md). The ONLY difference between them is `GOOPG_JOINTREE_PIPELINE`:

```
engine-id     d71748906848b967c1fdf1fe611581228e569e40 …  (identical)
engine-binary on-disk=3e61809f585fd51c                    (identical)
host load     1.86 / 2.02                                 (comparable)
```

## Result: the pipeline is timing-neutral except on sublink queries

| query | default | knob | ratio |
|---|---|---|---|
| **Q4** | 0.37s | 12.98s | **35.1x** |
| **Q20** | 0.13s | 3.58s | **27.5x** |
| **Q17** | 0.38s | 7.61s | **20.0x** |
| **Q21** | 2.63s | 18.94s | **7.2x** |
| Q22 | 0.51s | 0.43s | 0.8x |
| every other label (19 of them) | — | — | 1.0x (±10%) |
| **TOTAL** | 62.43s | 102.46s | 1.64x |

Nineteen of twenty-four labels land inside ±10%, so the jointree pipeline
itself costs nothing measurable. The entire 1.64x total is four queries, and
all four are sublink queries:

- **Q4, Q20, Q21** are the semijoins M0145-0007 traced: the pull-up hands them
  to the search, the search files an NLI path and out-costs it, and the plan it
  prefers instead runs 7-35x slower. That answers the ledgered cost question:
  the preference is not merely unvalidated, it is wrong on every one of the
  three queries where it fires.
- **Q17** is a correlated SCALAR subquery, which the pull-up does not touch —
  PG does not convert `EXPR_SUBLINK` either. Its 20x is therefore a DIFFERENT
  mechanism and must not be folded into the semijoin story; it is recorded here
  because the same A/B surfaced it, and it is unattributed.
- **Q22** is a `NOT EXISTS` that does not regress, which is the useful control:
  being a sublink query is not sufficient to regress.

## What this means for the cutover

M0145-0008 flips `GOOPG_JOINTREE_PIPELINE` to on. Flipping it today makes TPC-H
1.64x slower in total and Q4 35x slower, with every value gate still green.
That is a hard blocker, and unlike the two blockers M0145-0007 retired, this one
is measured rather than inferred.

Two prerequisites, both filed:

1. the semijoin NLI cost comparison (Q4/Q20/Q21) — why does `add_path` reject a
   path that runs 7-35x faster? The candidate costs are both in scope at
   `addPath` in `addNLIPaths`, so the next step is to log the pair for these
   three queries rather than to reason about the formula;
2. Q17's 20x, mechanism unknown, scalar-subquery route.

## Note for whoever re-runs this

The comparison is only meaningful with the binary and engine-id pinned, as
above — the arm prints both, and a run whose `engine-binary` differs from its
counterpart is measuring two trees, not two arms.
