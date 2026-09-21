# Measured re-evaluation of the derived-input blockers (M0145-0011)

Status: scopes (a), (b) and (d) DONE 2026-09-21 — the diagnostic flag is landed,
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

## Hard constraints honoured

The firewall stays ON for the default arm — the default-arm sweep is 99/99
plan-identical and `PASS=96 MISMATCH=0`, the TPC-H acceptance arm 24/24, and
spotcheck Q12=2/Q13=33. **No relaxation is landed by this task.** The `rows<=1`
guard is untouched. All firewall-off captures are knob-arm private evidence
(G8), taken on private result lanes.

## Not done here

Scope (c) / E2 — relaxing `flattenPulledBodyTree`'s bare-`*SeqScan` rule for
`*CTEScan` leaves on the knob arm — is deferred and ledgered. It is downstream
of (a) and wants its own loop, since it needs the pull-up/seam decline census
re-run afterwards to say what it moved.
