# Measured re-evaluation of the derived-input blockers (M0145-0011)

Status: scopes (a), (b), (c) and (d) DONE 2026-09-21 — the diagnostic flag is landed,
E1's evidence is gathered and **clean at both SF0.25 and SF1**, and the
adjudication is reported. Scope (c) (E2) deferred with a ledger
row; scope (d)'s adjudication is reported here for the owner.

Task: `.ralph/fix_plan.md` M0145-0011. Kind: recon. Parent: none.

## Why this task exists

M0145-0009's census established that the 648 unresolvable CTE-output column
asks are columns **PostgreSQL itself would not resolve** — so "wait for
CTE-output statistics" cannot be the standing answer to the residual blockers.
But the residuals' named unblock conditions are ROW-estimate conditions, and
row-level CTE estimates already sit at PG-equivalent granularity
(`EstimateRows(*CTEScan)` recurses the body, ≈ `set_cte_size_estimates`'
`plan_rows` propagation). Whether the DP still misprices catastrophically under
today's estimates is therefore a MEASUREMENT question.

## Scope (a) — the diagnostic flag, and why it is NOT exempt

`GOOPG_DERIVED_FIREWALL=off` bypasses the `outer-over-derived` decline
(`relfromjoinlist.go`). Read once at process start, default ON.

It is registered in the flag-provenance **table**, deliberately NOT in
`flagProvenanceExempt`. The exempt map is for flags that "never change a chosen
plan"; this one changes the chosen plan — that is its purpose — so an artefact
that does not name it cannot say whether the firewall was in force. Putting it
in the exempt map would have been a false rationale weakening a real guard, and
the completeness test (`TestFlagProvenanceTableCoversPlannerEnv`) is what forced
the decision rather than letting it pass silently.

Sibling audit: the `outer-over-derived` decline exists at exactly ONE site, and
`leafIsDerivedInput` is referenced only in comments elsewhere. There is no
sibling gate to keep in sync.

## Scope (b) — E1's evidence, and it is clean

Knob arm, TPC-DS SF0.25, firewall ON vs OFF, same build:

**Plan channel — exactly two queries move: Q77 and Q78.**

