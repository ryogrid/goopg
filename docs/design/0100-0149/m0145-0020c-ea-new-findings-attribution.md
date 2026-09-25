# M0145-0020c — the two NEW ea-ratchet findings are not M0145-0020a's

Status: **CLOSED measured-no-gap 2026-09-22.** Movement: none — recon, no
production change.
Kind: recon
Parent: M0145-0020a

## Question

`make ea-ratchet` was FAIL when M0145-0020a landed, with two NEW findings
against the default baseline: `Q44:item+ss1` and
`Q83:cte:sr_items+cte:wr_items`. Both relsets are grouped/CTE-derived — the
exact population whose estimate M0145-0020a moved — so the landing commit
attributed them to itself rather than claiming them pre-existing, and filed
this task to confirm that attribution before anyone proposed a fix.

## Method

An A/B in ONE environment, which is what the earlier contradictory numbers
(61 NEW vs 17 NEW, recorded on M0145-0020a and M0145-0020b) lacked. The
committed change is two production files, so the arm is exact:

```
git diff 4c6f7ab8d^ 4c6f7ab8d -- internal/optimizer/cardinality.go \
    internal/optimizer/joinkeyproof.go > /tmp/0020a.patch
git apply -R /tmp/0020a.patch && make ea-ratchet     # WITHOUT
git apply    /tmp/0020a.patch && make ea-ratchet     # WITH (= HEAD)
```

Everything else — clone, data, baseline, PG oracle directory, the unrelated
WIP in the tree — is identical between the arms, so it cancels.

## Result: the attribution is REFUTED

| arm | findings | NEW | FIXED |
|---|---|---|---|
| without M0145-0020a | 56 | `Q44:item+ss1`, `Q83:cte:sr_items+cte:wr_items` | — |
| with M0145-0020a (HEAD) | 53 | the same two | Q62, Q80, Q99 |

Both NEW findings are present **with the change reverted**, with byte-identical
estimates (`Q44` est 1831 / actual 10; `Q83` est 4 / actual 171). The change
introduced zero findings and removed three: on this instrument it is a strict
improvement, 56 -> 53.

M0145-0020b's clean-HEAD run corroborates this independently — its 17-NEW list
contains both ids. The reason that run's total differed is now also settled:
it ratcheted against `c20a-estimator-census-**20260907**/ea-baseline.txt`
(178 findings) while `make ea-ratchet`'s default is
`c20a-estimator-census-**20260915**/ea-baseline.txt` (54 findings). The "61 NEW"
and "17 NEW" figures were never comparable to each other, and neither was
comparable to the default gate.

## What the two findings actually are

Both carry `pg_est: null` in `tmp/c20a/ea-findings.json` — UNMATCHED-IN-PG. The
scorer found no PG node with that relset key, so the finding says "goopg formed
a relset PG's plan does not contain", not "goopg's estimate is worse than PG's".
This is the mechanism the M0142-0016b/M0142-0016d ledger rows describe.

- **Q83 `cte:wr_items`** — goopg estimates 4, actual 171. PG **inlines** the
  CTE (`bench/tpcds/plans-pg/Q83.txt` has `GroupAggregate`, no `CTE Scan`), so
  there is no key to match. PG's estimate for the same branch is `rows=5`
  against the same 171 actual: **PG is wrong in the same direction and by a
  similar factor.** A shared formula error, surfaced by a key-matching
  artifact — not a goopg estimator defect, and not something a goopg-side fix
  should chase toward PG's own wrong number.
- **Q44 `item+ss1`** — goopg estimates 1831, actual 10. PG never forms this
  relset: its plan joins `item` last through two `Nested Loop`s over the
  window-function subqueries (`Q44.txt:2-3`). The 183x is a real over-estimate,
  but it is priced on a **join order PG does not use**, so the primary
  divergence is join order (the `join-order=91` parity category), not this
  estimate. Fixing the estimate would leave the shape divergence untouched.

Neither finding is therefore a defect this task's parent introduced, and
neither has a fix that is well-posed on its own terms today.

## The residual: the gate is red with no sanctioned way back to green

Both keys are absent from the 2026-09-15 baseline, so the shapes appeared from
plan churn after that capture. AGENT.md G4 allows `make ea-ratchet-repin` only
when the corpus or dataset changes — never in a commit that changes code — and
the loop may not repin on its own judgement. So a baseline made stale by
*goopg's own* plan churn has no path back to green, and every later task
inherits a red gate it did not cause and cannot clear. Filed as
**M0145-0020d** for an owner decision; the loop does not repin.

## Gates

None beyond the two `make ea-ratchet` runs above and the pre-commit pgbench
smoke — no production file was touched (C1: this is a recon, and it stages no
`internal/` path).
