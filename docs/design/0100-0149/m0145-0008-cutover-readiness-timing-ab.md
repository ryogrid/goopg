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

## Attribution: Q4's semijoin DISAPPEARS on the knob arm (2026-09-21)

The obvious next step was "log the filed NLI cost against the winning path's
cost and see which term is wrong". `noteSemiJoinrelPaths` (nlicensus.go) dumps
every path filed for a semi/anti joinrel with its kind and cost, once per
`addPathsToJoinrel`, so the winner is the minimum total and the NLI's margin of
loss is readable. What it found is that the premise was wrong again.

Q4 alone, same binary, same clone, only the knob differing:

| | default (1.49s) | knob (16.00s) |
|---|---|---|
| `SEMICOST` (semi joinrel candidates) | one path: `kind=nli total=502253.16` | **no semi joinrel at all** |
| `NLIGATE` | `gate=filed` | none |
| `NLICENSUS` (node built) | `route=rewrite jointype=semi probe=idx_lineitem_orderkey_fkidx` | **none** |
| `SUBLINKCENSUS` (route taken) | — | **neither route fired** |

So on the knob arm Q4's `EXISTS` never becomes a semijoin at all. It is not
out-costed — no semi joinrel is ever built, no NLI path is ever filed, and
neither the jointree pull-up nor the legacy pinned-spine route runs. The
correlated subquery stays a per-row subplan, which is the 10x.

Note also what the default-arm dump shows: the semi joinrel there has exactly
ONE filed path, the NLI. So even on the default arm the NLI is not competing
against a hash semijoin and losing — it wins its own joinrel uncontested.

**This retires the "add_path out-costs the NLI" reading** that the previous two
loops recorded (including this document's own first section). The A/B numbers
stand; the mechanism behind Q4's share of them does not.

### CORRECTION (same day): both routes DO fire — the joinrel never forms

The section above reported that neither sublink route fired for Q4 on the knob
arm. That was read off a run which also had `GOOPG_PGSHAPED_DP_TRACE=1` set,
and it was wrong. Re-running Q4 on the knob arm without the trace flag, twice,
gives:

```
PULLUPCENSUS decline=(pulled)
SUBLINKCENSUS route=jointree-pullup
SUBLINKCENSUS route=pinned-spine
```

The pull-up fires and the `EXISTS` **is** pulled up. What does not happen is
anything after that: widening `noteSemiJoinrelPaths` from semi/anti to EVERY
join type produced **zero** lines for Q4, so `addPathsToJoinrel` is never
reached at all — the search forms no joinrel of any kind. The DPPATH trace
agrees: it shows `relids={0}`, a single-relation problem.

So the pulled body's leaves never enter the search, the `EXISTS` stays a
per-row subplan, and Q4 runs 16.0s against the default arm's 1.5s.

This is the failure mode M0145-0003's own notes predicted: `pulled` marks the
conjunct, which SUPPRESSES the legacy pre-DP arm for that scope, and if the
seam then declines the pulled bodies there is no semijoin left from either
route. The note put it exactly: "without `exprListHasLocalAndLevel1Ref`,
`pulled` would mark bodies the seam declines anyway and wrongly suppress the
pre-DP arm".

### What was ruled out

- **The NOT NULL reduction wired into the generic WHERE arm** (M0145-0005, same
  day) is NOT involved: disabling its block and re-running Q4 on the knob arm
  gives 17.78s against 16.03s with it — no routing change, no timing change.
- **The cost comparison** is not involved either: no joinrel is formed, so
  nothing is costed and nothing is out-costed.

### The next probe, precisely

The pull-up marks the conjunct and the seam then declines. Name the decline
class: run Q4 on the knob arm with the seam's own decline trace and read which
class fires after `ctx.jtPullup != nil` — `classifyPulledQuals` returning
false, `splicePulledLeaves` returning false, or a leaf-count/legality gate. The
fix belongs to M0145-0003 (either the seam accepts what the pull-up marked, or
the pull-up stops marking what the seam will decline), not to the cutover.

## What this means for the cutover

M0145-0008 flips `GOOPG_JOINTREE_PIPELINE` to on. Flipping it today makes TPC-H
1.64x slower in total and Q4 35x slower, with every value gate still green.
That is a hard blocker, and unlike the two blockers M0145-0007 retired, this one
is measured rather than inferred.

Two prerequisites, both filed:

1. ~~the semijoin NLI cost comparison~~ — ANSWERED below, and differently than
   this section first framed it: for Q4 the semijoin is never built on the knob
   arm, so there is no cost comparison to correct. The open question is why
   neither sublink route fires;
2. Q17's 20x, mechanism unknown, scalar-subquery route.

## Note for whoever re-runs this

The comparison is only meaningful with the binary and engine-id pinned, as
above — the arm prints both, and a run whose `engine-binary` differs from its
counterpart is measuring two trees, not two arms.