Q78 (the firewall's own named witness) stays a `Hash Left Join` in both arms —
**no NL-epsilon shape** — and its CTE estimates become markedly more honest:

```
firewall ON      CTE Scan on ss  rows=6      CTE Scan on cs  rows=4
firewall OFF     CTE Scan on ss  rows=1407   CTE Scan on cs  rows=820
```

The epsilon the firewall exists to distrust is exactly what disappears when the
search is allowed to run: with the firewall ON the problem declines and falls
back to the syntactic tree, whose legacy rewrites produce the rows=6/rows=4
estimates.

Q77 does introduce one `Nested Loop Left Join` where a `Hash Left Join` stood —
the shape class the firewall guards — but at `rows=1, cost=0.06`, inside a
grouping-sets `Append` branch. Shape alone cannot adjudicate that; execution
can.

**Values and timing channel** (knob arm, firewall OFF, full corpus):

```
PASS=96  MISMATCH=0  CKMISMATCH=0  ERROR=0  TIMEOUT=0
Q77  PASS   726ms    44 rows   ck=eb9e091ac7669153
Q78  PASS  3521ms    15 rows   ck=c06cf981a7819a37
```

Every E1 criterion is met: no catastrophic NL-epsilon shape, Q78 holds (no
timeout, checksum-verified), values identical corpus-wide.

## Scope (d) — adjudication, with its limit stated

On this corpus, at this scale, the firewall is **not earning its keep**: it
costs Q78 an honest set of estimates and buys nothing measurable, and the one
NL shape it would have prevented (Q77) executes correctly in 726 ms.

**But the evidence is SF0.25 and the catastrophe it guards was measured
elsewhere.** C-04a's 15 s → 327 s blowup is a scale-dependent failure: an
epsilon rows=1 estimate hurts in proportion to the data it mis-sizes. SF0.25
evidence is necessary but **not sufficient** to retire a guard whose failure
mode appears at larger scale.

Recommendation to the owner — the loop does not pick:

1. **Before any relaxation lands, repeat E1 at SF1** on a private clone,
   specifically re-running C-04a's own witness. If Q78 and the ~12
   `outer-over-derived` fires hold there too, the unblock conditions can be
   redefined as "PG-equivalent row estimates + measured safety at the scale the
   catastrophe was measured".
2. If SF1 is dirty, the residual is the cost model or the search shape rather
   than statistics, and the options are the structural narrowing the task text
   lists (veto NL paths with a derived inner; `inner.Relids ∩ derived`;
   preserved-side-only decline) or documented permanence.

## E1 repeated at SF1 — the scale the catastrophe was measured at (2026-09-21)

Recommendation 1 above is now discharged. The A/B was re-run on a **private
clone** of the SF=1 TPC-DS datadir (`bench/tpcds/runtime_goopg/data` copied to
`/tmp/m11sf1`, served on `127.0.0.1:5547` under `GOOPG_CG_UNIT`), knob arm
(`GOOPG_JOINTREE_PIPELINE=1`), same build both ways, `store_sales` = 2 880 404
rows. Evidence dir: `tmp/m0145-0011-sf1/` (gitignored, so the numbers are
transcribed here).

**Plan channel — the same two queries move, and only those two.** All 99
queries were EXPLAINed in both arms and diffed. Five files differ; three of
them (Q36, Q70, Q86) differ ONLY in the temp-file name inside a pre-existing
`syntax error at or near ";"` (the grouping-sets parse gap), so the real
movers are **Q77 and Q78** — identical to the SF0.25 result.

Q78, the firewall's own witness, stays a **hash join** — no NL-epsilon shape.
What changes is commutation plus honest estimates:

```
firewall ON    Hash Left Join   CTE Scan on cs rows=11    CTE Scan on ws rows=7
firewall OFF   Hash Right Join  CTE Scan on cs rows=2280  CTE Scan on ws rows=1485
```

Q77 again gains one `Nested Loop Left Join` (rows=1, cost=0.06) inside the
grouping-sets `Append`, and one `Hash Left Join` becomes `Hash Right Join`.

**Values and timing channel** (fresh server per arm, so server age is held
constant; row counts and md5 of the result set both captured):

```
        firewall ON              firewall OFF            values
Q77     9015 ms   44 rows        5306 ms   44 rows       ck 9bd1900a34ce55c5 both
Q78    50885 ms  100 rows       48066 ms  100 rows       ck 3331a74f6d9f53e6 both
```

Every E1 criterion holds at SF1: no catastrophic NL shape, Q78 holds (it is in
fact ~6% faster), values byte-identical, and Q77 — the one query that gains the
NL shape the firewall guards against — is **1.7x faster** without the firewall.

**Adjudication at scale.** The SF0.25 finding was explicitly limited because
C-04a's 15 s → 327 s blowup is scale-dependent. At SF=1 — 4x the data, and the
scale at which the original catastrophe was measured — the firewall still costs
Q78 its honest estimates and buys nothing measurable, and the NL shape it would
have prevented executes faster than the plan it forces. The unblock conditions
in the residual blockers can therefore be redefined as **"PG-equivalent row
estimates + measured safety at the scale the catastrophe was measured"**, and
a relaxation can be filed as its own task.

The limit that remains: SF=1 is still not the identical configuration C-04a was
measured under, and the shape that mattered there (an epsilon-driven nested
loop over the full fact table) is not the shape the search now picks in either
arm. The evidence says the firewall is inert-to-harmful on this corpus at this
scale; it does not prove the hazard class it was written for cannot recur under
a different plan shape. That is an argument for narrowing the guard to the
shape (veto NL paths with a derived inner), not for keeping a decline that
poisons the estimates of every problem it touches. **The loop does not pick.**

## Scope (c) / E2 — the relaxation is a TWO-SITE invariant, measured (2026-09-21)

Scope (c) relaxes `flattenPulledBodyTree`'s bare-`*SeqScan` leaf rule for
`*CTEScan` leaves, behind `GOOPG_PULLUP_CTE_LEAF=on` (default OFF, registered
in the flag-provenance table for the same reason the firewall flag is: it
changes the chosen plan). It is paired with `GOOPG_DERIVED_FIREWALL=off`,
because a pulled ANY/EXISTS body becomes a JoinSemi/JoinAnti SJI and the
firewall's jointype switch covers Semi/Anti.

**The pull-up census moves exactly as predicted.** TPC-DS SF0.25, knob arm,
`GOOPG_NLI_CENSUS=1`, all 99 queries:

```
                                          firewall off   + CTE leaf on
PULLUPCENSUS decline=(pulled)                       42             72
PULLUPCENSUS decline=any-body-leaf-(*CTEScan)       30              0
PULLUPCENSUS decline=SubqueryExpr                   29             29
PULLUPCENSUS decline=any-nested-sublink             12             12
PULLUPCENSUS decline=ExistsExpr                      4              4
PULLUPCENSUS decline=InExpr                          2              2
PULLUPCLASSIFY refusal=<any>                      none           none
```

All 30 come from three queries — Q14 (5), Q23 (4), Q95 (2) at the per-query
re-measure — and all 30 convert into `(pulled)`.

**And the seam census says they do not get anywhere.** With
`GOOPG_PGSHAPED_DP_TRACE=1` on the same three queries:

```
              CTE leaf OFF          CTE leaf ON
Q14    3 x seam-decline leaf-count   5 x seam-decline pulled-leaf-not-scan
Q23    4 x seam-decline leaf-count   4 x seam-decline pulled-leaf-not-scan
Q95    1 x seam-decline leaf-count   1 x seam-decline pulled-leaf-not-scan
```

`pulled-leaf-not-scan` is `tryPGShapedJoinSearch`'s own leaf-kind check, whose
comment names `flattenPulledBodyTree` as its guarantor. So the bare-`*SeqScan`
rule is **one invariant held at two sites — a producer and a consumer** — and
relaxing the producer alone cannot put a CTE leaf into the DP. The decline
merely relocates.

**What the moved plans actually are.** Three plans move (Q14, Q23, Q95 — the
same three, no others across all 99). Estimated costs fall sharply:

```
        firewall off    + CTE leaf on     values        runtime
Q14        40347.58          27948.74     identical   13068 -> 14490 ms
Q23        22482.79           8389.09     identical   15024 -> 15545 ms
Q95       169930.68          70692.51     identical    3004 -> 3230 ms
```

Semi/anti join counts are preserved in every plan (3/3, 4/4, 2/2) and every
result set is byte-identical, so nothing semantic was lost. But the movement is
NOT the search finding a better order — the search never ran. It is the
documented `pulled`-suppression side effect: once the pull-up marks a sublink
`pulled` it suppresses the legacy pre-DP arm for the WHERE clause, and when the
seam then declines, the statement lands on a different fallback. That is the
same mechanism that cost TPC-H Q4 a 10x regression. Here it happens to be
harmless-to-slightly-negative — runtimes are flat or 2-11% worse against
estimated costs that fell 1.4x-2.4x, which is itself a cost-model signal.

**Resume point for anyone finishing scope (c).** The work is at the seam, not
at the producer's type switch: the pulled-leaf binding loop needs a
`rangeBinding` for a leaf that has no `Table`/`Alias`, and an
`estimateBaseRelInfo`/`applyRelSizeFallback` arm for a leaf with no catalog
statistics. Both sibling comments now say so.

## Hard constraints honoured

The firewall stays ON for the default arm — the default-arm sweep is 99/99
plan-identical and `PASS=96 MISMATCH=0`, the TPC-H acceptance arm 24/24, and
spotcheck Q12=2/Q13=33. **No relaxation is landed by this task.** The `rows<=1`
guard is untouched. All firewall-off captures are knob-arm private evidence
(G8), taken on private result lanes.

## Not done here

Scope (c) is measured and reported above, but it is deliberately NOT finished:
the seam-side half of the two-site invariant is left unimplemented, and is
ledgered. M0145-0011 lands no relaxation, so finishing it belongs to whichever
task the owner files against the seam.
